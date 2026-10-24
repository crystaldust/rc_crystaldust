package api

import (
	"errors"
	"strings"
	"testing"

	"messagehub/internal/domain"
)

func TestCheckStrictJSON(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string // empty: no duplicate expected
	}{
		{"no duplicates", `{"a":1,"b":{"c":[1,{"d":2}]}}`, ""},
		{"top level duplicate", `{"event_type":"a.b","event_type":"c.d"}`, "event_type"},
		{"duplicate inside payload", `{"payload":{"order":{"id":1,"id":2}}}`, "payload.order.id"},
		{"duplicate in an object inside an array", `{"payload":{"items":[{"sku":1},{"sku":1,"sku":2}]}}`, "payload.items[1].sku"},
		{"same key in sibling objects is fine", `{"a":{"k":1},"b":{"k":2}}`, ""},
		{"same key at different depths is fine", `{"k":{"k":1}}`, ""},
		{"invalid json is left to the envelope parser", `{"a":`, ""},
		{"unterminated array is left to the envelope parser", `{"a":[1,2`, ""},
		{"scalar document", `"text"`, ""},
		{"non-string key is left to the envelope parser", `{1:2}`, ""},
		{"unclosed object is left to the envelope parser", `{"a":1`, ""},
		{"NUL in a payload string", `{"payload":{"note":"a\u0000b"}}`, "payload.note"},
		{"NUL in a key", `{"payload":{"a\u0000":1}}`, "payload.a\x00"},
		{"NUL in an array string", `{"payload":{"tags":["ok","\u0000"]}}`, "payload.tags[1]"},
		{"huge exponent", `{"payload":{"n":1e999999999}}`, "payload.n"},
		{"huge negative exponent", `{"payload":{"n":1E-5000}}`, "payload.n"},
		{"over-long number", `{"payload":{"n":` + strings.Repeat("9", maxJSONNumberLen+1) + `}}`, "payload.n"},
		{"ordinary numbers pass", `{"payload":{"a":-1.5e10,"b":0,"c":123456789012345678901234567890}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := CheckStrictJSON([]byte(tt.raw))

			// Assert
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("error = %v, want none", err)
				}
				return
			}
			var verr *domain.ValidationError
			if !errors.As(err, &verr) || !verr.HasField(tt.wantField) {
				t.Fatalf("error = %v, want a problem on %s", err, tt.wantField)
			}
		})
	}
}

func TestCheckStrictJSONLargeDocument(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"payload":{"items":[`)
	for i := range 5000 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"sku":"s","qty":1,"tags":["a","b"]}`)
	}
	b.WriteString(`]}}`)

	if err := CheckStrictJSON([]byte(b.String())); err != nil {
		t.Fatalf("error = %v, want none", err)
	}
}

func TestCheckStrictJSONDeepNesting(t *testing.T) {
	// Deeper than maxJSONDepth fails closed: a duplicate hidden after deep
	// nesting must not slip through.
	deep := `{"payload":` + strings.Repeat(`[`, maxJSONDepth+5) + strings.Repeat(`]`, maxJSONDepth+5) +
		`,"source_event_id":"a","source_event_id":"b"}`
	var verr *domain.ValidationError
	if err := CheckStrictJSON([]byte(deep)); !errors.As(err, &verr) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	ok := strings.Repeat(`[`, maxJSONDepth) + strings.Repeat(`]`, maxJSONDepth)
	if err := CheckStrictJSON([]byte(ok)); err != nil {
		t.Fatalf("nesting at the limit: error = %v", err)
	}
}

func TestCheckStrictJSONRejectsInvalidUTF8(t *testing.T) {
	var verr *domain.ValidationError
	if err := CheckStrictJSON([]byte("{\"payload\":{\"a\":\"\xff\"}}")); !errors.As(err, &verr) {
		t.Fatalf("error = %v, want a validation error", err)
	}
}
