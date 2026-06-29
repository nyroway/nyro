# nyro CLI 重构设计：cobra + gateway/admin/tool 分离

日期：2026-06-29
分支：feat/go-gateway

## 背景

当前 `go/cmd/nyro-server/main.go` 是单进程入口，装配了 proxy 路由、admin API、WebUI、OAuth session、driver 注册、OAuth 后台刷新循环、日志保留循环、storage 选择。所有职责混在一个 `main()` 里，无法独立部署数据面或控制面。

Rust 网关运行两个独立监听器（proxy `:19530`、admin `:19531`）并支持 `--mode proxy|admin|both`。本次重构把 Go 入口拆成 cobra CLI，数据面/控制面分离，并新增 standalone 配置文件模式。

## 目标

1. 根 `nyro.go`（`package main`）作为唯一二进制入口，用 cobra 组织命令。
2. 三个子命令：`nyro gateway`（数据面）、`nyro admin`（控制面）、`nyro tool`（原 nyro-tools）。
3. gateway 与 admin 是两个独立子命令/进程，各自监听端口，**共享同一 storage/DB**。
4. gateway 支持 **standalone 模式**：`--config <yaml>` 从配置文件 seed memory storage，无需 admin/DB。
5. 删除 `cmd/nyro-server` 与 `cmd/nyro-tools`；共享装配提取到 `internal/bootstrap`。

## 非目标

- `nyro tool` 的具体功能（parity harness record/replay + diff）不在本次范围——本次只把它改造成 cobra 子命令并保留 placeholder 行为。
- gateway/admin 之间的进程间协调、配置热重载不在本次范围。
- standalone 模式不支持 OAuth 上游（需 admin session 获取 token）。

## 命令结构

```
nyro              # root，显示 help
nyro gateway      # 数据面，默认 :19530
nyro admin        # 控制面，默认 :19531
nyro tool         # 原 nyro-tools（placeholder）
```

## 文件布局

```
go/
  nyro.go                     # package main: main() + rootCmd（注册三个子命令）
  cmd/
    gateway/gateway.go        # package gateway: NewCmd() *cobra.Command
    admin/admin.go            # package admin:   NewCmd() *cobra.Command
    tool/tool.go              # package tool:    NewCmd() *cobra.Command
  internal/
    bootstrap/
      bootstrap.go            # OpenStorage / RegisterDrivers / NewSessionStore / StartRetentionLoop
      bootstrap_test.go       # (从 cmd/nyro-server/main_test.go 搬迁)
    config/
      config.go               # Config + LoadYAML + ApplyTo
      config_test.go
    ... (proxy/admin/auth/storage/vendor/... 现有包不动)
```

删除：`cmd/nyro-server/`、`cmd/nyro-tools/`。

## 参数

**root 持久 flag**（定义在 rootCmd.PersistentFlags，子命令继承）：
- `--storage string`（memory|sqlite|postgres|mysql，默认 memory）
- `--db-dsn string`

**gateway 本地 flag**：
- `--addr string`（默认 `127.0.0.1:19530`）
- `--config string`（可选；指定即进入 standalone 模式）

**admin 本地 flag**：
- `--addr string`（默认 `127.0.0.1:19531`）
- `--admin-token string`
- `--webui-dir string`

## 组件与接口

### `internal/bootstrap`（共享装配，从现 main.go 提取）

```go
// OpenStorage 打开指定后端并对 SQL 后端执行 Migrate。
func OpenStorage(backend, dsn string) (storage.Storage, error)

// RegisterDrivers 注册 claude-code/codex/vertexai 三个 OAuth driver。
func RegisterDrivers(reg *auth.Registry)

// StartRetentionLoop 启动日志保留清理（默认 7 天，读 log_retention_days）。
func StartRetentionLoop(ctx context.Context, st storage.Storage)
```

（`NewSessionStore` 直接用 `auth.NewSessionStore()`，无需包装；`StartOAuthRefreshLoop` 已在 `proxy.Gateway` 上，gateway 直接调。）

### `internal/config`（standalone 配置文件）

```go
type ProviderSpec struct {
    Name     string `yaml:"name"`
    Vendor   string `yaml:"vendor,omitempty"`
    Protocol string `yaml:"protocol"`
    BaseURL  string `yaml:"base_url"`
    APIKey   string `yaml:"api_key"`
}
type ModelTargetSpec struct {
    Provider string `yaml:"provider"`
    Model    string `yaml:"model"`
}
type ModelSpec struct {
    Name       string           `yaml:"name"`
    EnableAuth bool             `yaml:"enable_auth,omitempty"`
    Targets    []ModelTargetSpec `yaml:"targets"`
}
type APIKeySpec struct {
    Name    string   `yaml:"name"`
    Key     string   `yaml:"key"`
    Models  []string `yaml:"models"`
}
type Config struct {
    Providers []ProviderSpec `yaml:"providers"`
    Models    []ModelSpec    `yaml:"models"`
    APIKeys   []APIKeySpec   `yaml:"api_keys"`
}

// LoadYAML 读取并解析 yaml 配置文件。
func LoadYAML(path string) (*Config, error)

// ApplyTo 把配置 seed 进 storage（复用 Providers/Models/APIKeys 的 Create 接口）。
func (c *Config) ApplyTo(st storage.Storage) error
```

