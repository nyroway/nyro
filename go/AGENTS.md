<!-- This file governs go/ and all of its subdirectories. -->
<!-- The general rules in the repository-root AGENTS.md still apply. If they conflict, the more specific Go rules in this file take precedence. -->

# Nyro Go

## Purpose

`go/` is the Go implementation of Nyro AI Gateway. It contains the gateway runtime, protocol conversion, request pipeline, routing, provider integrations, configuration management, storage, quota enforcement, telemetry, administration services, and WebUI.

The long-term architecture goal is to evolve Nyro Go into a modular microkernel system with explicit boundaries while keeping the migration incremental, verifiable, and reversible.

## Current State

Nyro Go is currently a modular monolith. The microkernel and module migration has not been completed.

The microkernel and module boundaries in this file are:

- forward constraints for new code;
- the target state for architectural migration;
- criteria for reviewing existing and proposed designs.

Existing code that does not satisfy the target boundaries is migration debt. It does not authorize broad refactoring during unrelated work.

Unless a task explicitly requires it, do not change existing behavior, public APIs, configuration semantics, or compatibility merely to make the code superficially match the target architecture.

## Core Architecture Principle

> Everything that varies is a lifecycle-managed module behind a narrow, typed capability interface; system invariants stay in the microkernel.

"Everything is a plugin" is shorthand for this principle. It does not mean that every piece of code must be a plugin, nor does it imply the use of Go dynamic plugins.

Capabilities that vary by deployment, protocol, provider, policy, or runtime environment should live behind module boundaries.

Rules that define system semantics, security, consistency, and execution order must remain under microkernel control and must not be delegated to arbitrary modules.

## Microkernel Responsibilities

The microkernel retains only the system invariants that must remain consistent across implementations, including:

- the Canonical IR and core capability contracts;
- module registration, dependency resolution, initialization, startup, replacement, and shutdown;
- request, response, and streaming-event orchestration;
- request context, cancellation, timeout, and resource-release semantics;
- unified error classification, propagation, and external error mapping;
- mandatory security enforcement and processing-stage ordering;
- authority over routing, retry, failover, and termination decisions;
- extension namespaces, ownership, and passthrough safety rules;
- configuration snapshot validation, activation, and consistency guarantees;
- consistent propagation of observability context through the execution path.

The microkernel must not directly depend on a concrete:

- northbound protocol;
- provider or vendor API;
- database, cache, or state backend;
- quota implementation;
- telemetry exporter;
- routing algorithm;
- administrative transport;
- desktop, server, or WebUI runtime environment.

The microkernel may depend on stable, narrow, typed capability interfaces. Dependencies must not be hidden behind global variables, string-based service locators, or unconstrained generic maps.

## Variable Capabilities and Module Boundaries

The following capabilities should generally be replaceable modules:

- ingress adapters;
- egress adapters;
- provider drivers;
- routing and load-balancing strategies;
- optional pipeline stages;
- storage and state backends;
- quota backends;
- telemetry exporters;
- configuration synchronization transports;
- Admin and WebUI access services;
- bootstrap and integration code for specific deployment environments.

Every module must:

- expose capabilities through explicit typed interfaces;
- declare dependencies explicitly rather than retrieving them from hidden global state;
- let a unified lifecycle owner create and close runtime resources;
- validate configuration and dependencies before activation;
- return explicit errors for unsupported capabilities;
- never bypass microkernel-defined security, routing, quota, error, or stream-state semantics.

Modules are compiled into the Nyro binary by default and selected through registries and configuration.

Do not use Go `buildmode=plugin` or `.so`-based dynamic plugins unless a separately approved design demonstrates a concrete requirement that static registration cannot satisfy.

## Constraints During the Current Migration Stage

Until a unified module host and lifecycle mechanism exist:

