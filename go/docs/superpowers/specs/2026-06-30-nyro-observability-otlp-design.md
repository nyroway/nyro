# Nyro Go 可观测性重构设计：OTLP collector（三信号）

- 日期：2026-06-30
- 状态：Draft（待用户复审）
- 前置：xDS P1–P3b 已完成（commits `1dee5d5..17f1ae7`）。gateway 的 config 读已走 ConfigCache/xDS，quota 已内存化（`internal/proxy/quota`），OAuth 已进 ConfigCache + 本地 refresh。本设计建立在"gateway 即将无 DB"的基础上。
- 后续：本设计 P3 完成后，xDS **P3c**（gateway 切断残留 `Storage`、`/readyz`→cache）即收尾。

---

## 1. 背景与目标

当前数据面（gateway）仍持有 `storage.Storage`，其**唯一**写用途是 `appendLog`（`internal/proxy/logrec.go:83`）把每请求审计写进 SQL 表 `request_logs`；唯一其它残留是 `/readyz`（`internal/proxy/server.go:37`，读 `Bootstrap().Health()`）。控制面（admin）的 `/api/v1/logs`、`/stats/*` 均为对该表的 SQL 聚合。

**目标**：

1. gateway 变**无状态纯数据面**：用 OTel SDK 采集 logs/metrics/traces 三信号，按可配置 sink（`none`/`stdout`/`otlp`）发出；默认两进程部署下 OTLP/HTTP push 给 admin。**不再写 `request_logs`**，不再持有 `Storage`。
2. admin 变**自带可观测后端**：起 OTLP/HTTP receiver → parquet 落盘 → `/logs`、`/stats/*` 查询（Go 聚合）+ 现有 WebUI。生产大规模可关掉自带后端，让 gateway 直连外部 Loki/Prometheus/Tempo。
3. **移除 `request_logs`**：表、DDL、`LogStore` 接口、两个后端的 `logStore`、表级 retention 全删。
4. **纯 Go，无 CGO**：parquet 用 `parquet-go`，聚合在 Go 内存完成；不上 DuckDB。
5. tpm/tpd/rpm/rpd 配额**留 gateway 内存**（P3a 已完成，本次不动，不上报）。

**非目标（YAGNI，留 seam 不实现）**：`file`/`loki`/`prometheus`(`/metrics` pull)/`datadog` 等额外 sink；`/metrics` Prometheus 端点（OTel→Prom bridge）；traces 查询 UI；DuckDB/SQL 查询层。

---

## 2. 现状（file:line 证据）

- `request_logs` 唯一生产者：`proxy/logrec.go:54 appendLog`，在 `dispatcher.go:43` 的 `defer` 内调用；填充 31 列中的 21 列（headers/bodies 10 列始终空）。
- `/readyz`：`proxy/server.go:37`，`gw.Storage.Bootstrap().Health()`。
- admin 读侧：`internal/admin/admin.go` —— `/logs`(213)、`/logs/{id}`(224)、`DELETE /logs`(236) 调 `s.Logs().Query/FindByID/ClearAll`；`/stats/overview|models|providers|api-keys|hourly`(255–302) 调 `s.Logs().Stats*`。
- SQL 聚合实现：`internal/storage/sqlite/stats_extra.go`（GORM GROUP BY）+ `sqlite/sqlite.go:770 logStore`；memory 实现：`internal/storage/memory/logs.go` + `memory/stats_extra.go`。
- `request_logs` DDL：`internal/storage/sqlite/schema.sql:96`（表）+ `:132`（索引 `idx_request_logs_created_at`）。
- retention：`internal/bootstrap/bootstrap.go:74 StartRetentionLoop` → `st.Logs().DeleteBefore`，两进程均启动（`cmd/gateway/gateway.go:66`、`cmd/admin/admin.go:53`）。
- phase hook 框架：`internal/plugin/phase.go`，5 phase（`OnRequest/OnAccess/OnUpstream/OnResponse/OnLog`）+ `PhaseContext{Ctx, Bag, ...}` + `ContextBag{Set/Get}` + 全局 `Register`/`RunPhaseHooks`。**当前零注册**（框架是 no-op）。dispatcher 在 `dispatcher.go:42/53/72/137/231(+266)` 调 `RunPhaseHooks`，但未传 `Bag`。
- `logCtx`（`logrec.go:38`）已采集 apikey_name/protocols/models/method/path/upstream_status/latency —— 是三信号的共享原料。
- greenfield：`go.mod` 无任何 parquet/otel 依赖；全树无 CGO（sqlite 用 pure-Go `glebarez/sqlite`）。

