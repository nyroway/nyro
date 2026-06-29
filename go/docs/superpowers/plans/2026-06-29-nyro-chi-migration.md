# nyro Web 框架迁移 gin → chi Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 nyro gateway 的 web 层从 gin 切换到 chi（v5），handler 改为纯 net/http 风格（`func(w http.ResponseWriter, r *http.Request)`）+ 自写 helper，移除 gin 依赖。

**Architecture:** chi router + 原生 `net/http` handler；新增 `internal/web` helper 包（JSON/Error/Decode）替代 `c.JSON`/`c.ShouldBindJSON`；SSE/streaming 不受影响（dispatcher 已是 `io.Writer`+`http.Flusher`）。按包迁移（proxy / admin），中间状态 gin+chi 共存，最后 `go mod tidy` 移除 gin。

**Tech Stack:** `github.com/go-chi/chi/v5`, `github.com/go-chi/chi/v5/middleware`, 标准库 `net/http`+`encoding/json`。

## Global Constraints

- 代码在 `go/`（module `github.com/nyroway/nyro/go`），gofmt-clean。
- handler 一律 `func(w http.ResponseWriter, r *http.Request)`（纯 net/http，**不**用 chi/render）。
- 保持现有路由路径、请求/响应 JSON 形状、行为完全不变（纯框架替换，无功能变更）。
- 每个任务以 `go build ./...` + `go test ./...` 通过 + 独立 commit 收尾。
- streaming（dispatcher `serveStream`/`serveNonStream`/`writeSSE`）不改——它已经是 net/http。

---

## API 映射规则（所有任务统一应用）

| gin | chi / net/http |
|---|---|
| `func(c *gin.Context)` | `func(w http.ResponseWriter, r *http.Request)` |
| `c.JSON(status, v)` | `web.JSON(w, status, v)` |
| `c.JSON(status, gin.H{...})` | `web.JSON(w, status, map[string]any{...})` |
| `c.ShouldBindJSON(&v)`（+ badRequest on err） | `if err := web.Decode(r, &v); err != nil { web.Error(w, http.StatusBadRequest, err.Error(), "invalid_request"); return }` |
| `c.Param("id")` | `chi.URLParam(r, "id")` |
| `c.Query("k")` | `r.URL.Query().Get("k")` |
| `c.DefaultQuery("k", "d")` | inline: `v := r.URL.Query().Get("k"); if v == "" { v = "d" }` |
| `c.Status(code)` | `w.WriteHeader(code)` |
| `c.Header().Set(k, v)` | `w.Header().Set(k, v)` |
| `c.Writer` / `c.Request` | `w` / `r` |
| `c.File(path)` | `http.ServeFile(w, r, path)` |
| `r.GET/POST(path, h)` / `r.PUT/DELETE` | `r.Get/Post/Put/Delete(path, h)` |
| `r.Group("/x")` + `g.Use`/`g.GET` | `r.Route("/x", func(r chi.Router) { r.Use(...); r.Get(...) })` |
| `r.NoRoute(h)` | `r.NotFound(h)` |
| `gin.New()` | `chi.NewRouter()` |
| `gin.Default()` | `chi.NewRouter()` + `r.Use(middleware.Recoverer)` |
| `gin.Recovery()` | `middleware.Recoverer` |
| `gin.SetMode(gin.TestMode)` | 删除（chi 无 mode） |
| bearerAuth `gin.HandlerFunc`（`c.AbortWithStatus`/`c.Next`） | chi middleware: `func(next http.Handler) http.Handler { return http.HandlerFunc(func(w, r) { ...; next.ServeHTTP(w, r) }) }`（拒绝时 `web.Error(...); return`，不调 next） |

---

## File Structure

