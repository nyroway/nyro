package snapshot

import "testing"

// The mutations below would expose a Snapshot retaining caller-owned mutable
// data, which would let configuration change without an atomic Cache swap.
func TestBuilderBuildCopiesMutableInput(t *testing.T) {
	credentials := []byte(`{"api_key":"before"}`)
	targets := []RouteTarget{{UpstreamID: "up-1", Model: "gpt-5", Weight: 1}}
	routes := []string{"gpt-5"}
	quotas := []ConsumerQuota{{QuotaType: "requests", QuotaLimit: 10, Window: "1m"}}

	var builder Builder
	builder.SetUpstream(Upstream{ID: "up-1", CredentialsJSON: credentials})
	builder.SetRoute(Route{ID: "route-1", Model: "gpt-5", Upstreams: targets})
	builder.AddConsumerKey("key-1", "consumer-1", "primary", previewOf("nyro_tok_0000"), hashKey("nyro_tok_0000"), true, "", routes, quotas)
	builder.SetSetting("proxy.max_retries", "3")
	snap := builder.Build()

	credentials[12] = 'x'
	targets[0].Model = "mutated"
	routes[0] = "mutated"
	quotas[0].QuotaLimit = 999
	builder.SetSetting("proxy.max_retries", "4")

	if got := string(snap.UpstreamGet("up-1").CredentialsJSON); got != `{"api_key":"before"}` {
		t.Fatalf("upstream credentials = %q, want original value", got)
	}
	if got := snap.RouteByModel("gpt-5").Upstreams[0].Model; got != "gpt-5" {
		t.Fatalf("route target model = %q, want original value", got)
	}
	if got := snap.FindKey("nyro_tok_0000"); got == nil || got.Routes[0] != "gpt-5" || got.Quotas[0].QuotaLimit != 10 {
		t.Fatalf("consumer access = %+v, want original grants and quotas", got)
	}
	if got, _ := snap.SettingGet("proxy.max_retries"); got != "3" {
		t.Fatalf("setting = %q, want original value", got)
	}
}

// The mutations below would expose getters returning aliases into the frozen
// Snapshot, allowing an individual request to corrupt subsequent requests.
func TestSnapshotQueriesReturnCopies(t *testing.T) {
	rawKey := "nyro_tok_query_copy_0000"
	var builder Builder
	builder.SetUpstream(Upstream{ID: "up-1", CredentialsJSON: []byte(`{"api_key":"secret"}`)})
	builder.SetRoute(Route{ID: "route-1", Model: "gpt-5", Upstreams: []RouteTarget{{UpstreamID: "up-1", Model: "gpt-5"}}})
	builder.AddConsumerKey("key-1", "consumer-1", "primary", previewOf(rawKey), hashKey(rawKey), true, "", []string{"gpt-5"}, []ConsumerQuota{{QuotaType: "requests", QuotaLimit: 10, Window: "1m"}})
	snap := builder.Build()

	upstream := snap.UpstreamGet("up-1")
	upstream.CredentialsJSON[12] = 'x'
	upstreams := snap.UpstreamsList()
	upstreams[0].CredentialsJSON[12] = 'y'
	routes := snap.RoutesList()
	routes[0].Upstreams[0].Model = "mutated"
	access := snap.FindKey(rawKey)
	access.Routes[0] = "mutated"
	access.Quotas[0].QuotaLimit = 999

	if got := string(snap.UpstreamGet("up-1").CredentialsJSON); got != `{"api_key":"secret"}` {
		t.Fatalf("upstream credentials = %q, want original value", got)
	}
	if got := snap.RouteByModel("gpt-5").Upstreams[0].Model; got != "gpt-5" {
		t.Fatalf("route target model = %q, want original value", got)
	}
	if got := snap.FindKey(rawKey); got.Routes[0] != "gpt-5" || got.Quotas[0].QuotaLimit != 10 {
		t.Fatalf("consumer access = %+v, want original grants and quotas", got)
	}
}

