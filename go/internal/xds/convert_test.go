package xds

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
	pb "github.com/nyroway/nyro/go/internal/xds/pb/xds/v1"
)

func int32P(v int32) *int32 { return &v }

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
	if snap == nil || snap.ModelByName("anything") != nil {
		t.Error("nil input should yield an empty-but-usable snapshot")
	}
}

func TestSnapshotFromProto_Providers(t *testing.T) {
	in := &pb.ConfigSnapshot{
		Providers: []*pb.Provider{{
			Id: "prov-1", Name: "openai", Vendor: "openai", Protocol: "openai",
			BaseUrl: "https://api.openai.com", ApiKey: "sk-x", AuthMode: "apikey",
			UseProxy: true, IsEnabled: true,
		}},
	}
	snap := protoRoundtrip(t, in)
	p := snap.ProviderGet("prov-1")
	if p == nil || p.Name != "openai" || !p.UseProxy || !p.IsEnabled || p.APIKey != "sk-x" {
		t.Errorf("provider not carried: %+v", p)
	}
	if snap.ProviderGet("missing") != nil {
		t.Error("missing provider should be nil")
	}
}

func TestSnapshotFromProto_ModelsWithTargets(t *testing.T) {
	in := &pb.ConfigSnapshot{
		Models: []*pb.Model{{
			Id: "m-1", Name: "gpt-4", Balance: "weighted", EnableAuth: true, IsEnabled: true,
			Targets: []*pb.ModelBackend{{
				Id: "t-1", ModelId: "m-1", ProviderId: "prov-1", Model: "gpt-4o", Weight: 3, Priority: 1,
			}},
		}},
	}
	snap := protoRoundtrip(t, in)
	m := snap.ModelByName("gpt-4")
	if m == nil || !m.EnableAuth || string(m.Balance) != "weighted" {
		t.Errorf("model fields not carried: %+v", m)
	}
	if len(m.Targets) != 1 || m.Targets[0].ProviderID != "prov-1" || m.Targets[0].Weight != 3 {
		t.Errorf("targets not carried: %+v", m.Targets)
	}
}

func TestSnapshotFromProto_ApiKeysAndBindings(t *testing.T) {
	in := &pb.ConfigSnapshot{
		ApiKeys: []*pb.ApiKey{{
			Id: "k-1", Token: "tok-1", Name: "alice", IsEnabled: true,
			Rpm: int32P(100), BoundModelIds: []string{"m-1", "m-2"},
		}, {
			Id: "k-2", Token: "", Name: "ignored", // empty token dropped
		}},
	}
	snap := protoRoundtrip(t, in)
	rec := snap.FindAPIKey("tok-1")
	if rec == nil || rec.ID != "k-1" || rec.Name != "alice" || rec.RPM == nil || *rec.RPM != 100 {
		t.Errorf("apikey not carried: %+v", rec)
	}
	if !snap.ModelBindingExists("k-1", "m-1") || !snap.ModelBindingExists("k-1", "m-2") {
		t.Error("bindings not carried")
	}
	if snap.ModelBindingExists("k-1", "m-3") {
		t.Error("non-bound model should be false")
	}
}

func TestSnapshotFromProto_UnsetQuotaStaysNil(t *testing.T) {
	// An ApiKey with no rpm/rpd/tpm/tpd oneof must yield nil pointers, NOT 0,
	// so "unset" round-trips distinctly from "0".
	in := &pb.ConfigSnapshot{
		ApiKeys: []*pb.ApiKey{{Id: "k-1", Token: "tok-1", Name: "n"}},
	}
	snap := protoRoundtrip(t, in)
	rec := snap.FindAPIKey("tok-1")
	if rec == nil {
		t.Fatal("apikey missing")
	}
	if rec.RPM != nil || rec.RPD != nil || rec.TPM != nil || rec.TPD != nil {
		t.Errorf("unset quotas should be nil; got rpm=%v rpd=%v", rec.RPM, rec.RPD)
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
	st, p, mOpen, _, k := newPopulatedStorage(t)

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

	// providers
	if gp := got.ProviderGet(p.ID); gp == nil || gp.BaseURL != p.BaseURL {
		t.Errorf("provider via proto path mismatch: %+v", gp)
	}
	// models with targets
	gm := got.ModelByName(mOpen.Name)
	dm := direct.ModelByName(mOpen.Name)
	if gm == nil || len(gm.Targets) != len(dm.Targets) {
		t.Errorf("model targets mismatch: got %d want %d", len(gm.Targets), len(dm.Targets))
	}
	// apikeys
	if gr := got.FindAPIKey(k.Token); gr == nil || gr.ID != k.ID {
		t.Errorf("apikey via proto path mismatch: %+v", gr)
	}
	// settings
	if v, ok := got.SettingGet("proxy_url"); !ok || v == "" {
		t.Errorf("setting via proto path missing: %q %v", v, ok)
	}
}

func TestEpochFromStorage(t *testing.T) {
	st := memory.New()
	if got := EpochFromStorage(st.Storage()); got != 0 {
		t.Errorf("absent epoch = %d; want 0", got)
	}
	if err := st.Settings().Set("config_epoch", "42"); err != nil {
		t.Fatal(err)
	}
	if got := EpochFromStorage(st.Storage()); got != 42 {
		t.Errorf("epoch = %d; want 42", got)
	}
	// garbage value parses to 0.
	if err := st.Settings().Set("config_epoch", "not-a-number"); err != nil {
		t.Fatal(err)
	}
	if got := EpochFromStorage(st.Storage()); got != 0 {
		t.Errorf("garbage epoch = %d; want 0", got)
	}
}

// ensure storage import is used in this test file's helpers.
var _ = storage.Provider{}
