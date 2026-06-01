# Nyro Standalone 模式

Standalone 模式通过单个 YAML 文件驱动，无需数据库，仅运行代理服务。适用于：

- 容器化部署，配置通过 ConfigMap / Volume 挂载
- CI/CD 管道中的临时代理
- 嵌入式或边缘场景，资源受限
- Config-as-Code 工作流

```bash
nyro-server --config config.yaml
```

---

## 配置文件示例

```yaml
server:
  proxy_host: "0.0.0.0"
  proxy_port: 19530

providers:
  - name: openai
    default_protocol: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    models_source: https://api.openai.com/v1/models

  - name: anthropic
    default_protocol: anthropic
    endpoints:
      anthropic:
        base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}

  - name: deepseek-openai
    default_protocol: openai
    endpoints:
      openai:
        base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}

  - name: deepseek-anthropic
    default_protocol: anthropic
    endpoints:
      anthropic:
        base_url: https://api.deepseek.com/anthropic
    api_key: ${DEEPSEEK_API_KEY}

routes:
  - name: gpt-4o
    vmodel: gpt-4o
    targets:
      - provider: openai
        model: gpt-4o

  - name: claude-sonnet
    vmodel: claude-sonnet-4-20250514
    targets:
      - provider: anthropic
        model: claude-sonnet-4-20250514

  - name: deepseek-chat
    vmodel: deepseek-chat
    strategy: priority
    targets:
      - provider: deepseek-openai
        model: deepseek-chat
        priority: 1
      - provider: openai
        model: gpt-4o-mini
        priority: 2

  - name: text-embedding-3-small
    vmodel: text-embedding-3-small
    targets:
      - provider: openai
        model: text-embedding-3-small
```

### 最小配置

```yaml
providers:
  - name: openai
    default_protocol: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    api_key: sk-xxx

routes:
  - name: gpt-4o
    vmodel: gpt-4o
    targets:
      - provider: openai
        model: gpt-4o
```

### 简化写法（等价）

`default_protocol` / `api_key` 支持别名 `protocol` / `apikey`，单 endpoint 时 `protocol` 可省略（自动取 endpoints 的第一个协议）：

```yaml
providers:
  - name: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    apikey: sk-xxx

routes:
  - name: gpt-4o
    vmodel: gpt-4o
    targets:
      - provider: openai
        model: gpt-4o
```

> 规范名与别名**不能同时出现**（例如同时写 `default_protocol` 和 `protocol`），否则启动时报错。

---

## 配置字段说明

### server

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `proxy_host` | `127.0.0.1` | 代理监听地址 |
| `proxy_port` | `19530` | 代理监听端口 |

> `--proxy-host` / `--proxy-port` CLI 参数不覆盖 YAML 值，Standalone 模式代理地址完全由 YAML 控制。

### providers[]

| 字段 | 别名 | 必填 | 说明 |
|------|------|------|------|
| `name` | — | 是 | Provider 名称，路由中通过此名称引用 |
| `default_protocol` | `protocol` | 否 | 默认出口协议，必须在 `endpoints` 中有对应条目；省略时自动取 `endpoints` 声明顺序的第一个协议（多 endpoint 时会打印 WARN 日志建议显式设置） |
| `endpoints` | — | 是 | 协议 → 端点映射，key 为协议名（`openai` / `anthropic` / `gemini`），保留 YAML 声明顺序 |
| `api_key` | `apikey` | 是 | API 密钥，支持 `${ENV_VAR}` 环境变量引用 |
| `use_proxy` | — | 否 | 是否使用系统 HTTP 代理（默认 `false`） |
| `models_source` | — | 否 | 模型发现 URL 或 `ai://models.dev/{vendor}` |
| `capabilities_source` | — | 否 | 模型能力发现 URL 或 `ai://models.dev/{vendor}` |
| `static_models` | — | 否 | 静态模型列表，无 API 时的硬编码兜底 |

