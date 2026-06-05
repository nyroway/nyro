<p align="center">
  <img width="120" src="docs/images/NYRO-logo.png">
</p>

<h2 align="center">Nyro AI Gateway</h2>

<p align="center">
  让你的 AI 编码工具运行在任意模型、任意提供商之上。<br>
  一个网关，兼容所有协议，无需改代码。
</p>

<p align="center">
  <a href="https://github.com/nyroway/nyro/releases/latest"><img src="https://img.shields.io/github/v/release/nyroway/nyro" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
  <a href="README.md"><img src="https://img.shields.io/badge/Language-English-2d7ff9" alt="English"></a>
</p>

---

<p align="center">
  <img src="docs/images/NYRO-ui-home-cn.png" width="800">
</p>

---

## Nyro 是什么？

Nyro 是一个本地 AI 网关，位于你的 AI 工具与模型提供商之间。它可实时转换协议格式，因此 Claude Code、Codex CLI、Gemini CLI、OpenCode，以及任何使用 OpenAI / Anthropic / Gemini SDK 的客户端，都可以在不改一行代码的前提下，路由到你指定的任意后端模型。

将客户端指向 `http://localhost:19530`，其余交给 Nyro。

```
Claude Code · Codex CLI · Gemini CLI · OpenCode
     OpenAI SDK · Anthropic SDK · Gemini SDK
              任意 HTTP API 客户端
                      ↓
              Nyro AI Gateway
            (localhost:19530)
                      ↓
   OpenAI · Anthropic · Google · DeepSeek
    MiniMax · xAI · Zhipu · Ollama · ...
```

Nyro 同时提供 **桌面应用**（macOS / Windows / Linux）和 **独立服务端二进制**，适用于无头部署与自托管场景。

---

## 为什么选择 Nyro？

**任意工具配任意模型。** Claude Code 使用 Anthropic 协议，Codex CLI 使用 OpenAI Responses API，Gemini CLI 使用 Gemini 协议。Nyro 可以在三者之间相互转换，让同一个模型同时服务所有工具。

**切换提供商不改工具配置。** 在 Nyro UI 中更换目标模型或提供商即可，工具侧配置保持不变。

**全程本地。** API Key 本地存储。请求仅在你的设备上流转，无云中转、无共享基础设施。

**一个 UI 管全部。** 在同一界面管理提供商、路由、API Key、日志与用量统计，可在桌面应用或浏览器访问。

---

## 界面截图

<table>
  <tr>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-providers-add-cn.png" height="260"><br><sub>Provider 管理</sub></td>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-routes-cn.png" height="260"><br><sub>路由配置</sub></td>
  </tr>
  <tr>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-apikeys-cn.png" height="260"><br><sub>API Key 管理</sub></td>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-connect-cn.png" height="260"><br><sub>Connect：代码与 CLI 集成</sub></td>
  </tr>
</table>

---

## 功能特性

### 协议转换

- **入口协议**：OpenAI（Chat Completions + Responses API）、Anthropic Messages、Gemini GenerateContent
- **出口协议**：可路由到任意 OpenAI 兼容、Anthropic 或 Gemini 上游
- **流式响应**：完整 SSE 透传与跨协议格式转换
- **推理内容**：支持 `<think>` 标签解析与跨协议转换
- **工具调用**：跨协议 tool call 与结果格式归一化
- **路由类型**：支持 chat 和 embedding 两种路由类型

### 路由能力

- 基于 `virtual_model` 的精确匹配路由
- 通过虚拟模型名解耦客户端请求与真实后端模型
- 多目标路由，支持加权负载均衡（weighted）和优先级失败转移（priority）两种策略
- 健康感知：连续 3 次失败标记目标不健康，30s 后自动恢复
- 按路由进行 API Key 访问控制

### 缓存能力

- **精确缓存**：完全相同的请求直接返回缓存结果
- **语义缓存**：基于 embedding 向量相似度匹配，相似请求复用缓存
- 支持按路由独立配置 TTL 和相似度阈值
- 缓存命中时支持流式回放

### 模型能力识别

