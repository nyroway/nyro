<!-- This file governs go/ and all of its subdirectories. -->
<!-- The repository-root AGENTS.md still applies. These Go-specific rules take precedence on conflict. -->

# Nyro Go

## Purpose and current state

`go/` is the Go implementation of Nyro AI Gateway. It includes the data plane,
standalone server, control plane, WebUI, configuration management, storage,
quota, telemetry, and administrative tooling.

The workload-neutral generation Host and the trusted LLM vertical slice are
implemented. Current requests use immutable configuration Snapshots, atomic
typed runtime generations, leases, explicit Protocol and Provider catalogs,
the fixed LLM pipeline, and generation-owned runtime resources.

Other module migrations are not complete. MCP, cross-domain Integration
composition, and Image, Audio, and Video workload runtimes are future designs
only. Do not claim their packages or capabilities exist. Preserve current
behavior while migrating other vertical slices incrementally.

## Architecture principle

> Varying capabilities are explicitly composed behind narrow typed contracts;
> resource owners have explicit lifecycles; workload-neutral invariants stay in
> the kernel; workload invariants stay in trusted runtimes.

This is static Go composition, not a dynamic plugin system. Do not use
`buildmode=plugin`, `.so` loading, or a third-party plugin ABI without a
separately approved design demonstrating a concrete requirement.

The implemented architecture is documented in
`docs/design/architecture.md`. Keep implementation and that document aligned.

## Kernel responsibilities

`internal/kernel` owns only workload-neutral invariants:

- component identity and dependency-graph validation;
- deterministic dependency ordering;
- lifecycle startup, rollback, retirement, and shutdown;
- typed Candidate and Host contracts plus atomic runtime-generation activation;
- leases that keep retiring generations alive until release; and
- readiness and runtime-generation status.

Kernel production code must use only the Go standard library. It must not
depend on:

- LLM or another workload type;
- request, response, stream, routing, retry, failover, or error semantics;
- protocols, providers, configuration parsing, or configuration transport;
- security, quota, telemetry, storage, Admin, WebUI, or HTTP transport; or
- global service locators, string-keyed dependency maps, or module discovery.

## Trusted LLM runtime

`internal/llm/runtime` owns all LLM-domain invariants: Canonical LLM IR
execution, mandatory phase order, routing authority, attempt isolation,
normalized errors, extension ownership, retry and failover, streaming commit,
health decisions, and trusted terminal delivery.

The phase order is fixed:

```text
Observe -> Resolve -> Authenticate -> Authorize -> Admit
        -> optional PreDispatch -> Dispatch -> optional PostResponse
        -> trusted terminal delivery -> reverse Finalizers
```

Optional phases may participate only at `PreDispatch` or `PostResponse`. They
must not change mandatory order, invoke the next phase, control terminal
delivery, bypass authorization or admission, or suppress reverse finalization.
Stream observers observe canonical deltas without controlling stream flow.

## Explicit composition and catalogs

Concrete Protocol Codecs and Provider Drivers are enumerated by
`internal/bootstrap`. Bootstrap constructs immutable typed catalogs, resolves
configuration, builds inactive Snapshot-bound runtime candidates and resources,
and submits their lifecycle graphs to the Kernel Host.

Importing a module package must not register, start, or activate it. Do not use:

- package `init()` for module registration or dependency wiring;
- blank imports whose purpose is Nyro module registration;
- mutable global registries populated as an import side effect;
- hidden discovery based on initialization order; or
- configuration I/O, goroutine startup, or resource acquisition from `init()`.

The reviewed blank import of `github.com/glebarez/go-sqlite` in
`internal/platform/database/sqlite` is a `database/sql` driver integration,
not module registration.

Tests must explicitly assemble the catalogs and dependencies they require.
Configuration may select only implementations present in an explicitly built
catalog; it must not cause otherwise unreferenced code to execute.

When adding a varying capability, define the smallest stable typed contract at
its consumer or in an established contract package. Do not introduce a service
locator, global container, general plugin framework, or speculative extension
point.

## LLM protocol and provider boundaries

The implemented request path is:

```text
Client Wire
    -> generic HTTP Server
    -> LLM HTTP Ingress
    -> Canonical LLM IR
    -> trusted Runtime Pipeline and Router
    -> Provider Driver extension
    -> Egress Codec
    -> Provider Driver preparation
    -> generation-owned Provider HTTP Transport
    -> Upstream
```