> 规范名与别名互斥：同一 provider 下同时出现 `default_protocol` + `protocol`，或 `api_key` + `apikey`，启动时直接报错。

### routes[]

| 字段 | 别名 | 必填 | 说明 |
|------|------|------|------|
| `name` | — | 是 | 路由名称 |
| `virtual_model` | `vmodel` | 是 | 客户端请求的模型 ID（精确匹配） |
| `type` | — | 否 | 路由类型：`chat`（默认）/ `embedding` |
| `strategy` | — | 否 | 负载策略：`weighted`（默认）/ `priority` |
| `targets` | — | 是 | 目标列表（至少一个） |
| `access_control` | — | 否 | 是否启用访问控制（默认 `false`）。别名 `enable_auth` |

### routes[].targets[]

| 字段 | 必填 | 说明 |
|------|------|------|
| `provider` | 是 | Provider 名称（需与 `providers[].name` 匹配） |
| `model` | 是 | 实际模型 ID |
| `weight` | 否 | 权重，`weighted` 策略下使用（默认 `100`） |
| `priority` | 否 | 优先级，`priority` 策略下使用（默认 `1`，数字越小优先级越高） |

---

## 缓存配置

Standalone 模式支持请求缓存，在 `cache` 段配置：

```yaml
cache:
  exact:
    enabled: true
    default_ttl: 3600       # 缓存有效期（秒），默认 3600
    max_entries: 1000       # 最大缓存条数，默认 1000

  semantic:
    enabled: true
    embedding_route: text-embedding-3-small   # 用于语义向量化的路由名
    similarity_threshold: 0.92               # 语义相似度阈值，默认 0.92
    vector_dimensions: 1536                  # 向量维度，需与 embedding 模型匹配
    default_ttl: 600                         # 缓存有效期（秒），默认 600
    max_entries: 500                         # 最大缓存条数，默认 500
```

两种缓存策略：

| 策略 | 说明 |
|------|------|
| `exact` | 完全匹配缓存，相同请求体直接返回缓存结果 |
| `semantic` | 语义相似缓存，通过 embedding 向量计算相似度，相似请求复用缓存 |

---

## 多协议转发

当 Provider 声明了多个协议端点时，Nyro 按请求协议自动选择对应端点：

```yaml
providers:
  - name: deepseek
    default_protocol: openai
    endpoints:
      openai:
        base_url: https://api.deepseek.com/v1
      anthropic:
        base_url: https://api.deepseek.com/anthropic
```

- OpenAI 客户端请求 → 直接转发到 `openai` 端点
- Anthropic 客户端请求 → 直接转发到 `anthropic` 端点
- Gemini 客户端请求 → Provider 不支持，自动转换为 `default_protocol`（`openai`）后转发

---

## Docker 部署

```dockerfile
FROM rust:1.83 AS builder
WORKDIR /app
COPY . .
RUN cargo build --release -p nyro-server

FROM debian:bookworm-slim
COPY --from=builder /app/target/release/nyro-server /usr/local/bin/
COPY config.yaml /etc/nyro/config.yaml
EXPOSE 19530
CMD ["nyro-server", "--config", "/etc/nyro/config.yaml"]
```

`proxy_host` 在 YAML 中设置为 `0.0.0.0` 即可对容器外暴露。

---

## 与完整模式的差异

| 能力 | 完整模式 | Standalone |
|------|----------|------------|
| Provider / Route 管理 | Admin API 实时读写 | 编辑 YAML + 重启 |
| Admin API | 完整 | 不启动 |
| WebUI | 完整 | 不启动 |
| 请求日志 | DB 存储 + WebUI 查看 | 仅 stdout |
| 数据持久化 | SQLite / Postgres | 无（进程重启从 YAML 恢复） |
| 缓存 | — | exact + semantic 缓存（内存） |
| 部署依赖 | 二进制 + 存储目录 | 二进制 + YAML 文件 |
