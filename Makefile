GO_FILES := $(shell git ls-files '*.go')

.PHONY: lint test test-integration cover cover-gaps cover-gaps-all check-secrets build config-check

CONFIG ?= config/examples/hub.example.yaml

lint:
	@unformatted="$$(gofmt -l $(GO_FILES))"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

cover:
	go test -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out

# ADR-0008: no tracked .env files; run gitleaks when it is installed (CI must install it).
check-secrets:
	@tracked="$$(git ls-files | grep -E '(^|/)\.env(\..+)?$$' | grep -v -E '(^|/)\.env\.example$$' || true)"; \
	if [ -n "$$tracked" ]; then echo "tracked .env files are not allowed:"; echo "$$tracked"; exit 1; fi
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks detect --no-banner --redact; \
	else echo "gitleaks not installed: skipped repository secret scan (ADR-0008)"; fi

# Uncovered blocks across all packages; compare with docs/testing/coverage-gaps.md.
cover-gaps:
	bash scripts/cover-gaps.sh

# Same, including integration tests (needs Docker). Packages that only talk to the
# database are measured with this one.
cover-gaps-all:
	COVER_TAGS=integration bash scripts/cover-gaps.sh

build:
	go build ./...
	go build -o bin/messagehub ./cmd/messagehub

# Validate CONFIG and check that every environment variable it references is set.
# Exit code 2 means only environment variables are missing.
config-check: build
	bin/messagehub config check --config $(CONFIG)
