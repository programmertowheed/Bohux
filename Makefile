.PHONY: help generate lint test test-integration conformance build image run-dev footprint

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

test-integration: ## Integration tests (needs BOHUX_TEST_POSTGRES_URL, see docs/reference/config.md)
	go test ./... -race -count=1 -tags=integration

conformance: ## Provider conformance — the gate for any new provider
	go test ./providers/_conformance -provider=$(PROVIDER) -target=$(TARGET) -v

build: ## Build the single binary
	go build -o bin/bohux ./cmd/bohux

image: ## Build the control plane image (ADR-0030); VERSION=x.y.z to stamp it
	docker build -t bohux:$(or $(VERSION),dev) --build-arg VERSION=$(or $(VERSION),dev) .

run-dev: ## Local stack: postgres, temporal, openbao, buildkit, caddy
	docker compose -f deploy/dev/compose.yaml up -d

footprint: ## Measure stack memory for the ADR-0020 gate (run on the target VPS)
	scripts/footprint.sh
