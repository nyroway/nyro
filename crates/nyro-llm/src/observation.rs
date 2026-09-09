//! LLM request and attempt records. Accounting stays separate from log delivery.
use crate::{
    EmbeddingUsage, Usage, Workload,
    codec::{ChatFormat, CodecError},
    ingress::body::Outcome,
    quota::AttemptQuota,
};
use std::sync::{Arc, Mutex};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

pub(crate) fn protocol(format: ChatFormat, workload: Workload) -> &'static str {
    match (format, workload) {
        (ChatFormat::OpenAiChat, Workload::Embedding) => "openai_embedding",
        (ChatFormat::OpenAiChat, _) => "openai_chat",
        (ChatFormat::OpenAiResponses, _) => "openai_responses",
        (ChatFormat::Anthropic, _) => "anthropic",
        (ChatFormat::Gemini, _) => "gemini",
    }
}

fn outcome_name(outcome: Outcome) -> &'static str {
    match outcome {
        Outcome::Complete => "complete",
        Outcome::Cancelled => "cancelled",
        Outcome::Timeout => "timeout",
        Outcome::Error => "error",
    }
}

fn interrupted(cancellation: &CancellationToken, deadline: Instant) -> &'static str {
    if cancellation.is_cancelled() {
        "cancelled"
    } else if Instant::now() >= deadline {
        "timeout"
    } else {
        "cancelled"
    }
}

#[derive(Default)]
struct Totals {
    input: u128,
    output: u128,
    total: u128,
    quota: u128,
    known: u32,
    complete: u32,
    invalid: bool,
}

pub(crate) struct RequestObservation {
    pub id: String,
    pub model: String,
    pub backend: String,
    pub protocol: &'static str,
    pub workload: &'static str,
    pub streaming: bool,
    pub attempts: u32,
    pub status: u16,
    pub error_code: &'static str,
    pub delivery: Option<Outcome>,
    started: Instant,
    cancellation: CancellationToken,
    deadline: Instant,
    totals: Arc<Mutex<Totals>>,
}

impl RequestObservation {
    pub fn new(started: Instant, deadline: Instant, cancellation: CancellationToken) -> Self {
        Self {
            id: format!("{:032x}", rand::random::<u128>()),
            model: String::new(),
            backend: String::new(),
            protocol: "unknown",
            workload: "unknown",
            streaming: false,
            attempts: 0,
            status: 0,
            error_code: "",
            delivery: None,
            started,
            cancellation,
            deadline,
            totals: Arc::default(),
        }
    }

    pub fn attempt(
        &mut self,
        backend: &str,
        provider: &str,
        protocol: &'static str,
        quota: Option<AttemptQuota>,
    ) -> AttemptObservation {
        self.attempts += 1;
        self.backend = backend.to_owned();
        AttemptObservation {
            request_id: self.id.clone(),
            model: self.model.clone(),
            backend: backend.to_owned(),
            provider: provider.to_owned(),
            protocol,
            number: self.attempts,
            started: Instant::now(),
            cancellation: self.cancellation.clone(),
            deadline: self.deadline,
            totals: self.totals.clone(),
            quota,
            usage: None,
            invalid_usage: false,
            status: 0,
            finished: false,
        }
    }
}

impl Drop for RequestObservation {
    fn drop(&mut self) {
        let delivery = self.delivery.map_or("none", outcome_name);
        let outcome = match self.delivery {
            Some(Outcome::Complete) => match self.error_code {
                "" => "complete",
                "request_timeout" => "timeout",
                "request_cancelled" => "cancelled",
                _ => "error",
            },
            Some(outcome) => outcome_name(outcome),
            None => interrupted(&self.cancellation, self.deadline),
        };
        let totals = self.totals.lock().unwrap();
        let usage_state = if self.attempts == 0 {
            "not_attempted"
        } else if totals.invalid {
            "invalid"
        } else if totals.complete == self.attempts {
            "complete"
        } else if totals.known > 0 {
            "partial"
        } else {
            "missing"
        };
        tracing::info!(target: "nyro::request",
            request_id = %self.id, model = %self.model, backend = %self.backend,
            protocol = self.protocol, workload = self.workload, streaming = self.streaming,
            attempts = self.attempts, status = self.status, outcome, delivery_outcome = delivery,
            error_code = self.error_code, duration_ms = self.started.elapsed().as_millis() as u64,
            input_tokens = totals.input, output_tokens = totals.output, total_tokens = totals.total,
            usage_state, quota_charged_tokens = totals.quota, "LLM request finished");
    }
}