func TestFingerprintIsStableForEquivalentSnapshots(t *testing.T) {
	first := snapshotForFingerprint(false)
	second := snapshotForFingerprint(true)

	if got, want := first.Fingerprint(), second.Fingerprint(); got != want {
		t.Fatalf("equivalent snapshots have fingerprints %q and %q", got, want)
	}
}

func TestFingerprintIsStableWhenConsumerKeyIdentityTies(t *testing.T) {
	build := func(reverse bool) *Snapshot {
		entries := []struct {
			enabled bool
			expires string
			routes  []string
			quota   int64
		}{
			{enabled: true, expires: "", routes: []string{"gpt-5"}, quota: 10},
			{enabled: false, expires: "2026-12-01T00:00:00Z", routes: []string{"gpt-4"}, quota: 20},
		}
		var builder Builder
		if reverse {
			entries[0], entries[1] = entries[1], entries[0]
		}
		for _, entry := range entries {
			builder.AddConsumerKey(
				"key-1", "consumer-1", "primary", previewOf("nyro_tok_tied_0000"), hashKey("nyro_tok_tied_0000"),
				entry.enabled, entry.expires, entry.routes,
				[]ConsumerQuota{{ID: "quota-1", ConsumerID: "consumer-1", QuotaType: "requests", QuotaLimit: entry.quota, Window: "1m"}},
			)
		}
		return builder.Build()
	}
	if got, want := build(false).Fingerprint(), build(true).Fingerprint(); got != want {
		t.Fatalf("tied consumer keys have fingerprints %q and %q", got, want)
	}
}

func TestFingerprintChangesForEffectiveDataPlaneConfiguration(t *testing.T) {
	base := snapshotForFingerprint(false)
	tests := []struct {
		name   string
		mutate func(*Builder)
	}{
		{
			name: "upstream credential",
			mutate: func(b *Builder) {
				b.SetUpstream(Upstream{ID: "up-1", Name: "primary", CredentialsJSON: []byte(`{"api_key":"changed"}`), Enabled: true})
			},
		},
		{
			name: "route target",
			mutate: func(b *Builder) {
				b.SetRoute(Route{ID: "route-1", Model: "gpt-5", Balance: "priority", Enabled: true, Upstreams: []RouteTarget{{ID: "target-1", UpstreamID: "up-1", Model: "changed", Weight: 1, Priority: 1, Enabled: true}}})
			},
		},
		{
			name:   "setting",
			mutate: func(b *Builder) { b.SetSetting("proxy.max_retries", "4") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var builder Builder
			populateFingerprintBuilder(&builder, false)
			tt.mutate(&builder)
			if got := builder.Build().Fingerprint(); got == base.Fingerprint() {
				t.Fatal("fingerprint did not change")
			}
		})
	}
}

func TestFingerprintChangesForConsumerGrantAndQuota(t *testing.T) {
	base := snapshotWithConsumerAccess([]string{"gpt-5"}, 10)
	for _, test := range []struct {
		name string
		snap *Snapshot
	}{
		{name: "grant", snap: snapshotWithConsumerAccess([]string{"gpt-4"}, 10)},
		{name: "quota", snap: snapshotWithConsumerAccess([]string{"gpt-5"}, 11)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.snap.Fingerprint(); got == base.Fingerprint() {
				t.Fatal("fingerprint did not change")
			}
		})
	}
}

func snapshotWithConsumerAccess(routes []string, quotaLimit int64) *Snapshot {
	var builder Builder
	builder.SetUpstream(Upstream{ID: "up-1", Name: "primary", CredentialsJSON: []byte(`{"api_key":"secret"}`), Enabled: true})
	builder.SetRoute(Route{ID: "route-1", Model: "gpt-5", Balance: "weighted", Enabled: true, Upstreams: []RouteTarget{{ID: "target-1", UpstreamID: "up-1", Model: "gpt-5", Weight: 1, Priority: 1, Enabled: true}}})
	rawKey := "nyro_tok_fingerprint_consumer_0000"
	builder.AddConsumerKey("key-1", "consumer-1", "primary", previewOf(rawKey), hashKey(rawKey), true, "", routes, []ConsumerQuota{{ID: "quota-1", ConsumerID: "consumer-1", QuotaType: "requests", QuotaLimit: quotaLimit, Window: "1m"}})
	return builder.Build()
}

