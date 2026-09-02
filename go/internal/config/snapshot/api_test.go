package snapshot

import (
	"testing"
)

func TestBuilderBuildsUsableSnapshot(t *testing.T) {
	var builder Builder
	builder.SetUpstream(Upstream{ID: "up-1", Name: "primary"})
	builder.SetRoute(Route{ID: "route-1", Model: "gpt-5"})
	builder.SetSetting("proxy.max_retries", "3")

	snap := builder.Build()
	if got := snap.UpstreamGet("up-1"); got == nil || got.Name != "primary" {
		t.Fatalf("UpstreamGet = %+v, want primary upstream", got)
	}
	if got := snap.RouteByModel("gpt-5"); got == nil || got.ID != "route-1" {
		t.Fatalf("RouteByModel = %+v, want route-1", got)
	}
	if got, ok := snap.SettingGet("proxy.max_retries"); !ok || got != "3" {
		t.Fatalf("SettingGet = %q, %v, want 3, true", got, ok)
	}
}

func TestBuilderBuildsEmptyUsableSnapshot(t *testing.T) {
	snap := (&Builder{}).Build()
	if snap == nil {
		t.Fatal("Build returned nil")
	}
	if snap.UpstreamGet("missing") != nil || snap.RouteByModel("missing") != nil {
		t.Fatal("empty snapshot returned a resource")
	}
	if _, ok := snap.SettingGet("missing"); ok {
		t.Fatal("empty snapshot returned a setting")
	}
}
