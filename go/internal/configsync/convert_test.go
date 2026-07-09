package configsync

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage/memory"
	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
)

// protoRoundtrip builds a pb snapshot, converts it to the internal model, and
// returns the internal snapshot for assertions.
func protoRoundtrip(t *testing.T, in *pb.ConfigSnapshot) *ConfigSnapshot {
	t.Helper()
	got := SnapshotFromProto(in)
	if got == nil {
		t.Fatal("SnapshotFromProto returned nil")
	}
	return got
}

func TestSnapshotFromProto_NilSafe(t *testing.T) {
	snap := SnapshotFromProto(nil)
	if snap == nil || snap.RouteByModel("anything") != nil {
		t.Error("nil input should yield an empty-but-usable snapshot")
	}
}

func TestSnapshotFromProto_Upstreams(t *testing.T) {
	in := &pb.ConfigSnapshot{
		Upstreams: []*pb.Upstream{{
			Id: "up-1", Name: "openai-main", Protocol: "openai-chat",
			BaseUrl: "https://api.openai.com", CredentialsJson: `{"api_key":"sk-x"}`,
			ProxyUrl: "http://proxy.local", Enabled: true,
		}},
	}
	snap := protoRoundtrip(t, in)
	u := snap.UpstreamGet("up-1")
	if u == nil || u.Name != "openai-main" || !u.Enabled || string(u.CredentialsJSON) != `{"api_key":"sk-x"}` {
		t.Errorf("upstream not carried: %+v", u)
	}
	if snap.UpstreamGet("missing") != nil {
		t.Error("missing upstream should be nil")
	}
}

func TestSnapshotFromProto_RoutesWithTargets(t *testing.T) {
	in := &pb.ConfigSnapshot{
		Routes: []*pb.Route{{
			Id: "r-1", Model: "gpt-4", Balance: "weighted", EnableAuth: true, Enabled: true,
			Targets: []*pb.RouteUpstream{{
				Id: "t-1", RouteId: "r-1", UpstreamId: "up-1", Model: "gpt-4o", Weight: 3, Priority: 1, Enabled: true,
			}},
		}},
	}
	snap := protoRoundtrip(t, in)
	r := snap.RouteByModel("gpt-4")
	if r == nil || !r.EnableAuth || string(r.Balance) != "weighted" {
		t.Errorf("route fields not carried: %+v", r)
	}
	if len(r.Upstreams) != 1 || r.Upstreams[0].UpstreamID != "up-1" || r.Upstreams[0].Weight != 3 {
		t.Errorf("targets not carried: %+v", r.Upstreams)
	}
}

func TestSnapshotFromProto_ConsumerKeysAndGrants(t *testing.T) {
	in := &pb.ConfigSnapshot{
		Consumers: []*pb.Consumer{{
			Id: "c-1", Name: "alice", Enabled: true,
			Keys: []*pb.ConsumerKeyRef{{
				Id: "k-1", ConsumerId: "c-1", KeyPreview: "sk-abcd", KeyHash: "deadbeef", Enabled: true,
			}, {
				Id: "k-2", ConsumerId: "c-1", KeyPreview: "", KeyHash: "x", // empty preview dropped
			}},
			Routes: []string{"m-1", "m-2"},
			Quotas: []*pb.ConsumerQuota{{
				Id: "q-1", ConsumerId: "c-1", QuotaType: "requests", QuotaLimit: 60, Window: "1m",
			}},
		}},
	}
	snap := protoRoundtrip(t, in)

	entries := snap.keysByPreview["sk-abcd"]
	if len(entries) != 1 || entries[0].ConsumerID != "c-1" || entries[0].KeyHash != "deadbeef" {
		t.Errorf("consumer key not carried: %+v", entries)
	}
	if len(entries[0].Routes) != 2 {
		t.Errorf("route grants not carried: %+v", entries[0].Routes)
	}
	if len(entries[0].Quotas) != 1 || entries[0].Quotas[0].QuotaLimit != 60 {
		t.Errorf("quotas not carried: %+v", entries[0].Quotas)
	}
	if _, ok := snap.keysByPreview[""]; ok {
		t.Error("empty key_preview should not be stored")
	}
}

func TestSnapshotFromProto_Settings(t *testing.T) {
	in := &pb.ConfigSnapshot{Settings: map[string]string{"proxy_url": "http://p:8080"}}
	snap := protoRoundtrip(t, in)
	if v, ok := snap.SettingGet("proxy_url"); !ok || v != "http://p:8080" {
		t.Errorf("setting not carried: %q %v", v, ok)
	}
	if _, ok := snap.SettingGet("absent"); ok {
		t.Error("absent setting should be ok=false")
	}
}

