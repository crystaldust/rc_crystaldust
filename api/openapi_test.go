package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"messagehub/internal/domain"
)

const (
	specPath           = "openapi.yaml"
	validFixtureDir    = "testdata/events/valid"
	invalidFixtureDir  = "testdata/events/invalid"
	ingestSchemaName   = "IngestEventRequest"
	minValidFixtures   = 4
	minInvalidFixtures = 8
)

type fixture struct {
	name    string
	raw     []byte
	isValid bool
}

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load %s: %v", specPath, err)
	}
	return doc
}

func ingestSchema(t *testing.T, doc *openapi3.T) *openapi3.Schema {
	t.Helper()
	ref, ok := doc.Components.Schemas[ingestSchemaName]
	if !ok || ref.Value == nil {
		t.Fatalf("components.schemas.%s not found", ingestSchemaName)
	}
	return ref.Value
}

func readFixtures(t *testing.T, dir string, isValid bool) []fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	fixtures := make([]fixture, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		fixtures = append(fixtures, fixture{name: filepath.Base(p), raw: raw, isValid: isValid})
	}
	return fixtures
}

func allFixtures(t *testing.T) []fixture {
	t.Helper()
	valid := readFixtures(t, validFixtureDir, true)
	invalid := readFixtures(t, invalidFixtureDir, false)
	if len(valid) < minValidFixtures {
		t.Fatalf("got %d valid fixtures, want at least %d", len(valid), minValidFixtures)
	}
	if len(invalid) < minInvalidFixtures {
		t.Fatalf("got %d invalid fixtures, want at least %d", len(invalid), minInvalidFixtures)
	}
	return append(valid, invalid...)
}

// validateWithSchema validates a raw body the way an OpenAPI request validator
// would. Format validation is enabled so `format: date-time` is checked too.
func validateWithSchema(schema *openapi3.Schema, raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return schema.VisitJSON(value, openapi3.VisitAsRequest(), openapi3.MultiErrors(), openapi3.EnableFormatValidation())
}

func TestSpecIsValid(t *testing.T) {
	// Arrange
	doc := loadSpec(t)

	// Act
	err := doc.Validate(context.Background(), openapi3.EnableExamplesValidation())

	// Assert
	if err != nil {
		t.Fatalf("openapi.yaml is invalid: %v", err)
	}
}

func TestIngestSchemaAgainstFixtures(t *testing.T) {
	schema := ingestSchema(t, loadSpec(t))

	for _, f := range allFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			// Act
			err := validateWithSchema(schema, f.raw)

			// Assert
			if f.isValid && err != nil {
				t.Fatalf("valid fixture rejected by schema: %v", err)
			}
			if !f.isValid && err == nil {
				t.Fatal("invalid fixture accepted by schema")
			}
		})
	}
}

// TestSchemaAndDomainValidatorAgree guards against drift between the OpenAPI
// request schema and domain.ParseEnvelope. The payload size limit (262144
// bytes, HTTP 413) and the metadata key pattern cannot be expressed in the
// OpenAPI 3.0 schema, so the shared fixtures deliberately contain no case that
// depends on them; TestServerOnlyRules covers those rules separately.
func TestSchemaAndDomainValidatorAgree(t *testing.T) {
	schema := ingestSchema(t, loadSpec(t))

	for _, f := range allFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			// Act
			schemaErr := validateWithSchema(schema, f.raw)
			_, domainErr := domain.ParseEnvelope(f.raw)

			// Assert
			if (schemaErr == nil) != (domainErr == nil) {
				t.Fatalf("validators disagree: schema err = %v, domain err = %v", schemaErr, domainErr)
			}
			if (domainErr == nil) != f.isValid {
				t.Fatalf("domain validator: err = %v, fixture valid = %v", domainErr, f.isValid)
			}
		})
	}
}

// TestServerOnlyRules pins down the rules the schema cannot express: the schema
// accepts these bodies and only domain.ParseEnvelope rejects them. If a future
// schema change starts enforcing one of them, move the case into the shared
// fixtures.
func TestServerOnlyRules(t *testing.T) {
	oversizedPayload := `{"blob":"` + strings.Repeat("x", domain.MaxPayloadBytes) + `"}`
	tests := []struct {
		name          string
		body          string
		wantTooLarge  bool
		wantBadFields string
	}{
		{
			name:         "payload over 256 KiB",
			body:         `{"source_event_id":"a","event_type":"order.paid","occurred_at":"2026-10-24T08:30:00Z","payload":` + oversizedPayload + `}`,
			wantTooLarge: true,
		},
		{
			name:          "metadata key with disallowed character",
			body:          `{"source_event_id":"a","event_type":"order.paid","occurred_at":"2026-10-24T08:30:00Z","payload":{},"metadata":{"bad key":"v"}}`,
			wantBadFields: "metadata.bad key",
		},
		{
			name:          "metadata key longer than 64 characters",
			body:          `{"source_event_id":"a","event_type":"order.paid","occurred_at":"2026-10-24T08:30:00Z","payload":{},"metadata":{"` + strings.Repeat("k", 65) + `":"v"}}`,
			wantBadFields: "metadata." + strings.Repeat("k", 65),
		},
	}
	schema := ingestSchema(t, loadSpec(t))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			raw := []byte(tt.body)

			// Act
			schemaErr := validateWithSchema(schema, raw)
			_, domainErr := domain.ParseEnvelope(raw)

			// Assert
			if schemaErr != nil {
				t.Fatalf("schema now rejects this body (%v); move the case into the shared fixtures", schemaErr)
			}
			if tt.wantTooLarge && !domain.IsPayloadTooLarge(domainErr) {
				t.Fatalf("domain err = %v, want ErrPayloadTooLarge", domainErr)
			}
			if tt.wantBadFields != "" {
				var verr *domain.ValidationError
				if !errors.As(domainErr, &verr) || !verr.HasField(tt.wantBadFields) {
					t.Fatalf("domain err = %v, want a problem at %q", domainErr, tt.wantBadFields)
				}
			}
		})
	}
}

func TestIngestSchemaIsClosed(t *testing.T) {
	// Arrange
	schema := ingestSchema(t, loadSpec(t))

	// Act
	_, hasSource := schema.Properties["source"]
	additional := schema.AdditionalProperties

	// Assert
	if hasSource {
		t.Error("IngestEventRequest must not declare a source property; source is derived from the API key")
	}
	if additional.Has == nil || *additional.Has {
		t.Error("IngestEventRequest must set additionalProperties: false")
	}
	if additional.Schema != nil {
		t.Error("IngestEventRequest must not allow additional properties of any schema")
	}
}
