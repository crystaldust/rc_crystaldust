package config

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestTemplateValidatorVisitsAllTemplateLocations(t *testing.T) {
	// Arrange
	var seen []string
	record := func(field, tmpl string) error {
		seen = append(seen, field+"="+tmpl)
		return nil
	}

	// Act
	mustLoad(t, "testdata/valid/minimal.yaml", WithTemplateValidator(record))

	// Assert
	want := []string{
		"destinations[0].request.body.status=paid",
		"destinations[0].request.headers.Content-Type=application/json",
		"destinations[0].request.url=https://crm.example.com/contacts/{{ event.payload.contact_id }}",
	}
	sort.Strings(seen)
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("visited = %v, want %v", seen, want)
	}
}

func TestTemplateValidatorVisitsNestedBodyLeaves(t *testing.T) {
	// Arrange
	var fields []string
	record := func(field, _ string) error {
		fields = append(fields, field)
		return nil
	}

	// Act
	mustLoad(t, exampleConfigPath, WithTemplateValidator(record))

	// Assert
	for _, want := range []string{
		"destinations[0].request.headers.X-Signature",
		"destinations[0].request.body.hashed_email",
		"destinations[1].request.url",
		"destinations[2].request.body.delta",
	} {
		if !contains(fields, want) {
			t.Errorf("validator not called for %s", want)
		}
	}
}

func TestTemplateValidatorErrorsAreReported(t *testing.T) {
	// Arrange
	reject := func(field, tmpl string) error {
		if strings.Contains(tmpl, "{{") {
			return errors.New("unsupported")
		}
		return nil
	}

	// Act
	_, err := Load("testdata/valid/minimal.yaml", WithTemplateValidator(reject))

	// Assert
	problems := validationProblems(t, err)
	if len(problems) != 1 || !hasProblem(problems, "destinations[0].request.url", "invalid template: unsupported") {
		t.Fatalf("problems = %v, want one url template problem", problems)
	}
}

func TestNilTemplateValidatorKeepsDefault(t *testing.T) {
	mustLoad(t, "testdata/valid/minimal.yaml", WithTemplateValidator(nil))
}

func TestWalkBodyStrings(t *testing.T) {
	// Arrange
	body := map[string]any{
		"a":     []any{"x", map[string]any{"b": "y"}, 3, nil, true},
		"c.d":   "z",
		"empty": map[string]any{},
	}
	var got []string

	// Act
	walkBodyStrings("body", body, func(field, s string) { got = append(got, field+"="+s) })

	// Assert
	want := []string{`body.a[0]=x`, `body.a[1].b=y`, `body["c.d"]=z`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
