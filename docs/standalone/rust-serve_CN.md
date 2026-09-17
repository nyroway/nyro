# 实验性 Rust 配置服务

[English](rust-serve.md)

源码构建的根命令 `nyro serve` 为[实验性 Rust 代理](rust-proxy_CN.md)增加 SQLite 或 PostgreSQL 配置控制面，复用相同的 LLM 运行时和配置格式。G10 当前两个后端均已支持完整配置草稿、Provider／Model／API Key CRUD 与显式发布闭环。WebUI、OAuth 管理、旧数据导入、持久化用量预算和多进程部署仍待后续实现。已发布的 `nyro-server` 与桌面入口继续使用原有数据库。

## 启动与重启

复制 [rust-proxy.yaml](rust-proxy.yaml) 为 `nyro.yaml`，设置 Provider 地址、模型与凭证，然后生成私有管理令牌文件并初始化专用数据库：

```sh
umask 077
openssl rand -hex 32 > admin.token
cargo run -p nyro -- serve \
  --database control.sqlite \
  --config nyro.yaml \
  --admin-token-file admin.token
```

`--database PATH`（SQLite）和 `--postgres-url-file PATH`（PostgreSQL）必须且只能选择一个；两者互斥。两种后端均要求 `--admin-token-file`。管理令牌需要 16–1024 个可见 ASCII 字符；末尾 CR/LF 会被移除，整个文件最多 1024 字节。该令牌与数据面客户端使用的 `security.api_keys` 分开。管理监听默认 `127.0.0.1:19531`，`--admin-listen` 只接受回环 IP 地址。数据监听由已发布配置的 `server.listen` 指定（默认 `127.0.0.1:19530`），两者需使用不同地址。

`--config` 仅用于为空的专用数据库建立草稿与已发布快照，初始版本均为 `1`。已初始化数据库拒绝再次传入 `--config`，重启时省略：

```sh
cargo run -p nyro -- serve \
  --database control.sqlite \
  --admin-token-file admin.token
```

启动校验并加载持久化的**已发布**快照。未发布草稿在重启后保留，但不会自动生效。修改种子 YAML 不会重新导入。`serve` 不支持 SIGHUP 重载，Unix 的默认信号行为可能终止进程；后续更新使用管理 API。

一个 SQLite 文件只允许一个进程持有，存储层保持独占连接，并拒绝旧版、外部、不支持或损坏的数据库，没有隐式转换或导入。Unix 上新数据库文件权限为 `0600`，已有 Unix 文件若有任何组／其他用户权限位则拒绝打开，不会悄悄修改权限。快照包含明文 Provider、代理和客户端凭证，需保护目录、令牌文件、数据库、备份及导出的 JSON。管理令牌本身从文件读取，不存入配置数据库。

## PostgreSQL 配置

先创建使用 UTF8 编码的专用空 PostgreSQL 数据库，并为连接角色授予创建和使用控制表的权限。将连接 URL 保存到 `postgres.url` 等私有普通文件；命令行只传文件路径，不传 URL。文件必须包含一个 UTF-8 URL，整个文件（含末尾 CR/LF）最多 16 KiB，读取时去掉末尾换行。文件可能包含数据库密码，需像管理令牌一样保护。

```sh
cargo run -p nyro -- serve \
  --postgres-url-file postgres.url \
  --config nyro.yaml \
  --admin-token-file admin.token
```

TLS 默认 `verify-full`。显式 `sslmode` 可选择 `require`、`verify-ca` 或 `verify-full`；`require` 不提供与 `verify-full` 相同的证书／主机名校验。`sslmode=disable` 仅对 `localhost` 或字面回环 IP 开放，用于本地测试。拒绝机会式 `prefer`／`allow` 模式。URL 必须指定数据库并使用 TCP，不接受 Unix socket 或 TLS 以外的查询参数；需要时可通过 `sslrootcert`、`sslcert`、`sslkey` 指定证书。

重启使用相同命令并去掉 `--config`。下文草稿、发布、实体 CRUD 和脱敏契约适用于 SQLite 与 PostgreSQL。PostgreSQL 快照保存于 `public.nyro_control_state`，必须使用专用数据库，不能将控制表加入已有业务库或 Nyro 旧库。不提供 SQLite／PostgreSQL 自动转换。详见[数据库说明](../database/schema.md)及生成的 [control-postgres.sql](../../deploy/schema/control-postgres.sql)，后者与旧版 `postgres.sql` 分开。生成 SQL 仅供参考，不应预先导入：Nyro 在空数据库中原子创建表与种子行；预建空表会被拒绝。

