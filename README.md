<p align="center">
  <img width="120" src="docs/images/NYRO-logo.png">
</p>

<h2 align="center">Nyro AI Gateway</h2>

<p align="center">
  Run your AI coding tools on any model, from any provider.<br>
  One gateway. All protocols. No code changes.
</p>

<p align="center">
  <a href="https://github.com/nyroway/nyro/releases/latest"><img src="https://img.shields.io/github/v/release/nyroway/nyro" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
  <a href="README_CN.md"><img src="https://img.shields.io/badge/文档-中文-8A2BE2" alt="中文"></a>
</p>

---

<p align="center">
  <img src="docs/images/NYRO-ui-home-en.png" width="800">
</p>

---

## What is Nyro?

Nyro is a local AI gateway that sits between your AI tools and model providers. It translates protocol formats on the fly — so Claude Code, Codex CLI, Gemini CLI, OpenCode, and any client using OpenAI / Anthropic / Gemini SDKs can all route through any backend model you choose, without changing a single line of code.

Point your clients at `http://localhost:19530`. Nyro handles the rest.

```
Claude Code · Codex CLI · Gemini CLI · OpenCode
     OpenAI SDK · Anthropic SDK · Gemini SDK
              Any HTTP API Client
                      ↓
              Nyro AI Gateway
            (localhost:19530)
                      ↓
    OpenAI · Anthropic · Google · DeepSeek
    MiniMax · xAI · Zhipu · Ollama · ...
```

Nyro ships as a **desktop app** (macOS / Windows / Linux) and a **standalone server binary** for headless and self-hosted deployments.

---

## Why Nyro?

**Use any model with any tool.** Claude Code expects Anthropic protocol. Codex CLI uses OpenAI Responses API. Gemini CLI speaks Gemini. Nyro translates between all three so one model can serve all your tools simultaneously.

**Switch providers without touching your tools.** Change the target model or provider from Nyro's UI. Your tools never need reconfiguring.

**Keep everything local.** API keys are stored locally. Requests stay on your machine. No cloud relay, no shared infrastructure.

**One UI for everything.** Manage providers, routes, API keys, logs, and usage stats from a single interface — desktop app or browser.

---

## Screenshots

<table>
  <tr>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-providers-add-en.png" height="260"><br><sub>Provider Management</sub></td>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-routes-en.png" height="260"><br><sub>Route Configuration</sub></td>
  </tr>
  <tr>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-apikeys-en.png" height="260"><br><sub>API Key Management</sub></td>
    <td align="center" width="50%"><img src="docs/images/NYRO-ui-connect-en.png" height="260"><br><sub>Connect — Code & CLI Integration</sub></td>
  </tr>
</table>

---

## Features

### Protocol Translation

- **Ingress**: OpenAI (Chat Completions + Responses API), Anthropic Messages, Gemini GenerateContent
- **Egress**: route to any OpenAI-compatible, Anthropic, or Gemini upstream
- **Streaming**: full SSE passthrough and cross-protocol format conversion
- **Reasoning**: `<think>` tag parsing and conversion across protocol boundaries
- **Tool calls**: cross-protocol tool call and result format normalization
- **Route types**: chat and embedding routes with type-aware handling

### Routing

- Exact match routing on `virtual_model`
- Virtual model names decouple client requests from actual backend models
- Multi-target routing with strategy selection: weighted load balancing or priority-based failover
- Health-aware failover: 3 consecutive failures mark a target unhealthy, auto-recovery after 30s
- Per-route access control with API key authorization

### Caching

- **Exact cache**: identical requests return cached responses instantly
- **Semantic cache**: embedding-based similarity matching reuses results for similar queries
- Per-route TTL and threshold overrides
- Stream replay for cached streaming responses

### Model Capabilities

- Auto-detect model capabilities (tool call, reasoning, context window, modalities, costs)
- `ai://models.dev` built-in data source for offline capability lookup
- HTTP model list endpoints for dynamic model discovery
- Capability tags shown in route configuration UI

### Security

- Independent proxy and admin bearer token controls
- Default-deny route authorization — keys must be explicitly bound to routes
- Per-key quotas: RPM / RPD / TPM / TPD

### Management

- Full CRUD for providers, routes, and API keys
- Request logs with provider, model, token, and latency detail
- Usage charts by model and provider
- Provider connectivity testing with live feedback
- Configuration import / export

### Connect — Integration

**Code Integration** — select a route and copy ready-to-use examples for:

| Protocol | Languages |
|---------|---------|
| OpenAI | Python · TypeScript · cURL |
| Anthropic | Python · TypeScript · cURL |
| Gemini | Python · TypeScript · cURL |

**CLI Integration** — one-click config sync for AI coding tools:

| Tool | Protocol |
|------|---------|
| Claude Code | Anthropic |
| Codex CLI | OpenAI Responses API |
| Gemini CLI | Gemini |
| OpenCode | OpenAI |

Nyro detects installed tools, generates the correct configuration for the selected route, and writes it with one click. Original configs are backed up automatically.

### Deployment

**Desktop App**

| Platform | Architecture |
|---|---|
| macOS | Apple Silicon (aarch64) · Intel (x64) |
| Windows | x64 · ARM64 |
| Linux | x86\_64 · aarch64 |

**Server Binary** — full mode (DB + Admin API + embedded WebUI) and standalone mode (YAML config only, no DB)

| Platform | Architecture | Access |
|---|---|---|
| macOS | x86\_64 · aarch64 | Proxy `:19530` · WebUI `http://localhost:19531` |
| Linux | x86\_64 · aarch64 | Proxy `:19530` · WebUI `http://localhost:19531` |
| Windows | x64 · ARM64 | Proxy `:19530` · WebUI `http://localhost:19531` |

