# 实验性 Rust SQLite 服务

[English](rust-serve.md)

源码构建的根命令 `nyro serve` 为[实验性 Rust 代理](rust-proxy_CN.md)增加本地 SQLite 配置控制面，复用相同的 LLM 运行时和配置格式。本轮仅交付 G10 的完整配置草稿与显式发布闭环。实体 CRUD、WebUI、OAuth 管理、PostgreSQL、旧数据导入、持久化用量预算和多进程部署仍待后续实现。已发布的 `nyro-server` 与桌面入口继续使用原有数据库。

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

`--database` 和 `--admin-token-file` 必填。管理令牌需要 16–1024 个可见 ASCII 字符；末尾 CR/LF 会被移除，整个文件最多 1024 字节。该令牌与数据面客户端使用的 `security.api_keys` 分开。管理监听默认 `127.0.0.1:19531`，`--admin-listen` 只接受回环 IP 地址。数据监听由已发布配置的 `server.listen` 指定（默认 `127.0.0.1:19530`），两者需使用不同地址。

`--config` 仅用于为空的专用数据库建立草稿与已发布快照，初始版本均为 `1`。已初始化数据库拒绝再次传入 `--config`，重启时省略：

```sh
cargo run -p nyro -- serve \
  --database control.sqlite \
  --admin-token-file admin.token
```

启动校验并加载持久化的**已发布**快照。未发布草稿在重启后保留，但不会自动生效。修改种子 YAML 不会重新导入。`serve` 不支持 SIGHUP 重载，Unix 的默认信号行为可能终止进程；后续更新使用管理 API。

一个 SQLite 文件只允许一个进程持有，存储层保持独占连接，并拒绝旧版、外部、不支持或损坏的数据库，没有隐式转换或导入。Unix 上新数据库文件权限为 `0600`，已有 Unix 文件若有任何组／其他用户权限位则拒绝打开，不会悄悄修改权限。快照包含明文 Provider、代理和客户端凭证，需保护目录、令牌文件、数据库、备份及导出的 JSON。管理令牌本身从文件读取，不存入配置数据库。

## 读取、保存与发布

所有受支持的管理路由都要求恰好一个 `Authorization: Bearer TOKEN` Header。数据面密钥不能用于管理认证；`x-api-key`、`x-goog-api-key` 和查询参数被拒绝。响应带 `Cache-Control: no-store`。**认证后的 `GET /admin/config` 返回包含明文凭证的完整草稿**，输出应按密钥保护。

| 请求 | 结果 |
|---|---|
| `GET /admin/config` | `200`：`{ "draft": { "revision": 1, "config": CONFIG }, "published_revision": 1, "active_revision": 1, "publication": "active" }` |
| `PUT /admin/config`，正文 `{ "expected_revision": 1, "config": CONFIG }` | 保存完成返回 `200`：`{ "draft_revision": 2 }`，仍在执行返回 `202`、`operation: "pending"`；不发布 |
| `POST /admin/config/publish`，正文 `{ "revision": 2 }` | 生效返回 `200`，待激活返回 `202`；发布完成响应包含 `published_revision`、`active_revision`、`publication` |

上表 `CONFIG` 指完整 JSON 配置对象，字段与 YAML 种子一致。草稿必须通过完整配置校验，不能保存未填完的局部配置。管理请求正文固定限制 1 MiB，与数据面限制独立。未知字段、格式或类型错误会被拒绝。每次保存都增加草稿版本，即使配置等价。`expected_revision` 必须匹配当前草稿，发布同样要求当前草稿版本；版本过期返回 `409`。

例如用 curl 和 jq 获取私有草稿，编辑配置后保留读取时的版本提交：

```sh
umask 077
export NYRO_ADMIN_TOKEN="$(cat admin.token)"
curl --fail-with-body http://127.0.0.1:19531/admin/config \
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

## 发布与恢复

服务先检查进程设置并构建运行时候选，再提交数据库发布。提交前拒绝保留原有发布目标和活动代际；已保存的有效草稿仍可修正。SQLite 提交是持久化发布边界，随后才进行 Host 激活；两者**不是同一个原子事务**。

激活成功返回 `200`、`publication: "active"`。持久化提交后若激活中断，返回 `202`、`publication: "pending"`；此时即使 `active_revision` 仍旧，已发布版本也已成为重启目标。用 GET 核对状态，再重试发布当前草稿版本，或重启恢复已发布目标。当前没有后台自动重试服务。数据面 `/readyz` 仅表示 Host 能否接收新 lease，不检查数据库健康或已发布版本是否已经激活。

请求正文读取最多 15 秒，超时在接收操作前返回 `408 request_timeout`。有效请求进入服务持有的队列后，HTTP 最多等待 10 秒；仍在执行的保存或发布返回 `202 { "operation": "pending" }`，之后可能成功、冲突或失败。它与确认持久化提交已完成的 `publication: "pending"` 不同。客户端断连或 HTTP 停止等待不取消已接收操作，应先 GET 核对草稿内容及草稿／已发布／活动版本，不能直接盲目重试写入。GET 在 10 秒内未完成则返回 `503 control_busy`。操作串行执行，最多接收 16 个，超出返回 `503`。关闭时先停止接收，等待服务持有的工作结束，再清理 Host；关闭开始时仍在排队的操作可能返回 `control_stopping`。

重复发布已激活版本，或发布配置指纹等价的草稿，不新建运行时代际。新请求使用激活后的快照，已有 lease 和 SSE 保持原代际。共享内存中的 rate、quota、主体窗口、健康及路由历史继续遵守原有身份与更新规则；配置入库不使计数器持久化，进程重启仍清零。

修改 `server.listen` 或 `limit.concurrency` 会在发布前返回 `409 restart_required`。仅重启同一数据库仍读取此前发布值；本轮没有离线发布这些修改的路径。需要新的进程配置时，应以修订后的种子另建数据库。活跃或仍保留历史的 rate/quota/window 策略修改也可能使候选构建被拒绝，具体共享资源约束见[代理指南](rust-proxy_CN.md)。

| 状态 | 错误码／含义 |
|---|---|
| `400` | `query_not_supported`，或 JSON 格式错误的 `invalid_request` |
| `401` | `authentication_failed` |
| `408` | `request_timeout`：操作接收前正文读取超时 |
| `409` | `revision_conflict` 或 `restart_required` |
| `413` / `415` | `invalid_request`：正文过大／Content-Type 不支持 |
| `422` | `invalid_config`、`candidate_rejected`，或 JSON 结构／类型错误的 `invalid_request` |
| `500` | `control_failed` |
| `503` | `storage_failed`、`control_busy` 或 `control_stopping` |

错误格式为 `{ "error": { "code": "..." } }`，不包含凭证或数据库细节。提交后激活出问题使用上述 `202` 待激活响应，不作为编辑拒绝返回。

## 本地回归

```sh
cargo test -p nyro-control -p nyro --offline
cargo build -p nyro --offline
python3 tests/serve_smoke.py
```

[进程回归](../../tests/serve_smoke.py)使用临时 SQLite 与回环 mock，覆盖认证、草稿／发布分离、版本冲突、发布拒绝、SSE 连续性和已发布快照的重启恢复。独立的[代理重载回归](../../tests/proxy_reload_smoke.py)覆盖文件模式 SIGHUP 行为。
