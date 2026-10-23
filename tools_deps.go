//go:build tools

// Temporary during Phase 2 implementation: keeps pinned dependencies in go.mod
// while packages that import them are written in parallel. Removed at the end.
package tools

import (
	_ "github.com/getkin/kin-openapi/openapi3"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/pressly/goose/v3"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "go.yaml.in/yaml/v3"
)
