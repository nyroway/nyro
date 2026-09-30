# nyro-sync

Configuration delivery independent of databases, gateway applications and kernel lifecycles.

- `Hub<T>` owns the latest complete snapshot and in-memory node delivery/application status. Equivalent JSON object content has the same fingerprint regardless of key order.
- `http_router` exposes authenticated long polling at `POST /v1/config/sync`. Mount it behind TLS or a trusted TLS terminator. It does not create a listener.
- `HttpClient<T>` uses HTTPS, with plaintext allowed only for loopback IPs. It disables redirects and environment proxy discovery, bounds responses, and verifies content fingerprints.
- `Source<T>` and `run` share application feedback and retry behavior between HTTP and memory. The applier validates and activates snapshots and must retain the old generation on failure. `Applied` means activation completed.

Configuration payloads may contain upstream credentials: snapshots deliberately have no `Debug`, response caching is disabled, and errors do not contain payloads or URLs. Synchronization tokens are independent of administrator and consumer credentials.

Memory delivery does not serialize and parse the payload for transport. Fingerprinting still serializes it. Node status expires after five minutes without polling; a returning node receives a fresh snapshot exchange. Configuration payloads are limited to 1 MiB. HTTP waits up to 25 seconds per poll; the client allows 35 seconds per request.

The runner retains the last successful fingerprint, retries broken exchanges with exponential backoff from 500 ms to 30 seconds, and fetches the latest full snapshot after an interruption. Permanent application rejections wait for different content; retryable activation failures refetch before retrying. Cancellation interrupts polling, waiting and activation; the supplied applier must be cancellation-safe.

The root `nyro` CLI uses HTTP for `proxy --server` and memory for `serve --enable-proxy`. `Hub::close` wakes outstanding polls during control-plane shutdown. There is no durable offline cache, database access, incremental synchronization, gRPC, telemetry transport or distributed accounting here.

See [Chinese documentation](README_CN.md).