func snapshotForFingerprint(reverse bool) *Snapshot {
	var builder Builder
	populateFingerprintBuilder(&builder, reverse)
	return builder.Build()
}

func populateFingerprintBuilder(builder *Builder, reverse bool) {
	upstreams := []Upstream{
		{ID: "up-1", Name: "primary", CredentialsJSON: []byte(`{"api_key":"secret"}`), Enabled: true},
		{ID: "up-2", Name: "secondary", CredentialsJSON: []byte(`{"api_key":"other"}`), Enabled: true},
	}
	routes := []Route{
		{ID: "route-1", Model: "gpt-5", Balance: "priority", Enabled: true, Upstreams: []RouteTarget{{ID: "target-1", UpstreamID: "up-1", Model: "gpt-5", Weight: 1, Priority: 1, Enabled: true}, {ID: "target-2", UpstreamID: "up-2", Model: "gpt-5", Weight: 1, Priority: 2, Enabled: true}}},
		{ID: "route-2", Model: "gpt-4", Balance: "weighted", Enabled: true, Upstreams: []RouteTarget{{ID: "target-3", UpstreamID: "up-1", Model: "gpt-4", Weight: 2, Enabled: true}}},
	}
	if reverse {
		for i := len(upstreams) - 1; i >= 0; i-- {
			builder.SetUpstream(upstreams[i])
		}
		for i := len(routes) - 1; i >= 0; i-- {
			route := routes[i]
			for left, right := 0, len(route.Upstreams)-1; left < right; left, right = left+1, right-1 {
				route.Upstreams[left], route.Upstreams[right] = route.Upstreams[right], route.Upstreams[left]
			}
			builder.SetRoute(route)
		}
		builder.SetSetting("state.url", "redis://127.0.0.1:6379/0")
		builder.SetSetting("proxy.max_retries", "3")
		builder.AddConsumerKey("key-2", "consumer-2", "secondary", previewOf("nyro_tok_fingerprint_1111"), hashKey("nyro_tok_fingerprint_1111"), true, "", []string{"gpt-4", "gpt-5"}, []ConsumerQuota{{ID: "quota-2", ConsumerID: "consumer-2", QuotaType: "requests", QuotaLimit: 20, Window: "1m"}})
		builder.AddConsumerKey("key-1", "consumer-1", "primary", previewOf("nyro_tok_fingerprint_0000"), hashKey("nyro_tok_fingerprint_0000"), true, "", []string{"gpt-5"}, []ConsumerQuota{{ID: "quota-1", ConsumerID: "consumer-1", QuotaType: "requests", QuotaLimit: 10, Window: "1m"}})
		return
	}
	for _, upstream := range upstreams {
		builder.SetUpstream(upstream)
	}
	for _, route := range routes {
		builder.SetRoute(route)
	}
	builder.AddConsumerKey("key-1", "consumer-1", "primary", previewOf("nyro_tok_fingerprint_0000"), hashKey("nyro_tok_fingerprint_0000"), true, "", []string{"gpt-5"}, []ConsumerQuota{{ID: "quota-1", ConsumerID: "consumer-1", QuotaType: "requests", QuotaLimit: 10, Window: "1m"}})
	builder.AddConsumerKey("key-2", "consumer-2", "secondary", previewOf("nyro_tok_fingerprint_1111"), hashKey("nyro_tok_fingerprint_1111"), true, "", []string{"gpt-5", "gpt-4"}, []ConsumerQuota{{ID: "quota-2", ConsumerID: "consumer-2", QuotaType: "requests", QuotaLimit: 20, Window: "1m"}})
	builder.SetSetting("proxy.max_retries", "3")
	builder.SetSetting("state.url", "redis://127.0.0.1:6379/0")
}