存储层在整个生命周期内保持单连接与会话级 advisory lock，第二个使用同一数据库的服务会被拒绝。应直连 PostgreSQL，不支持事务／语句级连接池、自动重连和多副本。建立连接及每个存储操作的期限为 5 秒，PostgreSQL 语句超时为 3 秒、锁等待超时为 1 秒。连接丢失或存储操作超过期限后，控制存储停止接收后续操作，直至重启。当前活动的数据面代际继续服务；`/readyz` 仍只反映 Host 可用性，不代表数据库健康。

两个后端的快照和数据库备份都包含明文凭证，需限制数据库访问并按密钥保护备份。advisory lock 用于协调 Nyro 实例，不能阻止其他数据库客户端修改表；服务运行时不要直接修改控制表。

## 读取、保存与发布

所有受支持的管理路由都要求恰好一个 `Authorization: Bearer TOKEN` Header。数据面密钥不能用于管理认证；`x-api-key`、`x-goog-api-key` 和查询参数被拒绝。响应带 `Cache-Control: no-store`。`GET /admin/config` 和实体查询返回不含凭证的视图。**认证后的 `GET /admin/config/export` 返回包含明文凭证的完整草稿**，沿用相同的管理认证和 `no-store` 策略；输出应按密钥保护。

| 请求 | 结果 |
|---|---|
| `GET /admin/config` | `200`：`{ "draft": { "revision": 1, "config": VIEW }, "published_revision": 1, "active_revision": 1, "publication": "active" }` |
| `GET /admin/config/export` | 相同外层结构，`draft.config` 为完整原始 `CONFIG` |
| `PUT /admin/config`，正文 `{ "expected_revision": 1, "config": CONFIG }` | 保存完成返回 `200`：`{ "draft_revision": 2 }`，仍在执行返回 `202`、`operation: "pending"`；不发布 |
| `POST /admin/config/publish`，正文 `{ "revision": 2 }` | 生效返回 `200`，待激活返回 `202`；发布完成响应包含 `published_revision`、`active_revision`、`publication` |

`VIEW` 将 Provider 的 `api_key`、整个 `transport.proxy_url` 和客户端 `secret` 替换为 `has_api_key`、`has_proxy_url`、`has_secret` 布尔值，不返回掩码密钥字符串。读取投影不能直接写回：`has_*` 会作为未知字段被拒绝。前端必须显式构建实体写入 DTO，或使用敏感导出进行完整配置编辑。

上表 `CONFIG` 指完整 JSON 配置对象，字段与 YAML 种子一致。草稿必须通过完整配置校验，不能保存未填完的局部配置。管理请求正文固定限制 1 MiB，与数据面限制独立。未知字段、格式或类型错误会被拒绝。每次保存都增加草稿版本，即使配置等价。`expected_revision` 必须匹配当前草稿，发布同样要求当前草稿版本；版本过期返回 `409`。

例如用 curl 和 jq 获取私有草稿，编辑配置后保留读取时的版本提交：

```sh
umask 077
export NYRO_ADMIN_TOKEN="$(cat admin.token)"
curl --fail-with-body http://127.0.0.1:19531/admin/config/export \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" > draft.json
# 执行下一条命令前，编辑 draft.json 中的 draft.config 对象。
jq '{expected_revision: .draft.revision, config: .draft.config}' draft.json > save.json
curl --fail-with-body -X PUT http://127.0.0.1:19531/admin/config \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @save.json > saved.json
jq '{revision: .draft_revision}' saved.json > publish.json
curl --fail-with-body -X POST http://127.0.0.1:19531/admin/config/publish \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @publish.json
unset NYRO_ADMIN_TOKEN
```

每一步都应检查响应后再继续。发生冲突后重新读取，并将修改与当前草稿核对。

## 实体草稿

集合路径为 `/admin/providers`、`/admin/models` 和 `/admin/api-keys`，三者使用相同契约。替换下表 `COLLECTION`，将完整 ID 编码为单个 URL 路径段，包括将 `/` 编码为 `%2F`，例如 `team/model` 变为 `team%2Fmodel`。ID 固定，不提供重命名操作。

| 请求 | 完成后的结果 |
|---|---|
| `GET /admin/COLLECTION` | `200`：`{ "draft_revision": 2, "items": [{ "id": "example", "value": VIEW }] }`，按 ID 排序 |
| `GET /admin/COLLECTION/ID` | `200`：`{ "draft_revision": 2, "item": { "id": "example", "value": VIEW } }` |
| `POST /admin/COLLECTION`，正文 `{ "expected_revision": 2, "id": "example", "value": INPUT }` | `201`：`{ "draft_revision": 3 }` |
| `PUT /admin/COLLECTION/ID`，正文 `{ "expected_revision": 2, "value": INPUT }` | `200`：`{ "draft_revision": 3 }` |
| `DELETE /admin/COLLECTION/ID`，正文 `{ "expected_revision": 2 }` | `200`：`{ "draft_revision": 3 }` |