- **Create** `go/internal/web/web.go` — JSON / Error / Decode helpers
- **Create** `go/internal/web/web_test.go`
- **Modify** `go/internal/proxy/server.go` — NewRouter → chi.Router；handleProxy → http.HandlerFunc
- **Modify** `go/internal/proxy/models_list.go` — handleModelsList → http.HandlerFunc
- **Modify** `go/internal/proxy/webui.go` — MountWebui(r chi.Router)；NoRoute→NotFound；c.File→http.ServeFile
- **Modify** `go/internal/proxy/*_test.go` — 去 gin.SetMode，NewRouter 返回值按 http.Handler 用（httptest 调用不变）
- **Modify** `go/internal/admin/admin.go` — Mount(r chi.Router)；所有 handler → http.HandlerFunc；bearerAuth → chi middleware；badRequest/conflict/writeJSONError → web helpers
- **Modify** `go/internal/admin/oauth.go` — MountOAuth(r chi.Router)；handlers → http.HandlerFunc
- **Modify** `go/internal/admin/*_test.go` — newEngine 返回 chi.Router
- **Modify** `go/cmd/admin/admin.go` — gin.New()+Recovery → chi.NewRouter()+middleware.Recoverer
- **Modify** `go/go.mod` / `go.sum` — 加 chi，最后移除 gin

`cmd/gateway/gateway.go` 与 `nyro.go` 不需改逻辑：`proxy.NewRouter` 返回 `chi.Router`（实现 `http.Handler`），直接传 `bootstrap.RunServer(http.Handler, ...)`；`nyro.go` 只注册子命令。

---

### Task 1: internal/web helper 包 + chi 依赖

**Files:**
- Create: `go/internal/web/web.go`, `go/internal/web/web_test.go`
- Modify: `go/go.mod`, `go/go.sum`

**Interfaces:**
- Produces: `web.JSON(w, status, v)`, `web.Error(w, status, message, errType)`, `web.Decode(r, v) error`

- [ ] **Step 1: 写 web_test.go**

```go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	web_JSONForTest := JSON
	web_JSONForTest(rec, http.StatusCreated, map[string]any{"ok": true, "n": 3})
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["ok"] != true || got["n"] != float64(3) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestError(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusBadRequest, "bad input", "invalid_request")
	var got map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["error"]["message"] != "bad input" || got["error"]["type"] != "invalid_request" {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestDecode(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"b"}`))
	var v map[string]string
	if err := Decode(req, &v); err != nil {
		t.Fatal(err)
	}
	if v["a"] != "b" {
		t.Errorf("decoded = %v", v)
	}
}
```
> 注：`TestJSON` 第一行的 `web_JSONForTest := JSON` 是个无意义别名，**实际写测试时直接调 `JSON(rec, ...)` 即可**（同包测试），删除该行。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd go && go test ./internal/web/`
Expected: FAIL（包/函数不存在）。

- [ ] **Step 3: 写 web.go**

```go
// Package web provides the small set of net/http JSON helpers used across the
// nyro HTTP layer (admin API + proxy), replacing gin's c.JSON / c.ShouldBind.
package web

import (
	"encoding/json"
	"net/http"
)

// JSON encodes v as JSON and writes it with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a gateway-style error envelope: {"error":{"message":...,"type":...}}.
func Error(w http.ResponseWriter, status int, message, errType string) {
	JSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": errType}})
}

// Decode reads a JSON request body into v.
func Decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
```

- [ ] **Step 4: 加 chi 依赖**

Run: `cd go && go get github.com/go-chi/chi/v5@latest && go mod tidy`
（gin 暂时保留——后续任务还在用它。）

- [ ] **Step 5: 跑测试 + 编译**

Run: `cd go && go test ./internal/web/ && go build ./...`
Expected: web 测试 PASS；全量编译通过。

- [ ] **Step 6: Commit**

```bash
git add go/internal/web/ go/go.mod go/go.sum && git commit -m "feat(go): add internal/web JSON helpers + chi dependency"
```

---

### Task 2: proxy 包迁移到 chi

**Files:**
- Modify: `go/internal/proxy/server.go`, `go/internal/proxy/models_list.go`, `go/internal/proxy/webui.go`
- Modify: `go/internal/proxy/*_test.go`（dispatcher_test, models_list_test, ready_test, latency_test, gateway_test, transform_test, failover_test, *_e2e_test, webui_test）
- Consumes: `internal/web`（Task 1）, `github.com/go-chi/chi/v5`, `github.com/go-chi/chi/v5/middleware`

- [ ] **Step 1: 迁移 server.go**