#[derive(Clone, Copy)]
struct Tokens {
    input: u64,
    output: u64,
    total: u64,
}

pub(crate) struct AttemptObservation {
    request_id: String,
    model: String,
    backend: String,
    provider: String,
    protocol: &'static str,
    number: u32,
    started: Instant,
    cancellation: CancellationToken,
    deadline: Instant,
    totals: Arc<Mutex<Totals>>,
    quota: Option<AttemptQuota>,
    usage: Option<Tokens>,
    invalid_usage: bool,
    pub status: u16,
    finished: bool,
}

impl AttemptObservation {
    pub fn observe(&mut self, usage: &Usage) -> Result<(), CodecError> {
        let valid =
            usage.prompt_tokens.checked_add(usage.completion_tokens) == Some(usage.total_tokens);
        self.observe_tokens(
            Tokens {
                input: usage.prompt_tokens,
                output: usage.completion_tokens,
                total: usage.total_tokens,
            },
            valid,
        );
        if let Some(quota) = self.quota.as_mut() {
            quota.observe(usage)?;
        }
        Ok(())
    }

    pub fn observe_embedding(&mut self, usage: &EmbeddingUsage) -> Result<(), CodecError> {
        self.observe_tokens(
            Tokens {
                input: usage.prompt_tokens,
                output: 0,
                total: usage.total_tokens,
            },
            usage.prompt_tokens == usage.total_tokens,
        );
        if let Some(quota) = self.quota.as_mut() {
            quota.observe_embedding(usage)?;
        }
        Ok(())
    }

    fn observe_tokens(&mut self, tokens: Tokens, valid: bool) {
        if !valid
            || self
                .usage
                .is_some_and(|previous| tokens.total < previous.total)
        {
            self.invalid_usage = true;
        } else {
            self.usage = Some(tokens);
        }
    }

    /// Protocol completion precedes downstream encoding/delivery and cannot be undone by it.
    pub fn complete(&mut self) {
        self.finish("complete");
    }

    pub fn fail(&mut self, outcome: &'static str) {
        self.finish(outcome);
    }

    fn finish(&mut self, outcome: &'static str) {
        if self.finished {
            return;
        }
        self.finished = true;
        let (charged, quota_outcome) = match self.quota.take() {
            Some(quota) if outcome == "complete" => (
                Some(quota.complete()),
                if self.usage.is_some() {
                    "actual"
                } else {
                    "fallback"
                },
            ),
            Some(quota) if outcome == "connect_error" => (Some(quota.release()), "released"),
            Some(quota) => (Some(quota.abandon()), "fallback"),
            None => (None, "disabled"),
        };
        let usage_state = if self.invalid_usage {
            "invalid"
        } else if self.usage.is_none() {
            "missing"
        } else if outcome == "complete" {
            "complete"
        } else {
            "partial"
        };
        {
            let mut totals = self.totals.lock().unwrap();
            if let Some(usage) = self.usage {
                totals.input += u128::from(usage.input);
                totals.output += u128::from(usage.output);
                totals.total += u128::from(usage.total);
                totals.known += 1;
            }
            totals.complete += u32::from(usage_state == "complete");
            totals.invalid |= self.invalid_usage;
            totals.quota += u128::from(charged.unwrap_or(0));
        }
        tracing::info!(target: "nyro::attempt",
            request_id = %self.request_id, attempt = self.number, model = %self.model,
            backend = %self.backend, provider = %self.provider, protocol = self.protocol,
            upstream_status = self.status, outcome,
            duration_ms = self.started.elapsed().as_millis() as u64, usage_state,
            input_tokens = self.usage.map(|u| u.input), output_tokens = self.usage.map(|u| u.output),
            total_tokens = self.usage.map(|u| u.total), quota_charged_tokens = charged,
            quota_outcome, "LLM upstream attempt finished");
    }
}

impl Drop for AttemptObservation {
    fn drop(&mut self) {
        self.finish(interrupted(&self.cancellation, self.deadline));
    }
}
