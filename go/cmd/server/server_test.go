package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func TestNewCmdFlags(t *testing.T) {
	cmd := NewCmd()
	if addr, _ := cmd.Flags().GetString("listen"); addr != "127.0.0.1:19531" {
		t.Errorf("default listen = %q, want 127.0.0.1:19531", addr)
	}
	if cmd.Use != "serve" {
		t.Errorf("Use = %q, want serve", cmd.Use)
	}
	wantStrings := map[string]string{
		"proxy-listen":     "127.0.0.1:19530",
		"redis-listen":     "127.0.0.1:16379",
		"otlp-listen":      "127.0.0.1:14318",
		"state-data-dir":   defaultDataDir(),
		"observe-data-dir": defaultDataDir(),
	}
	for name, want := range wantStrings {
		if got, _ := cmd.Flags().GetString(name); got != want {
			t.Errorf("default --%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"disable-proxy", "disable-redis", "disable-otlp"} {
		if got, _ := cmd.Flags().GetBool(name); got {
			t.Errorf("--%s defaults to true, want false", name)
		}
	}
	// Pre-rename flag names must be gone (no compatibility period).
	for _, old := range []string{"config-listen", "config-tls-ca", "config-tls-cert", "config-tls-key", "plaintext-keys", "obs-data-dir"} {
		if cmd.Flags().Lookup(old) != nil {
			t.Errorf("--%s should have been renamed", old)
		}
	}
}

func TestDataPlaneRuntimeCleanupIsIdempotent(t *testing.T) {
	runtime := new(recordingRuntimeShutdowner)
	cleanup := newDataPlaneRuntimeCleanup(runtime)
	cleanup()
	cleanup()
	if runtime.calls != 1 {
		t.Fatalf("runtime Shutdown calls = %d, want 1", runtime.calls)
	}
}

type recordingRuntimeShutdowner struct {
	calls int
}

func (runtime *recordingRuntimeShutdowner) Shutdown(context.Context) error {
	runtime.calls++
	return nil
}

func TestNewCmdDSNFlagDefault(t *testing.T) {
	cmd := NewCmd()
	if v, _ := cmd.Flags().GetString("dsn"); v != "" {
		t.Errorf("default dsn = %q, want empty (resolved at RunE time)", v)
	}
}

func TestDefaultDSNUsesConfigDatabase(t *testing.T) {
	want := "sqlite://" + filepath.Join(defaultDataDir(), "config.db")
	if got := defaultDSN(); got != want {
		t.Fatalf("defaultDSN() = %q, want %q", got, want)
	}
}

func TestRunE_RejectsMemoryDSN(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--dsn", "memory://"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error rejecting --dsn memory://, got nil")
	}
}

func TestRunE_RejectsUnknownDSNScheme(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--dsn", "bogus://x"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error rejecting an unrecognized --dsn scheme, got nil")
	}
}

// The config-sync TCP listener is opt-in: with an embedded data plane a
// single-node install needs no config-sync port, so one is not opened unless
// remote proxies have to subscribe.
func TestNewCmdSyncListenDefaultsToDisabled(t *testing.T) {
	cmd := NewCmd()
	if v, _ := cmd.Flags().GetString("sync-listen"); v != "" {
		t.Errorf("default sync-listen = %q, want empty (disabled)", v)
	}
}

// The embedded data plane is on by default and bound to loopback — unlike a
// standalone `nyro proxy`, which binds 0.0.0.0 because accepting off-host
// client traffic is its whole job.
func TestNewCmdProxyListenDefault(t *testing.T) {
	cmd := NewCmd()
	if v, _ := cmd.Flags().GetString("proxy-listen"); v != "127.0.0.1:19530" {
		t.Errorf("default proxy-listen = %q, want 127.0.0.1:19530", v)
	}
}

func TestRunERejectsEmptyEnabledListenAddresses(t *testing.T) {
	for _, name := range []string{"listen", "proxy-listen", "redis-listen", "otlp-listen"} {
		t.Run(name, func(t *testing.T) {
			cmd := NewCmd()
			if err := cmd.ParseFlags([]string{"--" + name + "=", "--dsn=memory://"}); err != nil {
				t.Fatal(err)
			}
			err := cmd.RunE(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), "--"+name) {
				t.Fatalf("RunE() error = %v, want --%s validation", err, name)
			}
		})
	}
}