---

## 3. 架构总览

```
nyro gateway（数据面，无 DB）                     nyro admin（控制面 + obs 后端，DB 唯一读者）
┌────────────────────────────────────┐          ┌───────────────────────────────────────────┐
│ OTel SDK（每请求采集三信号）         │          │ OTLP/HTTP receiver（chi 加 3 个 POST）       │
│   logs   = 请求审计（替 appendLog）  │          │   POST /v1/logs  /v1/metrics  /v1/traces    │
│   metrics= counter/histogram(维度)   │──OTLP/──▶│          ↓ decode（官方 proto 包）           │
│   traces = phase hook → span        │  HTTP ~5s │ parquet sink（每批新文件·原子 rename·zstd）  │
│ Sink 配置：none/stdout/otlp         │  batch   │   <data_dir>/{logs,metrics,traces}/         │
│ tpm/tpd 内存滑窗（P3a，不上报）      │          │          ↓                                  │
└────────────────────────────────────┘          │ 查询：/logs ← logs parquet                   │
                                                │       /stats/* ← metrics parquet（Go 聚合）   │
                                                │ janitor：按 retention 删旧 parquet           │
                                                │ + 现有 /api/v1 管理 + WebUI                  │
                                                └───────────────────────────────────────────┘
```

**分层原则**：数据面只采集 + emit，**永不落盘**；parquet sink 是 admin 专属。gateway 即使在 standalone 模式也不写 parquet（见 §5）。

---

## 4. 三信号数据流

| 信号 | gateway 采集点 | parquet 行 schema（admin 落盘） | 查询方 |
|---|---|---|---|
| **logs** | `OnLog` phase 发一条结构化 LogRecord（替 `appendLog` 的 21 列） | `LogRecord`：21 列（id/created_at/api_key_id/api_key_name/client_protocol/upstream_protocol/provider_id/provider_name/model_id/model_name/client_model/upstream_model/method/path/client_status_code/upstream_status_code/latency_total_ms/latency_upstream_ms/input_tokens/output_tokens/is_stream，外加 cache_read_tokens/stream_chunks_count/stream_first_chunk_ms 若有） | `/logs`、`/logs/{id}`、`DELETE /logs`：parquet-go 读 → Go 过滤+排序+分页 |
| **metrics** | dispatcher 每请求给 counter/histogram 打标签增量 `{model,provider,apikey,status_class}`；batch export 每 ~5s | `MetricSample`：`{ts_ns, name, labels_json, kind, value, hist_sum, hist_count}`（每次 export = 所有 series 一个快照） | `/stats/{overview,models,providers,api-keys,hourly}`：读 metrics parquet → Go 聚合 |
| **traces** | OTel TracerProvider；`OnRequest` 起 span / `OnLog` 结 span（经 `ContextBag` 传递）；首次激活 phase 框架 | `SpanSnapshot`：`{trace_id, span_id, parent_span_id, name, start_ns, end_ns, duration_ns, status_code, attrs_json}` | 暂无查询端点（落盘备查；未来加 `/traces/{id}`） |

**约定**：

- logs 的 **JSON tag 与旧 `RequestLog` 完全一致** → WebUI 无感。只落当前已采集的 21 列（与今天一致；headers/bodies 仍不抓，未来再扩）。
- metrics 的 `status_class`（`2xx`/`4xx`/`5xx`）折叠状态码，避免 label 基数爆炸。
- attrs/events 存 JSON blob 列（pure-Go `encoding/json`），避免宽 schema。

