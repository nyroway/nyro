package configsync

import (
	"encoding/json"
	"strconv"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/storage"
)

var dataPlaneProxySettingKeys = map[string]struct{}{
	"proxy.request_timeout": {},
	"proxy.connect_timeout": {},
	"proxy.max_retries":     {},
	"proxy.retry_on_status": {},
	"proxy.max_body_bytes":  {},
}

func isDataPlaneSettingKey(key string) bool {
	if _, ok := dataPlaneProxySettingKeys[key]; ok {
		return true
	}
	return observability.IsExporterSettingKey(key)
}

// SnapshotFromProto converts a wire ConfigSnapshot into the gateway's internal
// read model. Upstreams, routes (with targets), consumers (keys — prefix+hash
// only — route grants, and quotas), and settings are all carried into the cache.
func SnapshotFromProto(in *pb.ConfigSnapshot) *ConfigSnapshot {
	b := &Snapshot{}
	if in == nil {
		return b.Done()
	}

	for _, u := range in.GetUpstreams() {
		if u == nil {
			continue
		}
		b.SetUpstream(storage.Upstream{
			ID:              u.GetId(),
			Name:            u.GetName(),
			Protocol:        u.GetProtocol(),
			BaseURL:         u.GetBaseUrl(),
			CredentialsJSON: rawJSON(u.GetCredentialsJson()),
			ModelsJSON:      rawJSON(u.GetModelsJson()),
			ProxyURL:        u.GetProxyUrl(),
			Enabled:         u.GetEnabled(),
		})
	}

	for _, r := range in.GetRoutes() {
		if r == nil {
			continue
		}
		enablePayload := r.GetEnablePayload()
		route := storage.Route{
			ID:            r.GetId(),
			Model:         r.GetModel(),
			Balance:       storage.ModelBalance(r.GetBalance()),
			EnableAuth:    r.GetEnableAuth(),
			EnablePayload: &enablePayload,
			Enabled:       r.GetEnabled(),
		}
		for _, t := range r.GetTargets() {
			if t == nil {
				continue
			}
			route.Upstreams = append(route.Upstreams, storage.RouteUpstream{
				ID: t.GetId(), RouteID: t.GetRouteId(), UpstreamID: t.GetUpstreamId(),
				Model: t.GetModel(), Weight: t.GetWeight(), Priority: t.GetPriority(),
				Enabled: t.GetEnabled(),
			})
		}
		b.SetRoute(route)
	}

	for _, c := range in.GetConsumers() {
		if c == nil {
			continue
		}
		var quotas []storage.ConsumerQuota
		for _, q := range c.GetQuotas() {
			if q == nil {
				continue
			}
			quotas = append(quotas, storage.ConsumerQuota{
				ID: q.GetId(), ConsumerID: q.GetConsumerId(), QuotaType: q.GetQuotaType(),
				QuotaLimit: q.GetQuotaLimit(), Window: q.GetWindow(),
			})
		}
		routes := append([]string(nil), c.GetRoutes()...)
		for _, k := range c.GetKeys() {
			if k == nil || k.GetKeyPreview() == "" {
				continue
			}
			b.AddConsumerKey(k.GetId(), k.GetConsumerId(), k.GetName(), k.GetKeyPreview(), k.GetKeyHash(), k.GetEnabled(), k.GetExpiresAt(), routes, quotas)
		}
	}

	for k, v := range in.GetSettings() {
		b.SetSetting(k, v)
	}

	return b.Done()
}

// SnapshotFromStorage builds a wire ConfigSnapshot by querying storage once.
// version is the config epoch to stamp on the snapshot.
func SnapshotFromStorage(s storage.Storage, version int64) (*pb.ConfigSnapshot, error) {
	out := &pb.ConfigSnapshot{Version: version, Settings: map[string]string{}}

	upstreams, err := s.Upstreams().List()
	if err != nil {
		return nil, err
	}
	for _, u := range upstreams {
		out.Upstreams = append(out.Upstreams, &pb.Upstream{
			Id: u.ID, Name: u.Name, Protocol: u.Protocol,
			BaseUrl: u.BaseURL, CredentialsJson: jsonRaw(u.CredentialsJSON),
			ModelsJson: jsonRaw(u.ModelsJSON), ProxyUrl: u.ProxyURL, Enabled: u.Enabled,
		})
	}

	routes, err := s.Routes().List()
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		enablePayload := false
		if r.EnablePayload != nil {
			enablePayload = *r.EnablePayload
		}
		pr := &pb.Route{
			Id: r.ID, Model: r.Model, Balance: string(r.Balance),
			EnableAuth: r.EnableAuth, EnablePayload: enablePayload, Enabled: r.Enabled,
		}
		for _, t := range r.Upstreams {
			pr.Targets = append(pr.Targets, &pb.RouteUpstream{
				Id: t.ID, RouteId: t.RouteID, UpstreamId: t.UpstreamID,
				Model: t.Model, Weight: t.Weight, Priority: t.Priority, Enabled: t.Enabled,
			})
		}
		out.Routes = append(out.Routes, pr)
	}

	consumers, err := s.Consumers().List()
	if err != nil {
		return nil, err
	}
	for _, c := range consumers {
		pc := &pb.Consumer{Id: c.ID, Name: c.Name, Enabled: c.Enabled, Routes: append([]string(nil), c.Routes...)}
		for _, k := range c.Keys {
			// A key is only usable when both it and its owning consumer are
			// enabled — disabling a consumer must revoke every key it owns.
			pc.Keys = append(pc.Keys, &pb.ConsumerKeyRef{
				Id: k.ID, ConsumerId: k.ConsumerID, Name: k.Name, KeyPreview: k.KeyPreview, KeyHash: k.KeyHash,
				Enabled: k.Enabled && c.Enabled, ExpiresAt: k.ExpiresAt,
			})
		}
		for _, q := range c.Quotas {
			pc.Quotas = append(pc.Quotas, &pb.ConsumerQuota{
				Id: q.ID, ConsumerId: q.ConsumerID, QuotaType: q.QuotaType,
				QuotaLimit: q.QuotaLimit, Window: q.Window,
			})
		}
		out.Consumers = append(out.Consumers, pc)
	}

	settings, err := s.Settings().ListAll()
	if err != nil {
		return nil, err
	}
	for _, kv := range settings {
		if isDataPlaneSettingKey(kv.Key) {
			out.Settings[kv.Key] = kv.Value
		}
	}

	return out, nil
}

// EpochFromStorage reads the config_epoch setting (0 if absent/unparseable).
func EpochFromStorage(s storage.Storage) int64 {
	v, _ := s.Settings().Get("config_epoch")
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// rawJSON converts a wire JSON string to json.RawMessage (empty means "not set").
func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// jsonRaw converts a json.RawMessage DTO field to the wire string.
func jsonRaw(rm json.RawMessage) string {
	if len(rm) == 0 {
		return ""
	}
	return string(rm)
}
