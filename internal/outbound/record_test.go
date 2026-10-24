package outbound

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/template"
)

const fakeSecret = "tok-abc123-very-secret"

func recordVars() template.Vars {
	return template.Vars{
		Event: template.Event{
			ID: "evt-1", Source: "s", SourceEventID: "e", EventType: "a.b",
			OccurredAt: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC),
			ReceivedAt: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC),
			Payload:    map[string]any{"id": "o-1", "email": "a@example.com"},
		},
		Secrets: map[string]string{"TOKEN": fakeSecret},
		Now:     time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
	}
}

func render(t *testing.T, req config.Request) template.Rendered {
	t.Helper()
	r, err := template.Render(req, recordVars())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildRequestRecordMasksHeaders(t *testing.T) {
	// Arrange
	r := render(t, config.Request{
		URL: "https://x.example.com/",
		Headers: map[string]string{
			"Authorization":   "Bearer {{ secret.TOKEN }}",
			"X-Custom":        "v={{ secret.TOKEN }}",
			"X-Api-Key":       "literal-allowed-value",
			"X-Hub-Signature": "{{ request.body | hmac_sha256_hex secret.TOKEN }}",
			"Idempotency-Key": "{{ event.id }}",
			"Content-Type":    "application/json",
		},
		Body: map[string]any{"id": "{{ event.payload.id }}"},
	})

	// Act
	rec := BuildRequestRecord(r, "POST")

	// Assert
	for _, name := range []string{"Authorization", "X-Custom", "X-Api-Key", "X-Hub-Signature"} {
		if rec.Headers[name] != domain.MaskedValue {
			t.Errorf("header %s = %q, want masked", name, rec.Headers[name])
		}
	}
	if rec.Headers["Idempotency-Key"] != "evt-1" || rec.Headers["Content-Type"] != "application/json" {
		t.Errorf("non-sensitive headers changed: %v", rec.Headers)
	}
	if rec.Method != "POST" || rec.Body != `{"id":"o-1"}` || rec.Truncated {
		t.Errorf("record = %+v", rec)
	}
}

func TestBuildRequestRecordMasksURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"secret expression in path", "https://x/{{ secret.TOKEN }}/a", "https://x/***/a"},
		{"secret expression in query", "https://x/a?k={{ secret.TOKEN }}&id={{ event.payload.id }}", "https://x/a?k=***&id=o-1"},
		{"sensitive literal query name", "https://x/a?api_key=literal&id=1", "https://x/a?api_key=***&id=1"},
		{"escaped sensitive query name", "https://x/a?access%5Ftoken=literal#frag", "https://x/a?access%5Ftoken=***#frag"},
		{"bad escape in query name is kept", "https://x/a?%zz=1&token=2", "https://x/a?%zz=1&token=***"},
		{"parameter without value", "https://x/a?token", "https://x/a?token"},
		{"no query", "https://x/a", "https://x/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			r := render(t, config.Request{URL: tt.url})

			// Act
			rec := BuildRequestRecord(r, "POST")

			// Assert
			if rec.URL != tt.want {
				t.Fatalf("url = %s, want %s", rec.URL, tt.want)
			}
			if strings.Contains(rec.URL, fakeSecret) {
				t.Fatal("secret leaked into the recorded url")
			}
		})
	}
}

func TestBuildRequestRecordMasksBody(t *testing.T) {
	// Arrange
	r := render(t, config.Request{
		URL: "https://x/",
		Body: map[string]any{
			"auth":     map[string]any{"password": "literal", "user": "u"},
			"tokens":   []any{"a", "b"},
			"embedded": "Bearer {{ secret.TOKEN }}",
			"list":     []any{map[string]any{"client_secret": "x"}},
			"email":    "{{ event.payload.email }}",
		},
	})

	// Act
	rec := BuildRequestRecord(r, "PUT")

	// Assert
	want := `{"auth":{"password":"***","user":"u"},"email":"a@example.com","embedded":"***","list":[{"client_secret":"***"}],"tokens":"***"}`
	if rec.Body != want {
		t.Fatalf("body = %s\nwant   %s", rec.Body, want)
	}
}

func TestBuildRequestRecordWithoutBody(t *testing.T) {
	// Act
	rec := BuildRequestRecord(render(t, config.Request{URL: "https://x/"}), "POST")

	// Assert
	if rec.Body != "" || rec.Truncated {
		t.Fatalf("record = %+v, want empty body", rec)
	}
}

func TestRequestBodyMaskedBeforeTruncation(t *testing.T) {
	// Arrange: the secret leaf starts just before the 16 KiB cut.
	padding := strings.Repeat("p", domain.MaxRecordedBodyBytes-20)
	r := render(t, config.Request{
		URL:  "https://x/",
		Body: map[string]any{"a": padding, "b": "{{ secret.TOKEN }}", "c": strings.Repeat("q", 100)},
	})

	// Act
	rec := BuildRequestRecord(r, "POST")

	// Assert
	if !rec.Truncated || len(rec.Body) > domain.MaxRecordedBodyBytes {
		t.Fatalf("truncated = %v, len = %d", rec.Truncated, len(rec.Body))
	}
	for i := 3; i <= len(fakeSecret); i++ {
		if strings.Contains(rec.Body, fakeSecret[:i]) {
			t.Fatalf("partial secret %q left in record", fakeSecret[:i])
		}
	}
}

func TestBuildResponseRecord(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"json echo of a credential is masked", `{"access_token":"echoed","ok":true}`, `{"access_token":"***","ok":true}`},
		{"json without credentials is kept verbatim", `{ "ok": true }`, `{ "ok": true }`},
		{"non-json kept", "token=abc", "token=abc"},
		{"two json values kept", `{"token":1} {}`, `{"token":1} {}`},
		{"invalid utf-8 and nul replaced", "a\xffb\x00c", "a�b�c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			rec := BuildResponseRecord(500, "text/plain", []byte(tt.body))

			// Assert
			if rec.Body != tt.want || rec.StatusCode != 500 || rec.ContentType != "text/plain" {
				t.Fatalf("record = %+v, want body %q", rec, tt.want)
			}
		})
	}
}

func TestResponseRecordTruncatesOnCharacterBoundary(t *testing.T) {
	// Arrange: 3-byte characters never align with 16384.
	body := strings.Repeat("中", domain.MaxRecordedBodyBytes)

	// Act
	rec := BuildResponseRecord(200, "", []byte(body))

	// Assert
	if !rec.Truncated || !utf8.ValidString(rec.Body) || len(rec.Body) > domain.MaxRecordedBodyBytes {
		t.Fatalf("truncated = %v, valid = %v, len = %d", rec.Truncated, utf8.ValidString(rec.Body), len(rec.Body))
	}
	if len(rec.Body) != domain.MaxRecordedBodyBytes-domain.MaxRecordedBodyBytes%3 {
		t.Fatalf("len = %d, want the longest whole-character prefix", len(rec.Body))
	}
}