- inject dependencies through explicit constructors;
- use the existing registries and factory mechanisms;
- define interfaces at the capability consumer or in a stable contract package;
- do not introduce temporary service locators, global containers, or another plugin framework;
- do not scatter lifecycle management across unrelated business packages;
- do not add more concrete protocol, provider, storage, or strategy branches to core orchestration code.

When adding a capability expected to vary over time, first identify its stable contract, then connect it through an existing boundary. Do not design a general framework solely for hypothetical future implementations.

The following changes require an explicit, approved design and a phased migration plan:

- introducing a unified module host;
- changing the Canonical IR or core capability contracts;
- changing the execution order of request, response, or streaming stages;
- moving responsibilities between the microkernel and modules;
- introducing a new lifecycle model;
- introducing dynamic loading or a third-party plugin ABI;
- broadly restructuring existing protocol, provider, or configuration models.

## Protocol and Provider Boundaries

The target request path is:

```text
Client Wire
    ↓
Ingress Adapter
    ↓
Canonical IR
    ↓
Pipeline / Routing
    ↓
Provider Driver
    ↓
Egress Adapter
    ↓
Upstream
```

Responses and streaming events travel in the reverse direction.

### Ingress Adapter

An ingress adapter is responsible for:

- parsing requests, responses, and streaming frames in a northbound protocol;
- protocol-level structural validation;
- translating common protocol semantics into the Canonical IR;
- preserving valid protocol extensions that cannot be normalized;
- mapping internal results back to the client protocol.

An ingress adapter must not contain provider selection, provider authentication, retry, or vendor API logic.

### Canonical IR

The Canonical IR represents common semantics across protocols and providers.

A field belongs in the common IR only when it:

- is shared by multiple protocols or providers;
- has stable and well-defined cross-protocol semantics;
- is consumed by routing, quota, security, pipeline, or another core capability.

Do not continually expand the common IR for a single provider extension.

Protocol-specific information that must be preserved belongs in a namespaced, explicitly owned protocol extension area rather than in unconstrained common fields.

### Pipeline / Routing

Pipeline and routing are responsible for:

- executing system-defined processing stages;
- applying security, authentication, quota, and policy;
- selecting models, providers, and backends from normalized semantics;
- managing retry, failover, and termination conditions;
- propagating request context, cancellation, timeout, and telemetry information.

The microkernel controls the mandatory order of processing stages. Modules may participate only at declared extension points and may not arbitrarily change system invariants.

### Provider Driver

A provider driver owns provider-specific capabilities and behavior, including:

- provider capability declaration and negotiation;
- provider authentication;
- endpoint selection;
- provider defaults;
- request and response extensions;
- vendor-specific errors;
- vendor-specific streaming events;
- provider-specific request signing, headers, and parameter rules.

Provider extensions to a base protocol must be handled by the provider driver.

In the request direction:

1. Pipeline and routing produce a normalized, fully decided Canonical IR request.
2. The provider driver reads extensions that it owns.
3. The provider driver applies vendor rules and capability validation.
4. The egress adapter encodes the result into the upstream wire request.

In the response direction:

1. The egress adapter parses the upstream wire response.
2. The provider driver interprets vendor extensions, errors, and events.
3. Common semantics enter the Canonical IR.
4. Provider-specific information remains in the corresponding namespace.
5. Pipeline and the ingress adapter continue unified processing and client-protocol mapping.

A provider driver must not bypass:

- client authentication;
- mandatory security policies;
- quota accounting;
- routing decisions;
- retry budgets;
- unified error classification;
- the streaming state machine;
- request cancellation and resource release.

### Egress Adapter

An egress adapter encodes and decodes an upstream base protocol.

Multiple providers may share the same base protocol. The provider driver applies vendor differences on top of that protocol.

Do not duplicate an entire base-protocol conversion implementation for every provider unless the provider's wire protocol is genuinely incompatible.

## Extension Field Rules

Every extension field must have an explicit:

- namespace;
- owner;
- data type;
- lifecycle;
- set of processing stages where it may appear;
- passthrough and filtering rules;
- fallback or error behavior when unsupported.

