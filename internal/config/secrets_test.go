package config

import (
	"reflect"
	"strings"
	"testing"

	"messagehub/internal/domain"
)

func TestSecretRefs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"Bearer {{ secret.CRM_TOKEN }}", []string{"CRM_TOKEN"}},
		{"{{ request.body | hmac_sha256_hex secret.SIGNING_KEY }}", []string{"SIGNING_KEY"}},
		{"{{secret.A}}:{{secret.B}}", []string{"A", "B"}},
		{"secret.OUTSIDE_EXPRESSION", nil},
		{"{{ event.payload.secret }}", nil},
		{"plain", nil},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := secretRefs(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("secretRefs(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// destinationProblems runs the credential checks on one destination.
func destinationProblems(d Destination) []domain.FieldError {
	var p problems
	checkSecretUsage(&p, 0, d)
	return p
}

func TestCheckSecretUsageAcceptsSafeDestinations(t *testing.T) {
	secrets := map[string]string{"TOKEN": "env:TOKEN", "SIGNING_KEY": "env:SIGNING_KEY"}
	tests := []struct {
		name string
		d    Destination
	}{
		{"secret in header", Destination{Secrets: secrets, Request: Request{
			Headers: map[string]string{"Authorization": "Bearer {{ secret.TOKEN }}"}}}},
		{"hmac signature header", Destination{Secrets: secrets, Request: Request{
			Headers: map[string]string{"X-Signature": "{{ request.body | hmac_sha256_hex secret.SIGNING_KEY }}"}}}},
		{"secret in query", Destination{Secrets: secrets, Request: Request{
			URL: "https://v.example.com/hook?api_key={{ secret.TOKEN }}&lang=en"}}},
		{"secret in nested body array", Destination{Secrets: secrets, Request: Request{
			Body: map[string]any{"auth": []any{map[string]any{"token": "{{ secret.TOKEN }}"}}}}}},
		{"null under sensitive key", Destination{Request: Request{Body: map[string]any{"password": nil}}}},
		{"non-sensitive literals", Destination{Request: Request{
			URL:     "https://v.example.com/hook?lang=en&",
			Headers: map[string]string{"Content-Type": "application/json", "Idempotency-Key": "{{ event.idempotency_key }}"},
			Body:    map[string]any{"status": "paid", "count": 2}}}},
		{"allow_literal header is case-insensitive", Destination{
			AllowLiteral: []string{"headers.x-signature-version"},
			Request:      Request{Headers: map[string]string{"X-Signature-Version": "v1"}}}},
		{"allow_literal query and body", Destination{
			AllowLiteral: []string{"url.query.token_type", "body.auth.0.token_type"},
			Request: Request{
				URL:  "https://v.example.com/hook?token_type=bearer",
				Body: map[string]any{"auth": []any{map[string]any{"token_type": "bearer"}}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := destinationProblems(tt.d); len(got) != 0 {
				t.Fatalf("unexpected problems: %v", got)
			}
		})
	}
}

func TestCheckSecretUsageRejectsUnsafeDestinations(t *testing.T) {
	tests := []struct {
		name      string
		d         Destination
		wantField string
		wantText  string
	}{
		{"numeric literal under sensitive key",
			Destination{Request: Request{Body: map[string]any{"pin_password": 1234}}},
			"destinations[0].request.body.pin_password", `body field "pin_password"`},
		{"bool literal in nested array",
			Destination{Request: Request{Body: map[string]any{"creds": []any{map[string]any{"api_key": true}}}}},
			"destinations[0].request.body.creds[0].api_key", `body field "api_key"`},
		{"undeclared secret in url",
			Destination{Request: Request{URL: "https://v.example.com/{{ secret.PATH_KEY }}"}},
			"destinations[0].request.url", `undeclared secret "PATH_KEY"`},
		{"undeclared secret in body",
			Destination{Request: Request{Body: map[string]any{"note": "{{ secret.NOTE }}"}}},
			"destinations[0].request.body.note", `undeclared secret "NOTE"`},
		{"allow_literal does not cover a different field",
			Destination{AllowLiteral: []string{"headers.X-Signature-Version"},
				Request: Request{Headers: map[string]string{"X-Api-Key": "abc"}}},
			"destinations[0].request.headers.X-Api-Key", `header "X-Api-Key"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := destinationProblems(tt.d)

			// Assert
			for _, p := range got {
				if p.Field == tt.wantField && strings.Contains(p.Reason, tt.wantText) {
					return
				}
			}
			t.Fatalf("want problem on %s containing %q, got %v", tt.wantField, tt.wantText, got)
		})
	}
}
