.PHONY: test test-unit test-integration test-sdk test-all bench build clean demo-up demo-down demo-test

# Build
build:
	go build -o proxy ./cmd/proxy
	go build -o provider ./cmd/provider
	go build -o mockllm ./cmd/mockllm

# Unit tests (fast, no external deps)
test-unit:
	go test ./api/... ./internal/... -count=1

# Integration tests (embedded NATS, mock upstream)
test-integration:
	go test ./test/integration/... -count=1

# JS SDK tests (requires NATS on localhost:4222 for client tests)
test-sdk:
	cd sdk/js && npx vitest run

# All Go tests
test: test-unit test-integration

# Everything
test-all: test test-sdk

# Benchmarks
bench:
	go test ./test/benchmark/... -bench=. -benchmem -count=1

# Verbose test output
test-v:
	go test ./api/... ./internal/... ./test/integration/... -v -count=1

# Demo (multi-provider with mock LLM)
demo-up:
	docker compose -f demo/docker-compose.yaml up --build -d

demo-down:
	docker compose -f demo/docker-compose.yaml down

demo-test:
	bash demo/demo.sh

clean:
	rm -f proxy provider mockllm
