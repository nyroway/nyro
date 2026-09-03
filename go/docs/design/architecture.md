# Nyro Go Architecture

Nyro Go is a statically composed AI gateway. Its implemented data plane has a
workload-neutral microkernel generation host and a trusted LLM vertical slice.
The kernel manages safe activation and resource lifetime; the LLM runtime owns
all model-domain execution rules.

## Request and response flow

```text
Client
  -> generic HTTP Server
  -> LLM HTTP Ingress
  -> Canonical LLM IR
  -> trusted LLM Runtime (fixed Pipeline + Router)
  -> Provider Driver ExtendRequest
  -> Egress Codec encode
  -> Provider Driver Prepare
  -> generation-owned Provider HTTP Transport
  -> Upstream
```

Responses and stream events travel back through the corresponding provider,
egress, runtime, canonical IR, ingress, and HTTP boundaries. The process-level
HTTP server in `internal/transport/httpserver` owns generic mounting, health,
readiness, panic recovery, listening, and graceful shutdown. LLM-specific HTTP
routing and wire handling live in `internal/llm/ingress/http`.

The canonical IR in `internal/llm` currently defines chat and embedding
workloads. Protocol codecs translate between wire formats and that IR; they do
not select providers, authenticate upstream requests, or decide retries.

## Kernel and trusted runtime

`internal/kernel` is deliberately workload-neutral and uses only the Go
standard library. It owns:

- lifecycle dependency validation and deterministic startup order;
- startup rollback, reverse-order retirement, and shutdown;
- atomic publication of typed runtime generations;
- leases that keep retiring generations alive for in-flight requests; and
- readiness plus active-generation status.

The kernel has no LLM IR, protocol, provider, routing, retry, streaming,
configuration-source, security, quota, telemetry, storage, or HTTP knowledge.

`internal/llm/runtime` is trusted code for the LLM workload. It owns canonical
request execution, mandatory pipeline order, routing authority, attempt
cloning, retry and failover decisions, normalized errors, stream commitment,
and terminal client delivery. These domain invariants are not delegated to
optional phases, codecs, drivers, or transports.

The pipeline order is fixed:

```text
Observe -> Resolve -> Authenticate -> Authorize -> Admit
        -> optional PreDispatch -> Dispatch -> optional PostResponse
        -> trusted terminal delivery -> reverse Finalizers
```

Optional phases can continue, short-circuit, or reject at their declared slot,
but cannot reorder mandatory phases, invoke a next phase, control terminal
delivery, or suppress reverse finalization. Stream observers receive canonical
deltas without a control channel.

## Explicit composition

`internal/bootstrap` is the composition root. It explicitly enumerates the
protocol codecs and provider drivers compiled into the executable, constructs
immutable typed catalogs, builds a Snapshot-bound LLM runtime, declares its
resource lifecycle graph, and submits a typed application candidate to the
kernel host.

There is no module discovery through `init`, no blank imports used for Nyro
module registration, and no mutable global module registry. Importing a codec
or provider has no registration side effect. Nyro uses normal static Go
composition; it does not use `buildmode=plugin`, `.so` loading, or a third-party
plugin ABI.

The blank import of `github.com/glebarez/go-sqlite` under
`internal/platform/database/sqlite` is a reviewed `database/sql` driver
integration. It is not Nyro module registration.

## Provider boundary and attempt order

`internal/llm/provider` separates provider-specific behavior from shared wire
protocol codecs. A provider driver owns endpoint construction, credentials,
headers, signing, vendor extensions, and raw provider classification. The
egress codec owns the base wire schema. The provider transport only performs
the prepared exchange.

Each dispatch attempt follows this request order:

1. Clone the request and set the selected upstream model.
2. Apply `Driver.ExtendRequest` to Provider-owned canonical extensions.
3. Encode the canonical request with the Egress Codec.
4. Apply `Driver.Prepare` for endpoint, headers, authentication, and signing.
5. Send through the generation-owned Provider Transport.

Responses and errors follow this order:

