package storage

import (
	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/platform/state"
	"github.com/nyroway/nyro/go/internal/telemetry/schema"
)

var dataPlaneProxySettingKeys = map[string]struct{}{
	"proxy.request_timeout": {},
	"proxy.connect_timeout": {},
	"proxy.max_retries":     {},
	"proxy.retry_on_status": {},
	"proxy.max_body_bytes":  {},
}

// IsDataPlaneSettingKey reports whether key contributes to a runtime Snapshot.
func IsDataPlaneSettingKey(key string) bool {
	if _, ok := dataPlaneProxySettingKeys[key]; ok {
		return true
	}
	return schema.IsExporterSettingKey(key) || state.IsSettingKey(key)
}

// LoadSnapshot projects one complete storage read into the immutable runtime
// configuration model. Raw consumer keys are never loaded.
func LoadSnapshot(store Storage) (*configsnapshot.Snapshot, error) {
	builder := &configsnapshot.Builder{}
	upstreams, err := store.Upstreams().List()
	if err != nil {
		return nil, err
	}
	for _, upstream := range upstreams {
		builder.SetUpstream(configsnapshot.Upstream{
			ID: upstream.ID, Name: upstream.Name, Provider: upstream.Provider, Protocol: upstream.Protocol,
			BaseURL: upstream.BaseURL, CredentialsJSON: upstream.CredentialsJSON,
			ProxyURL: upstream.ProxyURL, Enabled: upstream.Enabled,
		})
	}
	routes, err := store.Routes().List()
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		targets := make([]configsnapshot.RouteTarget, 0, len(route.Upstreams))
		for _, target := range route.Upstreams {
			targets = append(targets, configsnapshot.RouteTarget{
				ID: target.ID, RouteID: target.RouteID, UpstreamID: target.UpstreamID, Model: target.Model,
				Weight: target.Weight, Priority: target.Priority, Enabled: target.Enabled,
			})
		}
		builder.SetRoute(configsnapshot.Route{
			ID: route.ID, Model: route.Model, Balance: string(route.Balance), EnableAuth: route.EnableAuth,
			EnablePayload: route.EnablePayload, Enabled: route.Enabled, Upstreams: targets,
		})
	}
	consumers, err := store.Consumers().List()
	if err != nil {
		return nil, err
	}
	for _, consumer := range consumers {
		quotas := make([]configsnapshot.ConsumerQuota, 0, len(consumer.Quotas))
		for _, quota := range consumer.Quotas {
			quotas = append(quotas, configsnapshot.ConsumerQuota{
				ID: quota.ID, ConsumerID: quota.ConsumerID, QuotaType: quota.QuotaType,
				QuotaLimit: quota.QuotaLimit, Window: quota.Window,
			})
		}
		for _, key := range consumer.Keys {
			builder.AddConsumerKey(key.ID, consumer.ID, key.Name, key.KeyPreview, key.KeyHash,
				key.Enabled && consumer.Enabled, key.ExpiresAt, consumer.Routes, quotas)
		}
	}
	settings, err := store.Settings().ListAll()
	if err != nil {
		return nil, err
	}
	for _, setting := range settings {
		if IsDataPlaneSettingKey(setting.Key) {
			builder.SetSetting(setting.Key, setting.Value)
		}
	}
	return builder.Build(), nil
}

// LoadAndSwap projects storage into a Snapshot and publishes it only after a
// complete load succeeds.
func LoadAndSwap(cache *configsnapshot.Cache, store Storage) error {
	snapshot, err := LoadSnapshot(store)
	if err != nil {
		return err
	}
	cache.Swap(snapshot)
	return nil
}
