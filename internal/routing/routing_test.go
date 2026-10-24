package routing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

const testConfig = `version: 1
principals:
  - id: order-service
    role: source
    api_key_sha256_ref: env:HUB_KEY_ORDER_SERVICE_SHA256
  - id: billing-service
    role: source
    api_key_sha256_ref: env:HUB_KEY_BILLING_SERVICE_SHA256
policies:
  - id: default
    timeout: 5s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success:
      status: ["2xx"]
destinations:
  - id: crm
    policy: default
    request: { method: POST, url: "https://crm.example.com/hook" }
  - id: warehouse
    policy: default
    request: { method: POST, url: "https://dwh.example.com/hook" }
routes:
  - id: paid-from-orders
    event_type: order.paid
    sources: [order-service]
    destinations: [crm]
  - id: paid-fanout
    event_type: order.paid
    destinations: [warehouse, crm]
  - id: paid-large
    event_type: order.paid
    match:
      - { path: payload.amount, op: eq, value: 100 }
    destinations: [warehouse]
  - id: paid-cn
    event_type: order.paid
    sources: [billing-service]
    match:
      - { path: metadata.region, op: eq, value: cn }
    destinations: [crm]
  - id: user-registered
    event_type: user.registered
    destinations: [crm]
`

func loadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	return cfg
}

func envelope(eventType, payload string, metadata map[string]string) domain.Envelope {
	return domain.Envelope{
		SourceEventID: "evt-1",
		EventType:     eventType,
		OccurredAt:    time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC),
		Payload:       json.RawMessage(payload),
		Metadata:      metadata,
	}
}

func TestMatch(t *testing.T) {
	cfg := loadConfig(t)
	tests := []struct {
		name       string
		env        domain.Envelope
		source     string
		wantRoutes []string
		wantDests  []string
	}{
		{
			name:   "no route for event type",
			env:    envelope("order.cancelled", `{}`, nil),
			source: "order-service",
		},
		{
			name:       "single route",
			env:        envelope("user.registered", `{}`, nil),
			source:     "order-service",
			wantRoutes: []string{"user-registered"},
			wantDests:  []string{"crm"},
		},
		{
			name:       "routes to the same destination are de-duplicated in first-seen order",
			env:        envelope("order.paid", `{"amount":5}`, nil),
			source:     "order-service",
			wantRoutes: []string{"paid-from-orders", "paid-fanout"},
			wantDests:  []string{"crm", "warehouse"},
		},
		{
			name:       "sources filter excludes other callers",
			env:        envelope("order.paid", `{"amount":5}`, nil),
			source:     "billing-service",
			wantRoutes: []string{"paid-fanout"},
			wantDests:  []string{"warehouse", "crm"},
		},
		{
			name:       "payload match rule compares numbers by value",
			env:        envelope("order.paid", `{"amount":100.0}`, nil),
			source:     "order-service",
			wantRoutes: []string{"paid-from-orders", "paid-fanout", "paid-large"},
			wantDests:  []string{"crm", "warehouse"},
		},
		{
			name:       "metadata match rule",
			env:        envelope("order.paid", `{}`, map[string]string{"region": "cn"}),
			source:     "billing-service",
			wantRoutes: []string{"paid-fanout", "paid-cn"},
			wantDests:  []string{"warehouse", "crm"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			routes, dests, err := Match(cfg, tt.env, tt.source)

			// Assert
			if err != nil {
				t.Fatalf("Match error: %v", err)
			}
			if strings.Join(routes, ",") != strings.Join(tt.wantRoutes, ",") {
				t.Errorf("routes = %v, want %v", routes, tt.wantRoutes)
			}
			if strings.Join(dests, ",") != strings.Join(tt.wantDests, ",") {
				t.Errorf("destinations = %v, want %v", dests, tt.wantDests)
			}
		})
	}
}

func TestMatchReturnsEmptyNonNilSlicesWhenUnrouted(t *testing.T) {
	routes, dests, err := Match(loadConfig(t), envelope("order.cancelled", `{}`, nil), "order-service")
	if err != nil {
		t.Fatalf("Match error: %v", err)
	}
	if routes == nil || dests == nil || len(routes) != 0 || len(dests) != 0 {
		t.Errorf("routes = %#v, destinations = %#v, want empty non-nil slices", routes, dests)
	}
}

func TestMatchRejectsUndecodablePayload(t *testing.T) {
	_, _, err := Match(loadConfig(t), envelope("order.paid", `{"amount":`, nil), "order-service")
	if err == nil {
		t.Fatal("Match succeeded with a broken payload")
	}
}
