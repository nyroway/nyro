from __future__ import annotations

import json
import os
import secrets
import socket
import subprocess
import tempfile
import textwrap
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[3]


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def make_isolated_schema(prefix: str = "nyro_storage_e2e", *, max_len: int = 63) -> str:
    suffix = f"{int(time.time())}_{secrets.token_hex(3)}"
    keep = max(1, max_len - len(suffix) - 1)
    return f"{prefix[:keep]}_{suffix}"


def load_pg_url() -> str | None:
    for key in ("DB_URL", "DATABASE_URL"):
        value = os.environ.get(key)
        if value:
            return value

    env_file = REPO_ROOT / ".env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            stripped = line.strip()
            if not stripped or stripped.startswith("#") or "=" not in stripped:
                continue
            key, value = stripped.split("=", 1)
            if key.strip() in ("DB_URL", "DATABASE_URL"):
                return value.strip().strip("'\"")
    return None


class _MockHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args: object) -> None:
        return

    def _write_json(self, status: int, payload: dict[str, object]) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.send_header("connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.wfile.flush()

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("content-length", "0"))
        raw = self.rfile.read(length) if length else b"{}"
        body = json.loads(raw) if raw else {}
        if self.path.split("?")[0] != "/v1/chat/completions":
            self._write_json(404, {"error": "not found"})
            return
        model = str(body.get("model", "mock"))
        self._write_json(
            200,
            {
                "id": "chatcmpl-storage-e2e",
                "object": "chat.completion",
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "ok"},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
            },
        )


