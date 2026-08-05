// Package otlphttp implements a lightweight embedded OTLP/HTTP protobuf receiver.
//
// It supports the standard /v1/logs, /v1/metrics, and /v1/traces endpoints.
// Accepted requests are acknowledged after entering a bounded in-memory queue;
// persistence is asynchronous and observable through Receiver.Stats.
package otlphttp
