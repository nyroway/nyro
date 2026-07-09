package admin

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func TestNewCmdFlags(t *testing.T) {
	cmd := NewCmd()
	if addr, _ := cmd.Flags().GetString("addr"); addr != "127.0.0.1:19531" {
		t.Errorf("default addr = %q, want 127.0.0.1:19531", addr)
	}
	if cmd.Use != "admin" {
		t.Errorf("Use = %q, want admin", cmd.Use)
	}
}

func TestNewCmdStorageFlagDefaults(t *testing.T) {
	cmd := NewCmd()
	if v, _ := cmd.Flags().GetString("storage"); v != "sqlite" {
		t.Errorf("default storage = %q, want sqlite", v)
	}
	if v, _ := cmd.Flags().GetString("db-dsn"); v != "" {
		t.Errorf("default db-dsn = %q, want empty (resolved at RunE time)", v)
	}
}

func TestRunE_RejectsMemoryStorage(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--storage", "memory"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error rejecting --storage memory, got nil")
	}
}

func TestRunE_RejectsUnknownStorage(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--storage", "bogus"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error rejecting --storage bogus, got nil")
	}
}

func TestNewCmdObsDataDirFlagDefault(t *testing.T) {
	cmd := NewCmd()
	if v, _ := cmd.Flags().GetString("obs-data-dir"); v != "./data/obs" {
		t.Errorf("default obs-data-dir = %q, want ./data/obs", v)
	}
}

func newMemStore(t *testing.T) storage.Storage {
	t.Helper()
	return memory.New().Storage()
}

// ── migrateLegacyObsSettings ──

func TestMigrateLegacyObsSettings_NoOldKeys_NoOp(t *testing.T) {
	st := newMemStore(t)
	migrateLegacyObsSettings(st.Settings())

	rows, err := st.Settings().ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no settings written on no-op migration, got %+v", rows)
	}
}

func TestMigrateLegacyObsSettings_StdoutSinks_MigratedAndOldKeysCleared(t *testing.T) {
	st := newMemStore(t)
	set := func(k, v string) {
		if err := st.Settings().Set(k, v); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
	set("obs_logs_sink", "stdout")
	set("obs_metrics_sink", "none") // explicit sentinel -> should not produce a new exporter key
	set("obs_traces_sink", "stdout")
	set("obs_sink", "stdout")
	set("obs_export_interval", "15s")

	migrateLegacyObsSettings(st.Settings())

	assertGet(t, st, "obs_logs_exporter", "stdout")
	assertGet(t, st, "obs_traces_exporter", "stdout")
	assertGet(t, st, "obs_metrics_exporter", "") // "none" sink migrates to nothing (empty = off)

	// Old keys must be cleared (Get returns "" — the store has no Delete, so
	// "cleared" means Set to "", which Get cannot distinguish from absent).
	for _, k := range []string{"obs_logs_sink", "obs_metrics_sink", "obs_traces_sink", "obs_sink", "obs_export_interval"} {
		assertGet(t, st, k, "")
	}
}

func TestMigrateLegacyObsSettings_OTLPSinkCopiesEndpointAndProtocol(t *testing.T) {
	st := newMemStore(t)
	set := func(k, v string) {
		if err := st.Settings().Set(k, v); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
	set("obs_logs_sink", "otlp")
	set("obs_metrics_sink", "otlp")
	set("obs_traces_sink", "otlp")
	set("obs_otlp_endpoint", "http://collector:4318")
	set("obs_traces_protocol", "grpc")
	set("obs_metrics_path", "/custom/metrics") // dead key, must be dropped, no destination

	migrateLegacyObsSettings(st.Settings())

	for _, signal := range []string{"logs", "metrics", "traces"} {
		assertGet(t, st, "obs_"+signal+"_exporter", "otlp")
		assertGet(t, st, "obs_"+signal+"_otlp_endpoint", "http://collector:4318")
	}
	assertGet(t, st, "obs_traces_otlp_protocol", "grpc")

	for _, k := range []string{
		"obs_logs_sink", "obs_metrics_sink", "obs_traces_sink",
		"obs_otlp_endpoint", "obs_metrics_path", "obs_traces_protocol",
	} {
		assertGet(t, st, k, "")
	}
}

func TestMigrateLegacyObsSettings_Idempotent(t *testing.T) {
	st := newMemStore(t)
	if err := st.Settings().Set("obs_logs_sink", "otlp"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := st.Settings().Set("obs_otlp_endpoint", "http://collector:4318"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	migrateLegacyObsSettings(st.Settings())
	assertGet(t, st, "obs_logs_exporter", "otlp")
	assertGet(t, st, "obs_logs_otlp_endpoint", "http://collector:4318")

	// Second run: old keys already cleared, must be a true no-op — the
	// already-migrated new keys must survive untouched.
	migrateLegacyObsSettings(st.Settings())
	assertGet(t, st, "obs_logs_exporter", "otlp")
	assertGet(t, st, "obs_logs_otlp_endpoint", "http://collector:4318")
}

// ── seedDefaultObsEndpoint ──

func TestSeedDefaultObsEndpoint_FullyEmpty_SeedsAllThree(t *testing.T) {
	st := newMemStore(t)
	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")

	for _, signal := range []string{"logs", "metrics", "traces"} {
		assertGet(t, st, "obs_"+signal+"_otlp_endpoint", "127.0.0.1:19531")
		assertGet(t, st, "obs_"+signal+"_exporter", "otlp")
	}
}

func TestSeedDefaultObsEndpoint_PartialConfig_NotOverwritten(t *testing.T) {
	st := newMemStore(t)
	// Simulate: nothing configured yet (all endpoints empty) but the user has
	// already picked stdout for logs explicitly.
	if err := st.Settings().Set("obs_logs_exporter", "stdout"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")

	// logs exporter must be left alone (already configured).
	assertGet(t, st, "obs_logs_exporter", "stdout")
	// but its endpoint is still seeded (only exporter is exempted from overwrite).
	assertGet(t, st, "obs_logs_otlp_endpoint", "127.0.0.1:19531")
	assertGet(t, st, "obs_metrics_exporter", "otlp")
	assertGet(t, st, "obs_traces_exporter", "otlp")
}

func TestSeedDefaultObsEndpoint_AlreadyConfigured_NoOp(t *testing.T) {
	st := newMemStore(t)
	if err := st.Settings().Set("obs_metrics_otlp_endpoint", "http://external:4318"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")

	assertGet(t, st, "obs_metrics_otlp_endpoint", "http://external:4318")
	assertGet(t, st, "obs_logs_otlp_endpoint", "")
	assertGet(t, st, "obs_traces_otlp_endpoint", "")
	assertGet(t, st, "obs_logs_exporter", "")
}

func TestSeedDefaultObsEndpoint_Idempotent(t *testing.T) {
	st := newMemStore(t)
	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")
	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")

	for _, signal := range []string{"logs", "metrics", "traces"} {
		assertGet(t, st, "obs_"+signal+"_otlp_endpoint", "127.0.0.1:19531")
		assertGet(t, st, "obs_"+signal+"_exporter", "otlp")
	}
}

func assertGet(t *testing.T, st storage.Storage, key, want string) {
	t.Helper()
	got, err := st.Settings().Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if got != want {
		t.Errorf("Get(%q) = %q, want %q", key, got, want)
	}
}
