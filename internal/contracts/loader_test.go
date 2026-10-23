package contracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validEvent = `
    event:
      id: e1
      source: s
      source_event_id: x
      event_type: a.b
      occurred_at: "2026-10-24T08:30:00Z"
      received_at: "2026-10-24T08:30:00Z"
      payload: {}`

func writeCases(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cases.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func templateDoc(fields string) string {
	return "cases:\n  - name: c\n" + fields + validEvent + "\n"
}

func TestLoadTemplateCasesRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"unknown field", templateDoc("    location: body\n    template: x\n    want: x\n    bogus: 1\n"), "field bogus not found"},
		{"bad location", templateDoc("    location: query\n    template: x\n    want: x\n"), "location"},
		{"missing template", templateDoc("    location: body\n    want: x\n"), "template is required"},
		{"want and want_error", templateDoc("    location: body\n    template: x\n    want: x\n    want_error: syntax\n"), "exactly one"},
		{"neither want", templateDoc("    location: body\n    template: x\n"), "exactly one"},
		{"unknown error class", templateDoc("    location: body\n    template: x\n    want_error: oops\n"), "unknown error class"},
		{"header template not string", templateDoc("    location: header\n    template: {a: b}\n    want: x\n"), "template must be a string"},
		{"url want not string", templateDoc("    location: url\n    template: x\n    want: 3\n"), "want must be a string"},
		{"request_body outside header", templateDoc("    location: body\n    template: x\n    want: x\n    request_body: b\n"), "request_body"},
		{"bad delivery attempt", templateDoc("    location: body\n    template: x\n    want: x\n    delivery: {id: d, attempt: 0}\n"), "delivery.attempt"},
		{"missing name", strings.Replace(templateDoc("    location: body\n    template: x\n    want: x\n"), "name: c", "name: \"\"", 1), "name is required"},
		{"missing event id", strings.Replace(templateDoc("    location: body\n    template: x\n    want: x\n"), "id: e1", "id: \"\"", 1), "id is required"},
		{"bad occurred_at", strings.Replace(templateDoc("    location: body\n    template: x\n    want: x\n"), `occurred_at: "2026-10-24T08:30:00Z"`, "occurred_at: yesterday", 1), "occurred_at"},
		{"bad received_at", strings.Replace(templateDoc("    location: body\n    template: x\n    want: x\n"), `received_at: "2026-10-24T08:30:00Z"`, "received_at: later", 1), "received_at"},
		{"payload not object", strings.Replace(templateDoc("    location: body\n    template: x\n    want: x\n"), "payload: {}", "payload: [1]", 1), "payload must be an object"},
		{"duplicate names", "cases:\n  - name: c\n    location: body\n    template: x\n    want: x" + validEvent + "\n  - name: c\n    location: body\n    template: x\n    want: x" + validEvent + "\n", "duplicate name"},
		{"no cases", "cases: []\n", "no cases"},
		{"not yaml", "cases: [\n", "load"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			path := writeCases(t, tt.doc)
			// Act
			_, err := LoadTemplateCases(path)
			// Assert
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadTemplateCasesExplicitNullWantIsPresent(t *testing.T) {
	// Arrange
	path := writeCases(t, templateDoc("    location: body\n    template: \"{{event.payload.x}}\"\n    want: null\n"))
	// Act
	cases, err := LoadTemplateCases(path)
	// Assert
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cases[0].ExpectsError() || cases[0].Want != nil {
		t.Fatalf("got want=%#v error=%q, want null value", cases[0].Want, cases[0].WantError)
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	loaders := map[string]func(string) error{
		"template": func(p string) error { _, err := LoadTemplateCases(p); return err },
		"success":  func(p string) error { _, err := LoadSuccessCases(p); return err },
		"retry":    func(p string) error { _, err := LoadRetryCases(p); return err },
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			if err := load(missing); err == nil {
				t.Fatal("want error for missing file")
			}
		})
	}
}

func successDoc(fields string) string {
	return "cases:\n  - name: c\n" + fields
}

const okResponse = "    response: {status: 200, content_type: x, body: x}\n    want_outcome: success\n"

func TestLoadSuccessCasesRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"empty success status", successDoc("    success: {status: []}\n" + okResponse), "success.status"},
		{"status out of range", successDoc("    success: {status: [700]}\n" + okResponse), "want 100-599"},
		{"bad status class", successDoc("    success: {status: [\"6xx\"]}\n" + okResponse), "class"},
		{"status not scalar", successDoc("    success: {status: [[200]]}\n" + okResponse), "scalar"},
		{"response and network error", successDoc("    success: {status: [200]}\n    network_error: dns\n" + okResponse), "exactly one"},
		{"neither response nor network error", successDoc("    success: {status: [200]}\n    want_outcome: success\n"), "exactly one"},
		{"bad response status", successDoc("    success: {status: [200]}\n    response: {status: 42}\n    want_outcome: success\n"), "response.status"},
		{"bad network error", successDoc("    success: {status: [200]}\n    network_error: reset\n    want_outcome: retryable\n"), "network_error"},
		{"bad outcome", successDoc("    success: {status: [200]}\n    network_error: dns\n    want_outcome: render_error\n"), "want_outcome"},
		{"rule without path", successDoc("    success: {status: [200], body: [{op: exists}]}\n" + okResponse), "path is required"},
		{"unknown op", successDoc("    success: {status: [200], body: [{path: a, op: gt, value: 1}]}\n" + okResponse), "unknown operator"},
		{"exists with value", successDoc("    success: {status: [200], body: [{path: a, op: exists, value: 1}]}\n" + okResponse), "takes no value"},
		{"eq without value", successDoc("    success: {status: [200], body: [{path: a, op: eq}]}\n" + okResponse), "requires a value"},
		{"in with scalar", successDoc("    success: {status: [200], body: [{path: a, op: in, value: 1}]}\n" + okResponse), "array value"},
		{"missing name", "cases:\n  - name: \"\"\n    success: {status: [200]}\n" + okResponse, "name is required"},
		{"unknown field", successDoc("    success: {status: [200]}\n    extra: 1\n" + okResponse), "field extra not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeCases(t, tt.doc)
			_, err := LoadSuccessCases(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadSuccessCasesKeepsNullRuleValue(t *testing.T) {
	path := writeCases(t, successDoc("    success: {status: [\"2xx\"], body: [{path: a, op: eq, value: null}]}\n"+okResponse))
	cases, err := LoadSuccessCases(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rule := cases[0].Success.Body[0]
	if !rule.HasValue || rule.Value != nil {
		t.Fatalf("got %+v, want explicit null value", rule)
	}
}

func retryDoc(fields string) string {
	return "cases:\n  - name: c\n" + fields
}

func TestLoadRetryCasesRejectsInvalid(t *testing.T) {
	const okBackoff = "    backoff: {initial: 5s, multiplier: 2, max: 1h, jitter: 0}\n"
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"bad initial", retryDoc("    backoff: {initial: 5 s, multiplier: 2, max: 1h}\n    retry_number: 1\n    want_delay: 5s\n"), "initial"},
		{"bad max", retryDoc("    backoff: {initial: 5s, multiplier: 2, max: 1.5h}\n    retry_number: 1\n    want_delay: 5s\n"), "max"},
		{"max below initial", retryDoc("    backoff: {initial: 5m, multiplier: 2, max: 1m}\n    retry_number: 1\n    want_delay: 5s\n"), "initial <= max"},
		{"multiplier below one", retryDoc("    backoff: {initial: 5s, multiplier: 0.5, max: 1h}\n    retry_number: 1\n    want_delay: 5s\n"), "multiplier"},
		{"non-zero jitter", retryDoc("    backoff: {initial: 5s, multiplier: 2, max: 1h, jitter: 0.2}\n    retry_number: 1\n    want_delay: 5s\n"), "jitter"},
		{"retry number zero", retryDoc(okBackoff + "    retry_number: 0\n    want_delay: 5s\n"), "retry_number"},
		{"bad status code", retryDoc(okBackoff + "    retry_number: 1\n    status_code: 99\n    want_delay: 5s\n"), "status_code"},
		{"bad want delay", retryDoc(okBackoff + "    retry_number: 1\n    want_delay: soon\n"), "want_delay"},
		{"missing name", "cases:\n  - name: \"\"\n" + okBackoff + "    retry_number: 1\n    want_delay: 5s\n", "name is required"},
		{"unknown field", retryDoc(okBackoff + "    retry_number: 1\n    want_delay: 5s\n    seed: 1\n"), "field seed not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeCases(t, tt.doc)
			_, err := LoadRetryCases(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