`NewRouter(gw *Gateway)` 当前返回 `*gin.Engine`，注册 `/healthz`、`/readyz`、`/v1/models`、`/v1/chat/completions` 等。改为返回 `chi.Router`：

- `gin.New()` + `gin.Recovery()` → `chi.NewRouter()` + `r.Use(middleware.Recoverer)`
- `r.GET("/healthz", func(c *gin.Context){ c.JSON(200, gin.H{...}) })` → `r.Get("/healthz", func(w http.ResponseWriter, r *http.Request){ web.JSON(w, 200, map[string]any{...}) })`
- `r.GET("/readyz", ...)` → `r.Get("/readyz", ...)`（同样 handler 改写）
- `r.GET("/v1/models", func(c){ handleModelsList(c, gw) })` → `r.Get("/v1/models", func(w, r){ handleModelsList(w, r, gw) })`
- `r.POST("/v1/chat/completions", func(c){ handleProxy(c, gw, ...) })` → `r.Post("/v1/chat/completions", func(w, r){ handleProxy(w, r, gw, ...) })`（其余 POST 路由同理）
- Gemini 路径 `/v1beta/models/:resource` → chi 的 `/v1beta/models/{resource}`，`chi.URLParam(r, "resource")`

`handleProxy(c *gin.Context, gw, ep, pathModel, pathStream)` → `handleProxy(w http.ResponseWriter, r *http.Request, gw, ep, pathModel, pathStream)`：
- `c.Writer` → `w`，`c.Request` → `r`
- `c.JSON(status, gin.H{"error":...})` → `web.Error/w/web.JSON`
- `gw.Dispatch(c.Writer, c.Request, req, h)` → `gw.Dispatch(w, r, req, h)`（Dispatch 签名已是 net/http，不改）

- [ ] **Step 2: 迁移 models_list.go**

`handleModelsList(c *gin.Context, gw)` → `handleModelsList(w http.ResponseWriter, r *http.Request, gw)`：
- `c.Request` → `r`，`c.JSON(200, gin.H{...})` → `web.JSON(w, 200, map[string]any{...})`
- `extractKey(c.Request)` → `extractKey(r)`（extractKey 在 inbound.go，已是 net/http）

- [ ] **Step 3: 迁移 webui.go**

`MountWebui(r *gin.Engine, dir string)` → `MountWebui(r chi.Router, dir string)`：
- `r.NoRoute(func(c){...})` → `r.NotFound(func(w, r){...})`
- `c.Request.URL.Path` → `r.URL.Path`；`c.Request.Method` → `r.Method`
- `c.JSON(404, gin.H{...})` → `web.Error(w, 404, "not found", "gateway_error")`
- `c.File(full)` / `c.File(index)` → `http.ServeFile(w, r, full)` / `http.ServeFile(w, r, index)`

- [ ] **Step 4: 迁移 proxy 测试**

所有 proxy 测试：
- 删除 `gin.SetMode(gin.TestMode)` 调用
- `NewRouter(gw)` 现返回 `chi.Router`；测试里 `r := NewRouter(gw)` 后用 `r.ServeHTTP(rec, req)` 不变（chi.Router 实现 http.Handler）
- 凡是 `func(c *gin.Context)` 的 helper（如 webui_test 里的测试 handler）改 `func(w, r)`
- `gin.H{...}` → `map[string]any{...}`
- import：去 `gin-gonic/gin`，按需加 `chi`（多数测试只需 httptest + 调 NewRouter，不必直接 import chi）

- [ ] **Step 5: 编译 + 测试**

Run: `cd go && go build ./... && go test ./internal/proxy/`
Expected: 编译通过；proxy 全部测试 PASS。

- [ ] **Step 6: Commit**

```bash
git add go/internal/proxy/ && git commit -m "refactor(go): migrate proxy package from gin to chi"
```

---

### Task 3: admin 包迁移到 chi

**Files:**
- Modify: `go/internal/admin/admin.go`, `go/internal/admin/oauth.go`
- Modify: `go/internal/admin/admin_test.go`, `go/internal/admin/oauth_test.go`, `go/internal/admin/stats_test.go`
- Consumes: `internal/web`、chi