**OTel SDK 选型**：logs/metrics/traces 均用官方 stable SDK（`go.opentelemetry.io/otel/{log,metric,sdk/trace}`），统一 batch processor + 统一 OTLP HTTP exporter。**不用** Prometheus client_golang（其 pull 模型与 OTLP push 统一三信号相矛盾）。

---

## 5. Sink 模型（数据面 emit，不落盘）

gateway 永远只 emit；sink 配置决定信号去哪。sink 接口（`internal/observability`）：

```go
type LogSink interface { Emit(ctx, []LogRecord) error; Flush() error; Shutdown(ctx) error }
// MetricSink / SpanSink 同形
```

本次实现三个 sink：

| sink | 实现 | 用途 |
|---|---|---|
| `none` | noop exporter | 丢弃 |
| `stdout` | OTel stdout exporter（结构化 JSON 行） | 容器原生：`kubectl logs`/Fluent Bit/Filebeat 接走 |
| `otlp` | 官方 `otlp{log,metric,trace}http` exporter → `obs_otlp_endpoint` | push 给 admin receiver 或外部 collector |

**配置项**（settings 表，沿用 `proxy_*` 风格）：

- `obs_sink`：全局默认（`none`/`stdout`/`otlp`）
- `obs_logs_sink` / `obs_metrics_sink` / `obs_traces_sink`：per-signal 覆盖（空 = 用全局）
- `obs_otlp_endpoint`：OTLP/HTTP URL（xDS 模式指向 admin；standalone 想外发时指向外部）
- `obs_export_interval`：batch 间隔（默认 `5s`）

**部署默认值**：

| 部署 | logs | metrics | traces | 理由 |
|---|---|---|---|---|
| xDS 两进程（nyro 默认） | `otlp`→admin | `otlp`→admin | `otlp`→admin | admin 就是 obs 后端 |
| standalone YAML（`--config`） | `stdout` | `none` | `none` | 容器原生 access log；metrics/traces 按需开 `otlp` |

匹配 Envoy/Nginx/Traefik 的容器默认：access log→stdout，metrics/traces 默认关、按需配。

> sink 接口是未来加 `file`/`prometheus`(`/metrics` pull)/`loki` 的 seam；本次只实现上述三个。

---

## 6. OTLP/HTTP 传输

**gateway 导出端**：官方 `otlploghttp` / `otlpmetrichttp` / `otlptracehttp` exporter（纯 Go、成熟），指向 `obs_otlp_endpoint`（如 `http://admin:19531`）。BatchSpanProcessor 风格，按 `obs_export_interval` 批量发出。

**admin 接收端**：**手写** OTLP/HTTP receiver，挂在 admin 现有 chi 路由上（不起新端口、不嵌官方 collector 框架）：

- 3 个路由：`POST /v1/logs`、`POST /v1/metrics`、`POST /v1/traces`（content-type `application/x-protobuf`，亦接受 JSON）。注意是顶层 `/v1/*`（OTLP 标准），**不是** `/api/v1/*`。
- 用官方预生成包 `go.opentelemetry.io/proto/opentelemetry/...` decode `ExportLogsServiceRequest` / `ExportMetricsServiceRequest` / `ExportTraceServiceRequest`。
- decode → 转内部行类型 → 入对应 parquet sink 的内存 buf。
- **接收即 ACK**（`Export{Signal}ServiceResponse{}`），落盘异步（有界队列；满则丢弃，见 §9）。

**鉴权边界**：OTLP `/v1/*` 路由挂在 admin chi 路由的**顶层**，**不受** `admin-token` bearer 保护（gateway 不是人类用户、不走 admin token）。生产加固留作未来（共享 secret header 校验 / mTLS）；当前默认假定 admin 端口仅在可信网络可达（与 admin-token 保护 `/api/v1` 并行，互不影响）。

> **不需要改 `buf.gen.yaml`**：OTLP proto 用官方预生成包；codegen 只管我们自己的 xDS proto。

---

## 7. 包结构

