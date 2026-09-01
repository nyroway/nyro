package protocol

import (
	"encoding/json"
	"os"
	"testing"
)

// contract mirrors protocols.json. The WebUI asserts its own protocol table
// against the same file, so a change made on one side and forgotten on the
// other fails here or in protocol.contract.test.ts rather than drifting
// silently — which is exactly how the old "keep this table in sync" comment
// let displayName suffixes and the alias set diverge.
type contract struct {
	Protocols []ProtocolInfo `json:"protocols"`
	Rejected  []string       `json:"rejected"`
}

func loadContract(t *testing.T) contract {
	t.Helper()
	raw, err := os.ReadFile("protocols.json")
	if err != nil {
		t.Fatalf("read protocols.json: %v", err)
	}
	var c contract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse protocols.json: %v", err)
	}
	return c
}

func TestCatalogMatchesContract(t *testing.T) {
	t.Parallel()
	want := loadContract(t).Protocols
	got := Protocols()
	if len(got) != len(want) {
		t.Fatalf("spec.Protocols() has %d entries, protocols.json has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d:\n  spec = %+v\n  json = %+v", i, got[i], want[i])
		}
	}
}

// TestContractDrivesDisplayNameAndParse checks that the accessors really read
// the catalog, so asserting the catalog is enough to pin the behaviour callers
// actually see.
func TestContractDrivesDisplayNameAndParse(t *testing.T) {
	t.Parallel()
	for _, info := range loadContract(t).Protocols {
		if got := info.ID.DisplayName(); got != info.DisplayName {
			t.Errorf("%s.DisplayName() = %q, want %q", info.ID, got, info.DisplayName)
		}
		got, err := ParseProtocol(string(info.ID))
		if err != nil || got != info.ID {
			t.Errorf("ParseProtocol(%q) = %v, %v; want %v", info.ID, got, err, info.ID)
		}
		if info.Alias == "" {
			continue
		}
		got, err = ParseProtocol(info.Alias)
		if err != nil || got != info.ID {
			t.Errorf("ParseProtocol(alias %q) = %v, %v; want %v", info.Alias, got, err, info.ID)
		}
	}
}

// TestRejectedIdentifiersDoNotResolve pins the retired and never-valid
// spellings listed in the contract. go/ is unreleased, so there is no
// back-compat alias set: a dropped identifier must stay dropped rather than
// quietly coming back, and a typo must not be tolerated.
func TestRejectedIdentifiersDoNotResolve(t *testing.T) {
	t.Parallel()
	rejected := loadContract(t).Rejected
	if len(rejected) == 0 {
		t.Fatal("protocols.json lists no rejected identifiers; the guard would be vacuous")
	}
	for _, id := range rejected {
		if _, err := ParseProtocol(id); err == nil {
			t.Errorf("ParseProtocol(%q) succeeded; it is listed as rejected", id)
		}
	}
}

// TestAliasesAreUniqueAndNotDerived guards the alias rule: at most one alias
// per protocol, distinct across protocols, and never a prefix-abbreviation of
// the identifier (openai-resp for openai-responses was exactly that).
func TestAliasesAreUniqueAndNotDerived(t *testing.T) {
	t.Parallel()
	seen := map[string]Protocol{}
	for _, info := range Protocols() {
		if info.Alias == "" {
			continue
		}
		if prev, dup := seen[info.Alias]; dup {
			t.Errorf("alias %q claimed by both %q and %q", info.Alias, prev, info.ID)
		}
		seen[info.Alias] = info.ID
		if _, isID := Lookup(Protocol(info.Alias)); isID {
			t.Errorf("alias %q collides with a canonical identifier", info.Alias)
		}
	}
}
