GO_FILES := $(shell git ls-files '*.go')

.PHONY: lint test test-integration cover build

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

build:
	go build ./...