Do not use unconstrained `map[string]any` values as cross-layer public contracts.

When upstream and downstream support the same extension, it may pass through after safety validation. Fields that can affect authentication, routing, request targets, billing, or security policy must never be passed through by default.

A module must not modify an extension namespace that it does not own.

## Configuration Principles

Every persistently operator-controlled gateway behavior that must remain consistent in standalone and managed modes must have one canonical, serializable configuration model and must be expressible in `config.yaml`.

This means:

- `config.yaml` is the canonical expression of configuration semantics;
- the Admin database and configuration synchronization are alternative storage or transport mechanisms for the same semantics;
- YAML, database, Admin API, and configuration synchronization must not maintain mutually incompatible configuration models;
- the same setting must have consistent meaning, defaults, and validation across sources;
- configuration precedence must be explicit and testable;
- configuration must be declarative data and must not contain functions, connections, clients, or other runtime objects.

The following do not need to be expressed by `config.yaml`:

- process bootstrap parameters and deployment-environment injection;
- database location or connection information;
- listen addresses;
- TLS files or key sources;
- secret references and external Secret identifiers;
- request contexts and cancellation signals;
- active connections, clients, and file handles;
- caches, health state, and statistics;
- other ephemeral or derived runtime state.

Secrets may be represented by references; plaintext secrets are not required in YAML.

Any new operator-visible setting that cannot be serialized through the canonical configuration model requires an explicit design justification. The inability to serialize a runtime object does not by itself indicate architectural coupling.

Configuration loading must be separated into:

1. parsing;
2. default application;
3. structural and semantic validation;
4. dependency resolution;
5. runtime resource construction;
6. atomic activation.

Invalid configuration must not be partially activated.

When hot replacement fails, retain the last known-good configuration snapshot where the relevant capability defines hot replacement, and report a diagnosable error.

## Layering and Dependency Direction

Follow the existing layers and package boundaries. Do not bypass them through circular dependencies, global state, or reverse imports.

The basic dependency direction is:

```text
Bootstrap / Transport / Admin
              ↓
Gateway / Runtime / Orchestration
              ↓
Protocol / Provider / Router / Quota capabilities
              ↓
Foundation contracts and data types
```

Concrete implementations depend on stable contracts. Stable contracts must not depend back on concrete implementations.

`internal/protocol` must not depend on Admin, WebUI, or a concrete deployment entry point.

Provider code must not depend on client HTTP handlers, Admin handlers, or WebUI code.

`internal/gateway` and runtime orchestration must not directly manipulate provider-specific wire fields.

Storage implementations must not own business rules. Services or domain layers own business semantics.

When adding or moving packages, inspect and update classifications and boundary tests in `tests/layering`.

## Simplicity Principle

Choose the smallest design that satisfies current explicit requirements and system invariants.

Do not introduce any of the following solely for hypothetical future requirements:

- abstraction layers;
- general-purpose frameworks;
- dependencies;
- configuration options;
- background services;
- compatibility layers;
- extension points;
- dynamic loading mechanisms.

Prefer:

- the Go standard library and existing dependencies;
- small, explicit interfaces;
- explicit dependencies;
- clear data ownership;
- immutable configuration snapshots;
- predictable control flow;
- pure conversion logic that can be tested independently;
- incremental, behavior-preserving refactoring.

Define interfaces around the capabilities that consumers actually need. Do not create a large interface merely to hide an equally large implementation.

Do not introduce a complex abstraction to remove a small amount of duplication. Extract a common capability only after the duplication demonstrates stable shared semantics.

Prefer readable, stable, diagnosable code over cleverness, excessive genericity, or minimum line count.

## Lifecycle and Reliability

Components that own any of the following resources must have an explicit lifecycle:

- goroutines;
- network clients and connections;
- database connections;
- files or streams;
- timers;
- subscriptions;
- cache refresh tasks;
- configuration synchronization tasks;
- telemetry exporters.

