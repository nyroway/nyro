# Nyro Go Observability（OTLP 三信号）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the `request_logs` SQL table; split observability into logs/metrics/traces, collected in the gateway via the OTel SDK and pushed over OTLP/HTTP to the admin, which writes parquet and serves `/logs` + `/stats/*` (Go aggregation); make the gateway storage-free (completes xDS P3c).

**Architecture:** OTLP collector model. The gateway is a stateless data plane: OTel SDK (logs/metrics/traces) emitted via configurable exporters (`none`/`stdout`/`otlp`); default in xDS mode = OTLP/HTTP push to admin. The admin is a self-hosted observability backend: a hand-written OTLP/HTTP receiver (3 POST routes on its existing chi router) decodes the official OTLP protobuf and writes hourly-rotated, atomically-renamed parquet files; `/logs` reads logs-parquet, `/stats/*` reads metrics-parquet (Go aggregation). tpm/tpd quota stays in gateway memory (done in P3a, untouched). The gateway never writes parquet.

**Tech Stack:** Go 1.25; `github.com/parquet-go/parquet-go` (pure-Go, zstd via struct tags); `go.opentelemetry.io/otel` + `/sdk` + `/metric` + `/sdk/log` + exporters `otlp{log,metric,trace}http` and `stdout{log,metric,trace}`; `go.opentelemetry.io/proto/otlp/collector/{logs,metrics,trace}/v1` (decode). No CGO. chi (already in tree).

**Spec:** `docs/superpowers/specs/2026-06-30-nyro-observability-otlp-design.md` (commit `12054cd`).

## Global Constraints

- **Pure Go only.** `CGO_ENABLED=0 go build ./...` must stay green from Phase 2 onward. No DuckDB, no cgo.
- **No `request_logs` after Phase 4.** Table, DDL, `LogStore`, both backends' `logStore`, and `StartRetentionLoop` are deleted.
- **WebUI REST contract unchanged.** `/api/v1/logs`, `/logs/{id}`, `DELETE /logs`, `/stats/{overview,models,providers,api-keys,hourly}` keep identical JSON shapes (the `Stats*` and request-log row types keep their exact JSON tags).
- **OTLP transport = HTTP.** Gateway uses official exporters; admin hand-writes the receiver on chi at top-level `/v1/{logs,metrics,traces}` (NOT under `/api/v1`, NOT behind `admin-token`).
- **tpm/tpd/rpm/rpd stays in gateway memory** (`internal/proxy/quota`, done). Not exported, not touched.
- **No-regression phasing.** Every phase merges green: P1 = standalone library; P2 = admin dual-write (old table + parquet, parquet preferred); P3 = gateway switches to OTel + cuts `Storage` (completes xDS P3c); P4 = delete `request_logs`.
- Each task ends with `go build ./... && go vet ./... && go test ./...` green and a commit.

## File Structure

New package `internal/observability/`:

| File | Responsibility |
|---|---|
| `doc.go` | Package doc; three-signal model + layering principle (data plane emits, admin stores). |
| `signals.go` | Value types: `LogRecord`, `MetricSample`, `SpanSnapshot`, `MetricLabels`. |
| `config.go` | `ObsConfig` + `LoadConfig(settings)`; per-signal sink/endpoint/dir/retention/export interval. |
| `parquet/sink.go` | Generic rotating `Sink[Row]`: buf → flush new file on hour/size boundary → atomic rename; zstd via struct tags. **Admin-only.** |
| `parquet/reader.go` | `ReadSince[Row]`: glob completed files in `[since,now]`, decode, return `[]Row`. |
| `parquet/logs_sink.go` | Typed wrapper: `LogsSink` = `Sink[LogRecord]`; schema via struct tags. |
| `parquet/metricseries_sink.go` | `MetricSeriesSink` = `Sink[MetricSample]`. |
| `parquet/traces_sink.go` | `TracesSink` = `Sink[SpanSnapshot]`. |
| `logs_query.go` | `Logs` read side: `Query`/`FindByID`/`ClearAll` over logs-parquet (Go filter/sort/paginate). |
| `stats_aggregate.go` | `AggregateStats(samples, hours)` → `StatsOverview`/`[]ModelStats`/`[]ProviderStats`/`[]ApiKeyStats`; `AggregateHourly`. Pure Go. |
| `janitor.go` | `StartJanitor`: delete parquet files older than per-signal retention. |
| `receiver.go` | OTLP/HTTP receiver: 3 chi POST handlers; decode OTLP protobuf → internal rows → parquet sink. **Admin-only.** |
| `provider.go` | `ObsProvider`: assemble OTel LoggerProvider/MeterProvider/TracerProvider from `ObsConfig` (exporter per signal = none/stdout/otlp); `Flush`/`Shutdown`. **Gateway-only.** |
| `metrics_handles.go` | Named OTel counter/histogram handles (`requests_total`, `tokens_*`, `latency_ms`, `upstream_latency_ms`, `in_flight`). **Gateway-only.** |
| `hooks.go` | `PhaseHook` impls: `OnRequest` start span (store in `ContextBag`); `OnLog` end span + emit `LogRecord` + record metrics. **Gateway-only.** |

Modified existing files:

| File | Change |
|---|---|
| `internal/proxy/dispatcher.go` | Alloc `ContextBag`, pass `Bag` to all `RunPhaseHooks`; replace `appendLog` Storage write with OTel emit; add metrics inc/dec. |
| `internal/proxy/logrec.go` | Row-build logic moves into the `OnLog` hook; remove `g.Storage.Logs().AppendBatch` call. Keep `statusRecorder`/`logCtx`. |
| `internal/proxy/gateway.go` | Remove `Storage` field; `NewGateway` no longer takes `storage.Storage`. |
| `internal/proxy/server.go:37` | `/readyz` → cache-fill state. |
| `cmd/gateway/gateway.go` | Remove `OpenStorage`/`StartRetentionLoop`; construct `ObsProvider`; standalone sink config. |
| `cmd/admin/admin.go` | Mount OTLP receiver; construct parquet sinks + `StartJanitor`; pass `LogSource`/`StatsSource` to admin. |
| `internal/admin/admin.go` | `/logs`+`/stats` handlers read injected `LogSource`/`StatsSource`. |
| `internal/storage/storage.go` | Delete `LogStore` + `Logs()`. |
| `internal/storage/memory/{logs.go,stats_extra.go}` | Delete. |
| `internal/storage/sqlite/{sqlite.go logStore, stats_extra.go}` | Delete logStore + stats_extra. |
| `internal/storage/sqlite/schema.sql:96,132` | Delete `request_logs` DDL + index; add `DROP TABLE IF EXISTS request_logs` to `Migrate`. |
| `internal/bootstrap/bootstrap.go` | Delete `StartRetentionLoop`. |
| `go.mod` | Add parquet-go, OTel SDK/exporters, OTLP proto. |

---

## Phase 1 — observability library (standalone; no existing-code changes)

### Task 1.1: Dependencies + value types

**Files:**
- Create: `internal/observability/doc.go`
- Create: `internal/observability/signals.go`
- Create: `internal/observability/signals_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: `observability.LogRecord`, `observability.MetricSample`, `observability.SpanSnapshot`, `observability.MetricLabels` (used by P1.3, P1.5, P2.1, P3.2).

- [ ] **Step 1: Add deps**

```bash
go get github.com/parquet-go/parquet-go@latest
go get go.opentelemetry.io/otel@latest
go get go.opentelemetry.io/otel/sdk@latest
go get go.opentelemetry.io/otel/metric@latest
go get go.opentelemetry.io/otel/sdk/log@latest
go get go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp@latest
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@latest
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp@latest
go get go.opentelemetry.io/otel/exporters/stdout/stdoutlog@latest
go get go.opentelemetry.io/otel/exporters/stdout/stdouttrace@latest
go get go.opentelemetry.io/otel/exporters/stdout/stdoutmetric@latest
go get go.opentelemetry.io/proto/opentelemetry@latest
go mod tidy
```

Verify pure-Go: `CGO_ENABLED=0 go build ./...` green.

- [ ] **Step 2: Write `doc.go`**

```go
// Package observability implements nyro's three-signal telemetry model.
//
// Layering: the data plane (gateway) only ever EMITS signals (via the OTel SDK
// and configurable exporters); it never persists them. The control plane
// (admin) is the self-hosted backend: it receives OTLP/HTTP, writes parquet,
// and serves /logs + /stats queries. parquet sinks are therefore instantiated
// only in the admin process.
package observability
```

- [ ] **Step 3: Write `signals.go`**

`LogRecord` JSON tags are copied verbatim from `storage.RequestLog` (auth_models.go) so the WebUI is unaffected; parquet tags mirror column names.

```go
package observability

// LogRecord is one request-audit row. JSON tags are identical to the legacy
// storage.RequestLog so the WebUI contract is unchanged. parquet tags define
// the columnar schema; zstd compression is applied per-column via struct tags.
type LogRecord struct {
	ID                 string `json:"id" parquet:"id,snappy"`
	CreatedAt          int64  `json:"created_at" parquet:"created_at"`
	APIKeyID           string `json:"api_key_id,omitempty" parquet:"api_key_id,dict"`
	APIKeyName         string `json:"api_key_name,omitempty" parquet:"api_key_name"`
	ClientProtocol     string `json:"client_protocol,omitempty" parquet:"client_protocol,dict"`
	UpstreamProtocol   string `json:"upstream_protocol,omitempty" parquet:"upstream_protocol,dict"`
	ProviderID         string `json:"provider_id,omitempty" parquet:"provider_id,dict"`
	ProviderName       string `json:"provider_name,omitempty" parquet:"provider_name"`
	ModelID            string `json:"model_id,omitempty" parquet:"model_id,dict"`
	ModelName          string `json:"model_name,omitempty" parquet:"model_name"`
	ClientModel        string `json:"client_model,omitempty" parquet:"client_model"`
	UpstreamModel      string `json:"upstream_model,omitempty" parquet:"upstream_model"`
	Method             string `json:"method,omitempty" parquet:"method,dict"`
	Path               string `json:"path,omitempty" parquet:"path"`
	ClientStatusCode   *int32 `json:"client_status_code,omitempty" parquet:"client_status_code,optional"`
	UpstreamStatusCode *int32 `json:"upstream_status_code,omitempty" parquet:"upstream_status_code,optional"`
	LatencyTotalMs     *int64 `json:"latency_total_ms,omitempty" parquet:"latency_total_ms,optional"`
	LatencyUpstreamMs  *int64 `json:"latency_upstream_ms,omitempty" parquet:"latency_upstream_ms,optional"`
	InputTokens        int32  `json:"input_tokens" parquet:"input_tokens"`
	OutputTokens       int32  `json:"output_tokens" parquet:"output_tokens"`
	CacheReadTokens    int32  `json:"cache_read_tokens" parquet:"cache_read_tokens"`
	IsStream           bool   `json:"is_stream" parquet:"is_stream"`
}

