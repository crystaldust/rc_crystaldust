package config

import (
	"reflect"
	"testing"
)

func TestRequiredEnvMatchesExampleConfig(t *testing.T) {
	// Arrange: the example uses principal key hashes, OAuth2 refs and destination secrets.
	cfg := mustLoad(t, exampleConfigPath)
	want := []string{
		"AD_PLATFORM_SIGNING_KEY",
		"CRM_CLIENT_ID",
		"CRM_CLIENT_SECRET",
		"DWH_WEBHOOK_SIGNING_KEY",
		"HUB_KEY_BILLING_SERVICE_SHA256",
		"HUB_KEY_ONCALL_OPERATOR_SHA256",
		"HUB_KEY_ORDER_SERVICE_SHA256",
		"HUB_KEY_SUPPORT_VIEWER_SHA256",
		"HUB_KEY_USER_SERVICE_SHA256",
		"INVENTORY_API_TOKEN",
	}

	// Act
	got := cfg.RequiredEnv()

	// Assert: keep docs/deploy/k8s-secrets.md in sync when this list changes.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredEnv() = %v, want %v", got, want)
	}
}

func TestRequiredEnvDeduplicatesSharedReferences(t *testing.T) {
	cfg := &Config{doc: document{
		Principals: []Principal{{APIKeySHA256Ref: "env:KEY_A"}},
		Destinations: []Destination{
			{Secrets: map[string]string{"TOKEN": "env:SHARED"}},
			{Secrets: map[string]string{"TOKEN": "env:SHARED"}, Auth: Auth{ClientIDRef: "env:KEY_A"}},
		},
	}}

	if got, want := cfg.RequiredEnv(), []string{"KEY_A", "SHARED"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredEnv() = %v, want %v", got, want)
	}
}