---

## Installation

### Desktop App

**Homebrew (macOS / Linux)**

```bash
brew tap nyroway/nyro
brew install --cask nyro
```

**Shell Script**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/nyroway/nyro/master/scripts/install/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/nyroway/nyro/master/scripts/install/install.ps1 | iex
```

**Manual Download**

Download the latest installer for your platform from [GitHub Releases](https://github.com/nyroway/nyro/releases/latest).

> **macOS**: After manual install run `sudo xattr -rd com.apple.quarantine /Applications/Nyro.app`, or use the install script which handles this automatically.
>
> **Windows**: SmartScreen may show "Unknown publisher" — click **More info → Run anyway**.

### Server Binary

```bash
# Download
curl -LO https://github.com/nyroway/nyro/releases/latest/download/nyro-server-linux-x86_64
chmod +x nyro-server-linux-x86_64

# Start (localhost only, no auth required)
./nyro-server-linux-x86_64

# Start (network-exposed, auth required)
./nyro-server-linux-x86_64 \
  --proxy-host 0.0.0.0 \
  --admin-host 0.0.0.0 \
  --admin-token YOUR_ADMIN_TOKEN

# Standalone mode (YAML config, no DB/Admin/WebUI)
./nyro-server-linux-x86_64 --config config.yaml
```

Available server binaries: `linux-x86_64`, `linux-aarch64`, `macos-x86_64`, `macos-aarch64`, `windows-x86_64.exe`, `windows-arm64.exe`

Open `http://localhost:19531` for the management UI. See [Server docs](docs/server/README.md) and [Standalone docs](docs/standalone/README.md) for full configuration reference.

### SQL Storage Backends

Default behavior: local SQLite under `--data-dir`. To use PostgreSQL or MySQL:

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

Or via environment variable:

```bash
export NYRO_POSTGRES_DSN="postgres://user:pass@host:5432/db"
./nyro-server-linux-x86_64 --storage-backend postgres

# or
export NYRO_MYSQL_DSN="mysql://user:pass@host:3306/db"
./nyro-server-linux-x86_64 --storage-backend mysql
```

### Multi-replica Production Deployment

When running multiple `nyro-server` replicas behind a load balancer, all replicas **must** share the same database and the same admin token:

| Requirement | Detail |
|---|---|
| Shared DB | Use `--storage-backend postgres` or `mysql` pointing to the same DSN across all replicas. SQLite cannot be shared. |
| Unified admin token | Set the same `--admin-token` / `NYRO_ADMIN_TOKEN` on every replica. |
| Config sync | Replicas poll the shared DB for config changes every `--config-poll-interval` seconds (default: 3 s). Route/model/provider changes propagate within one poll interval. |
| OAuth interactive flows | The OAuth authorization callback must reach the **same replica** that initiated the session. Configure sticky sessions (session affinity) on your load balancer for the admin port (`19531`). |

**Health probes:**

| Endpoint | Port | Use |
|---|---|---|
| `GET /healthz` | proxy + admin | Liveness — always `200` |
| `GET /readyz` | proxy + admin | Readiness — `200` if DB reachable, `503` otherwise |

**Key environment variables:**

```bash
NYRO_ADMIN_TOKEN=<secret>          # Required when admin host is not loopback
NYRO_STORAGE_BACKEND=postgres      # postgres | mysql | sqlite (default)
NYRO_POSTGRES_DSN=postgres://...   # Required when backend=postgres
NYRO_MYSQL_DSN=mysql://...         # Required when backend=mysql
NYRO_CONFIG_POLL_INTERVAL=3        # Seconds between config epoch polls (default: 3, 0=disabled)
NYRO_WEBUI_DIR=/path/to/dist       # Serve WebUI from external directory instead of embedded assets
```

### Docker

Pre-built server images are distributed via the separate [nyroway/docker-nyro](https://github.com/nyroway/docker-nyro) repository and published to Docker Hub as [nyroway/nyro](https://hub.docker.com/r/nyroway/nyro).

Quick start:

```bash
docker run --rm \
  -e NYRO_ADMIN_TOKEN=change-me \
  -p 19530:19530 \
  -p 19531:19531 \
  -v nyro-data:/var/lib/nyro \
  nyroway/nyro:latest
```

Open `http://127.0.0.1:19531` for the management UI. Use the same `NYRO_ADMIN_TOKEN` value as the Bearer token for admin API requests.

For Postgres- or MySQL-backed deployments and `docker compose` usage, see the [docker-nyro README](https://github.com/nyroway/docker-nyro).

---

## Quick Start

**1. Add a Provider**

Go to **Providers → New**. Enter your provider's Base URL and API key. Nyro auto-detects the protocol from the URL.

**2. Create a Route**

Go to **Routes → New**. Set a virtual model name (e.g. `gpt-4o`), select your provider and target model. Enable access control if needed.

**3. Point your client at Nyro**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:19530/v1",
    api_key="your-proxy-key"  # or "no-auth" if access control is off
)

response = client.chat.completions.create(
    model="gpt-4o",  # matches your virtual model name
    messages=[{"role": "user", "content": "Hello"}]
)
```

**4. Sync your AI tools (optional)**

Go to **Connect**, select a route, and click **Sync** next to Claude Code, Codex, Gemini CLI, or OpenCode. Nyro writes the correct config automatically.

---

## License

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

See the full license text in [LICENSE](LICENSE).