// MetricSample is one point of a metrics time-series snapshot (one OTLP
// metric export = one snapshot = many samples).
type MetricSample struct {
	Ts        int64   `parquet:"ts"`
	Name      string  `parquet:"name,dict"`
	LabelsJSON string `parquet:"labels_json"`
	Kind      string  `parquet:"kind,dict"` // "counter" | "histogram" | "gauge"
	Value     float64 `parquet:"value"`
	HistSum   float64 `parquet:"hist_sum"`
	HistCount int64   `parquet:"hist_count"`
}

// SpanSnapshot is one trace span, as written to parquet by the admin receiver.
type SpanSnapshot struct {
	TraceID      string `parquet:"trace_id,dict"`
	SpanID       string `parquet:"span_id"`
	ParentSpanID string `parquet:"parent_span_id"`
	Name         string `parquet:"name,dict"`
	StartNs      int64  `parquet:"start_ns"`
	EndNs        int64  `parquet:"end_ns"`
	DurationNs   int64  `parquet:"duration_ns"`
	StatusCode   int32  `parquet:"status_code"`
	AttrsJSON    string `parquet:"attrs_json"`
}

// MetricLabels is the fixed label set for nyro metrics (bounded cardinality).
type MetricLabels struct {
	Model       string
	Provider    string
	APIKey      string
	StatusClass string // "2xx" | "4xx" | "5xx"
}
```

- [ ] **Step 4: Write `signals_test.go`** (smoke test: JSON shape parity with a known row)

```go
package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogRecordJSONShape(t *testing.T) {
	code := int32(200)
	r := LogRecord{ID: "req_1", ModelName: "gpt", ClientStatusCode: &code, InputTokens: 10}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":"req_1"`, `"model_name":"gpt"`, `"client_status_code":200`, `"input_tokens":10`} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %q in %s", want, s)
		}
	}
}
```

- [ ] **Step 5: Run + commit**

```bash
go test ./internal/observability/... -v
git add -A && git commit -m "feat(go/obs): add observability package + signal value types"
```

---

### Task 1.2: Generic rotating parquet sink

**Files:**
- Create: `internal/observability/parquet/sink.go`
- Create: `internal/observability/parquet/sink_test.go`

**Interfaces:**
- Produces: `parquet.Sink[Row]` — `NewSink[Row](dir, signal string, maxRows int) (*Sink[Row], error)`; `(*Sink) Write(rows []Row) error`; `(*Sink) Flush() error`; `(*Sink) Close() error`. Used by P1.3, P2.1.
- Consumes: `observability.LogRecord` etc. (only via type parameter at use site).

- [ ] **Step 1: Write failing test**

```go
package parquet_test

