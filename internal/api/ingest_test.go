package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"messagehub/internal/domain"
	"messagehub/internal/store"
)

const purchaseBody = `{"source_event_id":"ord-1","event_type":"order.purchased",
	"occurred_at":"2026-10-24T08:00:00Z","ordering_key":"sku-1","payload":{"sku":"sku-1"}}`

func TestIngestNewEvent(t *testing.T) {
	// Arrange
	h := newHarness(t)
	h.store.ingestRes = store.IngestResult{
		EventID: domain.NewID(), RoutingStatus: domain.RoutingRouted, MatchedRouteIDs: []string{"purchase-fanout"},
		Deliveries: []store.DeliveryRef{{ID: domain.NewID(), DestinationID: "inventory", Status: domain.StatusPending}},
	}

	// Act
	rec, body := h.do(t, http.MethodPost, "/v1/events", "order-service", purchaseBody)

	// Assert
	if rec.Code != http.StatusAccepted || body["success"] != true {
		t.Fatalf("status %d, body %v", rec.Code, body)
	}
	in := h.store.ingestIn
	if in.Source != "order-service" || in.ConfigHash != h.cfg.Hash() || !slices.Equal(in.RouteIDs, []string{"purchase-fanout"}) {
		t.Errorf("ingest input = %+v", in)
	}
	if len(in.Deliveries) != 2 || in.Deliveries[0].DestinationID != "inventory" || in.Deliveries[0].PolicyID != "inventory-ordered" ||
		in.Deliveries[1].PolicyID != "default" {
		t.Errorf("deliveries = %+v", in.Deliveries)
	}
	data := body["data"].(map[string]any)
	if data["duplicate"] != false || data["routing_status"] != "routed" {
		t.Errorf("data = %v", data)
	}
	if !strings.Contains(h.logs.String(), `"msg":"event.received"`) || strings.Contains(h.logs.String(), `"sku-1"}`) {
		t.Errorf("logs = %s", h.logs)
	}
	if rec.Header().Get(requestIDHeader) == "" {
		t.Error("no request id header")
	}
}

func TestIngestDuplicateAndUnrouted(t *testing.T) {
	tests := []struct {
		name       string
		result     store.IngestResult
		wantStatus int
		wantLog    string
	}{
		{"duplicate answers 200", store.IngestResult{EventID: domain.NewID(), Duplicate: true, RoutingStatus: domain.RoutingRouted}, http.StatusOK, `"msg":"event.duplicate"`},
		{"unrouted answers 202 with a warning", store.IngestResult{EventID: domain.NewID(), RoutingStatus: domain.RoutingUnrouted}, http.StatusAccepted, `"level":"WARN","msg":"event.unrouted"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.ingestRes = tt.result

			rec, body := h.do(t, http.MethodPost, "/v1/events", "order-service", purchaseBody)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, body %v", rec.Code, body)
			}
			data := body["data"].(map[string]any)
			if data["matched_route_ids"] == nil || data["deliveries"] == nil {
				t.Errorf("empty lists must render as [], got %v", data)
			}
			if !strings.Contains(h.logs.String(), tt.wantLog) {
				t.Errorf("logs = %s, want %s", h.logs, tt.wantLog)
			}
		})
	}
}

func TestIngestErrors(t *testing.T) {
	bigPayload := `{"source_event_id":"e","event_type":"a.b","occurred_at":"2026-10-24T08:00:00Z","payload":{"x":"` +
		strings.Repeat("a", domain.MaxPayloadBytes) + `"}}`
	tests := []struct {
		name       string
		principal  string
		body       string
		storeErr   error
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{"no key", "", purchaseBody, nil, http.StatusUnauthorized, codeUnauthenticated, nil},
		{"viewer may not ingest", "support-viewer", purchaseBody, nil, http.StatusForbidden, codeForbidden, nil},
		{"operator may not ingest", "oncall-operator", purchaseBody, nil, http.StatusForbidden, codeForbidden, nil},
		{"validation problems are listed", "order-service", `{"event_type":"Bad","source":"x"}`, nil,
			http.StatusBadRequest, codeValidationFailed, []string{"source_event_id", "event_type", "occurred_at", "payload", "source"}},
		{"duplicate key", "order-service", `{"source_event_id":"a","source_event_id":"b"}`, nil,
			http.StatusBadRequest, codeValidationFailed, []string{"source_event_id"}},
		{"not json", "order-service", `nope`, nil, http.StatusBadRequest, codeValidationFailed, []string{""}},
		{"payload too large", "order-service", bigPayload, nil, http.StatusRequestEntityTooLarge, codePayloadTooLarge, nil},
		{"body too large", "order-service", strings.Repeat(" ", maxIngestBodyBytes+1), nil, http.StatusRequestEntityTooLarge, codePayloadTooLarge, nil},
		{"store failure", "order-service", purchaseBody, errors.New("db down"), http.StatusInternalServerError, codeInternal, nil},
		{"value the database rejects", "order-service", purchaseBody, store.ErrInvalidInput, http.StatusBadRequest, codeValidationFailed, nil},
		{"NUL in the payload", "order-service", `{"source_event_id":"e","event_type":"a.b","occurred_at":"2026-10-24T08:00:00Z","payload":{"x":"\u0000"}}`,
			nil, http.StatusBadRequest, codeValidationFailed, []string{"payload.x"}},
		{"NUL in a string field", "order-service", `{"source_event_id":"\u0000","event_type":"a.b","occurred_at":"2026-10-24T08:00:00Z","payload":{}}`,
			nil, http.StatusBadRequest, codeValidationFailed, []string{"source_event_id"}},
		{"huge payload number", "order-service", `{"source_event_id":"e","event_type":"a.b","occurred_at":"2026-10-24T08:00:00Z","payload":{"n":1e999999999}}`,
			nil, http.StatusBadRequest, codeValidationFailed, []string{"payload.n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t)
			h.store.ingestErr = tt.storeErr

			// Act
			rec, body := h.do(t, http.MethodPost, "/v1/events", tt.principal, tt.body)

			// Assert
			if rec.Code != tt.wantStatus || errorCode(body) != tt.wantCode || body["success"] != false {
				t.Fatalf("status %d code %q, want %d %q (body %v)", rec.Code, errorCode(body), tt.wantStatus, tt.wantCode, body)
			}
			for _, f := range tt.wantFields {
				if !slices.Contains(errorFields(body), f) {
					t.Errorf("details %v do not mention %q", errorFields(body), f)
				}
			}
			if tt.storeErr != nil && strings.Contains(rec.Body.String(), "db down") {
				t.Error("internal error leaked into the response")
			}
		})
	}
}
