# Database Schema

Nyro supports two storage backends — **SQLite** (default) and **PostgreSQL** — with identical table structures.

## Entity Relationship

```
providers ──1:N── model_backends ──N:1── models
    │                                    │
    └──1:1── provider_oauth_credentials  └──M:N── api_keys (via api_key_models)

api_keys ──M:N── models (via api_key_models)
request_logs (append-only)
settings (key-value)
```

---

## providers

AI 模型供应商配置（API endpoint、密钥、认证方式等）。

| Column | Type | Default | Description |
|---|---|---|---|
| `id` | TEXT PK | — | 主键，UUID |
| `name` | TEXT NOT NULL | — | 显示名称 |
| `vendor` | TEXT | NULL | 供应商标识（如 `openai`、`anthropic`） |
| `protocol` | TEXT NOT NULL | — | 默认通信协议（如 `openai-compatible`） |
| `base_url` | TEXT NOT NULL | — | API 端点基础 URL |
| `preset_key` | TEXT | NULL | 预设模板 key（内置供应商模板标识） |
| `channel` | TEXT | NULL | 预设通道 ID（如 `default`、`azure`） |
| `models_source` | TEXT | NULL | 模型列表获取方式 |
| `static_models` | TEXT | NULL | 静态模型列表（`\n` 分隔） |
| `api_key` | TEXT NOT NULL | — | API 密钥 |
| `auth_mode` | TEXT | `'apikey'` | 认证方式：`apikey` 或 `oauth` |
| `access_token` | TEXT | NULL | OAuth access token（迁移至 oauth 表后弃用） |
| `refresh_token` | TEXT | NULL | OAuth refresh token（迁移至 oauth 表后弃用） |
| `expires_at` | TEXT | NULL | OAuth token 过期时间（迁移至 oauth 表后弃用） |
| `use_proxy` | INTEGER | `0` | 是否通过代理发送请求 |
| `last_test_success` | INTEGER | NULL | 最近一次连通性测试是否成功 |
| `last_test_at` | TEXT | NULL | 最近一次连通性测试时间 |
| `is_enabled` | INTEGER | `1` | 是否启用 |
| `priority` | INTEGER | `0` | 优先级（预留） |
| `created_at` | TEXT | `datetime('now')` | 创建时间 |
| `updated_at` | TEXT | `datetime('now')` | 更新时间 |

---

## models

虚拟模型配置，定义客户端请求的模型名如何映射到后端。

| Column | Type | Default | Description |
|---|---|---|---|
| `id` | TEXT PK | — | 主键，UUID |
| `name` | TEXT NOT NULL | — | 显示名称 |
| `virtual_model` | TEXT | NULL | 客户端请求中使用的模型名（匹配键） |
| `balance` | TEXT | `'weighted'` | 多后端负载均衡策略：`weighted`、`priority`、`cooldown`、`latency` |
| `target_provider` | TEXT NOT NULL | — | 默认后端 provider ID（FK → providers.id） |
| `target_model` | TEXT NOT NULL | — | 默认后端使用的上游模型名 |
| `access_control` | INTEGER | `0` | 是否启用 API Key 访问控制 |
| `is_enabled` | INTEGER | `1` | 是否启用 |
| `priority` | INTEGER | `0` | 优先级（预留） |
| `created_at` | TEXT | `datetime('now')` | 创建时间 |

---

## model_backends

模型后端列表，一个 model 可对应多个 provider + 上游模型的组合。

| Column | Type | Default | Description |
|---|---|---|---|
| `id` | TEXT PK | — | 主键，UUID |
| `model_id` | TEXT NOT NULL | — | 所属模型 ID（FK → models.id, ON DELETE CASCADE） |
| `provider_id` | TEXT NOT NULL | — | 供应商 ID（FK → providers.id） |
| `model` | TEXT NOT NULL | — | 上游模型名（发送给 provider 的模型标识） |
| `weight` | INTEGER | `100` | 权重（`weighted` 策略下生效） |
| `priority` | INTEGER | `1` | 优先级，数值越小越优先（`priority` 策略下生效） |
| `created_at` | TEXT | `datetime('now')` | 创建时间 |

**索引**：`idx_model_backends_model_id` on `model_id`

---

## api_keys

API 密钥管理，用于代理端口的访问认证和限流。

| Column | Type | Default | Description |
|---|---|---|---|
| `id` | TEXT PK | — | 主键，UUID |
| `key` | TEXT NOT NULL UNIQUE | — | 密钥值（如 `nyro-xxxx`） |
| `name` | TEXT NOT NULL | — | 显示名称 |
| `rpm` | INTEGER | NULL | 每分钟请求数限制 |
| `rpd` | INTEGER | NULL | 每日请求数限制 |
| `tpm` | INTEGER | NULL | 每分钟 token 数限制 |
| `tpd` | INTEGER | NULL | 每日 token 数限制 |
| `is_enabled` | INTEGER | `1` | 是否启用 |
| `expires_at` | TEXT | NULL | 过期时间 |
| `created_at` | TEXT | `datetime('now')` | 创建时间 |
| `updated_at` | TEXT | `datetime('now')` | 更新时间 |

**索引**：`idx_api_keys_key` on `key`

---

## api_key_models

API Key 与模型的访问绑定关系（M:N 关联表）。仅当 model 启用 `access_control` 时生效。

| Column | Type | Description |
|---|---|---|
| `api_key_id` | TEXT NOT NULL | API Key ID（FK → api_keys.id, ON DELETE CASCADE） |
| `model_id` | TEXT NOT NULL | 模型 ID（FK → models.id, ON DELETE CASCADE） |