- 自动检测模型能力（工具调用、推理、上下文窗口、输入输出模态、成本）
- 内置 `ai://models.dev` 数据源，支持离线能力查询
- 支持 HTTP 模型列表端点动态发现
- 路由配置界面展示能力标签辅助选择

### 安全能力

- 代理层与管理层 Bearer Token 独立控制
- 默认拒绝策略：API Key 必须显式绑定路由才可访问
- 单 Key 配额：RPM / RPD / TPM / TPD

### 管理能力

- Provider、路由、API Key 的完整 CRUD
- 请求日志记录 Provider、模型、Token、延迟等信息
- 按模型与提供商的用量统计图表
- Provider 连通性测试与实时反馈
- 配置导入 / 导出

### Connect — 集成

**代码集成**：选择路由后一键复制可直接使用的示例：

| 协议 | 语言 |
|---------|---------|
| OpenAI | Python · TypeScript · cURL |
| Anthropic | Python · TypeScript · cURL |
| Gemini | Python · TypeScript · cURL |

**CLI 集成**：一键同步 AI 编码工具配置：

| 工具 | 协议 |
|------|---------|
| Claude Code | Anthropic |
| Codex CLI | OpenAI Responses API |
| Gemini CLI | Gemini |
| OpenCode | OpenAI |

Nyro 会自动检测已安装工具，为所选路由生成正确配置并一键写入，同时自动备份原始配置。

### 部署形态

**桌面应用**

| 平台 | 架构 |
|---|---|
| macOS | Apple Silicon (aarch64) · Intel (x64) |
| Windows | x64 · ARM64 |
| Linux | x86_64 · aarch64 |

**服务端二进制** — 完整模式（DB + Admin API + 内嵌 WebUI）和 Standalone 模式（纯 YAML 配置，无 DB）

| 平台 | 架构 | 访问 |
|---|---|---|
| macOS | x86_64 · aarch64 | Proxy `:19530` · WebUI `http://localhost:19531` |
| Linux | x86_64 · aarch64 | Proxy `:19530` · WebUI `http://localhost:19531` |
| Windows | x64 · ARM64 | Proxy `:19530` · WebUI `http://localhost:19531` |

---

## 安装

### 桌面应用

**Homebrew（macOS / Linux）**

```bash
brew tap nyroway/nyro
brew install --cask nyro
```

**Shell 脚本**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/nyroway/nyro/master/scripts/install/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/nyroway/nyro/master/scripts/install/install.ps1 | iex
```

**手动下载**

从 [GitHub Releases](https://github.com/nyroway/nyro/releases/latest) 下载你平台对应的最新安装包。

> **macOS**：手动安装后请运行 `sudo xattr -rd com.apple.quarantine /Applications/Nyro.app`，或使用安装脚本自动处理。
>
> **Windows**：若 SmartScreen 提示 "Unknown publisher"，点击 **More info → Run anyway**。

### 服务端二进制

```bash
# 下载
curl -LO https://github.com/nyroway/nyro/releases/latest/download/nyro-server-linux-x86_64
chmod +x nyro-server-linux-x86_64

# 启动（仅 localhost，无需鉴权）
./nyro-server-linux-x86_64

# 启动（暴露到网络，必须鉴权）
./nyro-server-linux-x86_64 \
  --proxy-host 0.0.0.0 \
  --admin-host 0.0.0.0 \
  --admin-token YOUR_ADMIN_TOKEN

# Standalone 模式（YAML 配置，无 DB/Admin/WebUI）
./nyro-server-linux-x86_64 --config config.yaml
```

可用服务端二进制：`linux-x86_64`、`linux-aarch64`、`macos-x86_64`、`macos-aarch64`、`windows-x86_64.exe`、`windows-arm64.exe`

打开 `http://localhost:19531` 进入管理界面。详细配置参见 [Server 文档](docs/server/README.md) 和 [Standalone 文档](docs/standalone/README.md)。

### SQL 存储后端

默认使用 `--data-dir` 下的本地 SQLite。如需切换到 PostgreSQL 或 MySQL：

```bash
# PostgreSQL
./nyro-server-linux-x86_64 \
  --storage-backend postgres \
  --postgres-dsn "postgres://user:pass@host:5432/db"

# MySQL
./nyro-server-linux-x86_64 \
  --storage-backend mysql \
  --mysql-dsn "mysql://user:pass@host:3306/db"
```

