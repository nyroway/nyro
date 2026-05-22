//! Streaming response handler.
//!
//! Two internal paths:
//! - PassThrough: ingress == egress protocol, no vendor mutations → forward raw
//!   SSE bytes; side-channel parser accumulates stats for logging.
//! - IR round-trip: parse → accumulate → format → re-emit as target-protocol SSE.

use std::convert::Infallible;

use axum::Json;
use axum::body::Body;
use axum::http::{StatusCode, header};
use axum::response::{IntoResponse, Response};
use bytes::Bytes;
use futures::StreamExt;
use reqwest::header::HeaderMap as ReqwestHeaderMap;
use serde_json::Value;
use tokio_stream::wrappers::ReceiverStream;

use crate::protocol::ids::ProtocolEndpoint;
use crate::protocol::ir::AiStreamDelta;
use crate::proxy::client::ProxyClient;
use crate::proxy::observability::headers_to_json;

use super::{
    CallCtx, LogBuilder, RequestExtras, StreamResponseAccumulator, ai_response_to_deltas,
    error_response,
};

// ── Streaming response handler ────────────────────────────────────────────────

pub(super) async fn handle_stream(
    client: ProxyClient,
    url: &str,
    headers: ReqwestHeaderMap,
    body: Value,
    call_ctx: &CallCtx<'_>,
    req_extras: &RequestExtras,
    passthrough_resp: bool,
) -> Response {
    let egress = call_ctx.egress;
    let ingress = call_ctx.ingress;
    // Shared log builder: identity + request-side extras pre-filled.
    let log = LogBuilder::from_ctx(call_ctx)
        .with_req_extras(req_extras)
        .upstream_url(url);

    let upstream_start = std::time::Instant::now();
    let call_result = match client.call_stream(url, headers.clone(), body.clone()).await {
        Ok(r) => r,
        Err(e) => {
            log.status(502)
                .resp_body(Some(
                    serde_json::json!({ "error": { "message": format!("upstream error: {e}") } })
                        .to_string(),
                ))
                .emit();
            return error_response(502, &format!("upstream error: {e}"));
        }
    };
    let upstream_req_hdrs_str = crate::proxy::observability::reqwest_headers_to_json(&headers);
    let upstream_req_body_str = serde_json::to_string(&body).ok();

    let (resp, status) = call_result;
    let upstream_hdrs_str = headers_to_json(resp.headers());

    if status >= 400 {
        let err_body: Value = resp
            .json()
            .await
            .unwrap_or_else(|_| serde_json::json!({"error": {"message": "upstream error"}}));
        let err_body_str = serde_json::to_string(&err_body).ok();
        log.status(status)
            .upstream_status(status as i32)
            .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
            .upstream_resp_headers(upstream_hdrs_str.clone())
            .upstream_resp_body(err_body_str.clone())
            .resp_body(err_body_str)
            .emit();
        return (
            StatusCode::from_u16(status).unwrap_or(StatusCode::BAD_GATEWAY),
            Json(err_body),
        )
            .into_response();
    }

    // ── Byte-level SSE passthrough ────────────────────────────────────────────
    // Used when ingress == egress protocol and the vendor declares no response
    // mutations (passthrough_resp=true). Upstream bytes are forwarded verbatim;
    // a side-channel parser accumulates usage stats for logging only.
    if passthrough_resp {
        let (pt_tx, pt_rx) = tokio::sync::mpsc::channel::<Result<Bytes, Infallible>>(64);

        // Clone the log builder into the spawn: all identity + request-side
        // fields are already owned inside the builder, so no individual variable
        // cloning is needed.
        let log_pt = log.clone();
        let upstream_hdrs_pt = upstream_hdrs_str.clone();
        let upstream_req_hdrs_pt = upstream_req_hdrs_str.clone();
        let upstream_req_body_pt = upstream_req_body_str.clone();
        let upstream_start_pt = upstream_start;

        tokio::spawn(async move {
            let mut log_buf: Vec<u8> = Vec::new();
            let mut undecided_buf: Vec<u8> = Vec::new();
            let mut byte_stream = resp.bytes_stream();
            let mut stream_error: Option<String> = None;
            let mut chunks_count: i32 = 0;
            let mut first_chunk_ms: Option<i64> = None;
            let mut passthrough_mode = PassthroughBodyMode::Undecided;
            let mut converted_client_sse: Option<String> = None;
            let mut converted_ai_resp = None;

            while let Some(result) = byte_stream.next().await {
                match result {
                    Ok(b) => {
                        if first_chunk_ms.is_none() {
                            first_chunk_ms = Some(upstream_start_pt.elapsed().as_millis() as i64);
                        }
                        chunks_count += 1;
                        log_buf.extend_from_slice(&b);
                        match passthrough_mode {
                            PassthroughBodyMode::Undecided => {
                                undecided_buf.extend_from_slice(&b);
                                match classify_passthrough_body(&undecided_buf) {
                                    Some(PassthroughBodyMode::RawSse) => {
                                        passthrough_mode = PassthroughBodyMode::RawSse;
                                        let pending = std::mem::take(&mut undecided_buf);
                                        if pt_tx.send(Ok(Bytes::from(pending))).await.is_err() {
                                            break; // client disconnected
                                        }
                                    }
                                    Some(PassthroughBodyMode::NonSseJson) => {
                                        passthrough_mode = PassthroughBodyMode::NonSseJson;
                                        undecided_buf.clear();
                                    }
                                    _ => {}
                                }
                            }
                            PassthroughBodyMode::RawSse => {
                                if pt_tx.send(Ok(b)).await.is_err() {
                                    break; // client disconnected
                                }
                            }
                            PassthroughBodyMode::NonSseJson => {
                                // Upstream returned a complete JSON response to a stream endpoint.
                                // Buffer until EOF, then convert it to the downstream SSE shape.
                            }
                        }
                    }
                    Err(e) => {
                        tracing::warn!(error = %e, "upstream stream error during passthrough");
                        stream_error = Some(e.to_string());
                        // Emit an Anthropic-protocol error event so the client
                        // gets an explicit signal instead of a truncated stream.
                        let msg = e.to_string().replace('"', "\\\"");
                        let err_sse = format!(
                            "event: error\ndata: {{\"type\":\"error\",\"error\":{{\"type\":\"stream_error\",\"message\":\"{msg}\"}}}}\n\n"
                        );
                        let _ = pt_tx.send(Ok(Bytes::from(err_sse))).await;
                        break;
                    }
                }
            }

            let upstream_latency_ms = upstream_start_pt.elapsed().as_millis() as i64;
            let raw_sse = String::from_utf8_lossy(&log_buf).into_owned();

            if matches!(
                passthrough_mode,
                PassthroughBodyMode::NonSseJson | PassthroughBodyMode::Undecided
            ) && let Some((client_sse, ai_resp)) =
                format_non_sse_stream_response(&raw_sse, egress, ingress)
            {
                let _ = pt_tx.send(Ok(Bytes::from(client_sse.clone()))).await;
                converted_client_sse = Some(client_sse);
                converted_ai_resp = Some(ai_resp);
            }

            // Parse accumulated buffer for usage stats (best-effort).
            let mut log_parser = egress.handler().make_stream_response_decoder();
            let mut accumulator = StreamResponseAccumulator::default();
            if let Ok(ai_deltas) = log_parser.parse_chunk(&raw_sse) {
                accumulator.apply_all(&ai_deltas);
            }
            if let Ok(ai_deltas) = log_parser.finish() {
                accumulator.apply_all(&ai_deltas);
            }

            let mut ai_resp = converted_ai_resp.unwrap_or_else(|| accumulator.into_ai_response());
            if ai_resp.id.is_empty() {
                ai_resp.id = format!("msg_{}", uuid::Uuid::new_v4().simple());
            }
            if ai_resp.model.is_empty() {
                ai_resp.model = log_pt.upstream_model.clone();
            }

            log_pt
                .status(200)
                .upstream_status(200)
                .usage(ai_resp.usage.clone())
                .maybe_error(stream_error)
                .with_upstream_request(upstream_req_hdrs_pt, upstream_req_body_pt)
                .with_upstream_response(
                    200,
                    upstream_hdrs_pt,
                    Some(raw_sse.clone()),
                    Some(upstream_latency_ms),
                )
                .with_client_response(None, Some(converted_client_sse.unwrap_or(raw_sse)))
                .stream_metrics(chunks_count, first_chunk_ms)
                .emit();
        });

        let stream = ReceiverStream::new(pt_rx);
        let body = Body::from_stream(stream);
        let response = Response::builder()
            .status(StatusCode::OK)
            .header(header::CONTENT_TYPE, "text/event-stream")
            .header(header::CACHE_CONTROL, "no-cache")
            .header(header::CONNECTION, "keep-alive")
            .body(body)
            .unwrap();
        return response;
    }

    // ── IR round-trip path ────────────────────────────────────────────────────
    let mut stream_parser = egress.handler().make_stream_response_decoder();
    let mut stream_formatter = ingress.handler().make_stream_response_encoder();
    let mut byte_stream = resp.bytes_stream();
    let (tx, rx) = tokio::sync::mpsc::channel::<Result<String, Infallible>>(64);

    // Move the log builder into the spawn.  Extract the fields we need AFTER
    // emit() consumes the builder, before passing it to the spawn.
    let log_ir = log;
    let act_model_ir = log_ir.upstream_model.clone();
    let upstream_hdrs_owned = upstream_hdrs_str;

    tokio::spawn(async move {
        let mut accumulator = StreamResponseAccumulator::default();
        let mut upstream_raw_buf: Vec<u8> = Vec::new();
        let mut client_sse_parts: Vec<String> = Vec::new();
        let mut chunks_count: i32 = 0;
        let mut first_chunk_ms: Option<i64> = None;

        while let Some(chunk) = byte_stream.next().await {
            let bytes = match chunk {
                Ok(b) => b,
                Err(e) => {
                    // P1: emit an explicit terminal event instead of silently breaking,
                    // so the client receives a defined stop_reason and does not hang.
                    tracing::warn!(error = %e, "upstream stream error; emitting terminal event");
                    let error_deltas = [AiStreamDelta::Done {
                        stop_reason: "error".to_string(),
                    }];
                    let events = stream_formatter.format_deltas(&error_deltas);
                    for ev in events {
                        let _ = tx.send(Ok(ev.to_sse_string())).await;
                    }
                    break;
                }
            };
            if first_chunk_ms.is_none() {
                first_chunk_ms = Some(upstream_start.elapsed().as_millis() as i64);
            }
            chunks_count += 1;
            upstream_raw_buf.extend_from_slice(&bytes);
            let text = String::from_utf8_lossy(&bytes);
            if let Ok(ai_deltas) = stream_parser.parse_chunk(&text) {
                accumulator.apply_all(&ai_deltas);
                let events = stream_formatter.format_deltas(&ai_deltas);
                for ev in events {
                    let sse = ev.to_sse_string();
                    client_sse_parts.push(sse.clone());
                    if tx.send(Ok(sse)).await.is_err() {
                        return;
                    }
                }
            }
        }

        if let Ok(ai_deltas) = stream_parser.finish() {
            accumulator.apply_all(&ai_deltas);
            let events = stream_formatter.format_deltas(&ai_deltas);
            for ev in events {
                let sse = ev.to_sse_string();
                client_sse_parts.push(sse.clone());
                let _ = tx.send(Ok(sse)).await;
            }
        }

        let done_events = stream_formatter.format_done();
        for ev in done_events {
            let sse = ev.to_sse_string();
            client_sse_parts.push(sse.clone());
            let _ = tx.send(Ok(sse)).await;
        }

        let upstream_latency_ms = upstream_start.elapsed().as_millis() as i64;
        let upstream_raw_str = String::from_utf8_lossy(&upstream_raw_buf).into_owned();
        let client_sse_str = client_sse_parts.join("");

        let usage = stream_formatter.usage();
        let mut ai_resp = accumulator.into_ai_response();
        if ai_resp.usage.prompt_tokens == 0 && ai_resp.usage.completion_tokens == 0 {
            ai_resp.usage = usage.clone();
        }
        if ai_resp.id.is_empty() {
            ai_resp.id = format!("chatcmpl-{}", uuid::Uuid::new_v4().simple());
        }
        if ai_resp.model.is_empty() {
            ai_resp.model = act_model_ir.clone();
        }
        if ai_resp.stop_reason.is_none() {
            ai_resp.stop_reason = Some("stop".to_string());
        }

        log_ir
            .status(200)
            .upstream_status(200)
            .usage(ai_resp.usage.clone())
            .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
            .with_upstream_response(
                200,
                upstream_hdrs_owned,
                Some(upstream_raw_str),
                Some(upstream_latency_ms),
            )
            .with_client_response(None, Some(client_sse_str))
            .stream_metrics(chunks_count, first_chunk_ms)
            .emit();
    });

    let stream = ReceiverStream::new(rx);
    let body = Body::from_stream(stream);

    let response = Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "text/event-stream")
        .header(header::CACHE_CONTROL, "no-cache")
        .header(header::CONNECTION, "keep-alive")
        .body(body)
        .unwrap();
    response
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PassthroughBodyMode {
    Undecided,
    RawSse,
    NonSseJson,
}

