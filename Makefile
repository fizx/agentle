# agentle developer tasks. Most flows are plain `go` commands; these are shortcuts.

.PHONY: build test race cover web run tidy

# Build the all-in-one server (embeds the prebuilt dashboard in web/dist).
build:
	go build -o bin/agentle ./cmd/agentle

# Run the backend test suite.
test:
	go test ./...

# Run tests with the race detector (covers parallel_map + the dispatcher).
race:
	go test -race ./...

# Enforce the test-coverage threshold (override with COVERAGE_MIN=NN).
cover:
	./scripts/coverage.sh

# Rebuild the embedded dashboard.
web:
	cd web && npm install && npm run build

# Run the server against ./data (no Docker/Postgres/Redis needed).
run:
	go run ./cmd/agentle

tidy:
	go mod tidy
