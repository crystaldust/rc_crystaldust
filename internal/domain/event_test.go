package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func envelopeJSON(t *testing.T, overrides map[string]any, drop ...string) []byte {
	t.Helper()
	base := map[string]any{
		"source_event_id": "order:1:paid",
		"event_type":      "order.paid",
		"occurred_at":     "2026-10-24T08:00:00Z",
		"payload":         map[string]any{"order_id": 1},
	}
	merged := make(map[string]any, len(base)+len(overrides))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	for _, k := range drop {
		delete(merged, k)
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func metadataOf(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("k%d", i)] = "v"
	}
	return m
}

func TestParseEnvelopeAcceptsValidInput(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]any
	}{
		{"minimal", nil},
		{"empty payload object", map[string]any{"payload": map[string]any{}}},
		{"all optional fields", map[string]any{
			"idempotency_key": "idem-1", "ordering_key": "order:1",
			"metadata": map[string]string{"trace.id": "abc"},
		}},
		{"limits reached", map[string]any{
			"source_event_id": strings.Repeat("s", MaxSourceEventIDLen),
			"metadata":        metadataOf(MaxMetadataEntries),
			"ordering_key":    strings.Repeat("o", MaxOrderingKeyLen),
		}},
		{"offset time zone", map[string]any{"occurred_at": "2026-10-24T16:00:00.123+08:00"}},
		{"multibyte counted as characters", map[string]any{"source_event_id": strings.Repeat("订", MaxSourceEventIDLen)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			env, err := ParseEnvelope(envelopeJSON(t, tt.overrides))

			// Assert
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if env.EventType != "order.paid" || env.OccurredAt.IsZero() || len(env.Payload) == 0 {
				t.Fatalf("envelope not populated: %+v", env)
			}
		})
	}
}

func TestParseEnvelopeRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]any
		drop      []string
		wantField string
	}{
		{"missing source_event_id", nil, []string{"source_event_id"}, "source_event_id"},
		{"missing payload", nil, []string{"payload"}, "payload"},
		{"missing occurred_at", nil, []string{"occurred_at"}, "occurred_at"},
		{"missing event_type", nil, []string{"event_type"}, "event_type"},
		{"source_event_id too long", map[string]any{"source_event_id": strings.Repeat("s", MaxSourceEventIDLen+1)}, nil, "source_event_id"},
		{"empty source_event_id", map[string]any{"source_event_id": ""}, nil, "source_event_id"},
		{"bad event_type", map[string]any{"event_type": "OrderPaid"}, nil, "event_type"},
		{"event_type too long", map[string]any{"event_type": "a." + strings.Repeat("b", MaxEventTypeLen)}, nil, "event_type"},
		{"event_type not string", map[string]any{"event_type": 1}, nil, "event_type"},
		{"occurred_at without zone", map[string]any{"occurred_at": "2026-10-24T08:00:00"}, nil, "occurred_at"},
		{"occurred_at not string", map[string]any{"occurred_at": 1}, nil, "occurred_at"},
		{"payload is array", map[string]any{"payload": []int{1}}, nil, "payload"},
		{"payload is null", map[string]any{"payload": nil}, nil, "payload"},
		{"source field present", map[string]any{"source": "other-service"}, nil, "source"},
		{"unknown field", map[string]any{"extra": true}, nil, "extra"},
		{"idempotency_key null", map[string]any{"idempotency_key": nil}, nil, "idempotency_key"},
		{"ordering_key too long", map[string]any{"ordering_key": strings.Repeat("o", MaxOrderingKeyLen+1)}, nil, "ordering_key"},
		{"metadata too many", map[string]any{"metadata": metadataOf(MaxMetadataEntries + 1)}, nil, "metadata"},
		{"metadata bad key", map[string]any{"metadata": map[string]string{"bad key": "v"}}, nil, "metadata.bad key"},
		{"metadata non-string value", map[string]any{"metadata": map[string]any{"k": 1}}, nil, "metadata.k"},
		{"metadata value too long", map[string]any{"metadata": map[string]string{"k": strings.Repeat("v", MaxMetadataValueLen+1)}}, nil, "metadata.k"},
		{"metadata not object", map[string]any{"metadata": "x"}, nil, "metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := ParseEnvelope(envelopeJSON(t, tt.overrides, tt.drop...))

			// Assert
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("want *ValidationError, got %v", err)
			}
			if !verr.HasField(tt.wantField) {
				t.Fatalf("want problem on %q, got %v", tt.wantField, verr)
			}
		})
	}
}

func TestParseEnvelopeAggregatesProblems(t *testing.T) {
	raw := envelopeJSON(t, map[string]any{"event_type": "Bad", "source": "x"}, "payload")

	_, err := ParseEnvelope(raw)

	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Problems) != 3 {
		t.Fatalf("want 3 problems, got %v", err)
	}
	if !strings.Contains(verr.Error(), "event_type") {
		t.Fatalf("error text should list fields: %q", verr.Error())
	}
}

func TestParseEnvelopeRejectsNonObjectBody(t *testing.T) {
	for _, body := range []string{"", "[]", "null", "\"x\"", "{"} {
		_, err := ParseEnvelope([]byte(body))
		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("body %q: want *ValidationError, got %v", body, err)
		}
	}
}

func TestParseEnvelopePayloadSizeLimit(t *testing.T) {
	// payload `{"p":"xxx..."}` sized exactly at, then one byte over, the limit
	overhead := len(`{"p":""}`)
	atLimit := map[string]any{"p": strings.Repeat("x", MaxPayloadBytes-overhead)}
	overLimit := map[string]any{"p": strings.Repeat("x", MaxPayloadBytes-overhead+1)}

	if _, err := ParseEnvelope(envelopeJSON(t, map[string]any{"payload": atLimit})); err != nil {
		t.Fatalf("payload at limit rejected: %v", err)
	}
	_, err := ParseEnvelope(envelopeJSON(t, map[string]any{"payload": overLimit}))
	if !IsPayloadTooLarge(err) {
		t.Fatalf("want ErrPayloadTooLarge, got %v", err)
	}
}

func TestRoutingStatusFor(t *testing.T) {
	tests := []struct {
		name    string
		matched []string
		want    RoutingStatus
	}{
		{"nil", nil, RoutingUnrouted},
		{"empty", []string{}, RoutingUnrouted},
		{"one route", []string{"r1"}, RoutingRouted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RoutingStatusFor(tt.matched); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