- [ ] **Step 1: 迁移 admin.go**

- `func Mount(r gin.IRouter, s storage.Storage, adminToken string)` → `func Mount(r chi.Router, s storage.Storage, adminToken string)`
- `g := r.Group("/api/v1")` + `g.Use(bearerAuth(token))` → `r.Route("/api/v1", func(g chi.Router) { g.Use(bearerAuth(adminToken)); g.Get("/status", ...); ... })`
- 所有 `g.GET/POST/PUT/DELETE(path, func(c *gin.Context){...})` → `g.Get/Post/Put/Delete(path, func(w, r){...})`
- 每个 handler 内：`c.JSON`→`web.JSON`、`c.ShouldBindJSON`→`web.Decode`+err、`c.Param`→`chi.URLParam`、`c.Query`→`r.URL.Query().Get`、`gin.H`→`map[string]any`、`c.Writer`/`c.Request`→`w`/`r`
- 现有 helper `badRequest`/`conflict`/`bumpEpoch`：badRequest/conflict 当前是 `func(c *gin.Context, err)`——改为 `func(w, r, ...)` 用 web.Error；或直接内联 `web.Error(...)` 删除这些 helper（DRY：web.Error 已覆盖）
- `bearerAuth(token) gin.HandlerFunc` → chi middleware：
  ```go
  func bearerAuth(token string) func(http.Handler) http.Handler {
      return func(next http.Handler) http.Handler {
          return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
              if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+token) {
                  web.Error(w, http.StatusUnauthorized, "unauthorized", "auth_error")
                  return
              }
              next.ServeHTTP(w, r)
          })
      }
  }
  ```
- `parseHours` 闭包：`func(c *gin.Context) int64` → `func(r *http.Request) int64`（读 `r.URL.Query()`）

- [ ] **Step 2: 迁移 oauth.go**

- `MountOAuth(g gin.IRouter, s, reg, sessions)` → `MountOAuth(r chi.Router, s, reg, sessions)`
- `auth_g := g.Group("/api/v1/auth")` → `r.Route("/api/v1/auth", func(ag chi.Router){ ag.Post("/sessions", ...); ... })`
- `g.POST("/api/v1/providers/:id/oauth/connect", ...)` → `r.Post("/api/v1/providers/{id}/oauth/connect", ...)`（chi 用 `{id}`）
- 所有 handler 改 http.HandlerFunc + API 映射规则
- `c.JSON`、`c.ShouldBindJSON`、`c.Param`、`sessions.Update` 等业务逻辑不变

- [ ] **Step 3: 迁移 admin 测试**

- `newEngine(t, token)` 当前返回 `(*gin.Engine, *memory.Backend)`：改为返回 `(chi.Router, *memory.Backend)`——`r := chi.NewRouter(); Mount(r, st.Storage(), token); return r, st`
- 删 `gin.SetMode`
- `do(r *gin.Engine, ...)` → `do(r http.Handler, ...)`（或 chi.Router）；httptest 调用不变
- `gin.H` → `map[string]any`（如有）

- [ ] **Step 4: 编译 + 测试**

Run: `cd go && go build ./... && go test ./internal/admin/`
Expected: 编译通过；admin 全部测试 PASS。

- [ ] **Step 5: Commit**

```bash
git add go/internal/admin/ && git commit -m "refactor(go): migrate admin package from gin to chi"
```

---

### Task 4: cmd/admin + 兼容确认

**Files:**
- Modify: `go/cmd/admin/admin.go`
- Verify: `go/cmd/gateway/gateway.go`、`go/nyro.go`（应无需改逻辑）

- [ ] **Step 1: 迁移 cmd/admin/admin.go**

RunE 内：
- `engine := gin.New(); engine.Use(gin.Recovery())` → `engine := chi.NewRouter(); engine.Use(middleware.Recoverer)`
- `admin.Mount(engine, st, adminToken)` —— engine 现在是 chi.Router（Mount 签名 Task 3 已改）
- `admin.MountOAuth(engine, st, reg, sessions)`、`proxy.MountWebui(engine, webuiDir)` —— 同样接收 chi.Router
- `bootstrap.RunServer(engine, addr)` —— chi.Router 是 http.Handler，不变
- import：去 gin，加 chi + chi/middleware

