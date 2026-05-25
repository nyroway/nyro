use tokio::sync::mpsc;

use crate::protocol::ir::Usage;
use crate::storage::DynStorage;

const DEFAULT_RETENTION_DAYS: i64 = 7;
const DEFAULT_RECORD_PAYLOADS: bool = true;
pub const LOG_RECORD_PAYLOADS_KEY: &str = "log_record_payloads";
pub const LOG_RETENTION_DAYS_KEY: &str = "log_retention_days";

#[derive(Debug, Clone)]
pub struct LogEntry {
    // === 标识 ===
    pub api_key_id: Option<String>,
    pub api_key_name: Option<String>,
    /// Unix 毫秒时间戳
    pub created_at: i64,

    // === 模型 ===
    pub client_protocol: String,
    pub upstream_protocol: String,
    pub provider_id: String,
    pub provider_name: String,
    pub model_id: Option<String>,
    pub model_name: Option<String>,
    pub upstream_url: Option<String>,
    pub client_model: String,
    pub upstream_model: String,

    // === HTTP 元 ===
    pub method: Option<String>,
    pub path: Option<String>,

    // === 客户端 wire ===
    pub client_request_headers: Option<String>,
    pub client_request_body: Option<String>,
    pub client_response_headers: Option<String>,
    pub client_response_body: Option<String>,

    // === 上游 wire ===
    pub upstream_request_headers: Option<String>,
    pub upstream_request_body: Option<String>,
    pub upstream_response_headers: Option<String>,
    pub upstream_response_body: Option<String>,

    // === 状态 ===
    pub upstream_status_code: Option<i32>,
    pub client_status_code: i32,

    // === 性能 ===
    pub latency_total_ms: i64,
    pub latency_upstream_ms: Option<i64>,
    pub usage: Usage,

    // === 流式 ===
    /// 客户端请求中声明的 stream 标志（stream: true），比 stream_chunks_count > 0 更严谨
    pub is_stream: bool,
    /// 收到的上游 SSE chunk 数；> 0 表示流式请求，非流式为 0
    pub stream_chunks_count: i32,
    /// TTFB（ms）；非流式为 None
    pub stream_first_chunk_ms: Option<i64>,
}

impl LogEntry {
    pub fn input_tokens(&self) -> i32 {
        self.usage.prompt_tokens as i32
    }

    pub fn output_tokens(&self) -> i32 {
        self.usage.completion_tokens as i32
    }

    pub fn cache_read_tokens(&self) -> i32 {
        self.usage.cache_read_tokens.unwrap_or(0) as i32
    }
}

pub async fn run_collector(mut rx: mpsc::Receiver<LogEntry>, storage: DynStorage) {
    let mut buffer: Vec<LogEntry> = Vec::with_capacity(32);
    let mut flush_interval = tokio::time::interval(std::time::Duration::from_secs(2));
    let mut cleanup_interval = tokio::time::interval(std::time::Duration::from_secs(600));

    loop {
        tokio::select! {
            Some(entry) = rx.recv() => {
                buffer.push(entry);
                if buffer.len() >= 32 {
                    flush(storage.clone(), &mut buffer).await;
                }
            }
            _ = flush_interval.tick() => {
                if !buffer.is_empty() {
                    flush(storage.clone(), &mut buffer).await;
                }
            }
            _ = cleanup_interval.tick() => {
                cleanup_old_logs(storage.clone()).await;
            }
        }
    }
}

async fn cleanup_old_logs(storage: DynStorage) {
    let days = storage
        .settings()
        .get(LOG_RETENTION_DAYS_KEY)
        .await
        .ok()
        .flatten()
        .and_then(|v| v.parse().ok())
        .unwrap_or(DEFAULT_RETENTION_DAYS);

    let cutoff = format!("-{days} days");
    if let Ok(deleted) = storage.logs().cleanup_before(&cutoff).await
        && deleted > 0
    {
        tracing::info!("cleaned up {deleted} logs older than {days} days");
    }
}

async fn read_record_payloads(storage: &DynStorage) -> bool {
    storage
        .settings()
        .get(LOG_RECORD_PAYLOADS_KEY)
        .await
        .ok()
        .flatten()
        .map(|v| {
            !matches!(
                v.to_ascii_lowercase().as_str(),
                "false" | "0" | "off" | "no"
            )
        })
        .unwrap_or(DEFAULT_RECORD_PAYLOADS)
}

async fn flush(storage: DynStorage, buffer: &mut Vec<LogEntry>) {
    let mut entries = std::mem::take(buffer);
    let record_payloads = read_record_payloads(&storage).await;
    if !record_payloads {
        for entry in entries.iter_mut() {
            entry.client_request_headers = None;
            entry.client_request_body = None;
            entry.client_response_headers = None;
            entry.client_response_body = None;
            entry.upstream_request_headers = None;
            entry.upstream_request_body = None;
            entry.upstream_response_headers = None;
            entry.upstream_response_body = None;
        }
    }
    let _ = storage.logs().append_batch(entries).await;
}
