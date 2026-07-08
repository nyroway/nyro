package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func TestLoadYAMLAndApplyTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nyro.yaml")
	const yaml = `
version: 1
settings:
  proxy:
    request_timeout: "45s"
    max_retries: 3
  observability:
    logs:
      exporter: stdout
upstreams:
  - name: openai
    provider: openai
    protocol: openai-chat
    base_url: https://api.openai.com
    credentials:
      api_key: sk-***
routes:
  - model: gpt-4o
    upstreams:
      - {name: openai, model: gpt-4o}
consumers:
  - name: local
    keys:
      - {name: primary, api_key: nyro-secret}
    access:
      models: [gpt-4o]
    quotas:
      requests:
        - {limit: 60, window: "1m"}
      concurrency: {limit: 10}
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].Name != "openai" {
		t.Errorf("upstreams parsed wrong: %+v", cfg.Upstreams)
	}

	st := memory.New()
	core := st.Storage()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}

	// upstream seeded
	ups, _ := core.Upstreams().List()
	if len(ups) != 1 || ups[0].Name != "openai" {
		t.Errorf("upstream not seeded: %+v", ups)
	}

	// route seeded with a target on the upstream
	routes, _ := core.Routes().List()
	if len(routes) != 1 || routes[0].Model != "gpt-4o" {
		t.Errorf("route not seeded: %+v", routes)
	}
	if len(routes[0].Upstreams) != 1 || routes[0].Upstreams[0].UpstreamID != ups[0].ID {
		t.Errorf("route target binding wrong: %+v", routes[0].Upstreams)
	}

	// consumer with explicit token, route grant, and quotas
	consumers, _ := core.Consumers().List()
	if len(consumers) != 1 || len(consumers[0].Keys) != 1 {
		t.Fatalf("consumer not seeded: %+v", consumers)
	}
	if len(consumers[0].Routes) != 1 || consumers[0].Routes[0] != "gpt-4o" {
		t.Errorf("consumer route grant wrong: %+v", consumers[0].Routes)
	}
	if len(consumers[0].Quotas) != 2 {
		t.Fatalf("consumer quotas wrong: %+v", consumers[0].Quotas)
	}
	var sawRequests, sawConcurrency bool
	for _, q := range consumers[0].Quotas {
		switch q.QuotaType {
		case "requests":
			sawRequests = q.QuotaLimit == 60 && q.Window == "1m"
		case "concurrency":
			sawConcurrency = q.QuotaLimit == 10 && q.Window == ""
		}
	}
	if !sawRequests {
		t.Errorf("requests quota missing/wrong: %+v", consumers[0].Quotas)
	}
	if !sawConcurrency {
		t.Errorf("concurrency quota missing/wrong: %+v", consumers[0].Quotas)
	}
	rec, _ := core.Auth().FindKey("nyro-secret")
	if rec == nil {
		t.Error("explicit token not discoverable after ApplyTo")
	}

	// settings flattened: proxy.* stored verbatim, observability.* mapped onto obs_* keys.
	if v, _ := core.Settings().Get("proxy.request_timeout"); v != "45s" {
		t.Errorf("proxy.request_timeout = %q, want 45s", v)
	}
	if v, _ := core.Settings().Get("proxy.max_retries"); v != "3" {
		t.Errorf("proxy.max_retries = %q, want 3", v)
	}
	if v, _ := core.Settings().Get("obs_logs_sink"); v != "stdout" {
		t.Errorf("obs_logs_sink = %q, want stdout", v)
	}
}

// TestApplyTo_ProviderTemplateExpansion verifies that an UpstreamSpec setting
// only `provider` (plus credentials) expands to the vendor's default
// protocol/base_url at ApplyTo time. Provider is also persisted verbatim (it
// anchors the control-plane preset, the model discovery fallback, and the
// outbound auth scheme lookup).
func TestApplyTo_ProviderTemplateExpansion(t *testing.T) {
	cfg := &Config{
		Upstreams: []UpstreamSpec{{
			Name: "openai", Provider: "openai", Credentials: map[string]string{"api_key": "sk-x"},
		}},
	}
	st := memory.New()
	core := st.Storage()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	ups, _ := core.Upstreams().List()
	if len(ups) != 1 {
		t.Fatalf("upstream not seeded: %+v", ups)
	}
	u := ups[0]
	if u.Provider != "openai" {
		t.Errorf("Provider = %q, want openai to be persisted", u.Provider)
	}
	if u.Protocol != "openai-chat" {
		t.Errorf("Protocol = %q, want openai-chat (from provider default)", u.Protocol)
	}
	if u.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want https://api.openai.com/v1 (from provider default)", u.BaseURL)
	}
}

// TestApplyTo_ProviderTemplateExplicitProtocolWins verifies that an explicit
// `protocol` in yaml always wins over the provider template's default — the
// template only fills in what's left empty.
func TestApplyTo_ProviderTemplateExplicitProtocolWins(t *testing.T) {
	cfg := &Config{
		Upstreams: []UpstreamSpec{{
			Name: "openai", Provider: "openai", Protocol: "openai-responses",
			Credentials: map[string]string{"api_key": "sk-x"},
		}},
	}
	st := memory.New()
	core := st.Storage()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	ups, _ := core.Upstreams().List()
	if len(ups) != 1 {
		t.Fatalf("upstream not seeded: %+v", ups)
	}
	u := ups[0]
	if u.Protocol != "openai-responses" {
		t.Errorf("Protocol = %q, want explicit openai-responses to win over provider default", u.Protocol)
	}
	// base_url still filled in from the provider def for the explicit protocol.
	if u.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want https://api.openai.com/v1 (from provider default for openai-responses)", u.BaseURL)
	}
}

func TestApplyTo_NormalizesProtocolAlias(t *testing.T) {
	cfg := &Config{
		Upstreams: []UpstreamSpec{{
			Name: "openai", Provider: "openai", Protocol: "openai-resp",
			Credentials: map[string]string{"api_key": "sk-x"},
		}},
	}
	st := memory.New()
	core := st.Storage()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	ups, _ := core.Upstreams().List()
	if len(ups) != 1 {
		t.Fatalf("upstream not seeded: %+v", ups)
	}
	u := ups[0]
	if u.Protocol != "openai-responses" {
		t.Errorf("Protocol = %q, want alias normalized to openai-responses", u.Protocol)
	}
	if u.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want provider default for normalized protocol", u.BaseURL)
	}
}

func TestApplyTo_RequiresProvider(t *testing.T) {
	cfg := &Config{Upstreams: []UpstreamSpec{{Name: "x"}}}
	if err := cfg.ApplyTo(memory.New().Storage()); err == nil {
		t.Error("expected error for missing provider")
	}
}

func TestApplyTo_ModelsAndModelsURLMutuallyExclusive(t *testing.T) {
	cfg := &Config{Upstreams: []UpstreamSpec{{
		Name: "x", Provider: "openai", Models: []string{"gpt-4o"}, ModelsURL: "https://x/models",
	}}}
	if err := cfg.ApplyTo(memory.New().Storage()); err == nil {
		t.Error("expected error for models + models_url both set")
	}
}

func TestApplyTo_UnknownProvider(t *testing.T) {
	cfg := &Config{Upstreams: []UpstreamSpec{{Name: "x", Provider: "nope"}}}
	if err := cfg.ApplyTo(memory.New().Storage()); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestApplyTo_CustomRequiresBaseURLAndModelSource(t *testing.T) {
	cases := []struct {
		name string
		spec UpstreamSpec
	}{
		{"missing base_url", UpstreamSpec{Name: "x", Provider: "custom", ModelsURL: "https://x/models"}},
		{"missing model source", UpstreamSpec{Name: "x", Provider: "custom", BaseURL: "https://x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Upstreams: []UpstreamSpec{tc.spec}}
			if err := cfg.ApplyTo(memory.New().Storage()); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestApplyTo_CustomWithBaseURLAndModels(t *testing.T) {
	cfg := &Config{Upstreams: []UpstreamSpec{{
		Name: "x", Provider: "custom", BaseURL: "https://x", Models: []string{"m1"},
	}}}
	st := memory.New()
	core := st.Storage()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	got, _ := core.Upstreams().List()
	if len(got) != 1 || got[0].Provider != "custom" || got[0].BaseURL != "https://x" {
		t.Errorf("upstream not seeded as expected: %+v", got)
	}
}

func TestApplyTo_PersistsModelsURL(t *testing.T) {
	cfg := &Config{Upstreams: []UpstreamSpec{{
		Name: "x", Provider: "openai", ModelsURL: "https://api.openai.com/v1/models", Credentials: map[string]string{"api_key": "sk-x"},
	}}}
	st := memory.New()
	core := st.Storage()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	ups, _ := core.Upstreams().List()
	if len(ups) != 1 || ups[0].ModelsURL != "https://api.openai.com/v1/models" {
		t.Errorf("models_url not persisted: %+v", ups)
	}
	if len(ups[0].ModelsJSON) != 0 {
		t.Errorf("models_json should be empty when models_url is set: %+v", ups[0].ModelsJSON)
	}
}

func TestApplyToUnknownUpstream(t *testing.T) {
	cfg := &Config{
		Routes: []RouteSpec{{Model: "m", Upstreams: []RouteUpstreamSpec{{Name: "nope", Model: "x"}}}},
	}
	if err := cfg.ApplyTo(memory.New().Storage()); err == nil {
		t.Error("expected error for unknown upstream reference")
	}
}

func TestApplyToUnknownRoute(t *testing.T) {
	cfg := &Config{
		Consumers: []ConsumerSpec{{Name: "c", Access: ConsumerAccessSpec{Models: []string{"nope"}}}},
	}
	if err := cfg.ApplyTo(memory.New().Storage()); err == nil {
		t.Error("expected error for unknown route reference")
	}
}

func TestBuildSnapshot_BuildsReadableSnapshot(t *testing.T) {
	cfg := &Config{
		Upstreams: []UpstreamSpec{{
			Name: "openai", Provider: "openai", Protocol: "openai-chat",
			BaseURL: "https://api.openai.com", Credentials: map[string]string{"api_key": "sk-x"},
		}},
		Routes: []RouteSpec{{
			Model: "gpt-4o", Upstreams: []RouteUpstreamSpec{{Name: "openai", Model: "gpt-4o"}},
		}},
		Consumers: []ConsumerSpec{{
			Name: "local", Keys: []ConsumerKeySpec{{Name: "primary", APIKey: "nyro-secret"}},
			Access: ConsumerAccessSpec{Models: []string{"gpt-4o"}},
		}},
	}
	snap, err := cfg.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	routes := snap.RoutesList()
	if len(routes) != 1 || routes[0].Model != "gpt-4o" {
		t.Fatalf("route missing/wrong: %+v", routes)
	}
	rt := snap.RouteByModel("gpt-4o")
	if rt == nil || len(rt.Upstreams) != 1 {
		t.Fatalf("route target missing: %+v", rt)
	}
	u := snap.UpstreamGet(rt.Upstreams[0].UpstreamID)
	if u == nil || u.BaseURL != "https://api.openai.com" || string(u.CredentialsJSON) != `{"api_key":"sk-x"}` {
		t.Errorf("upstream missing/wrong: %+v", u)
	}

	rec := snap.FindKey("nyro-secret")
	if rec == nil {
		t.Fatalf("key missing")
	}
	if len(rec.Routes) != 1 || rec.Routes[0] != "gpt-4o" {
		t.Errorf("route grant missing/wrong: %+v", rec.Routes)
	}
}

func TestBuildSnapshot_UnknownRefs(t *testing.T) {
	cfg := &Config{Routes: []RouteSpec{{Model: "m", Upstreams: []RouteUpstreamSpec{{Name: "nope", Model: "x"}}}}}
	if _, err := cfg.BuildSnapshot(); err == nil {
		t.Error("expected error for unknown upstream reference")
	}
}

func TestConsumerQuotas_ExpandsAllCategories(t *testing.T) {
	q := ConsumerQuotasSpec{
		Requests:    []QuotaLimitSpec{{Limit: 60, Window: "1m"}, {Limit: 10000, Window: "1d"}},
		Tokens:      []QuotaLimitSpec{{Limit: 100000, Window: "1m"}},
		Concurrency: &ConsumerConcurrencySpec{Limit: 10},
		Budgets:     []BudgetLimitSpec{{Limit: 100, Window: "1mo", Currency: "USD"}},
	}
	got := consumerQuotas(q)
	if len(got) != 5 {
		t.Fatalf("consumerQuotas() = %d rows, want 5: %+v", len(got), got)
	}
	var requestsCount, tokensCount, concurrencyCount, budgetCount int
	for _, row := range got {
		switch row.QuotaType {
		case "requests":
			requestsCount++
		case "tokens":
			tokensCount++
			if row.QuotaLimit != 100000 || row.Window != "1m" {
				t.Errorf("tokens row wrong: %+v", row)
			}
		case "concurrency":
			concurrencyCount++
			if row.QuotaLimit != 10 || row.Window != "" {
				t.Errorf("concurrency row wrong (window must be empty): %+v", row)
			}
		case "budget":
			budgetCount++
			if row.QuotaLimit != 100 || row.Window != "1mo" || row.Currency != "USD" {
				t.Errorf("budget row wrong: %+v", row)
			}
		}
	}
	if requestsCount != 2 {
		t.Errorf("requests rows = %d, want 2", requestsCount)
	}
	if tokensCount != 1 {
		t.Errorf("tokens rows = %d, want 1", tokensCount)
	}
	if concurrencyCount != 1 {
		t.Errorf("concurrency rows = %d, want 1", concurrencyCount)
	}
	if budgetCount != 1 {
		t.Errorf("budget rows = %d, want 1", budgetCount)
	}
}

func TestFlattenSettings(t *testing.T) {
	s := SettingsSpec{
		Server: ServerSpec{Listen: "127.0.0.1:19530", BaseURL: "http://127.0.0.1:19530"},
		Proxy:  ProxySpec{RequestTimeout: "120s", MaxRetries: 2, RetryOnStatus: []int{429, 500}},
		Observability: ObservabilitySpec{
			Logs:   ObservabilityLogsSpec{Exporter: "stdout"},
			Traces: ObservabilityTracesSpec{Exporter: "otlp", Endpoint: "http://127.0.0.1:4317"},
		},
	}
	got := flattenSettings(s)

	want := map[string]string{
		"server.listen":         "127.0.0.1:19530",
		"server.base_url":       "http://127.0.0.1:19530",
		"proxy.request_timeout": "120s",
		"proxy.max_retries":     "2",
		"proxy.retry_on_status": "[429,500]",
		"obs_logs_sink":         "stdout",
		"obs_traces_sink":       "otlp",
		"obs_otlp_endpoint":     "http://127.0.0.1:4317",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("flattenSettings()[%q] = %q, want %q", k, got[k], v)
		}
	}
	// Absent values must not produce empty-string rows.
	if _, ok := got["obs_metrics_sink"]; ok {
		t.Error("unset observability.metrics.exporter should not appear in flattened settings")
	}
}

func TestLoadYAMLExpandsEnvVars(t *testing.T) {
	t.Setenv("NYRO_TEST_API_KEY", "sk-from-env")

	dir := t.TempDir()
	path := filepath.Join(dir, "nyro.yaml")
	const yamlTmpl = `
