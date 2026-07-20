.PHONY: dev build server server-slim tools check test test-core test-server fmt fmt-check clean webui smoke smoke-storage release-check go-build go-test go-conversion-tests go-conversion-update go-vet go-fmt go-fmt-check go-lint go-lint-install go-check go-tidy go-gen-storage go-webui-build go-webui-embed-assets go-webui-embed-build go-webui-embed-run go-run help

# golangci-lint version pinned for reproducible fmt/lint (installed to go/bin, never touches go.mod)
GOLANGCI_LINT_VERSION := v2.6.0
GOLANGCI_LINT := $(CURDIR)/go/bin/golangci-lint

# Development — start Tauri desktop app with hot reload
dev: webui-build
	cargo tauri build --config src-tauri/tauri.dev.conf.json

# Build desktop app (release)
build: webui-build
	cargo tauri build

# Build server binary only (release, webui embedded)
server: webui-build
	cargo build -p nyro-server --release

# Build slim server binary (release, no embedded webui, no pnpm build required)
server-slim:
	cargo build -p nyro-server --release --no-default-features

# Build nyro-tools binary (release)
tools:
	cargo build -p nyro-tools --release

# Run server binary locally (debug, webui embedded)
server-dev: webui-build
	cargo run -p nyro-server -- --proxy-port 19530 --admin-port 19531

# Build webui
webui-build:
	cd webui && pnpm install && pnpm build

# Format all Rust code
fmt:
	cargo fmt --all

# Check Rust formatting (non-destructive, used in CI)
fmt-check:
	cargo fmt --all -- --check

# Type check & lint everything
check: fmt-check
	cargo check --workspace
	cd webui && pnpm build

# Run all Rust tests across the workspace
test:
	cargo test --workspace --exclude nyro-desktop --no-default-features

# Run tests for nyro-core only
test-core:
	cargo test -p nyro-core

# Run tests for nyro-server only
test-server:
	cargo test -p nyro-server --no-default-features

# End-to-end smoke (local mock upstream + nyro-server)
smoke:
	python3 scripts/smoke/server_smoke.py

# Storage backend smoke (default: sqlite + postgres)
smoke-storage:
	python3 scripts/smoke/storage_backends_smoke.py

# Pre-release verification gate
release-check: check smoke

# ── Go (nyro/go) — gateway + admin (data plane + control plane) ──
# Build the Go nyro CLI into go/bin/nyro
go-build:
	cd go && mkdir -p bin && go build -o bin/nyro .

# Run all Go tests
go-test:
	cd go && go test ./...

# Protocol-conversion matrix tests (internal/protocoltest): offline cassette
# replay, race-enabled. This is the every-PR gate — no keys, no network.
go-conversion-tests:
	cd go && go test -race ./internal/protocoltest/...

# Re-record cassettes from live providers and regenerate golden files. LOCAL
# ONLY — needs real credentials and hits real providers. Reads
# NYRO_TEST_API_KEY / NYRO_TEST_BASE_URL / NYRO_TEST_MODEL (or per-provider
# NYRO_TEST_<PROVIDER>_*; see .env.example). With only the generic vars, record
# one provider at a time via RUN, e.g.:
#   make go-conversion-update RUN=anthropic__openai
go-conversion-update:
	cd go && NYRO_TEST_RECORD=1 go test ./internal/protocoltest/... -count=1 -update \
		-run 'TestConversionMatrix/$(RUN)'

# Vet Go code
go-vet:
	cd go && go vet ./...

# Install golangci-lint into go/bin at the pinned version (idempotent, no go.mod/go.sum impact)
go-lint-install:
	@$(GOLANGCI_LINT) --version 2>/dev/null | grep -q $(GOLANGCI_LINT_VERSION:v%=%) || \
		GOBIN=$(CURDIR)/go/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Format Go code (gofumpt + goimports, in place)
go-fmt: go-lint-install
	cd go && $(GOLANGCI_LINT) fmt

# Check Go formatting without modifying files (used in CI)
go-fmt-check: go-lint-install
	cd go && $(GOLANGCI_LINT) fmt --diff

# Lint Go code (errcheck, staticcheck, unused, bodyclose, unconvert, ...)
go-lint: go-lint-install
	cd go && $(GOLANGCI_LINT) run

# Format, vet, lint, and test the Go module
go-check: go-fmt-check go-vet go-lint go-test

