GO_FILES := $(shell git ls-files '*.go')

.PHONY: lint test test-integration cover cover-gaps build

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

# Uncovered blocks across all packages; compare with docs/testing/coverage-gaps.md.
cover-gaps:
	bash scripts/cover-gaps.sh

build:
	go build ./...
