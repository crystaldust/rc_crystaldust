package config

import (
	"encoding/json"
	"strings"
	"testing"

	"messagehub/internal/domain"
)

func TestFieldPath(t *testing.T) {
	var instance any
	if err := json.Unmarshal([]byte(`{
		"destinations": [ { "request": { "headers": { "200": "x", "X-A.b": "y" } } } ],
		"list": [[1]]
	}`), &instance); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		tokens []string
		want   string
	}{
		{"root", nil, rootField},
		{"top key", []string{"destinations"}, "destinations"},
		{"array index", []string{"destinations", "0", "request"}, "destinations[0].request"},
		{"numeric map key stays a key", []string{"destinations", "0", "request", "headers", "200"}, "destinations[0].request.headers.200"},
		{"dotted key is quoted", []string{"destinations", "0", "request", "headers", "X-A.b"}, `destinations[0].request.headers["X-A.b"]`},
		{"nested arrays", []string{"list", "0", "0"}, "list[0][0]"},
		{"past a scalar", []string{"list", "0", "0", "x"}, "list[0][0].x"},
		{"out of range index", []string{"list", "9", "x"}, "list[9].x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldPath(tt.tokens, instance); got != tt.want {
				t.Fatalf("fieldPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSchemaReportsEveryStructuralProblem(t *testing.T) {
	// Arrange: two independent schema problems in one file.
	data := []byte(strings.Join([]string{
		"version: 1",
		"principals: []",
		"policies: []",
		"destinations: []",
		"routes: []",
		"extra_a: 1",
		"extra_b: 2",
	}, "\n"))

	// Act
	_, err := Parse(data)

	// Assert
	problems := validationProblems(t, err)
	if !hasProblem(problems, "extra_a", reasonUnknownField) || !hasProblem(problems, "extra_b", reasonUnknownField) {
		t.Fatalf("problems = %v, want both unknown fields", problems)
	}
}

func TestEmptyCollectionsAreValid(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nprincipals: []\npolicies: []\ndestinations: []\nroutes: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.RoutesForEventType("a.b") != nil || len(cfg.Routes()) != 0 {
		t.Fatal("expected no routes")
	}
}

func TestNonStringYAMLKeys(t *testing.T) {
	// A YAML map with an integer key must not crash the JSON conversion; the
	// schema rejects the unknown field instead.
	_, err := Parse([]byte("version: 1\nprincipals: []\npolicies: []\ndestinations: []\nroutes: []\n1: x\n"))
	problems := validationProblems(t, err)
	if !hasProblem(problems, "1", reasonUnknownField) {
		t.Fatalf("problems = %v, want unknown field 1", problems)
	}
}

func TestDedupeProblems(t *testing.T) {
	in := validationProblemsFrom("a", "x", "a", "x", "b", "y")
	if got := dedupeProblems(in); len(got) != 2 {
		t.Fatalf("dedupe = %v, want 2", got)
	}
}

func validationProblemsFrom(pairs ...string) []domain.FieldError {
	out := make([]domain.FieldError, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, domain.FieldError{Field: pairs[i], Reason: pairs[i+1]})
	}
	return out
}
