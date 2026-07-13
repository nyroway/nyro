# Config-Sync CLI Defaults and Polling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Simplify Admin/Gateway config-sync startup so plaintext is the default transport, mTLS is selected by complete certificate flags, and epoch polling is explicit opt-in.

**Architecture:** Config-sync transport is derived only from whether all three TLS paths are present. Admin creates an `EpochWatcher` only for a positive polling interval; otherwise local writes notify the broadcaster directly. HTTP TLS for Admin and Gateway remains the responsibility of the deployment proxy, ingress, load balancer, or service mesh.

**Tech Stack:** Go, Cobra, gRPC, `crypto/tls`, `log/slog`

## Global Constraints

- Remove `--config-insecure` without a compatibility period.
- Gateway must explicitly set exactly one of `--config-file` and `--config-server`.
- The three `config-tls-*` paths must be all empty or all configured; TLS load failures never fall back to plaintext.
- Keep listener defaults: Admin `127.0.0.1:19531`, config-sync `127.0.0.1:19532`, Gateway `0.0.0.0:19530`.
- Keep Admin REST/WebUI and Gateway client API as HTTP; deployment infrastructure terminates HTTPS.
- Do not change database schema.

---

### Task 1: Simplify config-sync TLS mode

**Files:**
- Modify: `cmd/admin/admin.go`, `cmd/admin/configtls_test.go`
- Modify: `cmd/gateway/gateway.go`, `cmd/gateway/configtls_test.go`

- [ ] Write failing tests for no-flags plaintext, complete mTLS, partial flag rejection, and TLS load failure.
- [ ] Remove `--config-insecure` and the resolver boolean parameter from both commands.
- [ ] Make no TLS paths select plaintext with a security WARN; keep complete paths as mTLS and partial paths as errors.
- [ ] Run `GOCACHE=/private/tmp/nyro-go-build-cache go test ./cmd/admin ./cmd/gateway -run 'ConfigSync.*TLS' -count=1`.

### Task 2: Make epoch polling explicit opt-in

**Files:**
- Modify: `cmd/admin/admin.go`, `cmd/admin/admin_test.go`
- Verify: `internal/admin/broadcast_test.go`, `internal/configsync/epochwatch_test.go`

- [ ] Write failing tests for the `0` default and a disabled watcher that performs no epoch read.
- [ ] Add a focused watcher-start helper that rejects negative intervals, returns nil without seeding for zero, and seeds/runs for a positive duration.
- [ ] Remove all database-backend gates and non-shared-backend warnings.
- [ ] Keep direct `Broadcaster.Notify()` when no watcher is installed.
- [ ] Run focused Admin, broadcaster, and watcher tests.

### Task 3: Validate option combinations and warn about exposed Admin APIs

**Files:**
- Modify: `cmd/admin/admin.go`, `cmd/admin/admin_test.go`
- Modify: `cmd/gateway/gateway.go`, `cmd/gateway/gateway_test.go`

- [ ] Write failing tests for negative polling, polling/TLS flags with disabled config-listen, config-file/TLS conflicts, and config-source XOR.
- [ ] Add preflight option validation so irrelevant flags fail instead of being ignored.
- [ ] Warn when Admin listens on a non-loopback address without `--token`; treat `127.0.0.1`, `::1`, and `localhost` as loopback.
- [ ] Run `GOCACHE=/private/tmp/nyro-go-build-cache go test ./cmd/admin ./cmd/gateway -count=1`.

### Task 4: Update documentation and comments

**Files:**
- Modify: `docs/security/config-sync-mtls.md`, `docs/cutover.md`
- Modify: `internal/configsync/client.go`, `internal/configsync/server.go`, `internal/configsync/pki/expiry.go`
- Modify: `../Makefile`

- [ ] Remove active `--config-insecure` references.
- [ ] Document plaintext + WARN, complete mTLS, and partial-flag failure.
- [ ] Document local, standalone, trusted-network plaintext, mTLS, multi-Admin polling, and config-sync-disabled deployments.
- [ ] Document deployment-layer HTTPS and optional Admin token behavior.

### Task 5: Final verification

- [ ] Run `gofmt` on modified Go files.
- [ ] Run `GOCACHE=/private/tmp/nyro-go-build-cache go test ./... -count=1`.
- [ ] Run `GOCACHE=/private/tmp/nyro-go-build-cache go vet ./...` and `go build ./...`.
- [ ] Run `git diff --check` and review the final branch diff.