`ApplyTo` 顺序：providers → models（target 用 provider Name 解析为 ID）→ api_keys（ModelIDs 用 model Name 解析为 ID）。因为 Create 返回的 ID 是生成的不透明串，Name→ID 映射需要在 ApplyTo 内部维护。

### `cmd/gateway`

```go
func NewCmd() *cobra.Command
```
RunE 逻辑：
1. 读 flags。
2. 若 `--config != ""`：`st := memory.New()`；`cfg, err := config.LoadYAML(*config)`；`cfg.ApplyTo(st)`。（standalone，忽略 --storage/--db-dsn。）
3. 否则：`st, err := bootstrap.OpenStorage(*storage, *dbDSN)`。
4. `reg := auth.NewRegistry(); bootstrap.RegisterDrivers(reg)`。
5. `gw := proxy.NewGateway(st); gw.SetDriverRegistry(reg); gw.StartOAuthRefreshLoop(ctx)`。
6. `bootstrap.StartRetentionLoop(ctx, st)`。
7. `engine := proxy.NewRouter(gw); srv.ListenAndServe(*addr)` + 优雅关闭（沿用现 main.go 的 signal 处理）。

### `cmd/admin`

```go
func NewCmd() *cobra.Command
```
RunE 逻辑：
1. `st, err := bootstrap.OpenStorage(*storage, *dbDSN)`。
2. `reg := auth.NewRegistry(); bootstrap.RegisterDrivers(reg); sessions := auth.NewSessionStore()`。
3. `bootstrap.StartRetentionLoop(ctx, st)`（admin 也做保留清理，避免只跑 admin 时日志无限增长）。
4. `engine := gin.New(); admin.Mount(engine, st, *adminToken); admin.MountOAuth(engine, st, reg, sessions); proxy.MountWebui(engine, *webuiDir)`。
5. ListenAndServe + 优雅关闭。

### `cmd/tool`

```go
func NewCmd() *cobra.Command
```
保留原 nyro-tools 的最小行为：`version` 子命令 + 默认 help。parity harness 实现留后续。

### `nyro.go`（package main）

```go
func main() {
    root := &cobra.Command{
        Use: "nyro",
        Short: "Nyro gateway",
    }
    root.PersistentFlags().String("storage", "memory", "...")
    root.PersistentFlags().String("db-dsn", "", "...")
    root.AddCommand(gateway.NewCmd(), admin.NewCmd(), tool.NewCmd())
    if err := root.Execute(); err != nil { os.Exit(1) }
}
```

## standalone 配置文件格式（YAML）

```yaml
providers:
  - name: openai
    protocol: openai-compatible
    base_url: https://api.openai.com
    api_key: sk-***
models:
  - name: gpt-4o
    targets:
      - {provider: openai, model: gpt-4o}
api_keys:
  - name: local
    key: nyro-secret
    models: [gpt-4o]
```

- 仅支持 apikey 模式上游（`api_key` 字段）。
- OAuth 上游不支持 standalone。

## 测试策略

- `internal/bootstrap/bootstrap_test.go`：`TestRegisterDrivers`、`TestNewStorage`（从 cmd/nyro-server/main_test.go 搬迁，函数名改为 `OpenStorage`）。
- `internal/config/config_test.go`：`TestLoadYAML`（解析）、`TestApplyTo`（seed 后 storage 含正确 providers/models/apikeys + 绑定）。
- `cmd/gateway`、`cmd/admin`：`NewCmd()` 的 flag 绑定与默认值（轻量，不启动真实监听）。
- 全量 `go test ./...` 必须通过。

## 依赖

- `github.com/spf13/cobra`（新增）
- `gopkg.in/yaml.v3`（新增）

## 迁移与收尾

- 删除 `cmd/nyro-server/`（main.go + main_test.go 搬迁后）。
- 删除 `cmd/nyro-tools/`（被 `cmd/tool` 取代）。
- `go.mod` 新增 cobra、yaml.v3 依赖（`go mod tidy`）。
- README/cutover 文档如提及 `cmd/nyro-server` 需更新（cutover.md 第 0 步的 build 命令）。

## 实现顺序建议

1. 加依赖（cobra、yaml.v3）。
2. `internal/bootstrap`（提取 + 搬测试）。
3. `internal/config`（loader + ApplyTo + 测试）。
4. `cmd/gateway`、`cmd/admin`、`cmd/tool`。
5. `nyro.go` root。
6. 删 `cmd/nyro-server`、`cmd/nyro-tools`；`go mod tidy`。
7. 全量 `go test ./...` + `go build`。