# Tidy go.mod / go.sum
go-tidy:
	cd go && go mod tidy

# Generate typed GORM query code for the Go storage backend
go-gen-storage:
	cd go && go run ./internal/storage/gen

# ── Go schema migrations (mysql/postgres only) ──
# No Makefile targets: schema is GORM AutoMigrate; to preview/apply DDL for a
# DDL-less deployment use the `nyro migrate dump`/`diff` subcommands (they only
# depend on GORM). See go/docs/schema/migrations.md.

# Build the Go WebUI bundle only
go-webui-build:
	cd go/webui && pnpm install && pnpm build

# Copy the built Go WebUI into the Go package-local embed directory
go-webui-embed-assets: go-webui-build
	rm -rf go/internal/webui/dist
	mkdir -p go/internal/webui/dist
	cp -R go/webui/dist/. go/internal/webui/dist/

# Build the Go nyro CLI with the Go WebUI embedded
go-webui-embed-build: go-webui-embed-assets
	cd go && mkdir -p bin && go build -tags webui_embed -o bin/nyro .

# Build and run the Go admin with embedded WebUI for local preview.
# --config-listen defaults to 127.0.0.1:19532 (config-sync gRPC server, a
# *separate* port from --listen's HTTP REST/WebUI), so
# `nyro gateway --config-server 127.0.0.1:19532` can connect for config
# hot-reload with no extra flags here. With no --config-tls-* paths, both
# processes use plaintext config-sync and log a security warning.
# --auto-migrate lets this first-boot admin create its own (default sqlite)
# schema; it's off by default regardless of backend (see
# go/docs/schema/database.md).
go-webui-embed-run: go-webui-embed-build
	cd go && ./bin/nyro admin --auto-migrate

# Run the Go gateway (data plane) locally
go-run:
	cd go && go run . gateway

# Clean all build artifacts
clean:
	cargo clean
	rm -rf webui/dist webui/node_modules/.vite go/webui/dist go/webui/node_modules/.vite go/internal/webui/dist

help:
	@echo "Nyro AI Gateway"
	@echo ""
	@echo "  make dev          Start Tauri desktop app (dev mode)"
	@echo "  make build        Build desktop app (release)"
	@echo "  make server       Build server binary (release, webui embedded)"
	@echo "  make server-slim  Build slim server binary (release, no embedded webui)"
	@echo "  make tools        Build nyro-tools binary (release)"
	@echo "  make server-dev   Run server binary (debug)"
	@echo "  make webui-build  Build frontend only"
	@echo "  make fmt          Format Rust code"
	@echo "  make fmt-check    Check Rust formatting (CI)"
	@echo "  make check        Type check Rust + TypeScript"
	@echo "  make test         Run all Rust tests (workspace)"
	@echo "  make test-core    Run nyro-core tests only"
	@echo "  make test-server  Run nyro-server tests only"
	@echo "  make smoke        Run local server smoke tests"
	@echo "  make smoke-storage Run storage smoke tests (default sqlite + postgres)"
	@echo "  make release-check Run check + smoke before release"
	@echo ""
	@echo "  Go (nyro/go):"
	@echo "  make go-build     Build Go nyro CLI → go/bin/nyro"
	@echo "  make go-test      Run Go tests"
	@echo "  make go-conversion-tests   Protocol-conversion matrix (offline replay, -race)"
	@echo "  make go-conversion-update  Re-record cassettes + goldens (local, needs keys)"
	@echo "  make go-vet       Vet Go code"
	@echo "  make go-fmt       Format Go code (gofumpt + goimports)"
	@echo "  make go-fmt-check Check Go formatting (CI)"
	@echo "  make go-lint      Lint Go code (golangci-lint)"
	@echo "  make go-check     go-fmt-check + go-vet + go-lint + go-test"
	@echo "  make go-tidy      Tidy go.mod/go.sum"
	@echo "  make go-gen-storage Generate Go storage query code"
	@echo "  (schema: GORM AutoMigrate; preview/apply DDL via 'nyro migrate dump|diff')"
	@echo "  make go-webui-build Build Go WebUI frontend only"
	@echo "  make go-webui-embed-build Build Go nyro CLI with embedded Go WebUI"
	@echo "  make go-webui-embed-run Build and run Go admin with embedded Go WebUI"
	@echo "  make go-run       Run Go gateway (data plane)"
	@echo "  make clean        Remove build artifacts"