或通过环境变量：

```bash
export NYRO_POSTGRES_DSN="postgres://user:pass@host:5432/db"
./nyro-server-linux-x86_64 --storage-backend postgres

# 或
export NYRO_MYSQL_DSN="mysql://user:pass@host:3306/db"
./nyro-server-linux-x86_64 --storage-backend mysql
```

### 多副本生产部署

在负载均衡器后运行多个 `nyro-server` 副本时，所有副本**必须**共享同一数据库和相同的 admin token：

| 要求 | 说明 |
|---|---|
| 共享数据库 | 使用 `--storage-backend postgres` 或 `mysql`，所有副本指向同一 DSN。SQLite 不支持共享。 |
| 统一 admin token | 每个副本设置相同的 `--admin-token` / `NYRO_ADMIN_TOKEN`。 |
| 配置同步 | 副本通过 `--config-poll-interval`（默认 3 秒）轮询共享 DB 的配置变更，路由/模型/Provider 的修改在一个轮询周期内传播。 |
| OAuth 交互式流程 | OAuth 授权回调必须路由到**发起该 session 的同一副本**。请在负载均衡器的管理端口（`19531`）上配置 sticky session（会话亲和）。 |

**健康探针：**

| 端点 | 端口 | 用途 |
|---|---|---|
| `GET /healthz` | proxy + admin | Liveness — 固定返回 `200` |
| `GET /readyz` | proxy + admin | Readiness — DB 可达返回 `200`，否则返回 `503` |

**关键环境变量：**

```bash
NYRO_ADMIN_TOKEN=<secret>          # 当 admin host 不是 loopback 时必须设置
NYRO_STORAGE_BACKEND=postgres      # postgres | mysql | sqlite（默认）
NYRO_POSTGRES_DSN=postgres://...   # backend=postgres 时必须设置
NYRO_MYSQL_DSN=mysql://...         # backend=mysql 时必须设置
NYRO_CONFIG_POLL_INTERVAL=3        # 配置 epoch 轮询间隔（秒，默认 3，0=禁用）
NYRO_WEBUI_DIR=/path/to/dist       # 从外部目录提供 WebUI（不填则使用嵌入资源）
```

### Docker

预构建的服务端镜像在独立仓库 [nyroway/docker-nyro](https://github.com/nyroway/docker-nyro) 中维护，发布到 Docker Hub 的 [nyroway/nyro](https://hub.docker.com/r/nyroway/nyro)。

快速开始：

```bash
docker run --rm \
  -e NYRO_ADMIN_TOKEN=change-me \
  -p 19530:19530 \
  -p 19531:19531 \
  -v nyro-data:/var/lib/nyro \
  nyroway/nyro:latest
```

打开 `http://127.0.0.1:19531` 进入管理界面。管理 API 请求时，请使用同一个 `NYRO_ADMIN_TOKEN` 作为 Bearer Token。

如需使用 Postgres / MySQL 后端或 `docker compose` 部署，请参考 [docker-nyro README](https://github.com/nyroway/docker-nyro)。


---

## 快速开始

**1. 添加 Provider**

进入 **Providers → New**，填写 Provider 的 Base URL 和 API Key。Nyro 会根据 URL 自动识别协议。

**2. 创建路由**

进入 **Routes → New**，设置虚拟模型名（例如 `gpt-4o`），选择 Provider 与目标模型。需要时可开启访问控制。

**3. 将客户端指向 Nyro**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:19530/v1",
    api_key="your-proxy-key"  # 若关闭访问控制可用 "no-auth"
)

response = client.chat.completions.create(
    model="gpt-4o",  # 对应你配置的虚拟模型名
    messages=[{"role": "user", "content": "Hello"}]
)
```

**4. 同步你的 AI 工具（可选）**

进入 **Connect**，选择路由后点击 Claude Code、Codex、Gemini CLI 或 OpenCode 旁边的 **Sync**。Nyro 会自动写入正确配置。

---

## 开源协议

```
Copyright 2026 The Nyro Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

完整协议见 [LICENSE](LICENSE)。
