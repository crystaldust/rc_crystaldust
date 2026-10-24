package template

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

func testVars() Vars {
	return Vars{
		Event: Event{
			ID: "evt-1", Source: "order-service", SourceEventID: "ord-1", EventType: "order.paid",
			OccurredAt: time.Date(2026, 10, 24, 8, 30, 0, 0, time.UTC),
			ReceivedAt: time.Date(2026, 10, 24, 8, 30, 1, 0, time.UTC),
			Payload:    map[string]any{"id": "A 1", "amount": 10.5},
			Metadata:   map[string]string{"tenant": "acme"},
		},
		Secrets:    map[string]string{"TOKEN": "tok-abc123", "KEY": "sign-abc123"},
		DeliveryID: "dlv-1",
		Attempt:    1,
		Now:        time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
	}
}

func TestRenderProducesActualAndMaskedRequests(t *testing.T) {
	// Arrange
	req := config.Request{
		Method: "POST",
		URL:    "https://x.example.com/o/{{event.payload.id}}?token={{secret.TOKEN}}&t={{event.metadata.tenant}}",
		Headers: map[string]string{
			"Authorization": "Bearer {{ secret.TOKEN }}",
			"X-Signature":   "{{ request.body | hmac_sha256_hex secret.KEY }}",
			"Content-Type":  "application/json",
		},
		Body: map[string]any{
			"amount": "{{event.payload.amount}}",
			"auth":   "{{secret.TOKEN}}",
			"note":   "<a&b>",
		},
	}

	// Act
	got, err := Render(req, testVars())

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	wantBody := `{"amount":10.5,"auth":"tok-abc123","note":"<a&b>"}`
	if string(got.Actual.BodyBytes) != wantBody {
		t.Errorf("actual body = %s, want %s", got.Actual.BodyBytes, wantBody)
	}
	if string(got.Masked.BodyBytes) != `{"amount":10.5,"auth":"***","note":"<a&b>"}` {
		t.Errorf("masked body = %s", got.Masked.BodyBytes)
	}
	if got.Actual.URL != "https://x.example.com/o/A%201?token=tok-abc123&t=acme" {
		t.Errorf("actual url = %s", got.Actual.URL)
	}
	if got.Masked.URL != "https://x.example.com/o/A%201?token=***&t=acme" {
		t.Errorf("masked url = %s", got.Masked.URL)
	}
	if got.Actual.Headers["Authorization"] != "Bearer tok-abc123" || got.Masked.Headers["Authorization"] != domain.MaskedValue {
		t.Errorf("authorization = %q / %q", got.Actual.Headers["Authorization"], got.Masked.Headers["Authorization"])
	}
	wantSig := hmacHex("sign-abc123", wantBody)
	if got.Actual.Headers["X-Signature"] != wantSig {
		t.Errorf("signature = %s, want %s (over actual body)", got.Actual.Headers["X-Signature"], wantSig)
	}
	if got.Masked.Headers["X-Signature"] != wantSig {
		t.Errorf("masked signature = %s, want unchanged %s", got.Masked.Headers["X-Signature"], wantSig)
	}
	if got.Masked.Headers["Content-Type"] != "application/json" {
		t.Errorf("content-type = %q", got.Masked.Headers["Content-Type"])
	}
}

func TestHeaderCopyingTheBodyIsMasked(t *testing.T) {
	tests := []struct {
		tmpl   string
		masked bool
	}{
		{"{{ request.body }}", true},
		{"{{ request.body | base64 }}", true},
		{"{{ request.body | sha256 | upper }}", true},
		{"{{ request.body | sha256 }}", false},
		{"sha256={{ request.body | hmac_sha256_hex secret.KEY }}", false},
	}
	for _, tt := range tests {
		t.Run(tt.tmpl, func(t *testing.T) {
			// Arrange
			req := config.Request{URL: "https://x/", Headers: map[string]string{"X-Copy": tt.tmpl},
				Body: map[string]any{"t": "{{ secret.TOKEN }}"}}

			// Act
			got, err := Render(req, testVars())

			// Assert
			if err != nil {
				t.Fatal(err)
			}
			if masked := got.Masked.Headers["X-Copy"] == domain.MaskedValue; masked != tt.masked {
				t.Fatalf("masked = %v (%q), want %v", masked, got.Masked.Headers["X-Copy"], tt.masked)
			}
		})
	}
}

