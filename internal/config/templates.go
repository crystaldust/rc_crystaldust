package config

import (
	"fmt"
	"sort"
	"strconv"

	"messagehub/internal/domain"
)

// validateTemplates calls tv for every template location of every
// destination (§7.1): request.url, each header value and each body leaf
// string. Map keys are visited in sorted order so results are stable.
func validateTemplates(doc document, tv TemplateValidator) []domain.FieldError {
	var out []domain.FieldError
	check := func(field, tmpl string) {
		if err := tv(field, tmpl); err != nil {
			out = append(out, domain.FieldError{Field: field, Reason: "invalid template: " + err.Error()})
		}
	}
	for i, d := range doc.Destinations {
		at := fmt.Sprintf("destinations[%d].request", i)
		check(at+".url", d.Request.URL)
		for _, name := range sortedKeys(d.Request.Headers) {
			check(joinField(at+".headers", name), d.Request.Headers[name])
		}
		walkBodyStrings(at+".body", d.Request.Body, check)
	}
	return out
}

// walkBodyStrings visits every string leaf of a decoded JSON value.
func walkBodyStrings(field string, v any, visit func(field, s string)) {
	switch t := v.(type) {
	case string:
		visit(field, t)
	case map[string]any:
		for _, k := range sortedKeys(t) {
			walkBodyStrings(joinField(field, k), t[k], visit)
		}
	case []any:
		for i, child := range t {
			walkBodyStrings(joinIndex(field, strconv.Itoa(i)), child, visit)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