**主键**：`(api_key_id, model_id)`

**索引**：`idx_api_key_models_model_id` on `model_id`

---

## provider_oauth_credentials

OAuth 凭据存储，用于需要 OAuth 认证的供应商（如 Google Vertex AI）。

| Column | Type | Default | Description |
|---|---|---|---|
| `provider_id` | TEXT PK | — | 供应商 ID（FK → providers.id, ON DELETE CASCADE） |
| `driver_key` | TEXT | `''` | OAuth 驱动标识 |
| `scheme` | TEXT | `''` | 认证方案 |
| `access_token` | TEXT | `''` | OAuth access token |
| `refresh_token` | TEXT | NULL | OAuth refresh token |
| `expires_at` | TEXT | NULL | Token 过期时间 |
| `resource_url` | TEXT | NULL | 资源 URL（部分 OAuth 流程需要） |
| `subject_id` | TEXT | NULL | 认证主体 ID |
| `scopes` | TEXT | `'[]'` | OAuth 权限范围（JSON 数组） |
| `meta` | TEXT | `'{}'` | 扩展元数据（JSON） |
| `status` | TEXT | `'connected'` | 连接状态 |
| `status_version` | INTEGER | `0` | 状态版本号（乐观锁） |
| `last_error` | TEXT | NULL | 最近一次错误信息 |
| `last_refresh_at` | TEXT | NULL | 最近一次 token 刷新时间 |
| `created_at` | TEXT | `datetime('now')` | 创建时间 |
| `updated_at` | TEXT | `datetime('now')` | 更新时间 |

---

## request_logs

请求日志（追加写入，记录每次代理请求的完整信息）。

| Column | Type | Default | Description |
|---|---|---|---|
| `id` | TEXT PK | — | 日志 ID |
| `created_at` | INTEGER | `0` | Unix 毫秒时间戳 |
| `api_key_id` | TEXT | NULL | 认证使用的 API Key ID |
| `api_key_name` | TEXT | NULL | API Key 名称（快照） |
| `client_protocol` | TEXT | NULL | 客户端协议（如 `openai/chat/v1`） |
| `upstream_protocol` | TEXT | NULL | 上游协议 |
| `provider_id` | TEXT | NULL | 供应商 ID |
| `provider_name` | TEXT | NULL | 供应商名称（快照） |
| `model_id` | TEXT | NULL | 匹配到的模型 ID |
| `model_name` | TEXT | NULL | 模型名称（快照） |
| `upstream_url` | TEXT | NULL | 上游请求 URL |
| `client_model` | TEXT | NULL | 客户端请求中的模型名 |
| `upstream_model` | TEXT | NULL | 实际发送给上游的模型名 |
| `method` | TEXT | NULL | HTTP 方法 |
| `path` | TEXT | NULL | 请求路径 |
| `client_request_headers` | TEXT | NULL | 客户端请求头（JSON，可选记录） |
| `client_request_body` | TEXT | NULL | 客户端请求体（可选记录） |
| `client_response_headers` | TEXT | NULL | 客户端响应头（JSON，可选记录） |
| `client_response_body` | TEXT | NULL | 客户端响应体（可选记录） |
| `upstream_request_headers` | TEXT | NULL | 上游请求头（JSON，可选记录） |
| `upstream_request_body` | TEXT | NULL | 上游请求体（可选记录） |
| `upstream_response_headers` | TEXT | NULL | 上游响应头（JSON，可选记录） |
| `upstream_response_body` | TEXT | NULL | 上游响应体（可选记录） |
| `upstream_status_code` | INTEGER | NULL | 上游 HTTP 状态码 |
| `client_status_code` | INTEGER | NULL | 返回给客户端的 HTTP 状态码 |
| `latency_total_ms` | INTEGER | NULL | 总延迟（毫秒） |
| `latency_upstream_ms` | INTEGER | NULL | 上游延迟（毫秒） |
| `input_tokens` | INTEGER | `0` | 输入 token 数 |
| `output_tokens` | INTEGER | `0` | 输出 token 数 |
| `cache_read_tokens` | INTEGER | `0` | 缓存命中 token 数 |
| `is_stream` | INTEGER | `0` | 是否为流式请求 |
| `stream_chunks_count` | INTEGER | `0` | 流式分块数量 |
| `stream_first_chunk_ms` | INTEGER | NULL | 首个分块延迟（毫秒） |

**索引**：
- `idx_logs_created_at` on `created_at`
- `idx_logs_provider_id` on `provider_id`
- `idx_logs_client_status` on `client_status_code`
- `idx_logs_upstream_model` on `upstream_model`
- `idx_logs_api_key` on `api_key_id`
- `idx_logs_client_protocol` on `client_protocol`
- `idx_logs_upstream_protocol` on `upstream_protocol`

---

## settings

系统配置键值对。

| Column | Type | Default | Description |
|---|---|---|---|
| `key` | TEXT PK | — | 配置键 |
| `value` | TEXT NOT NULL | — | 配置值 |
| `updated_at` | TEXT | `datetime('now')` | 更新时间 |

---

## 迁移说明

Nyro 采用 idempotent 迁移策略：`INIT_SQL` 创建旧名称表（如 `routes`、`route_targets`、`api_key_routes`），`migrate()` 末尾执行 rename：

```
routes             → models
route_targets      → model_backends
api_key_routes     → api_key_models
routes.strategy    → models.balance
request_logs.route_id   → request_logs.model_id
request_logs.route_name → request_logs.model_name
```

所有 rename 操作均为幂等：先检查旧列存在且新列不存在，才执行 `ALTER TABLE RENAME`。