func TestRenderWithoutBodyHasNoBodyBytes(t *testing.T) {
	// Act
	got, err := Render(config.Request{URL: "https://x.example.com/{{delivery.id}}/{{delivery.attempt}}"}, testVars())

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if got.Actual.BodyBytes != nil || got.Masked.BodyBytes != nil {
		t.Errorf("body bytes = %q / %q, want nil", got.Actual.BodyBytes, got.Masked.BodyBytes)
	}
	if got.Actual.URL != "https://x.example.com/dlv-1/1" {
		t.Errorf("url = %s", got.Actual.URL)
	}
}

func TestRenderReportsWhereItFailed(t *testing.T) {
	tests := []struct {
		name string
		req  config.Request
		want string
	}{
		{"url", config.Request{URL: "{{ event.payload.nope }}"}, "render url"},
		{"body", config.Request{URL: "u", Body: map[string]any{"x": "{{ event.nope }}"}}, "render body"},
		{"header", config.Request{URL: "u", Headers: map[string]string{"X-A": "{{ now | trim 1 }}"}}, "render header X-A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := Render(tt.req, testVars())

			// Assert
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if _, ok := ClassOf(err); !ok {
				t.Fatalf("err = %v, want a template error in the chain", err)
			}
		})
	}
}

func TestRenderBodyMasksWholeLeafWithSecret(t *testing.T) {
	// Arrange
	tmpl := []any{"id={{event.id}};key={{secret.KEY}}", 1, nil, map[string]any{"k": "{{ secret.TOKEN | upper }}"}}

	// Act
	got, err := RenderBody(tmpl, testVars())

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	wantMasked := []any{domain.MaskedValue, 1, nil, map[string]any{"k": domain.MaskedValue}}
	if !reflect.DeepEqual(got.Masked, wantMasked) {
		t.Errorf("masked = %#v, want %#v", got.Masked, wantMasked)
	}
	wantActual := []any{"id=evt-1;key=sign-abc123", 1, nil, map[string]any{"k": "TOK-ABC123"}}
	if !reflect.DeepEqual(got.Actual, wantActual) {
		t.Errorf("actual = %#v, want %#v", got.Actual, wantActual)
	}
}

func TestRenderHeaderWithoutRequestBodyIsEmpty(t *testing.T) {
	// Act
	got, err := RenderHeader("[{{ request.body }}]", testVars(), nil)

	// Assert
	if err != nil || got.Actual != "[]" {
		t.Fatalf("got %q, %v", got.Actual, err)
	}
}