func TestRunEDisableFlagsAllowUnusedEmptyAddresses(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{
		"--disable-proxy", "--proxy-listen=",
		"--disable-redis", "--redis-listen=",
		"--disable-otlp", "--otlp-listen=",
		"--dsn=memory://",
	}); err != nil {
		t.Fatal(err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unrecognized --dsn") {
		t.Fatalf("RunE() error = %v, want validation to reach DSN", err)
	}
}

func TestRunEGuardsEmbeddedInfrastructureExposure(t *testing.T) {
	tests := []struct {
		name    string
		flags   []string
		wantErr string
	}{
		{
			name:    "non-loopback Redis requires password",
			flags:   []string{"--redis-listen=0.0.0.0:16379", "--dsn=memory://"},
			wantErr: "--redis-password",
		},
		{
			name:    "authenticated non-loopback Redis passes network guard",
			flags:   []string{"--redis-listen=0.0.0.0:16379", "--redis-password=secret", "--dsn=memory://"},
			wantErr: "unrecognized --dsn",
		},
		{
			name:    "non-loopback OTLP remains rejected",
			flags:   []string{"--otlp-listen=0.0.0.0:14318", "--dsn=memory://"},
			wantErr: "loopback",
		},
		{
			name:    "loopback Redis needs no password",
			flags:   []string{"--redis-listen=127.0.0.1:16379", "--dsn=memory://"},
			wantErr: "unrecognized --dsn",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmd()
			if err := cmd.ParseFlags(tt.flags); err != nil {
				t.Fatal(err)
			}
			err := cmd.RunE(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RunE() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunEWarnsForAuthenticatedNonLoopbackRedisWithoutLeakingPassword(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	cmd := NewCmd()
	const password = "do-not-log-this"
	if err := cmd.ParseFlags([]string{
		"--redis-listen=0.0.0.0:16379",
		"--redis-password=" + password,
		"--dsn=memory://",
	}); err != nil {
		t.Fatal(err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unrecognized --dsn") {
		t.Fatalf("RunE() error = %v, want later DSN validation", err)
	}
	if !strings.Contains(logs.String(), "plaintext") || !strings.Contains(logs.String(), "private network") {
		t.Fatalf("missing Redis exposure warning: %q", logs.String())
	}
	if strings.Contains(logs.String(), password) {
		t.Fatalf("Redis password leaked in logs: %q", logs.String())
	}
}

// The poll cadence is derived from the DSN scheme rather than exposed as a
// flag: only postgres can have a sibling replica writing to the same database.
func TestEpochPollIntervalDerivedFromBackend(t *testing.T) {
	if got := epochPollInterval("postgres"); got != postgresEpochPollInterval {
		t.Errorf("epochPollInterval(postgres) = %v, want %v", got, postgresEpochPollInterval)
	}
	for _, backend := range []string{"sqlite", "memory", ""} {
		if got := epochPollInterval(backend); got != 0 {
			t.Errorf("epochPollInterval(%q) = %v, want 0 (no sibling replica can bump the epoch)", backend, got)
		}
	}
}

func TestNewCmdConfigPollIntervalFlagIsGone(t *testing.T) {
	if f := NewCmd().Flags().Lookup("config-poll-interval"); f != nil {
		t.Error("--config-poll-interval should have been removed in favour of DSN-derived polling")
	}
}

type countingEpochStore struct {
	mu    sync.Mutex
	value int64
	reads int
}

func (s *countingEpochStore) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return strconv.FormatInt(s.value, 10), nil
}

func (s *countingEpochStore) set(value int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}

func (s *countingEpochStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

type channelNotifier struct {
	notified chan struct{}
}

func (n *channelNotifier) Notify() {
	select {
	case n.notified <- struct{}{}:
	default:
	}
}

func TestStartEpochWatcher_ZeroReturnsNilWithoutReadingEpoch(t *testing.T) {
	store := &countingEpochStore{value: 7}
	notifier := &channelNotifier{notified: make(chan struct{}, 1)}

	watcher, err := startEpochWatcher(context.Background(), 0, store, notifier)
	if err != nil {
		t.Fatalf("startEpochWatcher: %v", err)
	}
	if watcher != nil {
		t.Fatalf("watcher = %v, want nil", watcher)
	}
	if got := store.readCount(); got != 0 {
		t.Fatalf("epoch reads = %d, want 0", got)
	}
}

func TestStartEpochWatcher_NonPositiveIntervalDisablesWithoutReadingEpoch(t *testing.T) {
	store := &countingEpochStore{value: 7}
	notifier := &channelNotifier{notified: make(chan struct{}, 1)}

	// The interval is no longer operator-supplied, so a non-positive value is
	// simply "polling disabled" rather than a validation error.
	watcher, err := startEpochWatcher(context.Background(), -time.Second, store, notifier)
	if err != nil {
		t.Fatalf("startEpochWatcher: %v", err)
	}
	if watcher != nil {
		t.Fatalf("watcher = %v, want nil", watcher)
	}
	if got := store.readCount(); got != 0 {
		t.Fatalf("epoch reads = %d, want 0", got)
	}
}

func TestStartEpochWatcher_PositiveIntervalSeedsAndRuns(t *testing.T) {
	store := &countingEpochStore{value: 7}
	notifier := &channelNotifier{notified: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	watcher, err := startEpochWatcher(ctx, 5*time.Millisecond, store, notifier)
	if err != nil {
		t.Fatalf("startEpochWatcher: %v", err)
	}
	if watcher == nil {
		t.Fatal("watcher = nil, want a running watcher")
	}
	if got := store.readCount(); got == 0 {
		t.Fatal("epoch reads = 0, want a synchronous seed read")
	}
	select {
	case <-notifier.notified:
		t.Fatal("seed unexpectedly notified")
	default:
	}

	store.set(8)
	select {
	case <-notifier.notified:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watcher to observe the new epoch")
	}
}

// The watcher must be seeded (one epoch read) before it is handed out, so the
// first poll tick cannot mistake the current epoch for a change and re-push a
// snapshot every subscriber already has.
func TestStartEpochWatcher_SeedsBeforeReturning(t *testing.T) {
	store := &countingEpochStore{value: 7}
	notifier := &channelNotifier{notified: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	watcher, err := startEpochWatcher(ctx, time.Hour, store, notifier)
	if err != nil {
		t.Fatalf("startEpochWatcher: %v", err)
	}
	if watcher == nil {
		t.Fatal("watcher = nil, want seeded watcher")
	}
	if got := store.readCount(); got != 1 {
		t.Fatalf("epoch reads after seeding = %d, want exactly 1", got)
	}
}

func TestRunE_RejectsConfigSyncFlagsWhenConfigListenerDisabled(t *testing.T) {
	tests := []struct {
		name string
		flag string
	}{
		{name: "TLS CA", flag: "--sync-tls-ca=ca.pem"},
		{name: "TLS certificate", flag: "--sync-tls-cert=cert.pem"},
		{name: "TLS key", flag: "--sync-tls-key=key.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmd()
			if err := cmd.ParseFlags([]string{
				"--sync-listen=",
				tt.flag,
				"--dsn=memory://",
			}); err != nil {
				t.Fatalf("parse flags: %v", err)
			}

			err := cmd.RunE(cmd, nil)
			if err == nil {
				t.Fatal("RunE returned nil error, want disabled config-listen validation error")
			}
			if !strings.Contains(err.Error(), "--sync-listen") {
				t.Fatalf("RunE error = %q, want disabled --sync-listen validation error", err)
			}
		})
	}
}

func TestServeCommandDoesNotExposeAdminTokenFlag(t *testing.T) {
	if flag := NewCmd().Flags().Lookup("token"); flag != nil {
		t.Fatalf("serve still exposes removed --token flag: %+v", flag)
	}
}

func TestRuntimeServiceSnapshotReflectsServeConfiguration(t *testing.T) {
	services := runtimeServiceSnapshot(runtimeServiceOptions{
		controlListen:  "127.0.0.1:19531",
		proxyListen:    "127.0.0.1:19530",
		disableProxy:   true,
		redisListen:    "127.0.0.1:16379",
		stateDataDir:   "/var/lib/nyro/state",
		otlpListen:     "127.0.0.1:14318",
		observeDataDir: "/var/lib/nyro/observe",
		configBackend:  "postgres",
		configPath:     "postgres://user:secret@example/nyro",
	})

	if len(services) != 4 {
		t.Fatalf("services len = %d, want 4: %+v", len(services), services)
	}
	if services[0].StorageBackend != "postgres" || services[0].DataPath != "" {
		t.Fatalf("control service must expose backend but not postgres DSN: %+v", services[0])
	}
	if services[1].Status != admin.ServiceDisabled {
		t.Fatalf("disabled proxy status = %q, want disabled", services[1].Status)
	}
	if services[2].DataPath != "/var/lib/nyro/state/state.db" {
		t.Fatalf("state data path = %q", services[2].DataPath)
	}
	if services[3].DataPath != "/var/lib/nyro/observe/observe.db" {
		t.Fatalf("observe data path = %q", services[3].DataPath)
	}
}

func TestRunE_WarnsForNonLoopbackAdminListen(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		wantWarn bool
	}{
		{name: "IPv4 loopback", listen: "127.0.0.1:19531"},
		{name: "IPv6 loopback", listen: "[::1]:19531"},
		{name: "localhost", listen: "localhost:19531"},
		{name: "IPv4 any", listen: "0.0.0.0:19531", wantWarn: true},
		{name: "IPv6 any", listen: "[::]:19531", wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			cmd := NewCmd()
			if err := cmd.ParseFlags([]string{
				"--listen=" + tt.listen,
				"--sync-listen=",
				"--dsn=memory://",
			}); err != nil {
				t.Fatalf("parse flags: %v", err)
			}
			if err := cmd.RunE(cmd, nil); err == nil {
				t.Fatal("RunE returned nil error, want memory DSN rejection after exposure check")
			}

			gotWarn := strings.Contains(logs.String(), "management API is listening on a non-loopback address without built-in authentication")
			if gotWarn != tt.wantWarn {
				t.Fatalf("warning present = %v, want %v; logs: %q", gotWarn, tt.wantWarn, logs.String())
			}
		})
	}
}

// An explicit --dsn naming a sqlite directory that doesn't exist must fail
// loudly rather than silently create it — unlike the ~/.nyro default,
// which is our own managed space and safe to auto-create. Silently
// creating a typo'd explicit path risks masking the mistake with a fresh
// empty DB instead of the one the operator meant to open.
func TestRunE_ExplicitDSNMissingDirectoryErrors(t *testing.T) {
	cmd := NewCmd()
	missing := filepath.Join(t.TempDir(), "does-not-exist", "nyro.db")
	if err := cmd.ParseFlags([]string{"--dsn", "sqlite://" + missing}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error for a missing explicit --dsn directory, got nil")
	}
}

// Same as above, for explicit embedded data directories. --dsn is pointed at a valid temp
// directory so RunE reaches the embedded data-directory check rather than failing
// there first — and so the test never touches the real ~/.nyro.
func TestRunE_ExplicitEmbeddedDataDirMissingDirectoryErrors(t *testing.T) {
	for _, flag := range []string{"state-data-dir", "observe-data-dir"} {
		t.Run(flag, func(t *testing.T) {
			cmd := NewCmd()
			dbDSN := filepath.Join(t.TempDir(), "config.db")
			missing := filepath.Join(t.TempDir(), "does-not-exist")
			args := []string{"--dsn", "sqlite://" + dbDSN, "--" + flag, missing}
			if flag == "observe-data-dir" {
				args = append(args, "--disable-redis")
			}
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatalf("parse flags: %v", err)
			}
			err := cmd.RunE(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), "--"+flag) {
				t.Fatalf("RunE() error = %v, want missing --%s directory error", err, flag)
			}
		})
	}
}

func newMemStore(t *testing.T) storage.Storage {
	t.Helper()
	return memory.New().Storage()
}

// ── seedDefaultObsEndpoint ──

func TestSeedDefaultObsEndpoint_FullyEmpty_SeedsAllThree(t *testing.T) {
	st := newMemStore(t)
	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")

	for _, signal := range []string{"logs", "metrics", "traces"} {
		assertGet(t, st, "obs_"+signal+"_otlp_endpoint", "http://127.0.0.1:19531")
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
	assertGet(t, st, "obs_logs_otlp_endpoint", "http://127.0.0.1:19531")
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
		assertGet(t, st, "obs_"+signal+"_otlp_endpoint", "http://127.0.0.1:19531")
		assertGet(t, st, "obs_"+signal+"_exporter", "otlp")
	}
}

// TestSeedDefaultObsEndpoint_SeededValueIsAValidAbsoluteURL is a regression
// test for the bug where the raw --addr value (a schemeless "host:port") was
// written directly to obs_<signal>_otlp_endpoint. provider.go's OTLP builders
// pass this value to otlploghttp/otlpmetrichttp/otlptracehttp's
// WithEndpointURL, which url.Parses it and requires an absolute URL — a
// schemeless value like "127.0.0.1:19531" fails that parse (its first path
// segment looks like a URL scheme, "cannot contain colon") and the OTel SDK
// silently falls back to its own built-in default (localhost:4318) instead of
// this admin's real address. This test exercises the same url.Parse path the
// SDK relies on and asserts the seeded value survives it with a real host.
func TestSeedDefaultObsEndpoint_SeededValueIsAValidAbsoluteURL(t *testing.T) {
	st := newMemStore(t)
	seedDefaultObsEndpoint(st.Settings(), "127.0.0.1:19531")

	for _, signal := range []string{"logs", "metrics", "traces"} {
		key := "obs_" + signal + "_otlp_endpoint"
		raw, err := st.Settings().Get(key)
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q) for %s failed (this is the exact failure mode otlp*http.WithEndpointURL hits, which makes the OTel SDK silently fall back to its own default instead of this admin's address): %v", raw, key, err)
		}
		if u.Scheme == "" {
			t.Errorf("%s = %q: url.Parse succeeded but has no scheme; WithEndpointURL requires an absolute URL", key, raw)
		}
		if u.Host == "" {
			t.Errorf("%s = %q: url.Parse succeeded but has no host", key, raw)
		}
		if u.Host != "127.0.0.1:19531" {
			t.Errorf("%s: url.Parse(%q).Host = %q, want %q", key, raw, u.Host, "127.0.0.1:19531")
		}
	}
}

func TestAddrToOTLPURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"bare host:port", "127.0.0.1:19531", "http://127.0.0.1:19531"},
		{"bare port only", ":8080", "http://:8080"},
		{"already http scheme", "http://127.0.0.1:19531", "http://127.0.0.1:19531"},
		{"already https scheme", "https://collector.example.com:4318", "https://collector.example.com:4318"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addrToOTLPURL(tt.addr); got != tt.want {
				t.Errorf("addrToOTLPURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
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

// The gate is what makes the plan's "no --insecure flag" decision safe: the
// only rejected combination has two escape hatches, both of which improve
// security, so there is nothing left for an override flag to be needed for.
func TestRunE_RefusesNonLoopbackPlaintextConfigSync(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{
		"--sync-listen=0.0.0.0:19532",
		"--dsn=memory://",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("RunE returned nil, want a refusal to expose an unauthenticated plaintext config-sync port")
	}
	// The message carries the discoverability a flag would have; if it stops
	// naming the ways out, operators are stuck with a bare refusal.
	for _, want := range []string{"--sync-token", "--sync-tls-ca", "loopback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%s", want, err)
		}
	}
}

// A join token is one of the two escape hatches, so it must actually get past
// the gate — the run then fails later on the rejected memory:// DSN, which is
// enough to prove the gate was cleared.
func TestRunE_NonLoopbackPlaintextAllowedWithToken(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{
		"--sync-listen=0.0.0.0:19532",
		"--sync-token=shared-secret",
		"--dsn=memory://",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err != nil && strings.Contains(err.Error(), "--sync-token") {
		t.Fatalf("a configured token must clear the plaintext gate, got: %v", err)
	}
}

// Loopback is the zero-config default and must stay silent: a warning every
// single-node user sees on every start is one they learn to ignore.
func TestRunE_LoopbackPlaintextConfigSyncIsNotGated(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{
		"--sync-listen=127.0.0.1:19532",
		"--dsn=memory://",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err != nil && strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("loopback config-sync must not be gated, got: %v", err)
	}
}
