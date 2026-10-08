# Rust 控制面

[English](rust-serve.md)

根 `nyro` 二进制支持以下部署：

| 命令 | 配置来源 | 角色 |
|---|---|---|
| `nyro proxy --config resources.yaml` | 启动时读取文件 | 独立数据面 |
| `nyro serve --database control.db --admin-token-file admin.token` | 独立 SQLite | 独立控制面 |
| `nyro serve --database control.db --admin-token-file admin.token --enable-proxy` | SQLite + memory 同步 | 控制面和数据面 |
| `nyro proxy --server https://control.example.com --sync-token-file sync.token --node-id edge-1` | HTTP 完整快照 | 远程数据面 |

`serve` 默认不监听代理端口，需显式提供 `--enable-proxy` / `NYRO_ENABLE_PROXY=true`。不提供 `--disable-proxy`。新数据库以合法空资源启动，无需种子文件或发布操作。旧草稿／快照数据库和旧资源文件格式不兼容，会被拒绝；应使用新的独立数据库。已发布桌面／服务端数据库不受影响，也不自动迁移。

## 启动

管理 token 文件应包含 16–1024 个可见 ASCII 字符。新 SQLite 文件的 Unix 权限为 `0600`；已有文件向组或其他用户开放权限时拒绝启动。数据库嵌套 JSON 保存明文凭证，请保护目录及备份。

```sh
nyro serve --database ./control.db --admin-token-file ./admin.token --enable-proxy
```

PostgreSQL 使用 `--postgres-url-file /run/secrets/control-postgres-url` 替代 `--database`，文件内保存独立 UTF-8 数据库的连接 URL。默认验证 TLS 证书和主机名，仅回环测试可禁用 TLS。一个控制进程独占连接，无连接池或自动重连。见[数据库结构](../database/schema.md)。

启动环境变量为 `NYRO_DATABASE`、`NYRO_POSTGRES_URL_FILE`、`NYRO_ADMIN_TOKEN_FILE`、`NYRO_ADMIN_LISTEN`、`NYRO_ENABLE_PROXY`、`NYRO_SYNC_LISTEN`、`NYRO_SYNC_TOKEN_FILE`、`NYRO_LISTEN`、`NYRO_CONCURRENCY`。

## 资源 API

管理监听默认 `127.0.0.1:19531`，必须使用回环地址。使用 `Authorization: Bearer <admin-token>`；管理 token、同步 token 和 Consumer 凭证相互独立。

| 方法和路径 | 操作 |
|---|---|
| `GET /v1/resources` | 读取全部资源，凭证脱敏 |
| `GET /v1/resources/{kind}` | 列出一个集合 |
| `POST /v1/resources/{kind}` | 创建资源 |
| `GET /v1/resources/{kind}/{id}` | 读取资源 |
| `PUT /v1/resources/{kind}/{id}` | 替换或改名 |
| `DELETE /v1/resources/{kind}/{id}` | 删除资源 |
| `GET /v1/nodes/{node-id}` | 查询发送、生效或拒绝反馈 |

`kind` 为 `upstreams`、`models`、`mcps` 或 `consumers`。POST/PUT 使用 `Content-Type: application/json`，请求体是单个完整资源对象，字段与 [YAML](rust-proxy_CN.md) 一致。PUT 提交不同 `id` 会改名并更新引用和授权，内部 UID 保持不变。被引用的 upstream 不允许删除；删除模型/MCP 会清理相应授权。MCP 改名会改变 `/mcp/{id}` 入口。

GET 不返回 `secret`，改为 `has_secret: true`。PUT 省略 secret 时保留匹配的原 target／credential 密钥；提交 secret 时轮换。新凭证必须提供 secret。删除凭证或设置 target `auth: null` 可移除认证。被替换资源中省略的集合按默认值处理。环境替换仅用于启动 YAML；API 和数据库保存已解析值。

保存时校验完整候选资源，提交数据库事务，再分发完整快照。没有草稿、全局修订编辑流程或 publish 接口；已接受的写入不会因调用方断连而取消。存储失败可能禁用后续写入直到重启；提交结果不确定时，应重新打开并检查持久化状态，再决定是否重试。

保存成功不表示所有数据面已同时生效。可查询节点反馈及数据面 `/readyz`。候选配置被拒绝时，旧代际和在途请求继续运行。

## HTTP 与 memory 同步

内嵌数据面通过 memory 传递类型化快照，共用远程 HTTP 的应用和回执循环，默认不暴露网络同步入口。

远程节点需要独立回环监听和不同于管理 token 的同步 token：

```sh
nyro serve --database ./control.db --admin-token-file ./admin.token \
  --sync-listen 127.0.0.1:19532 --sync-token-file ./sync.token
```

通过 **HTTPS 反向代理仅暴露 19532 端口**，代理超时需大于 35 秒。内置监听不终止 TLS，拒绝绑定非回环地址。19531 保持本地使用；同步 token 不授予管理权限。禁止缓存同步响应或记录其请求／响应凭证内容。

远程节点 POST `/v1/config/sync`。控制进程每次启动使用新的 epoch 和修订序列，通过内容指纹跳过重复构建。长轮询返回完整快照，区分收到和激活；断线后重新取全量，临时失败退避，永久拒绝的版本不重复构建。当前为单控制面所有者，不增加服务发现、分布式计数、gRPC、增量同步或磁盘离线缓存。

出口代理 URL 可能包含账号密码，因此管理查询同样隐藏 `egress.proxy_url`，返回 `has_proxy_url: true`。PUT 提供 `egress` 对象时，省略 `proxy_url` 保留旧值，指定 URL 替换，指定 `proxy_url: null` 清除。

## 本地验证

运行 `cargo test -p nyro-control -p nyro-sync -p nyro`，然后 `cargo build -p nyro` 和 `python3 tests/rust_resource_smoke.py`。进程测试使用本地模拟上游，覆盖文件、远程 HTTP 和内嵌 memory 部署。

PostgreSQL 集成测试需要通过 `NYRO_TEST_CONTROL_POSTGRES_URL` 明确指定一个空的临时数据库，再运行 `cargo test -p nyro-control --test resources postgres_resources -- --ignored`。测试会在该库保留测试资源；没有明确的数据库设置时不会执行。