def start_mock(port: int) -> ThreadingHTTPServer:
    server = ThreadingHTTPServer(("127.0.0.1", port), _MockHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def build_harness(work_dir: Path) -> None:
    cargo_toml = textwrap.dedent(
        f"""
        [package]
        name = "nyro-storage-e2e-harness"
        version = "0.1.0"
        edition = "2024"

        [dependencies]
        anyhow = "1"
        nyro-core = {{ path = "{REPO_ROOT / 'crates/nyro-core'}" }}
        reqwest = {{ version = "0.12", features = ["json"] }}
        serde_json = "1"
        sqlx = {{ version = "0.8", features = ["runtime-tokio", "postgres"] }}
        tokio = {{ version = "1", features = ["macros", "rt-multi-thread", "time"] }}
        """
    ).strip() + "\n"

    main_rs = textwrap.dedent(
        r"""
        use std::env;
        use std::path::PathBuf;
        use std::time::Duration;

        use anyhow::{Context, ensure};
        use nyro_core::config::{GatewayConfig, SqlStorageConfig, StorageBackendKind};
        use nyro_core::db::models::{CreateApiKey, CreateModel, CreateProvider, LogQuery};
        use nyro_core::{logging, Gateway};
        use reqwest::StatusCode;
        use sqlx::postgres::PgPoolOptions;

        #[tokio::main]
        async fn main() -> anyhow::Result<()> {
            let backend = env::var("NYRO_STORAGE_BACKEND").context("NYRO_STORAGE_BACKEND")?;
            let upstream = env::var("NYRO_STORAGE_UPSTREAM").context("NYRO_STORAGE_UPSTREAM")?;
            let data_dir = PathBuf::from(env::var("NYRO_STORAGE_DATA_DIR").context("NYRO_STORAGE_DATA_DIR")?);
            let proxy_port: u16 = env::var("NYRO_STORAGE_PROXY_PORT")
                .context("NYRO_STORAGE_PROXY_PORT")?
                .parse()
                .context("invalid NYRO_STORAGE_PROXY_PORT")?;

            let mut config = GatewayConfig {
                proxy_host: "127.0.0.1".to_string(),
                proxy_port,
                data_dir,
                ..Default::default()
            };

            match backend.as_str() {
                "sqlite" => {
                    config.storage.backend = StorageBackendKind::Sqlite;
                    config.storage.sqlite.migrate_on_start = true;
                }
                "postgres" => {
                    let pg_url = env::var("NYRO_STORAGE_PG_URL").context("NYRO_STORAGE_PG_URL")?;
                    let schema = env::var("NYRO_STORAGE_PG_SCHEMA").context("NYRO_STORAGE_PG_SCHEMA")?;
                    ensure!(
                        schema.chars().all(|c| c.is_ascii_alphanumeric() || c == '_'),
                        "invalid schema: {schema}"
                    );
                    let pool = PgPoolOptions::new().max_connections(1).connect(&pg_url).await?;
                    sqlx::query(&format!("CREATE SCHEMA IF NOT EXISTS {schema}")).execute(&pool).await?;
                    pool.close().await;

                    let url_with_schema = if pg_url.contains('?') {
                        format!("{pg_url}&options=-csearch_path%3D{schema}")
                    } else {
                        format!("{pg_url}?options=-csearch_path%3D{schema}")
                    };
                    config.storage.backend = StorageBackendKind::Postgres;
                    config.storage.postgres = SqlStorageConfig {
                        url: Some(url_with_schema),
                        ..Default::default()
                    };
                }
                other => anyhow::bail!("unknown backend: {other}"),
            }

            let (gw, log_rx) = Gateway::new(config).await?;
            let storage = gw.storage.clone();
            tokio::spawn(async move { logging::run_collector(log_rx, storage).await });

            let admin = gw.admin();
            let provider = admin.create_provider(CreateProvider {
                name: format!("{backend}-e2e-provider"),
                vendor: None,
                protocol: "openai".to_string(),
                base_url: format!("{upstream}/v1"),
                preset_key: None,
                channel: None,
                models_source: None,
                static_models: None,
                api_key: "dummy".to_string(),
                auth_mode: "apikey".to_string(),
                use_proxy: false,
            }).await?;

            let route = admin.create_model(CreateModel {
                name: format!("{backend}-e2e-model"),
                virtual_model: format!("{backend}-model"),
                balance: None,
                target_provider: provider.id.clone(),
                target_model: "gpt-4o-mini".to_string(),
                targets: vec![],
                access_control: Some(true),
            }).await?;

            let api_key = admin.create_api_key(CreateApiKey {
                name: format!("{backend}-e2e-key"),
                rpm: Some(10), rpd: None, tpm: None, tpd: None,
                expires_at: None,
                model_ids: vec![route.id.clone()],
            }).await?;

            ensure!(admin.list_providers().await?.len() == 1, "provider count");
            ensure!(admin.list_models().await?.len() == 1, "model count");
            ensure!(admin.list_api_keys().await?.len() == 1, "api key count");
            let export = admin.export_config().await?;
            ensure!(export.providers.len() == 1, "export providers");
            ensure!(export.models.len() == 1, "export models");

            tokio::spawn(async move { let _ = gw.start_proxy().await; });
            tokio::time::sleep(Duration::from_millis(250)).await;

            let client = reqwest::Client::new();
            let url = format!("http://127.0.0.1:{proxy_port}/v1/chat/completions");
            let payload = serde_json::json!({"model": format!("{backend}-model"), "messages": [{"role":"user","content":"hi"}]});

            let no_key = client.post(&url).json(&payload).send().await?;
            ensure!(no_key.status() == StatusCode::UNAUTHORIZED, "missing key should 401");

            let ok = client.post(&url).bearer_auth(&api_key.key).json(&payload).send().await?;
            ensure!(ok.status() == StatusCode::OK, "valid key should 200");
            let body: serde_json::Value = ok.json().await?;
            ensure!(body["choices"][0]["message"]["content"].as_str() == Some("ok"), "content mismatch");

            let mut logs_total = 0i64;
            let mut stats_requests = 0i64;
            for _ in 0..20 {
                let logs = admin.query_logs(LogQuery { limit: Some(10), offset: Some(0), ..Default::default() }).await?;
                let stats = admin.get_stats_overview(None).await?;
                logs_total = logs.total;
                stats_requests = stats.total_requests;
                if logs_total >= 1 && stats_requests >= 1 {
                    println!("backend={backend}");
                    println!("logs_total={logs_total}");
                    println!("stats_total_requests={stats_requests}");
                    println!("proxy_status_ok=200");
                    println!("proxy_status_no_key=401");
                    return Ok(());
                }
                tokio::time::sleep(Duration::from_millis(200)).await;
            }
            anyhow::bail!("log/stat timeout: logs={logs_total} requests={stats_requests}");
        }
        """
    ).strip() + "\n"

    (work_dir / "Cargo.toml").write_text(cargo_toml)
    src_dir = work_dir / "src"
    src_dir.mkdir(parents=True, exist_ok=True)
    (src_dir / "main.rs").write_text(main_rs)


def run_harness(
    backend: str,
    *,
    upstream_port: int,
    work_dir: Path,
    pg_url: str | None = None,
) -> str:
    env = os.environ.copy()
    env["NYRO_STORAGE_BACKEND"] = backend
    env["NYRO_STORAGE_UPSTREAM"] = f"http://127.0.0.1:{upstream_port}"
    env["NYRO_STORAGE_PROXY_PORT"] = str(find_free_port())
    env["NYRO_STORAGE_DATA_DIR"] = str(work_dir / f"{backend}-data")

    if backend == "postgres":
        if not pg_url:
            raise RuntimeError("postgres backend requires DB_URL or DATABASE_URL")
        env["NYRO_STORAGE_PG_URL"] = pg_url
        env["NYRO_STORAGE_PG_SCHEMA"] = make_isolated_schema()

    proc = subprocess.run(
        ["cargo", "run", "--quiet", "--manifest-path", str(work_dir / "Cargo.toml")],
        env=env,
        cwd=str(REPO_ROOT),
        text=True,
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"backend={backend} harness failed\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
        )
    return proc.stdout


@pytest.fixture(scope="module")
def storage_runtime() -> dict[str, object]:
    upstream_port = find_free_port()
    mock_server = start_mock(upstream_port)

    try:
        with tempfile.TemporaryDirectory(prefix="nyro-storage-e2e-") as tmp:
            tmpdir = Path(tmp)
            build_harness(tmpdir)
            yield {
                "upstream_port": upstream_port,
                "work_dir": tmpdir,
                "pg_url": load_pg_url(),
                "run_harness": run_harness,
            }
    finally:
        mock_server.shutdown()
        mock_server.server_close()