```
internal/observability/
  doc.go                  — 包文档；三信号模型与分层原则
  config.go               — ObsConfig（从 settings 读；sink/endpoint/dir/retention/export_interval）
  signals.go              — 共享值类型：LogRecord、MetricSample、SpanSnapshot、MetricLabels
  sink.go                 — LogSink/MetricSink/SpanSink 接口 + noop 实现
  stdout.go               — stdout sink（OTel stdout exporter 封装）
  otlp_export.go          — otlp sink（官方 otlp{log,metric,trace}http exporter 封装）；gateway 用
  provider.go             — ObsProvider：按配置装配三信号的 SDK（LoggerProvider/MeterProvider/TracerProvider）+ 选 sink + 生命周期（Flush/Shutdown）
  hooks.go                — PhaseHook 实现：OnRequest 起 span（存 Bag）/ OnLog 结 span + 发 log + 记 metrics；属性来自 logCtx
  metrics_handles.go      — 命名 counter/histogram 句柄（requests_total/tokens_in/out/cache_read/latency_ms/upstream_latency_ms/in_flight）
  parquet/
    sink.go               — 通用轮转 parquet sink：Write(rows)、按小时/行数轮转、zstd、原子 rename；admin 用
    reader.go             — 按时间段 glob 已完成文件 → parquet-go 解码 → []Row（Go 聚合输入）
    logs_sink.go          — LogRecord schema + typed Write
    traces_sink.go        — SpanSnapshot schema
    metricseries_sink.go  — MetricSample schema（时序）
  receiver.go             — OTLP/HTTP receiver（3 个 POST handler + decode）；admin 用
  logs_query.go           — Logs 读侧：Query/FindByID/ClearAll（parquet-go + Go 过滤/排序/分页）
  stats_aggregate.go      — 纯 Go 聚合：[]MetricSample → StatsOverview/ByModel/ByProvider/ByApiKey/Hourly
  janitor.go              — 后台循环：按 retention 删旧 parquet（三信号共用）
  *_test.go
```

**组织要点**：

1. `parquet/` 子包**只在 admin 进程被实例化**；gateway 永不导入 parquet 写路径（分层）。
2. sink 接口让 stdout/otlp 是两个实现，未来 file/prom 是第三、四个实现。
3. receiver 与 parquet sink 是 admin 侧组件；provider + hooks + otlp_export 是 gateway 侧组件。同属一包，按进程装配区分。
4. `quota` 已存在于 `internal/proxy/quota`（P3a），不迁移、不改动。

---

## 8. parquet 写模型与 schema

**写规则（避免追加危害）**：parquet 文件 Close 后不可追加（footer 冲突会破坏文件）。故：

- `Sink[Row]` 持内存 `buf []Row`；`Write` 追加到 buf。
- 轮转触发：① 小时边界跨越；② `len(buf) >= maxRows`（默认 50000，限内存）。
- 轮转时：`Flush()` 串行（sink mutex）把 buf 写成**新文件** `<dir>/<signal>/<YYYYMMDDHH>-<seq>.parquet.tmp`（zstd），`os.Rename` 成 `.parquet`（原子，杜绝半写），重置 buf。
- `Shutdown`：flush 剩余 buf。
- reader 只开**已完成**文件（无 `.tmp` 后缀；进行中的写独占 `.tmp` 句柄）。并发读写不同文件安全。

**压缩**：`parquet-go` 的 zstd（经 `github.com/klauspost/compress`，pure-Go）。从 P2 起 `CGO_ENABLED=0 go build ./...` 作为 CI 门。

**schema** 由 Row 结构体的 `parquet:` tag 定义（见 §4 行类型）。`labels_json`/`attrs_json` 用字符串列存 JSON blob。

---

## 9. 失败 / 背压语义

- **gateway OTLP export**：有界 batch 队列（默认上限如 2048 条/信号）；满则**丢弃最旧**（best-effort，等同今天 `_ = AppendBatch` 的 fire-and-forget）。**绝不阻塞转发热路径**。export 失败（admin 不可达）记 stderr 日志后继续。
- **admin receiver → parquet**：接收即 ACK；落盘走异步有界队列；队列满则丢弃该批 + 记日志，不阻塞 receiver、不反压 gateway。
- **parquet 写失败**（磁盘满/IO 错）：记日志 + 丢弃该批；不阻塞后续。
- **配置缺失容错**：`obs_otlp_endpoint` 空 + sink 选了 `otlp` → 启动期校验报错（fail-fast），不允许静默丢数据。

