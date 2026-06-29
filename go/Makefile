# Nyro gateway (Go) build targets.
# Self-contained — invoke via `make -C go <target>` or from within go/.
# Kept inside go/ so the Go port does not pollute the repo root.

.PHONY: build test vet fmt tidy run

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

run:
	go run ./cmd/nyro-server
