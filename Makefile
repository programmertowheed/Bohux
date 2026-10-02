.PHONY: help generate lint test test-integration conformance build run-dev

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

generate: ## Regenerate from proto/ (never hand-edit gen/)
	buf lint
	buf breaking --against '.git#branch=main'
	buf generate

lint: ## Lint Go and proto
	golangci-lint run ./...
	buf lint

test: ## Unit tests
	go test ./... -race -count=1

test-integration: ## Integration tests (needs docker)
	go test ./... -race -count=1 -tags=integration

conformance: ## Provider conformance — the gate for any new provider
	go test ./providers/_conformance -provider=$(PROVIDER) -target=$(TARGET) -v

build: ## Build the single binary
	go build -o bin/kiln ./cmd/kiln

run-dev: ## Local stack: postgres, temporal
	docker compose -f deploy/dev/compose.yaml up -d