---

## 10. gateway 侧改造

1. **OTel 装配**（`provider.go`）：`ObsProvider` 按 `ObsConfig` 构造 `LoggerProvider`/`MeterProvider`/`TracerProvider`，每个信号选 `none`/`stdout`/`otlp` sink；注入 `BatchLogRecordProcessor`/`BatchSpanProcessor`/周期 metric reader（`obs_export_interval`）。`cmd/gateway` 启动时构造、`defer Shutdown`。
2. **phase hook 激活**（`hooks.go`）：注册两个 hook（`plugin.Register`）：
   - `OnRequest`：`tracer.Start(ctx, "dispatch")` → span 存入 `ContextBag`。
   - `OnLog`：从 Bag 取 span，按 logCtx 设属性/状态 → `span.End()`；发一条结构化 `LogRecord`（21 列，由原 `appendLog` 的组行逻辑迁来）；记 metrics（counter/histogram 增量）。
   - dispatcher 需在 `Dispatch` 入口分配 `bag := plugin.NewContextBag()`，并在 5 处 `RunPhaseHooks` 的 `PhaseContext` 传 `Bag: bag`（小改动）。
3. **替换 `appendLog`**（`logrec.go`）：原 `appendLog` 的**组行逻辑**（从 `logCtx`+`usage` 填 21 列）迁入 OnLog hook；`Dispatch` 的 `defer` 不再调 `g.Storage.Logs().AppendBatch`（改由 hook 经 OTel logger 发出）。`logrec.go` 删除对 `Storage.Logs()` 的调用，`statusRecorder`/`logCtx` 保留。
4. **metrics 句柄**（`metrics_handles.go`）：dispatcher 在路由后/响应后给 `requests_total`、`tokens_in/out/cache_read`、`latency_ms`、`upstream_latency_ms`、`in_flight`（gauge，入 `Inc`/defer `Dec`）打标签增量；label 来自 `logCtx` + model/provider。
5. **切断 `Storage`（= xDS P3c）**：
   - `Gateway` 移除 `Storage` 字段；`NewGateway` 不再收 `storage.Storage`。
   - `cmd/gateway` 移除 `OpenStorage`/`storage` 依赖与 `StartRetentionLoop`（retention 归 admin janitor）。
   - `/readyz`（`server.go:37`）从 `Bootstrap().Health()` 改为"cache 已填充"状态（`ConfigCache.Load() != nil`）。
   - standalone YAML 模式不再 `memory.New()`（无需存储）；直接 sink 配置决定 emit 去向。

---

## 11. admin 侧改造

1. **OTLP receiver**（`receiver.go`）：在 admin 的 chi 路由**顶层**（`/v1/{logs,metrics,traces}`，**不在** `admin.Mount` 的 `/api/v1` bearer 保护组内）加 3 个 POST；decode 官方 proto → 内部行 → 入 parquet sink buf。接收即 ACK，异步落盘。
2. **parquet sink 实例化**：admin 启动时按 `obs_data_dir` 构造三信号的 `parquet.Sink[Row]`（logs/metrics/traces 各一）。
3. **`/logs` 改源**（`admin.go:213–243`）：从 `s.Logs().Query/FindByID/ClearAll` 改为注入的 `*observability.Logs`（parquet-go 读 + Go 聚合）。`admin.Mount` 增 `LogSource` 依赖（接口，避免 admin 直接依赖 observability 具体类型）。
4. **`/stats/*` 改源**（`admin.go:255–302`）：从 `s.Logs().Stats*` 改为 `observability.AggregateStats(metricsParquetDir, hours)`（读 metrics parquet 时序 + Go 聚合）。`admin.Mount` 增 `StatsSource` 依赖。**stats 不再扫任何明细表**。返回 JSON shape 与今天一致（WebUI 无感）。
5. **janitor**（`janitor.go`）：`StartJanitor(ctx, dataDir, retentionBySignal, period)` 替代旧 `StartRetentionLoop`；按 `obs_{logs,metrics,traces}_retention_days` 删旧 parquet。`cmd/admin` 启动它。
6. **双写期兼容**（P2，见 §14）：迁移期间 gateway 同时写旧 `request_logs` **和** OTLP；admin 优先读 parquet、旧表兜底，保证行为无回退。

