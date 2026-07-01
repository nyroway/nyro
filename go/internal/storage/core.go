package storage

// CoreStorage is the config-schema storage aggregate: upstreams/routes/consumers
// over the new tables. It coexists with the legacy Storage interface until
// callers (admin/xDS/proxy/config) migrate; implementations are added in a
// later step. This interface is the target contract for that migration.
type CoreStorage interface {
	Upstreams() UpstreamStore
	Routes() RouteStore
	Consumers() ConsumerStore
	Auth() KeyAuthStore
	Settings() CoreSettingsStore
	Bootstrap() Bootstrap
}