Responses and streams travel in reverse through their corresponding
boundaries.

### Ingress Codec

An Ingress Codec parses a northbound request, performs protocol-structural
validation, maps common semantics into the Canonical LLM IR, and encodes
canonical responses, errors, and stream deltas for its client protocol. It must
not select providers, add provider credentials, or decide retry/failover.

### Canonical LLM IR

A common IR field must have stable cross-protocol semantics and be used by more
than one implementation or trusted runtime concern. Provider- or
protocol-specific information belongs in an explicitly owned namespace with
defined type, lifecycle, processing positions, filtering, and fallback rules.
Do not use unconstrained `map[string]any` values as public cross-layer
contracts.

### Provider Driver and Egress Codec

A Provider Driver owns provider endpoints, credentials, headers, signing,
vendor extensions, raw response classification, and Provider-specific health
or retry classification. An Egress Codec owns a reusable upstream base wire
schema. The Transport performs only a prepared request.

The request order for every attempt is:

1. Clone the attempt request and set the selected target model.
2. Run `Driver.ExtendRequest` on Provider-owned canonical extensions.
3. Encode through the Egress Codec.
4. Run `Driver.Prepare` for endpoint, headers, authentication, and signing.
5. Call the Provider Transport.

The response/error order is:

1. Driver raw classification.
2. Egress response or error decoding.
3. `Driver.ExtendResponse` or `Driver.ExtendError`.
4. Runtime retry, failover, delivery, error, and health decisions.

Never describe request extensions as occurring after wire encoding. Drivers
must not bypass client authentication, authorization, admission, routing,
retry budgets, the stream state machine, cancellation, or resource release.

### Streaming and error compatibility

A stream becomes committed only when the first complete client-visible wire
frame has been successfully written and flushed. Zero-frame deltas, codec
buffers, usage, and other precommit state are attempt-local and must be reset
before retry/failover. No retry or failover is legal after commitment.

Opaque success or error passthrough is allowed only when ingress and egress use
the same Endpoint and both negotiate the capability. It preserves only status,
`Content-Type`, and body. Arbitrary Provider headers and raw extensions must
not cross protocols. Cross-Endpoint errors are canonicalized by the Runtime and
encoded by the Ingress Codec.

## Configuration and activation

Every persistently operator-controlled data-plane behavior that must remain
consistent in standalone and managed modes must have one canonical,
serializable configuration model expressible by `config.yaml`.

- YAML, Admin storage, and ConfigSync are sources or transports for the same
  semantics; they must not define incompatible models.
- Defaults, validation, and precedence must be explicit and consistent.
- Configuration is declarative data, not functions, connections, clients,
  contexts, handles, or other runtime objects.
- Secrets may be references; plaintext secrets are not required in YAML.

Process bootstrap parameters, database DSNs, listen addresses, TLS material,
external secret references, request contexts, active resources, caches, health,
and statistics may remain outside `config.yaml` when they are environment or
runtime state rather than persistent data-plane behavior.

Configuration activation must separate:

1. parsing and defaults;
2. structural and semantic validation;
3. immutable Snapshot construction;
4. deterministic fingerprint comparison;
5. dependency resolution and inactive resource construction;
6. Kernel lifecycle startup; and
7. atomic generation publication.

Equal fingerprints are a no-op. Construction or startup failure must close the
candidate and retain the last known-good generation. A request lease pins its
entire Snapshot, Runtime, and generation resources. Standalone initial failure
fails startup. ConfigSync remains live and not ready before its first successful
activation, and rejects bad hot candidates without replacing the last known
good generation.

## Dependency direction

Follow the concrete boundaries enforced by `tests/layering`:

- `internal/kernel` is stdlib-only.
- `internal/llm` model types are stdlib-only and do not import Runtime.
- `internal/llm/protocol` depends only on the Canonical LLM IR.
- `internal/llm/provider` depends only on the Canonical LLM IR and Protocol
  contracts.
- `internal/llm/routing` is transport- and storage-independent.
- `internal/llm/pipeline` depends on typed LLM, Protocol, and Security
  contracts, not concrete implementations.
- `internal/llm/runtime` orchestrates Snapshot, Pipeline, Protocol, Provider,
  Routing, Quota, and authentication contracts without importing Bootstrap.
- `internal/llm/ingress/http` adapts the Runtime to HTTP and does not own
  Runtime policy.