- [ ] **Step 2: 确认 cmd/gateway 与 nyro.go**

- `cmd/gateway/gateway.go`：`engine := proxy.NewRouter(gw)` 现在 `engine` 是 `chi.Router`；`bootstrap.RunServer(engine, addr)`（`http.Handler`）不变。**应无需改**。验证：读 gateway.go 确认无 gin 残留引用；若 import 了 gin 则删除。
- `nyro.go`：只 `AddCommand(gateway.NewCmd(), admin.NewCmd(), tool.NewCmd())`，无 web 框架代码。**应无需改**。

- [ ] **Step 3: 编译 + 全量测试**

Run: `cd go && go build ./... && go test ./...`
Expected: 全量通过。

- [ ] **Step 4: Commit**

```bash
git add go/cmd/admin/ go/cmd/gateway/ && git commit -m "refactor(go): migrate cmd/admin to chi; drop gin from commands"
```

---

### Task 5: 移除 gin 依赖 + 全量验证 + smoke

**Files:**
- Modify: `go/go.mod`, `go/go.sum`

- [ ] **Step 1: 确认无 gin 残留引用**

Run: `cd go && grep -rln "gin-gonic/gin" --include="*.go" .`
Expected: **无输出**（所有 gin 引用已迁移）。若有残留，修掉再继续。

- [ ] **Step 2: go mod tidy（移除 gin）**

Run: `cd go && go mod tidy && go build ./... && go test ./...`
Expected: `go.mod` 不再含 `gin-gonic/gin`；编译 + 全量测试通过。

- [ ] **Step 3: smoke 测试**

Run:
```bash
cd go && go build -o /tmp/nyro . \
  && /tmp/nyro gateway --config /dev/null 2>&1 | head -1 \
  && printf 'providers: []\nmodels: []\n' > /tmp/nyro.yaml \
  && /tmp/nyro gateway --addr 127.0.0.1:19539 --config /tmp/nyro.yaml & sleep 1 \
  && curl -s http://127.0.0.1:19539/readyz ; kill %1 2>/dev/null
```
Expected: gateway 能启动（standalone 从空 config）；/readyz 返回 ready。

- [ ] **Step 4: Commit**

```bash
git add go/go.mod go/go.sum && git commit -m "build(go): drop gin dependency after chi migration"
```

---

## Self-Review

**1. Spec coverage（设计要点）:**
- gin→chi + 纯 net/http handler → 全部任务（映射规则）✓
- internal/web helper（JSON/Error/Decode）→ Task 1 ✓
- proxy 包（server/models_list/webui + 测试）→ Task 2 ✓
- admin 包（admin/oauth + 测试）→ Task 3 ✓
- cmd/nyro.go 兼容 → Task 4 ✓
- 移除 gin 依赖 → Task 5 ✓
- streaming 不变 → 显式约束（dispatcher 不改）✓

**2. Placeholder scan:** Task 1 测试里的 `web_JSONForTest` 别名已注明删除。其余步骤含具体映射规则 + 文件清单，无 TBD。✓

**3. Type/接口一致性:**
- `web.JSON/Error/Decode` — Task 1 定义，Task 2/3/4 消费 ✓
- `proxy.NewRouter(gw) chi.Router` — Task 2 定义，cmd/gateway 消费（http.Handler 兼容）✓
- `admin.Mount(r chi.Router, ...)` / `admin.MountOAuth` / `proxy.MountWebui` — Task 2/3 改签名，cmd/admin 消费 ✓
- `bearerAuth` chi middleware 签名 — Task 3 定义 ✓
- `gw.Dispatch(w, r, ...)` — 已是 net/http，server.go Task 2 传 w/r ✓

**4. 风险点:**
- Task 2/3 中间状态：proxy 用 chi、admin 仍 gin（共存），`go build` 必须通过——两包独立路由，不冲突 ✓
- chi 路径参数语法 `:id`→`{id}`：Task 2（Gemini resource）、Task 3（oauth :id）必须改 ✓
- `gin.H` 是 `map[string]any` 别名——逐个替换不能漏 ✓
