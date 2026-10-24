GO_FILES := $(shell git ls-files '*.go')

.PHONY: lint test test-integration cover cover-gaps check-secrets build

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

build:
	go build ./...