---

## 12. request_logs 移除 + storage 接口手术（P4）

- `internal/storage/storage.go`：**删** `LogStore` 接口与 `Storage.Logs()`。
- 类型迁移：`StatsOverview`/`ModelStats`/`ProviderStats`/`ApiKeyStats`/`StatsHourly` 从 `storage/auth_models.go` **迁到** `observability`（admin handler 的 JSON 输出 shape 不变）。`RequestLog`/`LogQuery`/`LogPage` 迁到 `observability`（改名 `LogRecord` 等，JSON tag 保持）。
- `internal/storage/memory`：删 `logs.go` + `stats_extra.go` 的 `logStore` 实现。
- `internal/storage/sqlite`：删 `sqlite.go` 的 `logStore`（`:766–828`）+ `stats_extra.go`；`schema.sql` 删 `request_logs` DDL(`:96`) + 索引(`:132`)；`Migrate` 加一次性 `DROP TABLE IF EXISTS request_logs`（幂等；cutover 接受旧明细数据不迁移——用户明确"移除表"）。
- `internal/bootstrap/bootstrap.go`：**删** `StartRetentionLoop`（admin 改用 `observability.StartJanitor`）。
- `AuthAccessStore` 的 `RequestCountSince/TokenCountSince` 已在 P3a 删除，无需再动。

---

## 13. 配置（settings 表）

| key | 默认 | 说明 |
|---|---|---|
| `obs_sink` | xDS 模式 `otlp`；standalone `none` | 全局 sink 默认 |
| `obs_logs_sink` / `obs_metrics_sink` / `obs_traces_sink` | 空 | per-signal 覆盖 |
| `obs_otlp_endpoint` | 空 | OTLP/HTTP URL（admin 地址或外部） |
| `obs_export_interval` | `5s` | batch 导出间隔 |
| `obs_data_dir`（admin） | `./data/obs` | parquet 根目录 |
| `obs_logs_retention_days` | `7` | logs parquet 保留 |
| `obs_metrics_retention_days` | `30` | metrics parquet 保留 |
| `obs_traces_retention_days` | `3` | traces parquet 保留 |

启动时读一次；dir/retention 改动需重启（与现有 loop 行为一致；现有 `bumpEpoch` 不热加载 loop）。sink/endpoint 亦启动期校验。

---

## 14. 分阶段（同一交付，内部 4 步；每步独立可合并、全量绿、无行为回退）

> 顺序设计为**全程无回退**：先建库、再 admin 双写、再 gateway 切、最后拆旧表。

- **P1 — 包骨架 + parquet sink + janitor**（`internal/observability`）：纯库 + 单测；`Sink[Row]` 写/轮转/读 round-trip、`AggregateStats` 喂样本断言、janitor 删旧文件。不接业务，不改动现有行为。
- **P2 — admin OTLP receiver + parquet 落盘 + `/logs`、`/stats` 改读 parquet**：双写期（gateway 仍写旧 `request_logs`，同时开始 OTLP 导出）。admin 的 `/logs`、`/stats/*` 优先读 parquet，旧表兜底（parquet 空时回退 `Storage.Logs()`）。receiver 端到端单测。`go build && go test` 绿。
- **P3 — gateway 切 OTLP + 切断 `Storage`（= xDS P3c）**：激活 phase hook（span+metrics+log）、删 `appendLog` 写库、`NewGateway` 去 storage、`/readyz`→cache、`cmd/gateway` 去 `OpenStorage`/`StartRetentionLoop`。此步完成后 gateway 二进制不连 DB、不写 `request_logs`。**P3c 在此收尾**。
- **P4 — 移除 `request_logs`**：删表/DDL/`LogStore`/两后端 `logStore`+`stats_extra`/`StartRetentionLoop`；类型迁 `observability`；admin 去掉旧表兜底。grep `request_logs` 只剩迁移注释。