import (
	"path/filepath"
	"testing"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

func TestSinkWriteRotateReadBack(t *testing.T) {
	dir := t.TempDir()
	s, err := parquet.NewSink[observability.LogRecord](dir, "logs", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// maxRows=2 → writing 3 rows forces a rotation (2 files).
	if err := s.Write([]observability.LogRecord{
		{ID: "a", ModelName: "m1"}, {ID: "b", ModelName: "m1"}, {ID: "c", ModelName: "m1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := filepath.Glob(filepath.Join(dir, "logs", "*.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("expected >=2 rotated files, got %d (%v)", len(got), got)
	}
	// No .tmp files left behind (atomic rename).
	tmp, _ := filepath.Glob(filepath.Join(dir, "logs", "*.tmp"))
	if len(tmp) != 0 {
		t.Errorf("leftover .tmp files: %v", tmp)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`parquet.NewSink` undefined): `go test ./internal/observability/parquet/ -run TestSinkWriteRotateReadBack -v`

- [ ] **Step 3: Implement `sink.go`**

```go
// Package parquet holds the rotating parquet sink and reader used by the admin
// to persist the three observability signals. It is instantiated only in the
// admin process; the gateway never imports it.
package parquet

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
)

// Sink[Row] buffers rows in memory and flushes a NEW parquet file when the
// hour boundary crosses or len(buf) reaches maxRows. A closed parquet file is
// immutable, so we never append to one — every flush writes a brand-new file
// to <dir>/<signal>/<YYYYMMDDHH>-<seq>.parquet.tmp and atomically renames it.
type Sink[Row any] struct {
	dir     string
	signal  string
	maxRows int

	mu      sync.Mutex
	buf     []Row
	curHour int64
	seq     int64
}

func NewSink[Row any](dir, signal string, maxRows int) (*Sink[Row], error) {
	if maxRows <= 0 {
		maxRows = 50000
	}
	if err := os.MkdirAll(filepath.Join(dir, signal), 0o755); err != nil {
		return nil, err
	}
	return &Sink[Row]{dir: dir, signal: signal, maxRows: maxRows}, nil
}

func (s *Sink[Row]) Write(rows []Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		s.buf = append(s.buf, r)
		if len(s.buf) >= s.maxRows {
			if err := s.flushLocked(time.Now()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Sink[Row]) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked(time.Now())
}

func (s *Sink[Row]) Close() error {
	return s.Flush()
}

// flushLocked writes buf (if any) to a new file. Caller holds s.mu.
func (s *Sink[Row]) flushLocked(now time.Time) error {
	if len(s.buf) == 0 {
		return nil
	}
	hour := now.UTC().Truncate(time.Hour).UnixNano()
	if hour != s.curHour {
		s.seq = 0
		s.curHour = hour
	}
	s.seq++
	name := filepath.Join(s.dir, s.signal,
		fmt.Sprintf("%s-%04d.parquet", time.Unix(0, hour).UTC().Format("2006010215"), s.seq))
	tmp := name + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	pw := parquet.NewGenericWriter[Row](f)
	if _, err := pw.Write(s.buf); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := pw.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		return err
	}
	s.buf = s.buf[:0]
	return nil
}
```

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/observability/parquet/ -v`
- [ ] **Step 5: Commit.** `git add -A && git commit -m "feat(go/obs): add generic rotating parquet sink (atomic rename, no append)"`

---

### Task 1.3: Typed parquet sinks + reader

**Files:**
- Create: `internal/observability/parquet/reader.go`
- Create: `internal/observability/parquet/reader_test.go`

**Interfaces:**
- Produces: `parquet.ReadSince[Row](dir, signal string, since int64) ([]Row, error)` — globs `<dir>/<signal>/*.parquet` whose hour ≥ since, decodes each, returns all rows in ascending file order. Used by P1.5, P1.6, P2.3, P2.4.

- [ ] **Step 1: Failing test (round-trip via ReadSince)**

```go
package parquet_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

func TestReadSinceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	rows := []observability.LogRecord{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	s.Write(rows)
	s.Flush()
	s.Close()

	got, err := parquet.ReadSince[observability.LogRecord](dir, "logs", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	// IDs survive the parquet round-trip.
	want := map[string]bool{"a": false, "b": false, "c": false}
	for _, r := range got {
		want[r.ID] = true
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing id %q", k)
		}
	}
}
```

- [ ] **Step 2: Run — expect FAIL.** `go test ./internal/observability/parquet/ -run TestReadSinceRoundTrip -v`
- [ ] **Step 3: Implement `reader.go`**

```go
package parquet

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
)

// ReadSince opens every completed parquet file in <dir>/<signal>/ whose hour
// bucket is >= since (since<=0 → all) and returns all rows. Files are visited
// in name order (oldest first). Only completed files (no .tmp suffix) are read.
func ReadSince[Row any](dir, signal string, since int64) ([]Row, error) {
	matches, err := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var out []Row
	for _, name := range matches {
		if since > 0 {
			if h, ok := hourOf(filepath.Base(name)); ok && h < since {
				continue
			}
		}
		rows, err := readParquetFile[Row](name)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// hourOf parses the leading YYYYMMDDHH from a file base name into a unix-nano
// hour bucket; ok=false if the name does not match the convention.
func hourOf(base string) (int64, bool) {
	const layout = "2006010215"
	if len(base) < len(layout) {
		return 0, false
	}
	t, err := time.Parse(layout, base[:len(layout)])
	if err != nil {
		return 0, false
	}
	return t.UnixNano(), true
}

// CountFiles returns the number of completed parquet files for a signal
// (used by janitor tests and ClearAll).
func CountFiles(dir, signal string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	return len(matches), err
}

// RemoveAll deletes every parquet file for a signal (ClearAll implementation).
func RemoveAll(dir, signal string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range matches {
		if err := os.Remove(m); err == nil {
			n++
		}
	}
	return n, nil
}

func readParquetFile[Row any](name string) ([]Row, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pr := parquet.NewGenericReader[Row](f)
	rows := make([]Row, pr.NumRows())
	if _, err := pr.Read(rows); err != nil {
		return nil, err
	}
	return rows, pr.Close()
}
```

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/observability/parquet/ -v`
- [ ] **Step 5: Commit.** `git add -A && git commit -m "feat(go/obs): add parquet ReadSince reader + ClearAll helpers"`

---

### Task 1.4: Stats types + aggregation

**Files:**
- Create: `internal/observability/stats_types.go`
- Create: `internal/observability/stats_aggregate.go`
- Create: `internal/observability/stats_aggregate_test.go`

**Interfaces:**
- Produces: `observability.StatsOverview`, `ModelStats`, `ProviderStats`, `ApiKeyStats`, `StatsHourly` (JSON-identical to the legacy `storage.*Stats` types); `AggregateStats(samples []MetricSample, hours int64) (StatsOverview, []ModelStats, []ProviderStats, []ApiKeyStats, error)`; `AggregateHourly(samples []MetricSample, hours int64) ([]StatsHourly, error)`. Used by P2.4.

> The `Stats*` struct definitions here are **copies** of the current `internal/storage/auth_models.go` `Stats*` types (same JSON tags). Phase 4 deletes the storage copies; until then both exist and the admin picks which to return.

- [ ] **Step 1: Write `stats_types.go`** — copy the 5 `Stats*` structs verbatim from `internal/storage/auth_models.go` (lines 156–201), in package `observability`.

```go
package observability

// Stats* types are JSON-compatible copies of storage.Stats* (Phase 4 removes
// the storage copies). Tag-for-tag identical so the WebUI is unaffected.

type StatsOverview struct {
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
	ErrorCount        int64   `json:"error_count"`
}

type ModelStats struct {
	Model            string  `json:"model"`
	RequestCount     int64   `json:"request_count"`
	TotalInputTokens int64   `json:"total_input_tokens"`
	TotalOutputTokens int64  `json:"total_output_tokens"`
	AvgDurationMs    float64 `json:"avg_duration_ms"`
}

type ProviderStats struct {
	Provider      string  `json:"provider"`
	RequestCount  int64   `json:"request_count"`
	ErrorCount    int64   `json:"error_count"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
}

type ApiKeyStats struct {
	APIKeyID         string `json:"api_key_id"`
	APIKeyName       string `json:"api_key_name"`
	RequestCount     int64  `json:"request_count"`
	TotalInputTokens int64  `json:"total_input_tokens"`
	TotalOutputTokens int64 `json:"total_output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	LastUsedAt       int64  `json:"last_used_at"`
}

type StatsHourly struct {
	Hour              string  `json:"hour"`
	RequestCount      int64   `json:"request_count"`
	ErrorCount        int64   `json:"error_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
}
```

- [ ] **Step 2: Write failing test**

The metric-series convention (what the gateway exporter emits and the admin sink stores) — agreed names:

| Metric name | Kind | Labels | Meaning |
|---|---|---|---|
| `nyro_requests_total` | counter | model,provider,apikey,status_class | +1 per request |
| `nyro_tokens_total` | counter | model,provider,apikey,direction(`in`/`out`/`cache_read`) | token sum |
| `nyro_request_latency_ms` | histogram | model,provider | hist_sum/hist_count |

```go
package observability

import (
	"encoding/json"
	"testing"
)

func sampleReq(model, provider, apikey, status string, n int) []MetricSample {
	var out []MetricSample
	for i := 0; i < n; i++ {
		out = append(out, MetricSample{
			Ts:   1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels(model, provider, apikey, status),
		})
	}
	return out
}

func labels(model, provider, apikey, status string) string {
	b, _ := json.Marshal(map[string]string{"model": model, "provider": provider, "apikey": apikey, "status_class": status})
	return string(b)
}

func TestAggregateStatsOverview(t *testing.T) {
	samples := append(sampleReq("gpt", "openai", "k1", "2xx", 5),
		sampleReq("gpt", "openai", "k1", "5xx", 2)...) // 7 requests, 2 errors
	ov, _, _, _, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ov.TotalRequests != 7 {
		t.Errorf("TotalRequests=%d want 7", ov.TotalRequests)
	}
	if ov.ErrorCount != 2 {
		t.Errorf("ErrorCount=%d want 2", ov.ErrorCount)
	}
}

func TestAggregateStatsByModel(t *testing.T) {
	samples := append(sampleReq("gpt", "openai", "k1", "2xx", 3),
		sampleReq("claude", "anthropic", "k1", "2xx", 4)...)
	_, models, _, _, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 model rows, got %d", len(models))
	}
}
```

- [ ] **Step 3: Run — expect FAIL.** `go test ./internal/observability/ -run Aggregate -v`
- [ ] **Step 4: Implement `stats_aggregate.go`**

```go
package observability

import (
	"encoding/json"
	"sort"
	"time"
)

type metricLabels struct {
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	APIKey      string `json:"apikey"`
	StatusClass string `json:"status_class"`
	Direction   string `json:"direction"`
}

func parseLabels(s string) metricLabels {
	var l metricLabels
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

// AggregateStats rolls up a slice of MetricSamples (a window of metric-history
// parquet rows) into the four real-time stat shapes. hours<=0 means "all".
func AggregateStats(samples []MetricSample, _ int64) (StatsOverview, []ModelStats, []ProviderStats, []ApiKeyStats, error) {
	var ov StatsOverview
	type mAcc struct{ req, in, out int64; lat time.Duration }
	type pAcc struct{ req, err int64; lat time.Duration }
	type kAcc struct{ name string; req, in, out, cache int64; lastTs int64 }
	mmodels := map[string]*mAcc{}
	mprov := map[string]*pAcc{}
	mkey := map[string]*kAcc{}

	var latSum time.Duration
	var latCnt int64
	for _, s := range samples {
		l := parseLabels(s.LabelsJSON)
		switch s.Name {
		case "nyro_requests_total":
			ov.TotalRequests += int64(s.Value)
			if l.StatusClass == "5xx" || l.StatusClass == "4xx" {
				ov.ErrorCount += int64(s.Value)
			}
			mm := mmodels[l.Model]
			if mm == nil {
				mm = &mAcc{}
				mmodels[l.Model] = mm
			}
			mm.req++
			pp := mprov[l.Provider]
			if pp == nil {
				pp = &pAcc{}
				mprov[l.Provider] = pp
			}
			pp.req++
			if l.StatusClass == "4xx" || l.StatusClass == "5xx" {
				pp.err++
			}
			kk := mkey[l.APIKey]
			if kk == nil {
				kk = &kAcc{name: l.APIKey}
				mkey[l.APIKey] = kk
			}
			kk.req++
			if s.Ts > kk.lastTs {
				kk.lastTs = s.Ts
			}
		case "nyro_tokens_total":
			kk := mkey[l.APIKey]
			if kk == nil {
				kk = &kAcc{name: l.APIKey}
				mkey[l.APIKey] = kk
			}
			mm := mmodels[l.Model]
			if mm == nil {
				mm = &mAcc{}
				mmodels[l.Model] = mm
			}
			switch l.Direction {
			case "in":
				ov.TotalInputTokens += int64(s.Value)
				mm.in += int64(s.Value)
				kk.in += int64(s.Value)
			case "out":
				ov.TotalOutputTokens += int64(s.Value)
				mm.out += int64(s.Value)
				kk.out += int64(s.Value)
			case "cache_read":
				kk.cache += int64(s.Value)
			}
		case "nyro_request_latency_ms":
			latSum += time.Duration(s.HistSum * float64(time.Millisecond))
			latCnt += s.HistCount
			if mm := mmodels[l.Model]; mm != nil {
				mm.lat += time.Duration(s.HistSum * float64(time.Millisecond))
			}
		}
	}
	if latCnt > 0 {
		ov.AvgDurationMs = float64(latSum/time.Millisecond) / float64(latCnt)
	}

	models := make([]ModelStats, 0, len(mmodels))
	for name, a := range mmodels {
		avg := float64(0)
		models = append(models, ModelStats{
			Model: name, RequestCount: a.req, TotalInputTokens: a.in, TotalOutputTokens: a.out, AvgDurationMs: avg,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].RequestCount > models[j].RequestCount })

	provs := make([]ProviderStats, 0, len(mprov))
	for name, a := range mprov {
		provs = append(provs, ProviderStats{Provider: name, RequestCount: a.req, ErrorCount: a.err})
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i].RequestCount > provs[j].RequestCount })

	keys := make([]ApiKeyStats, 0, len(mkey))
	for id, a := range mkey {
		name := id
		if a.name != "" {
			name = a.name
		}
		keys = append(keys, ApiKeyStats{APIKeyID: id, APIKeyName: name, RequestCount: a.req,
			TotalInputTokens: a.in, TotalOutputTokens: a.out, CacheReadTokens: a.cache, LastUsedAt: a.lastTs})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].RequestCount > keys[j].RequestCount })

	return ov, models, provs, keys, nil
}

// AggregateHourly buckets samples into UTC hour buckets (ISO hour label).
func AggregateHourly(samples []MetricSample, _ int64) ([]StatsHourly, error) {
	type b struct{ req, err, in, out int64; latSum float64; latCnt int64 }
	buckets := map[string]*b{}
	for _, s := range samples {
		hour := time.Unix(0, s.Ts).UTC().Truncate(time.Hour).Format("2006-01-02T15:00:00Z")
		bb := buckets[hour]
		if bb == nil {
			bb = &b{}
			buckets[hour] = bb
		}
		l := parseLabels(s.LabelsJSON)
		switch s.Name {
		case "nyro_requests_total":
			bb.req += int64(s.Value)
			if l.StatusClass == "4xx" || l.StatusClass == "5xx" {
				bb.err += int64(s.Value)
			}
		case "nyro_tokens_total":
			if l.Direction == "in" {
				bb.in += int64(s.Value)
			} else if l.Direction == "out" {
				bb.out += int64(s.Value)
			}
		case "nyro_request_latency_ms":
			bb.latSum += s.HistSum
			bb.latCnt += s.HistCount
		}
	}
	out := make([]StatsHourly, 0, len(buckets))
	for hour, bb := range buckets {
		avg := float64(0)
		if bb.latCnt > 0 {
			avg = bb.latSum / float64(bb.latCnt)
		}
		out = append(out, StatsHourly{Hour: hour, RequestCount: bb.req, ErrorCount: bb.err,
			TotalInputTokens: bb.in, TotalOutputTokens: bb.out, AvgDurationMs: avg})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	return out, nil
}
```

- [ ] **Step 5: Run — expect PASS.** `go test ./internal/observability/ -v`
- [ ] **Step 6: Commit.** `git add -A && git commit -m "feat(go/obs): add stats types + Go aggregation over metric samples"`

---

### Task 1.5: Logs query facade + janitor + config

**Files:**
- Create: `internal/observability/logs_query.go` + `logs_query_test.go`
- Create: `internal/observability/janitor.go` + `janitor_test.go`
- Create: `internal/observability/config.go` + `config_test.go`

**Interfaces:**
- Produces:
  - `observability.LogQuery` (copy of `storage.LogQuery`), `observability.LogPage{Items []LogRecord; Total int64}`.
  - `observability.Logs` struct: `NewLogs(dir string) *Logs`; `Query(LogQuery) (LogPage, error)`; `FindByID(string) (*LogRecord, error)`; `ClearAll() (int64, error)`.
  - `observability.StartJanitor(ctx, dir string, retention SignalRetention, period time.Duration)`.
  - `observability.ObsConfig`, `observability.LoadConfig(get func(string)string) ObsConfig`.

- [ ] **Step 1: Write `logs_query.go`**

```go
package observability

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

type LogQuery struct {
	Limit     int64
	Offset    int64
	Provider  string
	Model     string
	StatusMin *int32
	StatusMax *int32
}

type LogPage struct {
	Items []LogRecord `json:"items"`
	Total int64       `json:"total"`
}

type Logs struct{ dir string }

func NewLogs(dir string) *Logs { return &Logs{dir: dir} }

func (l *Logs) Query(q LogQuery) (LogPage, error) {
	rows, err := parquet.ReadSince[LogRecord](l.dir, "logs", 0)
	if err != nil {
		return LogPage{}, err
	}
	filtered := rows[:0]
	for _, r := range rows {
		if q.Provider != "" && r.ProviderID != q.Provider {
			continue
		}
		if q.Model != "" && r.ModelID != q.Model {
			continue
		}
		if q.StatusMin != nil && r.ClientStatusCode != nil && *r.ClientStatusCode < *q.StatusMin {
			continue
		}
		if q.StatusMax != nil && r.ClientStatusCode != nil && *r.ClientStatusCode > *q.StatusMax {
			continue
		}
		filtered = append(filtered, r)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt > filtered[j].CreatedAt })
	total := int64(len(filtered))
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	start := q.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return LogPage{Items: filtered[start:end], Total: total}, nil
}

func (l *Logs) FindByID(id string) (*LogRecord, error) {
	rows, err := parquet.ReadSince[LogRecord](l.dir, "logs", 0)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ID == id {
			rc := r
			return &rc, nil
		}
	}
	return nil, nil
}

func (l *Logs) ClearAll() (int64, error) {
	n, err := parquet.RemoveAll(l.dir, "logs")
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}
```

(`ClearAll` only needs `parquet.RemoveAll`; no `filepath` import.)

- [ ] **Step 2: Write `janitor.go`**

```go
package observability

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// SignalRetention gives per-signal retention. A signal dir is cleaned if its
// files' hour bucket is older than now - Days.
type SignalRetention struct {
	Logs, Metrics, Traces int // days; <=0 → skip that signal
}

func StartJanitor(ctx context.Context, dir string, rt SignalRetention, period time.Duration) {
	if period <= 0 {
		period = time.Hour
	}
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				now := time.Now().UTC()
				cleanSignal(dir, "logs", rt.Logs, now)
				cleanSignal(dir, "metrics", rt.Metrics, now)
				cleanSignal(dir, "traces", rt.Traces, now)
			}
		}
	}()
}

func cleanSignal(dir, signal string, days int, now time.Time) {
	if days <= 0 {
		return
	}
	cutoff := now.AddDate(0, 0, -days)
	matches, _ := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	for _, f := range matches {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(f); err == nil {
				slog.Info("obs janitor removed file", "path", f)
			}
		}
	}
}
```

- [ ] **Step 3: Write `config.go`**

```go
package observability

import (
	"strconv"
	"time"
)

type ObsConfig struct {
	// Gateway (exporter) side.
	Sink               string // global default: none|stdout|otlp
	LogsSink           string // per-signal override ("" → Sink)
	MetricsSink        string
	TracesSink         string
	OTLPEndpoint       string // e.g. "http://127.0.0.1:19531"
	ExportInterval     time.Duration

	// Admin (sink) side.
	DataDir                string
	LogsRetentionDays      int
	MetricsRetentionDays   int
	TracesRetentionDays    int
}

func strOr(def, v string) string {
	if v != "" {
		return v
	}
	return def
}

// LoadConfig reads observability settings via get (typically s.Settings().Get).
// Empty strings resolve to documented defaults.
func LoadConfig(get func(string) (string, error)) ObsConfig {
	g := func(k string) string { v, _ := get(k); return v }
	itv, _ := time.ParseDuration(g("obs_export_interval"))
	if itv <= 0 {
		itv = 5 * time.Second
	}
	dataDir := g("obs_data_dir")
	if dataDir == "" {
		dataDir = "./data/obs"
	}
	ret := func(k string, def int) int {
		if v := g(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
		return def
	}
	return ObsConfig{
		Sink:               g("obs_sink"),
		LogsSink:           g("obs_logs_sink"),
		MetricsSink:        g("obs_metrics_sink"),
		TracesSink:         g("obs_traces_sink"),
		OTLPEndpoint:       g("obs_otlp_endpoint"),
		ExportInterval:     itv,
		DataDir:            dataDir,
		LogsRetentionDays:  ret("obs_logs_retention_days", 7),
		MetricsRetentionDays: ret("obs_metrics_retention_days", 30),
		TracesRetentionDays:  ret("obs_traces_retention_days", 3),
	}
}
```

- [ ] **Step 4: Tests** — `logs_query_test.go` (write 3 rows via a `parquet.Sink[LogRecord]`, then `Query` returns them sorted/paginated; `FindByID` hits; `ClearAll` empties); `janitor_test.go` (create a file with `ModTime` 10 days ago via `os.Chtimes`, run `cleanSignal` with days=7, assert removed; a recent file survives — call `cleanSignal` directly or factor it as exported `CleanSignal`); `config_test.go` (a `get` stub returns known values, assert `LoadConfig` maps them; defaults when empty).

- [ ] **Step 5: Run + commit.**

```bash
go test ./internal/observability/... -v
git add -A && git commit -m "feat(go/obs): add Logs query facade, janitor, config loader"
```

---

### Phase 1 exit

`go build ./... && go vet ./... && go test ./...` green. No existing file changed except `go.mod`/`go.sum`. `CGO_ENABLED=0 go build ./...` green.

---

## Phase 2 — admin OTLP receiver + `/logs` + `/stats` (dual-write)

> During Phase 2 the gateway still writes the old `request_logs` table AND begins OTLP export; admin prefers parquet, falls back to the old table when parquet is empty. Zero behavior regression.

### Task 2.1: OTLP/HTTP receiver (decode → parquet sink)

**Files:**
- Create: `internal/observability/receiver.go`
- Create: `internal/observability/receiver_test.go`

**Interfaces:**
- Produces: `observability.Receiver` — `NewReceiver(logs *parquet.Sink[LogRecord], metrics *parquet.Sink[MetricSample], traces *parquet.Sink[SpanSnapshot]) *Receiver`; `Mount(r chi.Router)` registers `POST /v1/{logs,metrics,traces}`. Used by P2.2.
- Consumes: the three `parquet.Sink[Row]` (P1.2/1.3), the official OTLP proto types.

- [ ] **Step 1: Write failing test** (POST a real `ExportLogsServiceRequest`, assert a `LogRecord` lands in the logs sink / parquet).

```go
package observability_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

func TestReceiverLogs(t *testing.T) {
	dir := t.TempDir()
	ls, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	ms, _ := parquet.NewSink[observability.MetricSample](dir, "metrics", 100)
	ts, _ := parquet.NewSink[observability.SpanSnapshot](dir, "traces", 100)
	rcv := observability.NewReceiver(ls, ms, ts)

	r := chi.NewRouter()
	rcv.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	req := &collectlogs.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{{
			ScopeLogs: []*logsv1.ScopeLogs{{
				LogRecords: []*logsv1.LogRecord{{
					Attributes: nyroLogAttrs("req_xyz", "gpt", "openai", "2xx"),
				}},
			}},
		}},
	}
	body, _ := proto.Marshal(req)
	resp, err := http.Post(srv.URL+"/v1/logs", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	rcv.Flush(context.Background())

	got, err := parquet.ReadSince[observability.LogRecord](dir, "logs", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "req_xyz" {
		t.Fatalf("unexpected logs: %+v", got)
	}
}

// nyroLogAttrs builds the attribute set the gateway emits for one request log,
// matching the receiver's attribute-key contract (Step 3).
func nyroLogAttrs(id, model, provider, statusClass string) []*commonv1.KeyValue {
	str := func(k, v string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
	}
	return []*commonv1.KeyValue{
		str("nyro.log.id", id),
		str("nyro.model_name", model),
		str("nyro.provider_name", provider),
		str("nyro.status_class", statusClass),
	}
}
```

(The `kv`/attribute helper is completed in Step 3 once the attribute key contract is fixed.)

- [ ] **Step 2: Run — expect FAIL.** `go test ./internal/observability/ -run TestReceiverLogs -v`

- [ ] **Step 3: Implement `receiver.go`**

The **attribute key contract** between gateway emitter and admin receiver (the gateway sets these as OTel log/span attributes; the receiver reads them by key):

| Key | Type | Maps to |
|---|---|---|
| `nyro.log.id` | string | `LogRecord.ID` |
| `nyro.log.created_ms` | int | `LogRecord.CreatedAt` |
| `nyro.api_key_id` / `nyro.api_key_name` | string | id/name |
| `nyro.client_protocol` / `nyro.upstream_protocol` | string | protocols |
| `nyro.provider_id` / `nyro.provider_name` | string | id/name |
| `nyro.model_id` / `nyro.model_name` | string | id/name |
| `nyro.client_model` / `nyro.upstream_model` | string | models |
| `nyro.method` / `nyro.path` | string | method/path |
| `nyro.client_status` / `nyro.upstream_status` | int | status codes |
| `nyro.latency_total_ms` / `nyro.latency_upstream_ms` | int | latencies |
| `nyro.input_tokens` / `nyro.output_tokens` / `nyro.cache_read_tokens` | int | tokens |
| `nyro.is_stream` | bool | stream flag |

```go
package observability

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"encoding/hex"
	"encoding/json"
)

// Receiver is the admin-side OTLP/HTTP receiver. It decodes the official OTLP
// protobuf and hands rows to the parquet sinks. Receive-then-ACK: decode is
// synchronous, parquet write is buffered (flushed by the sink on rotation and
// on Flush).
type Receiver struct {
	logs    *parquet.Sink[LogRecord]
	metrics *parquet.Sink[MetricSample]
	traces  *parquet.Sink[SpanSnapshot]

	mu sync.Mutex
}

func NewReceiver(logs *parquet.Sink[LogRecord], metrics *parquet.Sink[MetricSample], traces *parquet.Sink[SpanSnapshot]) *Receiver {
	return &Receiver{logs: logs, metrics: metrics, traces: traces}
}

func (rcv *Receiver) Mount(r chi.Router) {
	r.Post("/v1/logs", rcv.handleLogs)
	r.Post("/v1/metrics", rcv.handleMetrics)
	r.Post("/v1/traces", rcv.handleTraces)
}

func (rcv *Receiver) Flush(ctx context.Context) {
	rcv.logs.Flush()
	rcv.metrics.Flush()
	rcv.traces.Flush()
}

func (rcv *Receiver) handleLogs(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collectlogs.ExportLogsServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rows []LogRecord
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				rows = append(rows, logRecordFromOTLP(lr))
			}
		}
	}
	if len(rows) > 0 {
		if err := rcv.logs.Write(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	wb, _ := proto.Marshal(&collectlogs.ExportLogsServiceResponse{})
	_, _ = w.Write(wb)
}

// logRecordFromOTLP maps the gateway's attribute contract onto LogRecord.
func logRecordFromOTLP(lr *logsv1.LogRecord) LogRecord {
	m := attrMap(lr.GetAttributes())
	rec := LogRecord{
		ID:               m.str("nyro.log.id"),
		ClientProtocol:   m.str("nyro.client_protocol"),
		UpstreamProtocol: m.str("nyro.upstream_protocol"),
		ProviderID:       m.str("nyro.provider_id"), ProviderName: m.str("nyro.provider_name"),
		ModelID:          m.str("nyro.model_id"), ModelName: m.str("nyro.model_name"),
		ClientModel:      m.str("nyro.client_model"), UpstreamModel: m.str("nyro.upstream_model"),
		Method:           m.str("nyro.method"), Path: m.str("nyro.path"),
		APIKeyID:         m.str("nyro.api_key_id"), APIKeyName: m.str("nyro.api_key_name"),
		InputTokens:      int32(m.i("nyro.input_tokens")), OutputTokens: int32(m.i("nyro.output_tokens")),
		CacheReadTokens:  int32(m.i("nyro.cache_read_tokens")),
	}
	if v, ok := m.lookup("nyro.log.created_ms"); ok {
		rec.CreatedAt = v.I
	}
	if v, ok := m.lookupInt("nyro.client_status"); ok {
		c := int32(v)
		rec.ClientStatusCode = &c
	}
	if v, ok := m.lookupInt("nyro.upstream_status"); ok {
		c := int32(v)
		rec.UpstreamStatusCode = &c
	}
	if v, ok := m.lookupInt("nyro.latency_total_ms"); ok {
		l := v
		rec.LatencyTotalMs = &l
	}
	if v, ok := m.lookupInt("nyro.latency_upstream_ms"); ok {
		l := v
		rec.LatencyUpstreamMs = &l
	}
	rec.IsStream = m.bool("nyro.is_stream")
	return rec
}

func (rcv *Receiver) handleMetrics(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collectmetrics.ExportMetricsServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rows []MetricSample
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				name := m.GetName()
				switch d := m.Data.(type) {
				case *metricsv1.Metric_Gauge:
					for _, p := range d.Gauge.GetDataPoints() {
						rows = append(rows, MetricSample{Ts: int64(p.GetTimeUnixNano()), Name: name, Kind: "gauge",
							Value: dataPointValue(p), LabelsJSON: attrsToJSON(p.GetAttributes())})
					}
				case *metricsv1.Metric_Sum:
					for _, p := range d.Sum.GetDataPoints() {
						rows = append(rows, MetricSample{Ts: int64(p.GetTimeUnixNano()), Name: name, Kind: "counter",
							Value: dataPointValue(p), LabelsJSON: attrsToJSON(p.GetAttributes())})
					}
				case *metricsv1.Metric_Histogram:
					for _, p := range d.Histogram.GetDataPoints() {
						rows = append(rows, MetricSample{Ts: int64(p.GetTimeUnixNano()), Name: name, Kind: "histogram",
							HistSum: p.GetSum(), HistCount: int64(p.GetCount()), LabelsJSON: attrsToJSON(p.GetAttributes())})
					}
				}
			}
		}
	}
	if len(rows) > 0 {
		if err := rcv.metrics.Write(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	wb, _ := proto.Marshal(&collectmetrics.ExportMetricsServiceResponse{})
	_, _ = w.Write(wb)
}

func (rcv *Receiver) handleTraces(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collecttrace.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rows []SpanSnapshot
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				rows = append(rows, SpanSnapshot{
					TraceID: hex.EncodeToString(sp.GetTraceId()), SpanID: hex.EncodeToString(sp.GetSpanId()),
					ParentSpanID: hex.EncodeToString(sp.GetParentSpanId()), Name: sp.GetName(),
					StartNs: int64(sp.GetStartTimeUnixNano()), EndNs: int64(sp.GetEndTimeUnixNano()),
					DurationNs: int64(sp.GetEndTimeUnixNano() - sp.GetStartTimeUnixNano()),
					StatusCode: int32(sp.GetStatus().GetCode()), AttrsJSON: attrsToJSON(sp.GetAttributes()),
				})
			}
		}
	}
	if len(rows) > 0 {
		if err := rcv.traces.Write(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	wb, _ := proto.Marshal(&collecttrace.ExportTraceServiceResponse{})
	_, _ = w.Write(wb)
}

func dataPointValue(p *metricsv1.NumberDataPoint) float64 {
	if v, ok := p.Value.(*metricsv1.NumberDataPoint_AsDouble); ok {
		return v.AsDouble
	}
	if v, ok := p.Value.(*metricsv1.NumberDataPoint_AsInt); ok {
		return float64(v.AsInt)
	}
	return 0
}

// attrsToJSON flattens an OTLP attribute set to a compact JSON object string
// (sufficient for nyro's bounded label set).
func attrsToJSON(kvs []*commonv1.KeyValue) string {
	if len(kvs) == 0 {
		return "{}"
	}
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = anyValue(kv.GetValue())
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func anyValue(v *commonv1.AnyValue) any {
	switch x := v.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return x.StringValue
	case *commonv1.AnyValue_IntValue:
		return x.IntValue
	case *commonv1.AnyValue_BoolValue:
		return x.BoolValue
	case *commonv1.AnyValue_DoubleValue:
		return x.DoubleValue
	default:
		return v.String()
	}
}

// --- attribute helpers (logs path reads known keys by name) ---

type attrTable struct{ m map[string]*commonv1.KeyValue }

func attrMap(attrs []*v1.KeyValue) attrTable {
	out := attrTable{m: map[string]*v1.KeyValue{}}
	for _, kv := range attrs {
		out.m[kv.GetKey()] = kv
	}
	return out
}
func (a attrTable) str(k string) string {
	if kv, ok := a.m[k]; ok {
		return kv.GetValue().GetStringValue()
	}
	return ""
}
func (a attrTable) i(k string) int64 {
	if v, ok := a.lookupInt(k); ok {
		return v
	}
	return 0
}
func (a attrTable) bool(k string) bool {
	if kv, ok := a.m[k]; ok {
		return kv.GetValue().GetBoolValue()
	}
	return false
}
func (a attrTable) lookup(k string) (struct{ I int64 }, bool) {
	if kv, ok := a.m[k]; ok {
		if v := kv.GetValue().GetIntValue(); v != 0 {
			return struct{ I int64 }{v}, true
		}
	}
	return struct{ I int64 }{}, false
}
func (a attrTable) lookupInt(k string) (int64, bool) {
	if kv, ok := a.m[k]; ok {
		if v := kv.GetValue().GetIntValue(); v != 0 {
			return v, true
		}
	}
	return 0, false
}
```

- [ ] **Step 4: Run + commit.** `go test ./internal/observability/ -v` then `git add -A && git commit -m "feat(go/obs): add OTLP/HTTP receiver (logs/metrics/traces → parquet)"`

> The `handleMetrics`/`handleTraces` handlers above are complete (they decode `ExportMetricsServiceRequest`/`ExportTraceServiceRequest`, map `Sum`/`Gauge`/`Histogram` data points → `MetricSample` and `Span` → `SpanSnapshot`, write to the sink, and ACK with the empty service response). Add a metrics round-trip test mirroring `TestReceiverLogs` (build an `ExportMetricsServiceRequest` with one `Sum` counter + a label, POST, assert the `MetricSample` lands in the metrics parquet).

---

### Task 2.2: Admin wiring — receiver + sinks + janitor

**Files:**
- Modify: `cmd/admin/admin.go`
- Modify: `internal/admin/admin.go` (add `LogSource`/`StatsSource` injection points only — handler bodies change in T2.3/T2.4)

**Interfaces:**
- Produces (in `internal/admin`): `admin.LogSource` interface (`Query(storage.LogQuery) (storage.LogPage, error)` etc. — note: during dual-write these still return `storage.*` types; Phase 4 swaps to `observability.*`); `admin.StatsSource` interface (`StatsOverview/ByModel/... (hours int64) (...)`). Mount gains params.
- Consumes: `observability.NewReceiver`, `parquet.NewSink`, `observability.StartJanitor`, `observability.LoadConfig`.

- [ ] **Step 1: Define seams in `internal/admin/admin.go`**

```go
// LogSource is the read side for /logs. Backed by parquet (observability.Logs)
// in the new path; the legacy storage.LogStore satisfies it during dual-write.
type LogSource interface {
	Query(q storage.LogQuery) (storage.LogPage, error)
	FindByID(id string) (*storage.RequestLog, error)
	ClearAll() (int64, error)
}

// StatsSource is the read side for /stats/*.
type StatsSource interface {
	StatsOverview(hours int64) (storage.StatsOverview, error)
	StatsByModel(hours int64) ([]storage.ModelStats, error)
	StatsByProvider(hours int64) ([]storage.ProviderStats, error)
	StatsByApiKey(hours int64) ([]storage.ApiKeyStats, error)
	StatsHourly(hours int64) ([]storage.StatsHourly, error)
}
```

`admin.Mount` signature becomes:

```go
func Mount(r chi.Router, s storage.Storage, adminToken string, logs LogSource, stats StatsSource)
```

During dual-write, `logs`/`stats` can be nil → handlers fall back to `s.Logs()` (the old table). This keeps T2.2 itself behavior-neutral.

- [ ] **Step 2: Wire `cmd/admin/admin.go`**

```go
// after st is opened, before admin.Mount:
obsCfg := observability.LoadConfig(st.Settings().Get)
logSink, _ := parquet.NewSink[observability.LogRecord](obsCfg.DataDir, "logs", 50000)
metricSink, _ := parquet.NewSink[observability.MetricSample](obsCfg.DataDir, "metrics", 50000)
traceSink, _ := parquet.NewSink[observability.SpanSnapshot](obsCfg.DataDir, "traces", 50000)
rcv := observability.NewReceiver(logSink, metricSink, traceSink)
rcv.Mount(engine) // top-level /v1/* routes, NOT under /api/v1 bearer auth
observability.StartJanitor(ctx, obsCfg.DataDir, observability.SignalRetention{
	Logs: obsCfg.LogsRetentionDays, Metrics: obsCfg.MetricsRetentionDays, Traces: obsCfg.TracesRetentionDays,
}, time.Hour)

// parquet-backed sources with old-table fallback (P2.3/P2.4 fill these in):
var logSrc admin.LogSource    // = newParquetLogSource(logSink dir) or nil
var statsSrc admin.StatsSource // = newParquetStatsSource(metricSink dir) or nil
admin.Mount(engine, st, adminToken, logSrc, statsSrc)
```

(Move the existing `admin.Mount(engine, st, adminToken)` call to the new signature; pass `nil, nil` for now — handlers keep using `s.Logs()`. Update the one call site.)

- [ ] **Step 3: Run + commit.** `go build ./... && go test ./...` then `git add -A && git commit -m "feat(go/admin): mount OTLP receiver + parquet sinks + janitor; add LogSource/StatsSource seams"`

---

### Task 2.3: `/logs` → parquet (with old-table fallback)

**Files:**
- Modify: `internal/admin/admin.go` (the `/logs`, `/logs/{id}`, `DELETE /logs` handlers at 213–243)
- Create: `internal/admin/obs_source.go` (parquet-backed `LogSource` + `StatsSource` adapters with fallback)
- Create: `internal/admin/obs_source_test.go`

- [ ] **Step 1: Write `obs_source.go`** — a `parquetLogSource{logs *observability.Logs; fallback storage.Storage}` implementing `LogSource`: if `logs != nil`, call it; on empty result, fall back to `fallback.Logs()`. (Construct from the sink dir.) Provide `newParquetLogSource(dir string, fallback storage.Storage) LogSource`.

```go
package admin

import (
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/storage"
)

type parquetLogSource struct {
	logs     *observability.Logs
	fallback storage.Storage
}

func newParquetLogSource(dir string, fallback storage.Storage) LogSource {
	return &parquetLogSource{logs: observability.NewLogs(dir), fallback: fallback}
}

func (p *parquetLogSource) Query(q storage.LogQuery) (storage.LogPage, error) {
	page, err := p.logs.Query(toObsQuery(q))
	if err != nil {
		return storage.LogPage{}, err
	}
	if len(page.Items) > 0 || p.fallback == nil {
		return toStoragePage(page), nil
	}
	return p.fallback.Logs().Query(q) // dual-write fallback
}
// FindByID, ClearAll analogous (FindByID converts LogRecord→storage.RequestLog;
// ClearAll calls logs.ClearAll then fallback.Logs().ClearAll).
```

Provide `toObsQuery`, `toStoragePage`, and `logRecordToRequestLog(observability.LogRecord) storage.RequestLog` (field-for-field copy; both have identical 21 columns).

- [ ] **Step 2: Swap the three `/logs` handlers** in `admin.Mount` to use the injected `LogSource` (call `logs.Query/FindByID/ClearAll` when non-nil, else `s.Logs().*` as today).

- [ ] **Step 3: Test** — seed parquet via a sink, assert `/logs` returns the row; seed only the old table, assert fallback returns it.
- [ ] **Step 4: Run + commit.** `go build ./... && go test ./...` then `git commit -m "feat(go/admin): /logs reads parquet with old-table fallback"`

---

### Task 2.4: `/stats/*` → metrics parquet (with old-table fallback)

**Files:**
- Modify: `internal/admin/admin.go` (the five `/stats/*` handlers at 255–302)
- Modify: `internal/admin/obs_source.go` (add `parquetStatsSource`)

- [ ] **Step 1: Add `parquetStatsSource`** implementing `StatsSource`: read metrics parquet via `parquet.ReadSince[MetricSample](dir, "metrics", cutoffMs)`, call `observability.AggregateStats` / `AggregateHourly`; if the result is empty AND fallback non-nil, call `fallback.Logs().Stats*`. Convert `observability.Stats*` → `storage.Stats*` (identical fields).

- [ ] **Step 2: Swap the five `/stats/*` handlers** to use `stats.StatsOverview/ByModel/...` when non-nil.
- [ ] **Step 3: Test** — seed metrics parquet with known `MetricSample`s, assert `/stats/overview` and `/stats/models` return expected counts; empty-parquet + old-table-seeded → fallback returns old aggregates.
- [ ] **Step 4: Run + commit.** `go build ./... && go test ./...` then `CGO_ENABLED=0 go build ./...` (CI gate starts here) then `git commit -m "feat(go/admin): /stats reads metrics parquet with old-table fallback"`

---

### Phase 2 exit

`go build ./... && go vet ./... && go test ./...` green; `CGO_ENABLED=0 go build ./...` green. End-to-end (manual): `nyro admin` + `nyro gateway` (gateway still dual-writing), send a chat request → `/api/v1/logs` and `/api/v1/stats/overview` return data from parquet (verify by checking `data/obs/logs/*.parquet` grows). Old table still written (removed in P4).

---

## Phase 3 — gateway OTel SDK + cut `Storage` (completes xDS P3c)

> Implementation refinement (honors spec §4 "OTel SDK one API", simplifies spec §5's custom sink interface): the gateway does NOT define `LogSink`/`MetricSink`/`SpanSink` interfaces. It configures OTel providers with built-in exporters selected by sink setting (`none` = no processor registered; `stdout` = `stdout{log,trace,metric}`; `otlp` = `otlp{log,trace,metric}http`). The pluggable semantics are unchanged; one redundant abstraction layer is removed. The admin parquet sink is a separate, non-OTel component (P1/P2).

### Task 3.1: ObsProvider (assemble OTel providers)

**Files:**
- Create: `internal/observability/provider.go`
- Create: `internal/observability/provider_test.go`

**Interfaces:**
- Produces: `observability.ObsProvider` — `NewProvider(ctx, ObsConfig) (*ObsProvider, error)`; fields `Logger *log.Logger`, `Meter metric.Meter`, `Tracer trace.Tracer`; `Flush(ctx)`; `Shutdown(ctx)`. Used by P3.2, P3.4.

- [ ] **Step 1: Implement `provider.go`**

```go
package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

type ObsProvider struct {
	loggerProvider *sdklog.LoggerProvider
	meterProvider  *sdkmetric.MeterProvider
	tracerProvider *sdktrace.TracerProvider

	Logger log.Logger
	Meter  metric.Meter
	Tracer trace.Tracer
}

func resolve(cfg ObsConfig, logs, metrics, traces string) (string, string, string) {
	return strOr(cfg.Sink, logs), strOr(cfg.Sink, metrics), strOr(cfg.Sink, traces)
}

func NewProvider(ctx context.Context, cfg ObsConfig) (*ObsProvider, error) {
	if cfg.OTLPEndpoint == "" {
		// standalone with otlp sink but no endpoint: fail-fast (no silent data loss).
		l, m, t := resolve(cfg, cfg.LogsSink, cfg.MetricsSink, cfg.TracesSink)
		if l == "otlp" || m == "otlp" || t == "otlp" {
			return nil, errors.New("observability: obs_otlp_endpoint required when a sink is 'otlp'")
		}
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("nyro-gateway")))
	if err != nil {
		return nil, err
	}
	logSink, metricSink, traceSink := resolve(cfg, cfg.LogsSink, cfg.MetricsSink, cfg.TracesSink)

	// --- logs ---
	var lp *sdklog.LoggerProvider
	switch logSink {
	case "otlp":
		exp, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		lp = sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))
	case "stdout":
		exp, err := stdoutlog.New()
		if err != nil {
			return nil, err
		}
		lp = sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))
	default: // "none"
		lp = sdklog.NewLoggerProvider(sdklog.WithResource(res))
	}

	// --- metrics ---
	var mp *sdkmetric.MeterProvider
	switch metricSink {
	case "otlp":
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.ExportInterval))))
	case "stdout":
		exp, err := stdoutmetric.New()
		if err != nil {
			return nil, err
		}
		mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.ExportInterval))))
	default:
		mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res))
	}

	// --- traces ---
	var tp *sdktrace.TracerProvider
	switch traceSink {
	case "otlp":
		exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res),
			sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(cfg.ExportInterval)))
	case "stdout":
		exp, err := stdouttrace.New()
		if err != nil {
			return nil, err
		}
		tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exp))
	default:
		tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	}

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	return &ObsProvider{
		loggerProvider: lp, meterProvider: mp, tracerProvider: tp,
		Logger: lp.Logger("nyro"), Meter: mp.Meter("nyro"), Tracer: tp.Tracer("nyro"),
	}, nil
}

func (p *ObsProvider) Flush(ctx context.Context) error { _ = ctx; return nil }
func (p *ObsProvider) Shutdown(ctx context.Context) error {
	var errs []error
	if err := p.loggerProvider.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := p.meterProvider.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := p.tracerProvider.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
```

- [ ] **Step 2: Test** — `provider_test.go`: construct with `{Sink:"none"}` (no exporters, no error); with `{Sink:"stdout"}` (stdout exporters, no error); with `{Sink:"otlp"}` and empty endpoint (error). Shutdown is idempotent.
- [ ] **Step 3: Run + commit.** `go test ./internal/observability/ -v` then `git commit -m "feat(go/obs): add ObsProvider — assembles OTel providers from sink config"`

---

### Task 3.2: Metrics handles + phase hooks

**Files:**
- Create: `internal/observability/metrics_handles.go`
- Create: `internal/observability/hooks.go`
- Create: `internal/observability/hooks_test.go`

**Interfaces:**
- Produces: `observability.Handles` (counter/histogram handles created from a `metric.Meter`); `observability.RegisterHooks(tracer trace.Tracer, logger log.Logger, handles *Handles)` — registers the `OnRequest`/`OnLog` phase hooks via `plugin.Register`. The `OnLog` hook reads request state from `plugin.ContextBag` (set by the dispatcher in T3.3).

**ContextBag key contract** (set by dispatcher, read by hooks):

| Bag key | Value type | Set by | Read by |
|---|---|---|---|
| `obs.span` | `trace.Span` | OnRequest hook | OnLog hook (end span) |
| `obs.ctx` | `context.Context` (span ctx) | OnRequest hook | OnLog hook |
| `obs.model` | `storage.Model` | dispatcher | OnLog (attrs + labels) |
| `obs.provider` | `storage.Provider` | dispatcher | OnLog |
| `obs.api_key_id` / `obs.api_key_name` | string | dispatcher | OnLog |
| `obs.usage` | `ir.Usage` | dispatcher (after response) | OnLog (tokens) |
| `obs.started` | `time.Time` | dispatcher | OnLog (latency) |
| `obs.status` | `int` | dispatcher (via statusRecorder) | OnLog |
| `obs.logctx` | `logCtx` | dispatcher | OnLog (protocols/model names/path/method) |

- [ ] **Step 1: Add `LogCtx`/`NewRequestID` to observability; write `metrics_handles.go`**

Add `LogCtx` (exported fields) and `NewRequestID` to `internal/observability/signals.go` as NEW symbols. The proxy's existing unexported `logCtx`/`newRequestID` STAY in place for now — T3.3 switches the dispatcher to these observability copies and removes the proxy originals, so both tasks stay green. Add to `signals.go`:

```go
type LogCtx struct {
	APIKeyName        string
	ClientProtocol    string
	UpstreamProtocol  string
	ClientModel       string
	UpstreamModel     string
	Method            string
	Path              string
	IsStream          bool
	UpstreamStatus    *int32
	LatencyUpstreamMs *int64
}
```

Then write `metrics_handles.go`:

```go
package observability

import (
	"go.opentelemetry.io/otel/metric"
)

type Handles struct {
	requests    metric.Int64Counter
	tokens      metric.Int64Counter
	latency     metric.Float64Histogram
	inFlight    metric.Int64UpDownCounter
}

func NewHandles(m metric.Meter) *Handles {
	h := &Handles{}
	h.requests, _ = m.Int64Counter("nyro_requests_total")
	h.tokens, _ = m.Int64Counter("nyro_tokens_total")
	h.latency, _ = m.Float64Histogram("nyro_request_latency_ms", metric.WithUnit("ms"))
	h.inFlight, _ = m.Int64UpDownCounter("nyro_in_flight")
	return h
}
```

- [ ] **Step 2: Write `hooks.go`**

```go
package observability

import (
	"context"
	"time"

	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// Bag key contract. Exported (plain strings) so the proxy dispatcher can set
// them. plugin.ContextBag uses string keys.
const (
	BagSpan     = "obs.span"
	BagSpanCtx  = "obs.ctx"
	BagModel    = "obs.model"
	BagProvider = "obs.provider"
	BagAPIKeyID = "obs.api_key_id"
	BagLogCtx   = "obs.logctx"
	BagUsage    = "obs.usage"
	BagStarted  = "obs.started"
	BagStatus   = "obs.status"
)

type onRequestHook struct{ tracer trace.Tracer }

func (h onRequestHook) Name() string        { return "obs.on_request" }
func (h onRequestHook) Phase() plugin.Phase { return plugin.PhaseOnRequest }
func (h onRequestHook) Run(pctx *plugin.PhaseContext) plugin.PhaseOutcome {
	ctx, span := h.tracer.Start(pctx.Ctx, "dispatch")
	pctx.Bag.Set(BagSpanCtx, ctx)
	pctx.Bag.Set(BagSpan, span)
	return plugin.OutcomeContinue
}

type onLogHook struct {
	logger  log.Logger
	handles *Handles
}

func (h onLogHook) Name() string        { return "obs.on_log" }
func (h onLogHook) Phase() plugin.Phase { return plugin.PhaseOnLog }
func (h onLogHook) Run(pctx *plugin.PhaseContext) plugin.PhaseOutcome {
	bag := pctx.Bag
	span, _ := bag.Get(BagSpan).(trace.Span)
	spanCtx, _ := bag.Get(BagSpanCtx).(context.Context)
	model, _ := bag.Get(BagModel).(storage.Model)
	provider, _ := bag.Get(BagProvider).(storage.Provider)
	lc, _ := bag.Get(BagLogCtx).(LogCtx) // LogCtx moved into observability (Step 1)
	usage, _ := bag.Get(BagUsage).(ir.Usage)
	started, _ := bag.Get(BagStarted).(time.Time)
	status, _ := bag.Get(BagStatus).(int)
	apiKeyID, _ := bag.Get(BagAPIKeyID).(string)

	statusClass := classify(status)
	latencyMs := time.Since(started).Milliseconds()
	h.handles.requests.Add(spanCtx, 1,
		attribute.String("model", model.Name),
		attribute.String("provider", provider.Name),
		attribute.String("apikey", apiKeyID),
		attribute.String("status_class", statusClass))
	h.handles.tokens.Add(spanCtx, int64(usage.PromptTokens),
		attribute.String("model", model.Name), attribute.String("apikey", apiKeyID), attribute.String("direction", "in"))
	h.handles.tokens.Add(spanCtx, int64(usage.CompletionTokens),
		attribute.String("model", model.Name), attribute.String("apikey", apiKeyID), attribute.String("direction", "out"))
	h.handles.latency.Record(spanCtx, float64(latencyMs),
		attribute.String("model", model.Name), attribute.String("provider", provider.Name))

	// Emit the structured audit log record (replaces appendLog). Attribute keys
	// match the receiver's logRecordFromOTLP contract (Task 2.1).
	var rec log.Record
	rec.SetTimestamp(time.Now())
	rec.SetAttributes(
		attribute.String("nyro.log.id", NewRequestID()),
		attribute.Int64("nyro.log.created_ms", started.UnixMilli()),
		attribute.String("nyro.client_protocol", lc.ClientProtocol),
		attribute.String("nyro.upstream_protocol", lc.UpstreamProtocol),
		attribute.String("nyro.provider_id", provider.ID), attribute.String("nyro.provider_name", provider.Name),
		attribute.String("nyro.model_id", model.ID), attribute.String("nyro.model_name", model.Name),
		attribute.String("nyro.client_model", lc.ClientModel), attribute.String("nyro.upstream_model", lc.UpstreamModel),
		attribute.String("nyro.method", lc.Method), attribute.String("nyro.path", lc.Path),
		attribute.String("nyro.api_key_id", apiKeyID), attribute.String("nyro.api_key_name", lc.APIKeyName),
		attribute.Int("nyro.client_status", status),
		attribute.Int64("nyro.latency_total_ms", latencyMs),
		attribute.Int64("nyro.input_tokens", int64(usage.PromptTokens)),
		attribute.Int64("nyro.output_tokens", int64(usage.CompletionTokens)),
		attribute.Int64("nyro.cache_read_tokens", cacheRead(usage)),
		attribute.Bool("nyro.is_stream", lc.IsStream),
	)
	h.logger.Emit(spanCtx, rec)

	if span != nil {
		if status >= 500 {
			span.SetStatus(codes.Error, "upstream error")
		}
		span.SetAttributes(attribute.Int("nyro.client_status", status))
		span.End()
	}
	return plugin.OutcomeContinue
}

func cacheRead(u ir.Usage) int64 {
	if u.CacheReadTokens != nil {
		return int64(*u.CacheReadTokens)
	}
	return 0
}

func classify(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	default:
		return "2xx"
	}
}

func RegisterHooks(tracer trace.Tracer, logger log.Logger, handles *Handles) {
	plugin.Register(onRequestHook{tracer: tracer})
	plugin.Register(onLogHook{logger: logger, handles: handles})
}
```

> (The `LogCtx`/`NewRequestID` move is Step 1 above; `hooks.go` references them directly. `storage.Model`/`Provider` and `ir.Usage` are referenced by value — `observability → storage` and `→ protocol/ir` are acyclic.)

- [ ] **Step 3: Test** — `hooks_test.go`: register hooks against a no-op tracer/logger + an in-memory `metric.ManualReader`; push a `ContextBag` with known model/provider/usage/status; call `RunPhaseHooks(PhaseOnRequest)` then `RunPhaseHooks(PhaseOnLog)`; assert the counters in the manual reader show 1 request and the expected tokens, and that the span was ended (if using a test span recorder).
- [ ] **Step 4: Run + commit.** `go test ./internal/observability/ -v` then `git commit -m "feat(go/obs): add metrics handles + OnRequest/OnLog phase hooks"`

---

### Task 3.3: Dispatcher — wire bag + OTel; replace `appendLog`

**Files:**
- Modify: `internal/proxy/dispatcher.go`
- Modify: `internal/proxy/logrec.go` (remove the now-superseded `logCtx`+`newRequestID`; delete `Storage.Logs().AppendBatch` call)
- Modify: `internal/proxy/gateway.go` (drop `Storage` field; add `Obs *observability.ObsProvider`, `Handles *observability.Handles`)

- [ ] **Step 1: Switch dispatcher to `observability.LogCtx`; remove proxy's old `logCtx`/`newRequestID`.** The observability `LogCtx`/`NewRequestID` were added in T3.2 Step 1. Here: change the dispatcher's `lc := logCtx{...}` literal to `observability.LogCtx{...}`, remove the old `proxy.logCtx` + `proxy.newRequestID` from `logrec.go` (now superseded), and delete the `Storage.Logs().AppendBatch` call. Keep `statusRecorder` in `proxy` (HTTP-mux machinery).
- [ ] **Step 2: `Gateway` struct + constructor** — remove `Storage storage.Storage` field; `NewGateway` no longer takes storage. Add `Obs *observability.ObsProvider` and `Handles *observability.Handles`. The two existing `NewGateway`/`NewGatewayWithCache` call sites move to `cmd/gateway` (T3.4).
- [ ] **Step 3: `Dispatch`** — allocate `bag := plugin.NewContextBag()` at entry; pass `Bag: bag` in all five `RunPhaseHooks` `PhaseContext`s. In the `defer` block, replace `g.appendLog(...)` with bag population only:

```go
defer func() {
    bag.Set(string(obs.BagStarted), started)
    bag.Set(string(obs.BagStatus), rec.status)
    bag.Set(string(obs.BagModel), model)
    bag.Set(string(obs.BagProvider), provider)
    bag.Set(string(obs.BagAPIKeyID), apiKeyID)
    bag.Set(string(obs.BagLogCtx), obs.LogCtx{ /* from lc */ })
    bag.Set(string(obs.BagUsage), usage)
    // appendLog's Storage.Logs().AppendBatch call is GONE.
    if apiKeyID != "" {
        g.Quota.Record(apiKeyID, 1, int64(usage.PromptTokens+usage.CompletionTokens))
    }
}()
```

The `OnLog` hook (fired by the `defer plugin.RunPhaseHooks(PhaseOnLog, ...)` already at dispatcher.go:42) does the actual emit. Confirm `PhaseOnLog` runs AFTER the deferred bag-set — reorder so the bag is populated before `RunPhaseHooks(PhaseOnLog)` runs (move the bag-set ahead of the `RunPhaseHooks(PhaseOnLog)` defer, or populate the bag inline during the phases). Simplest: populate the bag at each phase as the data becomes known (model at routing, provider/upstream at failover, usage/status in the defer), and keep `RunPhaseHooks(PhaseOnLog)` as the terminal defer that fires the hook with a fully-populated bag.

- [ ] **Step 4: Build + run tests; fix breakage** in `gateway_test.go` / `dispatcher_test.go` (constructor changed; appendLog no longer writes storage — any test asserting a `request_logs` row now asserts via a stub OTel exporter or via the admin receiver; most proxy tests don't assert logs). 
- [ ] **Step 5: Commit.** `git add -A && git commit -m "feat(go/proxy): dispatch emits telemetry via phase hooks; drop appendLog Storage write"`

---

### Task 3.4: `cmd/gateway` — construct ObsProvider; drop Storage

**Files:**
- Modify: `cmd/gateway/gateway.go`

- [ ] **Step 1: Edit `buildGateway`** — remove `OpenStorage` from all three branches. Standalone YAML no longer creates `memory.New()`. Each branch builds the gateway via the new storage-less constructor and constructs an `ObsProvider` from `observability.LoadConfig(...)`:

```go
// standalone defaults: logs→stdout, metrics/traces→none (override via settings/env).
// xDS defaults: all→otlp, endpoint=obs_otlp_endpoint.
obsCfg := observability.LoadConfig(settingsGet) // from YAML or a settings source appropriate to the mode
provider, err := observability.NewProvider(ctx, obsCfg)
if err != nil { return nil, nil, err }
gw.Obs = provider
gw.Handles = observability.NewHandles(provider.Meter)
observability.RegisterHooks(provider.Tracer, provider.Logger, gw.Handles)
defer shutdown wraps provider.Shutdown.
```

For standalone mode the "settings source" is the YAML config (extend `internal/config` with an `observability` block) or env vars (`OTEL_EXPORTER_OTLP_ENDPOINT` is honored by the OTel exporters automatically). Remove `bootstrap.StartRetentionLoop(ctx, gw.Storage)`.

- [ ] **Step 2: `/readyz`** (T3.5) — change `server.go:37` from `gw.Storage.Bootstrap().Health()` to cache-fill: `gw.Cache.Load() != nil`. Delete the `Storage` read.
- [ ] **Step 3: Build + test + commit.** `go build ./... && go vet ./... && go test ./...` then `git commit -m "feat(go/gateway): wire ObsProvider; drop Storage; /readyz→cache (completes xDS P3c)"`

---

### Task 3.5: `/readyz` reflects cache state

(Folded into T3.4 Step 2; call out separately only if review wants it isolated.)

- [ ] Test `ready_test.go`: empty cache → 503; populated cache → 200.

---

### Phase 3 exit

`go build ./... && go vet ./... && go test ./...` green; `CGO_ENABLED=0 go build ./...` green. The `nyro gateway` binary no longer opens a DB (grep the build: no `sqlite`/`gorm` open path reachable from `cmd/gateway`); the only `storage` references remaining are in `cmd/admin`, `cmd/tool`, and the `storage` package itself. tpm/tpd quota still enforced in-memory (existing test). `/readyz` reflects cache fill. xDS P3c is complete.

---

## Phase 4 — remove `request_logs`

### Task 4.1: Storage interface surgery + backend cleanup + schema + type migration

**Files:**
- Modify: `internal/storage/storage.go` (delete `LogStore`, `Logs()`)
- Modify: `internal/storage/auth_models.go` (delete `RequestLog`, `LogQuery`, `LogPage`, and the 5 `Stats*` types — now in `observability`)
- Delete: `internal/storage/memory/logs.go`, `internal/storage/memory/stats_extra.go`
- Modify: `internal/storage/sqlite/sqlite.go` (delete `logStore` type + its methods, lines ~766–828)
- Delete: `internal/storage/sqlite/stats_extra.go`
- Modify: `internal/storage/sqlite/schema.sql` (delete `request_logs` DDL at 96 + index at 132)
- Modify: `internal/storage/sqlite/sqlite.go` `Migrate()` (add one-shot `DROP TABLE IF EXISTS request_logs`)
- Modify: `internal/bootstrap/bootstrap.go` (delete `StartRetentionLoop`)
- Modify: `internal/admin/admin.go` + `internal/admin/obs_source.go` (drop old-table fallback; `LogSource`/`StatsSource` now return `observability.*` types; handlers updated)
- Modify: `cmd/admin/admin.go` (drop `StartRetentionLoop`; pass parquet sources directly)
- Modify: any test referencing `storage.RequestLog`/`storage.LogStore` (rewrite to `observability.LogRecord`/`observability.Logs`).

- [ ] **Step 1: Delete the storage types + interface** (`LogStore`, `Logs()`, `RequestLog`, `LogQuery`, `LogPage`, `StatsOverview/ModelStats/ProviderStats/ApiKeyStats/StatsHourly`). Fix compile errors by pointing usages at `observability.*`.
- [ ] **Step 2: Delete backend impls** (`memory/logs.go`, `memory/stats_extra.go`, `sqlite` `logStore` + `stats_extra.go`). Update `memory.New()` / `sqlite.New()` if their `Storage()` composition referenced the deleted stores.
- [ ] **Step 3: Schema** — remove `request_logs` DDL + index from `schema.sql`; in `Migrate()` add:
```go
db.Exec("DROP TABLE IF EXISTS request_logs")
```
- [ ] **Step 4: Bootstrap** — delete `StartRetentionLoop`; remove its call in `cmd/admin`.
- [ ] **Step 5: Admin** — drop the old-table fallback in `obs_source.go`; `LogSource`/`StatsSource` now return `observability.LogPage`/`observability.Stats*`; handlers marshal those directly (JSON tags identical → WebUI unaffected).
- [ ] **Step 6: Grep + build + test.**
```bash
! grep -rn "request_logs\|storage.LogStore\|storage.RequestLog" --include="*.go" . | grep -v "_test.go" | grep -v "DROP TABLE"
# expect: no hits (migration DROP comment exempted)
go build ./... && go vet ./... && go test ./... && CGO_ENABLED=0 go build ./...
```
- [ ] **Step 7: Commit.** `git add -A && git commit -m "refactor(go): remove request_logs table, LogStore, and table retention"`

---

### Phase 4 exit (and project exit)

`go build ./... && go vet ./... && go test ./...` green; `CGO_ENABLED=0 go build ./...` green. Fresh sqlite DB has no `request_logs` table (`sqlite3 <db> ".tables"`). End-to-end: `nyro admin` + `nyro gateway` (xDS mode) — chat request → `/api/v1/logs`, `/api/v1/stats/{overview,models,providers,api-keys,hourly}` all return correct data from parquet; tpm quota enforced (429 on overflow); `obs_traces_sink` default off, set to `otlp` → `data/obs/traces/*.parquet` grows.

---

## Verification (cumulative)

- Every task: `go build ./... && go vet ./... && go test ./...` green.
- From Phase 2: `CGO_ENABLED=0 go build ./...` green (pure-Go gate).
- Phase 2: manual e2e (dual-write: parquet preferred, old table fallback).
- Phase 3: `nyro gateway` binary opens no DB; `/readyz`=cache; quota in-memory; xDS config push unaffected.
- Phase 4: `request_logs` absent in fresh DB; grep clean; WebUI `/logs`+`/stats` unchanged.

## Risks (carry-over from spec §17, with mitigations)

1. **OTel logs SDK API churn** — mitigate: T3.1 isolates all OTel importer code in `provider.go`/`hooks.go`; if a stable API differs, only those two files change.
2. **Bag ordering** (T3.3) — the `OnLog` hook must see a fully-populated bag; populate the bag at each phase as data arrives, fire `PhaseOnLog` last.
3. **Import cycle** (observability ↔ proxy) — resolved by moving `logCtx`/`newRequestID` into `observability` (T3.3 Step 1).
4. **Dual-write window** (P2) — brief old-table/parquet divergence is acceptable (audit, not billing); removed in P4.
5. **parquet schema evolution** — struct-tag-defined; adding a column later is backward-compatible via parquet-go's schema merge (reader uses `ReadSince` per file).
6. **Backpressure (spec §9)** — gateway side: OTel `BatchProcessor`/`PeriodicReader` bound their own queues and drop on overflow (defaults sane; tune via `WithMaxQueueSize`/`WithBatchMaxSize` if needed) — the data plane is never blocked. admin side: `Sink.Write` appends to a mutex-guarded buffer flushed at `maxRows`; add a hard cap (e.g. `5*maxRows`) in `Write` that drops + logs the oldest buffered batch when exceeded, so a flooded receiver degrades gracefully and never blocks its ACK or OOMs.