- `internal/transport/httpserver` remains generic process infrastructure.
- `internal/bootstrap` may assemble concrete implementations; domain packages
  must not import it.
- Storage implementations own persistence, not domain business rules.

When adding or moving packages, update the classifications and boundary tests
in `tests/layering`.

## Simplicity

Choose the smallest design that satisfies current requirements and system
invariants. Prefer:

- the Go standard library and existing dependencies;
- small typed interfaces and explicit constructors;
- immutable configuration and catalogs;
- clear ownership and predictable control flow;
- pure conversion functions with bidirectional and stream tests; and
- incremental, behavior-preserving changes.

Do not add abstraction layers, frameworks, dependencies, configuration,
background services, compatibility layers, or extension points solely for
hypothetical requirements. Do not introduce a broad interface merely to hide
an equally broad implementation, or a complex abstraction to remove small
duplication.

## Lifecycle and reliability

Components that own goroutines, connections, files, streams, timers,
subscriptions, caches, configuration watchers, or telemetry exporters require
an explicit lifecycle.

Lifecycle management must:

- be owned by the resource owner;
- accept and propagate `context.Context`;
- begin cleanup even when the close context is already canceled and return
  promptly when it is done;
- support idempotent shutdown;
- close resources created before initialization failure;
- start in dependency order and close in reverse order; and
- never create an unowned or unstoppable goroutine.

Errors must identify the failing stage, capability, and object without leaking
secrets, credentials, or sensitive request content. Never silently ignore
configuration errors, capability mismatches, data loss, or unsafe degradation.

## Coding and documentation rules

- Keep changes focused and reversible; avoid unrelated cleanup.
- Reuse established patterns before adding abstractions or dependencies.
- Inject dependencies through constructors or explicit parameters.
- Avoid mutable package-level state.
- Do not expose private internal APIs solely for tests.
- Test business logic through public behavior; package-private state machines
  may have internal tests.
- Update every implementation, caller, test, and relevant document when a
  public contract changes.
- Keep English as the canonical default for multilingual or i18n-capable
  values, following the repository-root rule.
- Put concise package comments in an ordinary `.go` source file. Put longer
  architecture explanations under `go/docs/`. Hand-written `doc.go` files are
  forbidden.

## Generated files

Do not edit generated artifacts directly, including:

- `internal/storage/query/*.gen.go`;
- generated Protobuf `.pb.go` files;
- `webui/dist/`; and
- files marked generated or `DO NOT EDIT`.

After changing GORM models or query definitions, run:

```bash
make go-gen-storage
```

For Protobuf changes, edit sources under `proto/` and use the repository's
generation workflow.

## Database changes

The Go schema sources of truth are the GORM models under
`internal/storage/model`, the model set returned by `model.All()`, and explicit
Go storage migration logic.

Database schema changes must:

1. update models and registration;
2. update required migration or compatibility logic;
3. regenerate `internal/storage/query`;
4. update `docs/schema/database.md` and relevant configuration/migration docs;
5. verify affected storage backends and migration paths.

The repository-root `deploy/schema/postgres.sql` and
`deploy/schema/mysql.sql` are Rust reference artifacts. Go database changes
must not modify them. Do not assume startup automatically migrates the
database; migration depends on the selected mode and explicit configuration.

## Testing and verification

Run the narrowest relevant test first, then expand in proportion to impact.
Unless specified otherwise, run Go commands from `go/` and Make targets from
the repository root.

At minimum, Go code changes require:

```bash
gofmt -w <changed Go files>
go test <affected packages> -count=1
```

For public behavior, cross-package interfaces, or Runtime orchestration, also
run:

```bash
go test ./... -count=1
go vet ./...
go build ./...
```

For Canonical IR, protocol, streaming, or Provider Codec changes, run:

```bash
go test -race ./tests/conversion/... -count=1
```

For package moves, dependency changes, or architecture-boundary changes, run:

```bash
go test ./tests/layering -count=1
```

For WebUI changes, run from `webui/`:

```bash
pnpm test
pnpm lint
pnpm build
```

Before committing, always run `git diff --check`. Never claim a command passed
unless it was run; report environmental skips explicitly.

## Documentation consistency

Update relevant files under `docs/` when architecture, boundaries,
configuration, schemas, or public behavior change. Documentation must describe
implemented behavior. Label MCP, Integration, media workloads, and other
unfinished migration work as future-only; never present them as current
packages or capabilities.