version: 1
upstreams:
  - name: openai
    provider: openai
    protocol: openai-chat
    base_url: https://api.openai.com
    credentials:
      api_key: "${NYRO_TEST_API_KEY}"
routes: []
consumers: []
`
	if err := os.WriteFile(path, []byte(yamlTmpl), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, missing, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none (NYRO_TEST_API_KEY is set)", missing)
	}
	if got := cfg.Upstreams[0].Credentials["api_key"]; got != "sk-from-env" {
		t.Errorf("api_key = %q, want expanded env value %q", got, "sk-from-env")
	}
}

func TestLoadYAMLReportsUnsetEnvVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nyro.yaml")
	const yamlTmpl = `
version: 1
upstreams:
  - name: openai
    provider: openai
    protocol: openai-chat
    base_url: https://api.openai.com
    credentials:
      api_key: "${NYRO_TEST_DEFINITELY_UNSET_VAR}"
routes: []
consumers: []
`
	if err := os.WriteFile(path, []byte(yamlTmpl), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, missing, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML: %v (unset vars must warn, not fail)", err)
	}
	if len(missing) != 1 || missing[0] != "NYRO_TEST_DEFINITELY_UNSET_VAR" {
		t.Errorf("missing = %v, want [NYRO_TEST_DEFINITELY_UNSET_VAR]", missing)
	}
	if got := cfg.Upstreams[0].Credentials["api_key"]; got != "" {
		t.Errorf("api_key = %q, want empty string for an unset var", got)
	}
}