fn classify_passthrough_body(bytes: &[u8]) -> Option<PassthroughBodyMode> {
    let text = String::from_utf8_lossy(bytes);
    let trimmed = text.trim_start();
    if trimmed.is_empty() {
        return None;
    }
    if trimmed.starts_with("data:")
        || trimmed.starts_with("event:")
        || trimmed.starts_with("id:")
        || trimmed.starts_with("retry:")
        || trimmed.starts_with(':')
    {
        return Some(PassthroughBodyMode::RawSse);
    }
    if trimmed.starts_with('{') || trimmed.starts_with('[') {
        return Some(PassthroughBodyMode::NonSseJson);
    }
    Some(PassthroughBodyMode::RawSse)
}

fn format_non_sse_stream_response(
    raw: &str,
    egress: ProtocolEndpoint,
    ingress: ProtocolEndpoint,
) -> Option<(String, crate::protocol::ir::AiResponse)> {
    let value = serde_json::from_str::<Value>(raw).ok()?;
    let ai_resp = egress
        .handler()
        .make_response_decoder()
        .parse_response(value)
        .ok()?;
    let deltas = ai_response_to_deltas(&ai_resp);
    let mut stream_formatter = ingress.handler().make_stream_response_encoder();
    let mut client_sse_parts = Vec::new();

    for ev in stream_formatter.format_deltas(&deltas) {
        client_sse_parts.push(ev.to_sse_string());
    }
    for ev in stream_formatter.format_done() {
        client_sse_parts.push(ev.to_sse_string());
    }

    Some((client_sse_parts.join(""), ai_resp))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::ids::GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA;

    #[test]
    fn non_sse_gemini_stream_response_is_formatted_as_sse() {
        let raw = serde_json::json!({
            "candidates": [{
                "content": {
                    "parts": [{"text": "hello"}],
                    "role": "model"
                },
                "finishReason": "STOP",
                "index": 0
            }],
            "modelVersion": "gemini-3.5-flash",
            "responseId": "resp-json-stream",
            "usageMetadata": {
                "candidatesTokenCount": 3,
                "promptTokenCount": 5,
                "totalTokenCount": 8
            }
        })
        .to_string();

        let (sse, ai_resp) = format_non_sse_stream_response(
            &raw,
            GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
            GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
        )
        .expect("complete JSON stream response should format as SSE");

        assert!(sse.starts_with("data: "), "SSE must use data frames: {sse}");
        assert!(
            sse.contains("\"usageMetadata\""),
            "terminal SSE must include Gemini usage metadata: {sse}"
        );
        assert_eq!(ai_resp.content, "hello");
        assert_eq!(ai_resp.usage.prompt_tokens, 5);
        assert_eq!(ai_resp.usage.completion_tokens, 3);
        assert_eq!(ai_resp.usage.total_tokens, 8);
    }
}