1. The Driver classifies raw status and metadata.
2. The Egress Codec decodes the response or error into canonical form.
3. The Driver applies `ExtendResponse` or `ExtendError`.
4. The Runtime decides retry, failover, error delivery, and health recording.

Provider extensions therefore act before wire encoding on requests and after
wire decoding on responses. A driver cannot bypass client authentication,
authorization, admission, routing, retry budgets, stream state, or trusted
terminal delivery.

## Streaming and compatibility

A stream remains uncommitted until the HTTP ingress successfully writes and
flushes the first complete client-visible wire frame. Canonical deltas that
produce no frame, usage state, codec buffers, and other precommit data remain
attempt-local. A retry or failover resets and discards that uncommitted state.
After commitment, the runtime never retries or fails over; later failures are
terminated on the committed client stream.

Opaque compatibility is deliberately narrow. When ingress and egress use the
same Endpoint and both advertise the relevant capability, the runtime may pass
through a success or error using only its status, `Content-Type`, and body.
Arbitrary provider headers and raw provider extensions are filtered. Across
different Endpoints, provider errors are normalized and the ingress codec
encodes them for the client protocol.

## Configuration and runtime generations

Standalone YAML and ConfigSync are configuration sources for the same
immutable `internal/config/snapshot.Snapshot` model. Activation proceeds as a
candidate transaction:

1. Produce and validate a complete immutable Snapshot.
2. Compute its deterministic effective-configuration fingerprint.
3. Skip reconciliation when the active fingerprint is equal.
4. Build an inactive typed application candidate and its owned resources.
5. Let the kernel validate dependencies and start components.
6. Atomically publish the new generation and retire the previous generation
   after its leases drain.

Construction or startup failure closes the candidate and leaves the last
known-good generation active. Every LLM ingress request acquires one generation
lease, pinning its Snapshot, LLM Runtime, and generation-owned resources for
the full request.

Standalone mode performs initial activation synchronously and fails startup if
it cannot build and start the first generation. ConfigSync mode starts live but
not ready, keeps receiving snapshots after a rejected candidate, and retains
the last known-good generation during failed hot updates.

## Package responsibilities

| Path | Responsibility |
|---|---|
| `internal/kernel` | Workload-neutral lifecycle graph, typed generations, leases, readiness, and status |
| `internal/llm` | Canonical LLM IR and extension ownership |
| `internal/llm/ingress/http` | LLM HTTP routes, request decoding, response encoding, and stream sink |
| `internal/llm/pipeline` | Fixed phase runner, typed exchange, terminalizer, and reverse finalizers |
| `internal/llm/protocol` | Endpoint contracts, ingress/egress codecs, wire types, and protocol catalog |
| `internal/llm/provider` | Provider definitions, drivers, transport contract, and provider catalog |
| `internal/llm/provider/httptransport` | Reusable outbound HTTP implementation |
| `internal/llm/routing` | Target selection strategies and health state |
| `internal/llm/runtime` | Trusted LLM orchestration, retry/failover, streaming, errors, and delivery |
| `internal/config` | Standalone YAML parsing, defaults, validation, and Snapshot construction |
| `internal/config/snapshot` | Immutable runtime configuration and deterministic fingerprint |
| `internal/configsync` | Authenticated full-Snapshot distribution and hot-update source |
| `internal/security` | Transport-neutral authentication and authorization contracts |
| `internal/quota` | Request, token, and concurrency accounting |
| `internal/telemetry` | Logging, metrics, tracing, and pipeline observation |
| `internal/storage` | Control-plane persistence contracts and implementations |
| `internal/admin` | Management API and control-plane services |
| `internal/bootstrap` | Executable composition, candidate construction, reconciliation, and shutdown |
| `internal/transport/httpserver` | Generic process-level HTTP server infrastructure |

## Future-only boundaries

MCP is a future sibling trusted runtime, and Integration is a future
cross-domain composition root. Image, Audio, and Video are future workload
runtimes. No current package or runtime capability exists for those designs;
they must not add workload knowledge to the kernel when implemented.

See the [protocol identity reference](../schema/protocols.md) and
[standalone configuration reference](../schema/config.md) for current public
configuration details.