所有编辑与完整配置 PUT 共用全局草稿版本，校验修改后的完整配置并保存，不自动发布。配置校验和版本冲突拒绝保留草稿与已发布状态；PostgreSQL 存储故障可能导致写入结果未知，见下文。实体操作复用既有认证、正文限制、服务持有队列和超时行为；写操作超过 10 秒仍未完成时返回 `202 { "operation": "pending" }`。最终草稿需通过 `/admin/config/publish` 显式发布。

`INPUT` 完整替换元数据，省略的元数据使用默认值，不是局部补丁。只有凭证在省略时保留旧值。凭证字段使用以下带标签对象：

| 凭证输入 | 语义 |
|---|---|
| 省略或 `{ "action": "keep" }` | 保留现有凭证 |
| `{ "action": "set", "value": "new-secret" }` | 替换凭证 |
| `{ "action": "clear" }` | 移除可选的 Provider 凭证 |
| `null`、直接传字符串、未知 action／字段 | 返回 `422 invalid_config` |

Provider 输入必填 `kind` 和 `base_url`；`native_chat`、`transport.http1_only` 默认 `false`，`api` 默认不显式选择。`api_key` 和 `transport.proxy_url` 各自使用凭证契约且允许清除；新 Provider 使用 keep 时维持未设置。省略 `transport` 会保留原代理 URL，但将 `http1_only` 重置为 `false`。普通查询只返回 `has_api_key` 和 `transport.has_proxy_url`，即使代理 URL 不含密码，也不会返回其地址。

API Key 输入接受 `secret`、`enabled`（默认 `true`）和 `expires_at`（Unix 秒；省略或 `null` 表示不过期）。新建必须提供 `secret: { "action": "set", "value": "..." }`；更新可省略 `secret` 保留原值，不允许清除客户端密钥。普通查询返回 `has_secret: true`。此 DTO 不接受限额字段：通过导出、编辑、完整配置 PUT 修改 `llm.subject_limits`。Model 输入为[代理指南](rust-proxy_CN.md)中的完整模型配置，省略的可选模型设置使用配置默认值。

例如，无需导出凭证即可向草稿添加一个尚未被引用的本地 Provider：

```sh
umask 077
export NYRO_ADMIN_TOKEN="$(cat admin.token)"
curl --fail-with-body http://127.0.0.1:19531/admin/providers \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" > providers.json
jq '{expected_revision: .draft_revision, id: "local-example", value: {
  kind: "openai", base_url: "http://127.0.0.1:8000/v1"
}}' providers.json > create.json
curl --fail-with-body -X POST http://127.0.0.1:19531/admin/providers \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @create.json
unset NYRO_ADMIN_TOKEN
```

继续编辑或发布前先检查返回版本。ID 已存在返回 `409 entity_exists`；版本过期返回 `409 revision_conflict`；实体不存在返回 `404 not_found`。Provider 被任意模型 backend 引用时不可删除，包括零权重 backend，返回 `409 entity_referenced`。API Key 被任意模型的 `subjects` 或 `llm.subject_limits` 引用时同样拒绝删除。不级联修改：先移除引用，或通过完整配置一次协调修改。删除最后一个模型返回 `422 invalid_config`，因为每次保存的草稿都必须有效。

## 发布与恢复

服务先检查进程设置并构建运行时候选，再提交数据库发布。提交前拒绝保留原有发布目标和活动代际；已保存的有效草稿仍可修正。数据库提交是持久化发布边界，随后才进行 Host 激活；两者**不是同一个原子事务**。

激活成功返回 `200`、`publication: "active"`。持久化提交后若激活中断，返回 `202`、`publication: "pending"`；此时即使 `active_revision` 仍旧，已发布版本也已成为重启目标。用 GET 核对状态，再重试发布当前草稿版本，或重启恢复已发布目标。当前没有后台自动重试服务。数据面 `/readyz` 仅表示 Host 能否接收新 lease，不检查数据库健康或已发布版本是否已经激活。

PostgreSQL 事务使用 `synchronous_commit = on`。COMMIT 确认因断连或超时丢失、无法确认写入结果时，API 返回 `503 storage_outcome_unknown`。写入**可能已经持久化**，该错误既不证明回滚，也不确认发布已提交。控制存储随后停用，不会自动重连或重试。恢复数据库可用性后，不带 `--config` 重启，读取草稿和已发布版本，核对预期修改后再决定是否重新提交。重启加载实际持久化的已发布快照，它可能比故障前的活动代际更新。读取及已知存储失败（包括发送 COMMIT 前的失败）返回 `503 storage_failed`。

