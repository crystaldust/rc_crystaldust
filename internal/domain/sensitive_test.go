package domain

import "testing"

func TestIsSensitiveName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Authorization", true},
		{"proxy-authorization", true},
		{"Cookie", true},
		{"key", true},
		{"X-Api-Key", true},
		{"api_key", true},
		{"apikey", true},
		{"access_token", true},
		{"X-Auth-Token", true},
		{"client_secret", true},
		{"password", true},
		{"X-Hub-Signature", true},
		{"AWS-Access-Key", true},
		{"private_key", true},
		{"x-credentials", true},
		{"Idempotency-Key", false},
		{"X-Idempotency-Key", false},
		{"Content-Type", false},
		{"Accept", false},
		{"email_hash", false},
		{"monkey", false},
		{"keyword", false},
		{"", false},
		{"   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSensitiveName(tt.name); got != tt.want {
				t.Fatalf("IsSensitiveName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
