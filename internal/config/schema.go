package config

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"messagehub/internal/domain"
	"messagehub/schemas"
)

const (
	reasonUnknownField    = "unknown field"
	reasonMissingField    = "required field missing"
	reasonNotAllowedHere  = "field not allowed here"
	reasonNoSchemaVariant = "does not match any allowed form"
)

// compiledSchema compiles the embedded schema once per process.
var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.ConfigSchema()))
	if err != nil {
		return nil, fmt.Errorf("parse embedded config schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(schemas.ConfigSchemaURL, doc); err != nil {
		return nil, fmt.Errorf("add embedded config schema: %w", err)
	}
	return c.Compile(schemas.ConfigSchemaURL)
})

// validateSchema validates the instance and converts failures into a
// *domain.ValidationError with config-style field paths.
func validateSchema(instance any) error {
	sch, err := compiledSchema()
	if err != nil {
		return err
	}
	err = sch.Validate(instance)
	var verr *jsonschema.ValidationError
	if err == nil {
		return nil
	}
	if !errors.As(err, &verr) {
		return fmt.Errorf("validate config schema: %w", err)
	}
	problems := dedupeProblems(schemaProblems(verr, instance))
	return &domain.ValidationError{Problems: problems}
}

// schemaProblems flattens the error tree into leaf problems. For oneOf /
// anyOf failures only the closest branch (fewest leaf problems) is kept.
func schemaProblems(e *jsonschema.ValidationError, instance any) []domain.FieldError {
	if len(e.Causes) == 0 {
		return leafProblems(e, instance)
	}
	switch e.ErrorKind.(type) {
	case *kind.OneOf, *kind.AnyOf:
		return closestBranch(e, instance)
	}
	var out []domain.FieldError
	for _, c := range e.Causes {
		out = append(out, schemaProblems(c, instance)...)
	}
	return out
}

// closestBranch keeps the problems of the branch the instance came closest
// to: branches failing on type / const (a different form entirely) rank
// below branches that only fail on details; then fewer problems win.
func closestBranch(e *jsonschema.ValidationError, instance any) []domain.FieldError {
	var best []domain.FieldError
	bestScore := -1
	for _, c := range e.Causes {
		p := schemaProblems(c, instance)
		score := len(p)
		if failsOnForm(c) {
			score += wrongFormPenalty
		}
		if bestScore < 0 || score < bestScore {
			best, bestScore = p, score
		}
	}
	if len(best) == 0 {
		return []domain.FieldError{{Field: fieldPath(e.InstanceLocation, instance), Reason: reasonNoSchemaVariant}}
	}
	return best
}

// wrongFormPenalty outranks any realistic count of detail problems.
const wrongFormPenalty = 1000

func failsOnForm(e *jsonschema.ValidationError) bool {
	switch e.ErrorKind.(type) {
	case *kind.Type, *kind.Const:
		return true
	}
	for _, c := range e.Causes {
		if failsOnForm(c) {
			return true
		}
	}
	return false
}

func leafProblems(e *jsonschema.ValidationError, instance any) []domain.FieldError {
	at := fieldPath(e.InstanceLocation, instance)
	switch k := e.ErrorKind.(type) {
	case *kind.AdditionalProperties:
		return childProblems(at, k.Properties, reasonUnknownField)
	case *kind.Required:
		return childProblems(at, k.Missing, reasonMissingField)
	case *kind.FalseSchema:
		return []domain.FieldError{{Field: at, Reason: reasonNotAllowedHere}}
	}
	return []domain.FieldError{{Field: at, Reason: leafMessage(e)}}
}

func childProblems(parent string, names []string, reason string) []domain.FieldError {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	out := make([]domain.FieldError, 0, len(sorted))
	for _, n := range sorted {
		out = append(out, domain.FieldError{Field: joinField(parent, n), Reason: reason})
	}
	return out
}

// leafMessage renders the library's message for a leaf error.
func leafMessage(e *jsonschema.ValidationError) string {
	out := e.BasicOutput()
	if out.Error == nil {
		return e.Error()
	}
	return out.Error.String()
}

// fieldPath turns a JSON pointer token list into "destinations[2].policy".
// Numeric tokens become [n] only when the parent value is an array, so map
// keys that look like numbers are kept as keys.
func fieldPath(tokens []string, instance any) string {
	path := rootField
	cur := instance
	for _, tok := range tokens {
		if arr, ok := cur.([]any); ok {
			path = joinIndex(path, tok)
			cur = indexValue(arr, tok)
			continue
		}
		path = joinField(path, tok)
		if obj, ok := cur.(map[string]any); ok {
			cur = obj[tok]
		} else {
			cur = nil
		}
	}
	return path
}

func indexValue(arr []any, tok string) any {
	i, err := strconv.Atoi(tok)
	if err != nil || i < 0 || i >= len(arr) {
		return nil
	}
	return arr[i]
}

// joinIndex appends an array index to a path.
func joinIndex(parent, index string) string {
	if parent == rootField {
		parent = ""
	}
	return parent + "[" + index + "]"
}

// joinField appends a key to a path; it handles the root and keys that are
// not plain identifiers (written as ["key"]).
func joinField(parent, key string) string {
	if parent == rootField {
		parent = ""
	}
	if !isPlainKey(key) {
		return parent + "[" + strconv.Quote(key) + "]"
	}
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func isPlainKey(key string) bool {
	if key == "" {
		return false
	}
	return !strings.ContainsAny(key, `.[]" `)
}

func dedupeProblems(in []domain.FieldError) []domain.FieldError {
	seen := make(map[domain.FieldError]bool, len(in))
	out := make([]domain.FieldError, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
