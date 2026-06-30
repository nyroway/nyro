package xds

import (
	"strconv"

	"github.com/nyroway/nyro/go/internal/storage"
	pb "github.com/nyroway/nyro/go/internal/xds/pb/xds/v1"
)

// SnapshotFromProto converts a wire ConfigSnapshot into the gateway's internal
// read model. Providers, models (with targets), API keys + bindings, settings,
// and OAuth credentials are all carried into the cache (P3b).
func SnapshotFromProto(in *pb.ConfigSnapshot) *ConfigSnapshot {
	snap := &ConfigSnapshot{
		providers: map[string]storage.Provider{},
		models:    map[string]storage.Model{},
		apikeys:   map[string]storage.ApiKeyAccessRecord{},
		bindings:  map[string]map[string]bool{},
		settings:  map[string]string{},
		oauth:     map[string]storage.OAuthCredential{},
	}
	if in == nil {
		return snap
	}

	for _, p := range in.GetProviders() {
		if p == nil {
			continue
		}
		snap.providers[p.GetId()] = storage.Provider{
			ID:           p.GetId(),
			Name:         p.GetName(),
			Vendor:       p.GetVendor(),
			Protocol:     p.GetProtocol(),
			BaseURL:      p.GetBaseUrl(),
			PresetKey:    p.GetPresetKey(),
			Channel:      p.GetChannel(),
			ModelsSource: p.GetModelsSource(),
			StaticModels: p.GetStaticModels(),
			APIKey:       p.GetApiKey(),
			AuthMode:     p.GetAuthMode(),
			UseProxy:     p.GetUseProxy(),
			IsEnabled:    p.GetIsEnabled(),
		}
	}

	for _, m := range in.GetModels() {
		if m == nil {
			continue
		}
		model := storage.Model{
			ID:         m.GetId(),
			Name:       m.GetName(),
			Balance:    storage.ModelBalance(m.GetBalance()),
			EnableAuth: m.GetEnableAuth(),
			IsEnabled:  m.GetIsEnabled(),
		}
		if len(m.GetTargets()) > 0 {
			model.Targets = make([]storage.ModelBackend, 0, len(m.GetTargets()))
			for _, t := range m.GetTargets() {
				if t == nil {
					continue
				}
				model.Targets = append(model.Targets, storage.ModelBackend{
					ID:         t.GetId(),
					ModelID:    t.GetModelId(),
					ProviderID: t.GetProviderId(),
					Model:      t.GetModel(),
					Weight:     t.GetWeight(),
					Priority:   t.GetPriority(),
				})
			}
		}
		snap.models[m.GetName()] = model
	}

	for _, k := range in.GetApiKeys() {
		if k == nil || k.GetToken() == "" {
			continue
		}
		snap.apikeys[k.GetToken()] = storage.ApiKeyAccessRecord{
			ID:        k.GetId(),
			Name:      k.GetName(),
			IsEnabled: k.GetIsEnabled(),
			ExpiresAt: k.GetExpiresAt(),
			RPM:       int32Ptr(k.GetRpm(), k.Rpm),
			RPD:       int32Ptr(k.GetRpd(), k.Rpd),
			TPM:       int32Ptr(k.GetTpm(), k.Tpm),
			TPD:       int32Ptr(k.GetTpd(), k.Tpd),
		}
		set := map[string]bool{}
		for _, mid := range k.GetBoundModelIds() {
			set[mid] = true
		}
		snap.bindings[k.GetId()] = set
	}

	for k, v := range in.GetSettings() {
		snap.settings[k] = v
	}

	for _, o := range in.GetOauthCredentials() {
		if o == nil || o.GetProviderId() == "" {
			continue
		}
		snap.oauth[o.GetProviderId()] = storage.OAuthCredential{
			ProviderID:    o.GetProviderId(),
			DriverKey:     o.GetDriverKey(),
			Scheme:        o.GetScheme(),
			AccessToken:   o.GetAccessToken(),
			RefreshToken:  o.GetRefreshToken(),
			ExpiresAt:     o.GetExpiresAt(),
			Status:        o.GetStatus(),
			StatusVersion: o.GetStatusVersion(),
		}
	}

	return snap
}

// SnapshotFromStorage builds a wire ConfigSnapshot by querying storage once.
// version is the config epoch to stamp on the snapshot. OAuth credentials are
// included so future phases can apply them on the gateway (P2 ignores them).
func SnapshotFromStorage(s storage.Storage, version int64) (*pb.ConfigSnapshot, error) {
	out := &pb.ConfigSnapshot{Version: version, Settings: map[string]string{}}

	providers, err := s.Providers().List()
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		out.Providers = append(out.Providers, &pb.Provider{
			Id: p.ID, Name: p.Name, Vendor: p.Vendor, Protocol: p.Protocol,
			BaseUrl: p.BaseURL, PresetKey: p.PresetKey, Channel: p.Channel,
			ModelsSource: p.ModelsSource, StaticModels: p.StaticModels,
			ApiKey: p.APIKey, AuthMode: p.AuthMode, UseProxy: p.UseProxy, IsEnabled: p.IsEnabled,
		})
	}

	models, err := s.Models().List()
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		pm := &pb.Model{
			Id: m.ID, Name: m.Name, Balance: string(m.Balance),
			EnableAuth: m.EnableAuth, IsEnabled: m.IsEnabled,
		}
		for _, t := range m.Targets {
			pm.Targets = append(pm.Targets, &pb.ModelBackend{
				Id: t.ID, ModelId: t.ModelID, ProviderId: t.ProviderID,
				Model: t.Model, Weight: t.Weight, Priority: t.Priority,
			})
		}
		out.Models = append(out.Models, pm)
	}

	keys, err := s.APIKeys().List()
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		out.ApiKeys = append(out.ApiKeys, &pb.ApiKey{
			Id: k.ID, Token: k.Token, Name: k.Name, IsEnabled: k.IsEnabled,
			ExpiresAt: k.ExpiresAt, Rpm: k.RPM, Rpd: k.RPD, Tpm: k.TPM, Tpd: k.TPD,
			BoundModelIds: append([]string(nil), k.ModelIDs...),
		})
	}

	// OAuth credentials are populated via ListAll so the gateway receives the
	// full set in one snapshot (P3b) and can refresh locally without DB reads.
	creds, err := s.OAuthCredentials().ListAll()
	if err != nil {
		return nil, err
	}
	for _, c := range creds {
		out.OauthCredentials = append(out.OauthCredentials, &pb.OAuthCredential{
			ProviderId: c.ProviderID, DriverKey: c.DriverKey, Scheme: c.Scheme,
			AccessToken: c.AccessToken, RefreshToken: c.RefreshToken, ExpiresAt: c.ExpiresAt,
			Status: c.Status, StatusVersion: c.StatusVersion,
		})
	}

	settings, err := s.Settings().ListAll()
	if err != nil {
		return nil, err
	}
	for _, kv := range settings {
		out.Settings[kv.Key] = kv.Value
	}

	return out, nil
}

// EpochFromStorage reads the config_epoch setting (0 if absent/unparseable).
func EpochFromStorage(s storage.Storage) int64 {
	v, _ := s.Settings().Get("config_epoch")
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// int32Ptr returns a pointer to v when the source oneof field was set (has).
// When has is nil (field absent), returns nil so "unset" round-trips.
func int32Ptr(v int32, has *int32) *int32 {
	if has == nil {
		return nil
	}
	x := v
	return &x
}