func TestSnapshotFromStorage_RoundtripsThroughProto(t *testing.T) {
	// Build storage, build pb snapshot, convert to internal, assert equivalence
	// with the direct LoadFromStorage path.
	st, u, rOpen, _, _, rawKey := newPopulatedStorage(t)

	pbSnap, err := SnapshotFromStorage(st.Storage(), 7)
	if err != nil {
		t.Fatalf("SnapshotFromStorage: %v", err)
	}
	if pbSnap.GetVersion() != 7 {
		t.Errorf("version = %d; want 7", pbSnap.GetVersion())
	}

	// Direct load for comparison.
	direct, err := LoadFromStorage(st.Storage())
	if err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}

	got := SnapshotFromProto(pbSnap)

	// upstreams
	if gu := got.UpstreamGet(u.ID); gu == nil || gu.BaseURL != u.BaseURL {
		t.Errorf("upstream via proto path mismatch: %+v", gu)
	}
	// routes with targets
	gr := got.RouteByModel(rOpen.Model)
	dr := direct.RouteByModel(rOpen.Model)
	if gr == nil || len(gr.Upstreams) != len(dr.Upstreams) {
		t.Errorf("route targets mismatch: got %d want %d", len(gr.Upstreams), len(dr.Upstreams))
	}
	// consumer keys
	if gk := got.FindKey(rawKey); gk == nil {
		t.Error("key via proto path mismatch: not found")
	}
	// settings
	if v, ok := got.SettingGet("proxy_url"); !ok || v == "" {
		t.Errorf("setting via proto path missing: %q %v", v, ok)
	}
}

// TestSnapshotFromStorage_CarriesObservabilitySettings is a targeted
// end-to-end check for the new per-signal observability setting keys
// (obs_<signal>_exporter, obs_<signal>_<engine>_<field>) through the full
// settings-push path: storage.Settings().Set -> LoadFromStorage/ListAll ->
// SetSetting -> SnapshotFromStorage (proto) -> SnapshotFromProto -> SettingGet.
// Existing settings coverage (TestLoadFromStorage_BuildsAllMaps,
// TestSnapshotFromProto_Settings, TestSnapshotFromStorage_RoundtripsThroughProto)
// only exercises generic keys like proxy_url; this proves the new obs key
// shapes specifically survive the same pipeline unmodified, with no
// config-sync changes required for them (as the plan calls for).
func TestSnapshotFromStorage_CarriesObservabilitySettings(t *testing.T) {
	st := memory.New()
	core := st.Storage()

	obsSettings := map[string]string{
		"obs_logs_exporter":             "stdout",
		"obs_metrics_exporter":          "prometheus",
		"obs_metrics_prometheus_listen": ":9464",
		"obs_metrics_prometheus_path":   "/metrics",
		"obs_traces_exporter":           "otlp",
		"obs_traces_otlp_endpoint":      "http://127.0.0.1:4317",
		"obs_traces_otlp_protocol":      "grpc",
		"obs_traces_otlp_interval":      "5s",
	}
	for k, v := range obsSettings {
		if err := core.Settings().Set(k, v); err != nil {
			t.Fatalf("Settings().Set(%q): %v", k, err)
		}
	}

	pbSnap, err := SnapshotFromStorage(core, 1)
	if err != nil {
		t.Fatalf("SnapshotFromStorage: %v", err)
	}
	got := SnapshotFromProto(pbSnap)

	for k, want := range obsSettings {
		if v, ok := got.SettingGet(k); !ok || v != want {
			t.Errorf("SettingGet(%q) = %q, %v; want %q, true", k, v, ok, want)
		}
	}
}

func TestEpochFromStorage(t *testing.T) {
	st := memory.New()
	if got := EpochFromStorage(st.Storage()); got != 0 {
		t.Errorf("absent epoch = %d; want 0", got)
	}
	if err := st.Storage().Settings().Set("config_epoch", "42"); err != nil {
		t.Fatal(err)
	}
	if got := EpochFromStorage(st.Storage()); got != 42 {
		t.Errorf("epoch = %d; want 42", got)
	}
	// garbage value parses to 0.
	if err := st.Storage().Settings().Set("config_epoch", "not-a-number"); err != nil {
		t.Fatal(err)
	}
	if got := EpochFromStorage(st.Storage()); got != 0 {
		t.Errorf("garbage epoch = %d; want 0", got)
	}
}
