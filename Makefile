.PHONY: dev build server server-slim tools check test test-core test-server fmt fmt-check clean webui smoke smoke-storage release-check go-build go-test go-vet go-fmt go-tidy go-run help

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

# Vet Go code
go-vet:
	cd go && go vet ./...

# Format Go code
go-fmt:
	cd go && go fmt ./...

# Tidy go.mod / go.sum
go-tidy:
	cd go && go mod tidy

# Run the Go gateway (data plane) locally
go-run:
	cd go && go run . gateway

# Clean all build artifacts
clean:
	cargo clean
	rm -rf webui/dist webui/node_modules/.vite

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
	@echo "  make go-vet       Vet Go code"
	@echo "  make go-fmt       Format Go code"
	@echo "  make go-tidy      Tidy go.mod/go.sum"
	@echo "  make go-run       Run Go gateway (data plane)"
	@echo "  make clean        Remove build artifacts"