Lifecycle management must:

- be owned by the component that owns the resource;
- accept and propagate `context.Context`;
- support idempotent shutdown;
- release already-created resources when initialization fails;
- shut down in reverse dependency order;
- never create a goroutine that cannot be stopped or has no clear owner.

Errors must preserve enough context to identify the failing stage, module, and object, but must not leak secrets, credentials, or sensitive request content.

Do not silently ignore configuration errors, capability mismatches, data loss, or unsafe degradation.

## Coding Rules

- Keep changes focused and reversible. Do not mix unrelated cleanup into feature or refactor work.
- Reuse existing patterns and utilities before adding abstractions or dependencies.
- Inject dependencies through constructors or explicit parameters.
- Avoid mutable package-level state.
- Do not expose otherwise private internal APIs solely for tests.
- Test business logic through public behavior; package-private state machines may have internal tests.
- Prefer pure functions for protocol conversion and cover bidirectional and streaming behavior.
- When changing a public interface, update every implementation, caller, test, and relevant document.
- Continue to follow the repository-root multilingual rule: the canonical default for multilingual or i18n-capable values must be English.

## Generated Files

Do not edit generated artifacts directly, including but not limited to:

- `internal/storage/query/*.gen.go`;
- generated Protobuf `.pb.go` files;
- `webui/dist/`;
- files marked as generated or `DO NOT EDIT`.

After changing a generation source, regenerate artifacts with the repository's existing generation command.

When GORM models or query definitions change, run:

```bash
make go-gen-storage
```

For Protobuf changes, edit the source files under `proto/` and use the repository-defined Protobuf generation workflow. Do not edit generated Go files directly.

## Database Changes

The sources of truth for the Go database schema are:

- the GORM models under `internal/storage/model`;
- the model set registered by `model.All()`;
- explicit migration logic in the Go storage layer.

When changing the database schema, you must:

1. update the GORM models and model registration;
2. update required migration or compatibility logic;
3. regenerate `internal/storage/query`;
4. update `docs/schema/database.md`;
5. update relevant configuration or migration documentation;
6. verify the affected storage backends and migration paths.

The repository-root `deploy/schema/postgres.sql` and `deploy/schema/mysql.sql` files are derived reference files for the Rust implementation. Go database changes must not modify them.

Do not assume that application startup always migrates the database automatically. Automatic migration must be controlled by the current runtime mode and explicit configuration.

## Testing and Verification

Run the narrowest relevant test first, then expand verification in proportion to the impact of the change.

Unless a command states otherwise, run Go commands from `go/` and Make targets from the repository root.

At minimum, Go code changes require:

```bash
gofmt -w <changed Go files>
go test <affected packages> -count=1
```

For changes to public behavior, cross-package interfaces, or runtime orchestration, also run:

```bash
go test ./... -count=1
go vet ./...
go build ./...
```

For changes to the Canonical IR, protocol conversion, streaming conversion, or provider codecs, run:

```bash
go test -race ./tests/conversion/... -count=1
```

For package moves, dependency-direction changes, or architecture-boundary changes, run:

```bash
go test ./tests/layering -count=1
```

For WebUI changes, run the relevant commands from `webui/`:

```bash
pnpm test
pnpm lint
pnpm build
```

For storage-model or generated-query changes, regenerate the query package, run the relevant storage tests, and inspect the generated result for consistency with the models.

Before committing, run:

```bash
git diff --check
```

Never claim that verification passed unless it was actually run. If an environmental limitation prevents a check, state the skipped command and the reason explicitly.

## Documentation Consistency

When architecture, protocol boundaries, configuration semantics, database structure, or public behavior changes, update the relevant documentation under `go/docs/`.

Documentation must describe currently implemented behavior. Incomplete microkernel work must be labeled as a target design or migration plan, not presented as an existing capability.

When public documentation has multilingual versions, keep the English and Chinese content semantically aligned.
