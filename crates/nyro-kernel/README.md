# nyro-kernel

[中文](README_CN.md)

Workload-neutral lifecycle and generation management for statically composed Rust applications.
The experimental, source-built root `nyro proxy` uses this crate for its typed runtime generation
and request leases. Existing `nyro-core`, released `nyro-server`, and Tauri request paths do not use
it. The Rust API remains experimental.

## Boundary

The kernel owns dependency validation, resource startup/rollback, atomic runtime publication,
generation leases, retirement, and shutdown. It depends only on Tokio, tokio-util, and async-trait.

Bootstrap builds a typed `Candidate<T>` and serializes configuration comparison/build/activation.
Version and fingerprint are opaque metadata: identical fingerprints do **not** skip activation here.
LLM authorization, routing, retries, streaming rules, configuration, HTTP, database access, and
provider catalogs stay outside the kernel. Pure codecs do not need dummy lifecycle implementations.
There is no global registry, service locator, dynamic loading, or business request context.

## Ownership and guarantees

- Construct candidates without starting tasks, registering global state, or mutating the live runtime.
  All allocations must be RAII-owned even if the candidate is dropped or rejected before startup.
- `activate()` transfers ownership immediately. Dropping its returned waiter does not cancel the
  transaction. Use the supplied `Context.cancellation` to cancel; it no longer affects a published
  generation. Activation deadlines include queueing and startup, but rollback gets a fresh cleanup budget.
- Validate nonempty, unique IDs, existing dependencies, and acyclicity before starting anything.
  Start in topological order, choosing the smallest ready ID first. Repeated dependencies are harmless.
- Publish `T` and its metadata together only after every start succeeds. Failed/cancelled candidates
  leave the active generation unchanged. Rollback stops all candidate allocations, including the
  partially started component and components never started. Started resources stop in reverse order.
  Invalid graphs have no dependency order, so their allocations stop in reverse input order.
- Every lease clone pins the generation. Keep leases through the whole stream and required finalizers.
  Normal reload has **no forced drain timeout**. Request deadlines and cancellation are the caller's
  responsibility. `Lease::cancellation()` returns an independent child signal of the generation.
- `shutdown()` immediately rejects new leases and activations and cancels pending startup. It drains
  existing leases for up to 30 seconds by default, then cancels remaining generation uses. Cleanup
  gets a separate 5-second budget; both durations are configurable. Lease-free generations begin
  cleanup immediately, including while an activation is blocked. Already running cleanup keeps its
  earlier deadline. Zero cleanup budget still calls every `begin_stop`, but reports incomplete cleanup.
- Cleanup calls `begin_stop()` then `wait_stopped()` for each resource in reverse order. One failure
  never skips another owner. Startup errors include rollback errors. Status retains cleanup failures;
  shutdown reports them, including earlier retirement/rollback failures. Repeated shutdown waiters
  receive the same completed result, even if another waiter was dropped.
- A rejected candidate is never started: its owners receive synchronous `begin_stop` and are dropped,
  without an asynchronous join. `Host::drop` initiates owned shutdown; resource Drop is a synchronous
  cancellation fallback, **not** proof of completed cleanup. Await shutdown before stopping Tokio.

Lifecycle methods, future polling, and destructors must not block or panic. Startup and stop-wait
futures must be safe to drop; background tasks/handles remain owned by their lifecycle object.
Each component has an independent start cancellation signal, set immediately before its `begin_stop`.
Dependency workers remain available until their dependents finish waiting (or cleanup times out).
Generation-use cancellation is separate. The stop context also has a separate phase-local signal,
allowing cleanup after startup cancellation. Keep deadlines local to their phase.
This is cooperative in-process lifecycle management, not isolation for untrusted plugins. Timeouts
cannot preempt blocking code, revoke a borrowed `&T`, or guarantee that an external operation stopped.

`T` is structurally replaced as one generation; the kernel cannot enforce deep immutability of handles
inside it. Process-wide listeners, database pools, and truly shared quota/health state belong to
bootstrap. Generation components must release only their own bindings, not close a pool another
generation uses. `Status.accepting` reports kernel admission, not complete business health.

## Example

The worker owns its task even when a lifecycle future is cancelled. Replace `Runtime` with your
application's typed runtime and explicitly supply its generation-owned components.

```rust
use std::time::Duration;
use async_trait::async_trait;
use nyro_kernel::{Candidate, CancellationToken, Component, Context, Host, HostOptions, Lifecycle};
use tokio::{task::JoinHandle, time::Instant};

struct Worker {
    stop: CancellationToken,
    task: Option<JoinHandle<()>>,
}

#[async_trait]
impl Lifecycle for Worker {
    async fn start(&mut self, context: &Context) -> Result<(), String> {
        self.stop = context.cancellation.child_token();
        let stop = self.stop.clone();
        self.task = Some(tokio::spawn(async move { stop.cancelled().await }));
        Ok(())
    }

    fn begin_stop(&mut self) { self.stop.cancel(); }

    async fn wait_stopped(&mut self, _: &Context) -> Result<(), String> {
        if let Some(task) = self.task.as_mut() {
            task.await.map_err(|error| error.to_string())?;
        }
        self.task = None;
        Ok(())
    }
}

impl Drop for Worker {
    fn drop(&mut self) {
        self.stop.cancel();
        if let Some(task) = &self.task { task.abort(); }
    }
}

struct Runtime { label: &'static str }

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
let host = Host::new(HostOptions::default());
host.activate(Candidate {
    version: "v1".into(),
    fingerprint: None,
    value: Runtime { label: "ready" },
    components: vec![Component {
        id: "worker".into(),
        after: vec![],
        lifecycle: Box::new(Worker { stop: CancellationToken::new(), task: None }),
    }],
}, Context {
    deadline: Instant::now() + Duration::from_secs(10),
    cancellation: CancellationToken::new(),
}).await?;
let lease = host.acquire()?;
assert_eq!(lease.value().label, "ready");
drop(lease);
host.shutdown().await?;
Ok(())
}
```

## Verification

```sh
cargo test -p nyro-kernel
cargo clippy -p nyro-kernel --all-targets -- -D warnings
cargo tree -p nyro-kernel --edges normal
```