请求正文读取最多 15 秒，超时在接收操作前返回 `408 request_timeout`。有效请求进入服务持有的队列后，HTTP 最多等待 10 秒；仍在执行的保存、实体编辑或发布返回 `202 { "operation": "pending" }`，之后可能成功、冲突或失败。它与确认持久化提交已完成的 `publication: "pending"` 不同。客户端断连或 HTTP 停止等待不取消已接收操作，应先 GET 核对草稿内容及草稿／已发布／活动版本，不能直接盲目重试写入。GET 在 10 秒内未完成则返回 `503 control_busy`。操作串行执行，最多接收 16 个，超出返回 `503`。关闭时先停止接收，等待服务持有的工作结束，再清理 Host；关闭开始时仍在排队的操作可能返回 `control_stopping`。

重复发布已激活版本，或发布配置指纹等价的草稿，不新建运行时代际。新请求使用激活后的快照，已有 lease 和 SSE 保持原代际。共享内存中的 rate、quota、主体窗口、健康及路由历史继续遵守原有身份与更新规则；配置入库不使计数器持久化，进程重启仍清零。

修改 `server.listen` 或 `limit.concurrency` 会在发布前返回 `409 restart_required`。仅重启同一数据库仍读取此前发布值；本轮没有离线发布这些修改的路径。需要新的进程配置时，应以修订后的种子另建数据库。活跃或仍保留历史的 rate/quota/window 策略修改也可能使候选构建被拒绝，具体共享资源约束见[代理指南](rust-proxy_CN.md)。

| 状态 | 错误码／含义 |
|---|---|
| `400` | `invalid_path`、`query_not_supported`，或 JSON 格式错误的 `invalid_request` |
| `401` | `authentication_failed` |
| `404` | `not_found`：集合未知或实体不存在 |
| `408` | `request_timeout`：操作接收前正文读取超时 |
| `409` | `revision_conflict`、`entity_exists`、`entity_referenced` 或 `restart_required` |
| `413` / `415` | `invalid_request`：正文过大／Content-Type 不支持 |
| `422` | `invalid_config`、`candidate_rejected`，或 JSON 结构／类型错误的 `invalid_request` |
| `500` | `control_failed` |
| `503` | `storage_failed`、`storage_outcome_unknown`、`control_busy` 或 `control_stopping` |

错误格式为 `{ "error": { "code": "..." } }`，不包含凭证或数据库细节。提交后激活出问题使用上述 `202` 待激活响应，不作为编辑拒绝返回。

## 本地回归

```sh
cargo test -p nyro-control -p nyro --offline
cargo build -p nyro --offline
python3 tests/serve_smoke.py
```

[进程回归](../../tests/serve_smoke.py)使用临时 SQLite 与回环 mock，覆盖认证、实体 CRUD、脱敏查询与显式导出、凭证轮换、草稿／发布分离、版本冲突、发布拒绝、SSE 连续性和已发布快照的重启恢复。独立的[代理重载回归](../../tests/proxy_reload_smoke.py)覆盖文件模式 SIGHUP 行为。


PostgreSQL 集成测试需显式启用。先准备可丢弃的 PostgreSQL 实例，将管理数据库 URL 保存到私有文件，连接角色需能创建和删除数据库。[存储测试](../../crates/nyro-control/tests/postgres.rs)自行创建隔离数据库，在检查成功后清理这些数据库：

```sh
export NYRO_TEST_POSTGRES_URL="$(cat /private/path/postgres-admin.url)"
cargo test -p nyro-control --test postgres -- --ignored
unset NYRO_TEST_POSTGRES_URL
```

运行 [PostgreSQL 进程回归](../../tests/serve_postgres_smoke.py)前，需另外在回环 PostgreSQL 服务上创建一个新的、空的、可丢弃数据库。将其 URL 保存到 `/private/path/test.url`，显式指定 `sslmode=disable`；本地故障代理通过该模式丢弃 COMMIT 确认。先构建根二进制，再运行驱动：

```sh
cargo build -p nyro --offline
python3 tests/serve_postgres_smoke.py --postgres-url-file /private/path/test.url
```

进程回归会初始化该数据库，并保留其内容供检查，不会清空已有数据库；每次运行需换用新空库。它覆盖共用 serve 行为，以及 PostgreSQL 所有权、启动脱敏和丢失 COMMIT 确认后的恢复。本轮真实数据库回归覆盖 PostgreSQL 16，不据此宣称兼容所有 PostgreSQL 版本。