func TestRenderEdgeValues(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{"now is utc rfc3339", "{{ now }}", "2026-10-24T09:00:00Z"},
		{"now unix", "{{ now | format_time unix }}", "1792832400"},
		{"format_time rfc3339 on a string", `{{ event.payload.ts | format_time rfc3339 }}`, "2026-10-24T00:30:00Z"},
		{"default with string literal escapes", `{{ event.payload.nope | default "a\"b" }}`, `a"b`},
		{"default true", "{{ event.payload.nope | default true }}", "true"},
		{"default false", "{{ event.payload.nope | default false }}", "false"},
		{"default null", "[{{ event.payload.nope | default null }}]", "[]"},
		{"missing flows through to default", `{{ event.payload.nope | lower | default "x" }}`, "x"},
		{"json of a time", "{{ event.occurred_at | json }}", `"2026-10-24T08:30:00Z"`},
		{"json of null", "{{ event.payload.nil | json }}", "null"},
		{"int attempt", "{{ delivery.attempt | to_string }}", "1"},
		{"metadata object", "{{ event.metadata }}", `{"tenant":"acme"}`},
		{"literal braces only close", "a }} b", "a }} b"},
		{"string literal with closing braces", `{{ event.payload.nope | default "}}" }}`, "}}"},
		{"hmac base64", "{{ event.id | hmac_sha256_base64 secret.KEY }}", "csTqPIjlVI6gn7Xehi/9O7X5hsvpECdCp7JqxZT49lI="},
		{"negative number default", "{{ event.payload.nope | default -1.5 }}", "-1.5"},
	}
	v := testVars()
	v.Event.Payload = map[string]any{"ts": "2026-10-24T08:30:00+08:00", "nil": nil}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := RenderHeader(tt.tmpl, v, nil)

			// Assert
			if err != nil {
				t.Fatal(err)
			}
			if got.Actual != tt.want {
				t.Errorf("got %q, want %q", got.Actual, tt.want)
			}
		})
	}
}

func TestRenderRuntimeErrors(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		want ErrorClass
	}{
		{"undeclared secret", "{{ secret.NOPE }}", ErrorBadArgument},
		{"hmac with undeclared secret", "{{ event.id | hmac_sha256_hex secret.NOPE }}", ErrorBadArgument},
		{"format_time on a number", "{{ delivery.attempt | format_time unix }}", ErrorBadArgument},
		{"format_time on a bad string", "{{ event.id | format_time unix }}", ErrorBadArgument},
		{"absent optional envelope field", "{{ event.idempotency_key }}", ErrorMissingPath},
		{"metadata key missing", "{{ event.metadata.nope }}", ErrorMissingPath},
		{"array index not a number", "{{ event.payload.items.x }}", ErrorMissingPath},
	}
	v := testVars()
	v.Event.Payload = map[string]any{"items": []any{"a"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := RenderHeader(tt.tmpl, v, nil)

			// Assert
			if class, _ := ClassOf(err); class != tt.want {
				t.Fatalf("err = %v, want class %s", err, tt.want)
			}
		})
	}
}

func TestRenderReportsErrorsPerLocation(t *testing.T) {
	tests := []struct {
		name   string
		render func() error
		want   ErrorClass
	}{
		{"url syntax", func() error { _, err := RenderURL("https://x/{{", testVars()); return err }, ErrorSyntax},
		{"embedded body expression", func() error {
			_, err := RenderBody(map[string]any{"x": "a={{ event.payload.nope }}"}, testVars())
			return err
		}, ErrorMissingPath},
		{"body leaf syntax", func() error { _, err := RenderBody("{{ | x }}", testVars()); return err }, ErrorSyntax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.render()

			// Assert
			if class, _ := ClassOf(err); class != tt.want {
				t.Fatalf("err = %v, want class %s", err, tt.want)
			}
		})
	}
}

func TestRenderBodyRejectsExcessiveNesting(t *testing.T) {
	// Arrange
	var body any = "x"
	for range maxBodyDepth + 1 {
		body = []any{body}
	}

	// Act
	_, err := RenderBody(body, testVars())

	// Assert
	if class, _ := ClassOf(err); class != ErrorSyntax {
		t.Fatalf("err = %v, want syntax", err)
	}
}

func TestMarshalJSONReportsUnencodableValues(t *testing.T) {
	// Act
	_, err := MarshalJSON(map[string]any{"f": func() {}})

	// Assert
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestErrorMessageNamesClass(t *testing.T) {
	// Act
	msg := (&Error{Class: ErrorSyntax, Detail: "x"}).Error()

	// Assert
	if msg != "syntax: x" {
		t.Fatalf("msg = %q", msg)
	}
}

func hmacHex(key, msg string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