每步退出条件：`go build ./... && go vet ./... && go test ./...` 绿；P2 起 `CGO_ENABLED=0 go build ./...` 绿。

---

## 15. 测试策略

- **P1 单测**：`parquet/sink_test.go`（写→轮转→读回、跨小时、原子 rename）；`stats_aggregate_test.go`（喂已知 `MetricSample` 断言 5 种 stats）；`logs_query_test.go`（Query/FindByID/ClearAll）；`janitor_test.go`（按 retention 删旧文件、保留新文件）。
- **P2 单测 + 集成**：receiver decode（构造 OTLP protobuf 请求 → 断言落 parquet）；端到端（gateway exporter → admin receiver → parquet → `/logs`、`/stats` 返回正确）。
- **P3**：`hooks_test.go`（注册 hook，跑 `RunPhaseHooks` 带 Bag，断言 span/log/metrics 发出到 in-memory 测试 sink）；配额 e2e 已有（P3a）；`/readyz` 反映 cache 状态。
- **P4**：grep `request_logs`/`LogStore`/`storage.RequestLog` → 仅迁移注释；fresh sqlite DB 无 `request_logs` 表。
- **回归**：`nyro gateway`（xDS + standalone 两模式）+ `nyro admin` 端到端 smoke 不回归（`/v1/chat/completions` → `/logs`、`/stats/overview` 正确）。

---

## 16. 关键文件

- `internal/observability/**`（新增，见 §7）
- `internal/proxy/dispatcher.go` — Dispatch 传 `Bag`；defer 改 OTel log；metrics 增量
- `internal/proxy/logrec.go` — 删 `Storage.Logs().AppendBatch`（改 OTel logger）
- `internal/proxy/gateway.go` — `NewGateway` 去 `storage.Storage`；去 `Storage` 字段
- `internal/proxy/server.go:37` — `/readyz` → cache 填充
- `cmd/gateway/gateway.go` — 去 `OpenStorage`/`StartRetentionLoop`；装 `ObsProvider`；sink 配置
- `cmd/admin/admin.go` — 挂 OTLP receiver；装 parquet sink + janitor
- `internal/admin/admin.go` — `/logs`、`/stats` 改注入的 `LogSource`/`StatsSource`
- `internal/storage/storage.go` — 删 `LogStore`/`Logs()`
- `internal/storage/{memory,sqlite}/*.go` — 删 `logStore`/`stats_extra`
- `internal/storage/sqlite/schema.sql` — 删 `request_logs` DDL + 索引
- `internal/bootstrap/bootstrap.go` — 删 `StartRetentionLoop`
- `go.mod` — 加 `parquet-go`、`go.opentelemetry.io/otel{,/sdk,/metric,/log}`、`otlp{log,metric,trace}http`、`go.opentelemetry.io/proto`

---

## 17. 风险与取舍

1. **stats 近实时**：两进程下 admin 读的是 gateway ~5s 前 export 的 metrics parquet 快照，非严格实时。固有取舍（用户已确认接受）。
2. **OTel logs SDK 成熟度**：Go logs SDK 已 stable（2025），但仍较新；若遇阻可退化为"手写 OTLP log 直接发"绕开 SDK。低概率。
3. **parquet-go 依赖体积**：pure-Go，引 `klauspost/compress`；P2 起以 `CGO_ENABLED=0` 门控确保无 CGO。
4. **standalone 无查询 UI**：standalone 只 emit（stdout/外部 otlp），无 admin 即无 `/logs`、`/stats` UI——与今天 standalone 行为一致（其 `memory` 存储本就无人读）。
5. **双写期短暂不一致**：P2 期间旧表与 parquet 并存，admin 优先 parquet；极小窗口内两者可能略有差异，可接受（审计非计费）。
6. **trace 体积**：每请求一 span，traces parquet 增长最快 → 默认 retention 3 天 + janitor；可配 `obs_traces_sink=none` 关闭。
7. **类型迁移跨包**：`Stats*`/`LogRecord` 从 `storage` 迁 `observability` 会触及 admin handler 与测试的 import；P4 集中处理。
