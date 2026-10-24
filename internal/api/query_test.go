package api

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
	"messagehub/internal/store"
)

func TestListEventsPassesFiltersAndScope(t *testing.T) {
	// Arrange
	h := newHarness(t)
	next := store.Cursor{Time: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC), ID: uuid.New()}
	h.store.events = store.Page[domain.Event]{Items: []domain.Event{{ID: uuid.New(), Source: "order-service"}}, NextCursor: &next}
	q := url.Values{
		"source_event_id": {"ord-1"}, "event_type": {"order.paid"}, "ordering_key": {"sku-1"},
		"routing_status": {"unrouted"}, "status": {"DEAD"}, "received_from": {"2026-10-24T00:00:00Z"},
		"received_to": {"2026-10-25T00:00:00+08:00"}, "limit": {"10"}, "cursor": {store.EncodeCursor(next)},
	}

	// Act
	rec, body := h.do(t, http.MethodGet, "/v1/events?"+q.Encode(), "order-service", "")

	// Assert
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %v", rec.Code, body)
	}
	f := h.store.eventFilter
	if f.SourceEventID != "ord-1" || f.EventType != "order.paid" || f.OrderingKey != "sku-1" ||
		f.RoutingStatus != domain.RoutingUnrouted || f.Status != domain.StatusDead || f.Limit != 10 ||
		f.ReceivedFrom == nil || f.ReceivedTo == nil || f.Cursor == nil || f.Cursor.ID != next.ID {
		t.Errorf("filter = %+v", f)
	}
	if h.store.scope.Source != "order-service" {
		t.Errorf("scope = %+v, want the source's own data", h.store.scope)
	}
	meta := body["meta"].(map[string]any)
	if meta["next_cursor"] != store.EncodeCursor(next) || meta["limit"] != float64(10) {
		t.Errorf("meta = %v", meta)
	}
	item := body["data"].([]any)[0].(map[string]any)
	if item["metadata"] == nil || item["idempotency_key"] != nil {
		t.Errorf("item = %v, want metadata {} and null idempotency_key", item)
	}
}

func TestQueryScopeByRole(t *testing.T) {
	for _, principal := range []string{"support-viewer", "oncall-operator"} {
		h := newHarness(t)

		rec, _ := h.do(t, http.MethodGet, "/v1/deliveries", principal, "")

		if rec.Code != http.StatusOK || h.store.scope.Source != "" {
			t.Errorf("%s: status %d, scope %+v; want unrestricted", principal, rec.Code, h.store.scope)
		}
	}
}

func TestListDeliveriesPassesFilters(t *testing.T) {
	h := newHarness(t)
	eventID := uuid.New()
	q := url.Values{"event_id": {eventID.String()}, "destination_id": {"crm"}, "status": {"DEAD"},
		"created_from": {"2026-10-24T00:00:00Z"}, "created_to": {"2026-10-25T00:00:00Z"}}

	rec, body := h.do(t, http.MethodGet, "/v1/deliveries?"+q.Encode(), "support-viewer", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %v", rec.Code, body)
	}
	f := h.store.delivFilter
	if f.EventID == nil || *f.EventID != eventID || f.DestinationID != "crm" || f.Status != domain.StatusDead ||
		f.CreatedFrom == nil || f.CreatedTo == nil || f.Limit != store.DefaultLimit {
		t.Errorf("filter = %+v", f)
	}
}

func TestQueryValidation(t *testing.T) {
	tests := []struct {
		name   string
		target string
		fields []string
	}{
		{"limit above maximum", "/v1/events?limit=101", []string{"limit"}},
		{"limit not a number", "/v1/deliveries?limit=ten", []string{"limit"}},
		{"bad event type and time", "/v1/events?event_type=Order&received_from=yesterday", []string{"event_type", "received_from"}},
		{"empty source event id", "/v1/events?source_event_id=", []string{"source_event_id"}},
		{"unknown enum values", "/v1/events?status=LOST&routing_status=maybe", []string{"status", "routing_status"}},
		{"forged cursor", "/v1/events?cursor=abc", []string{"cursor"}},
		{"bad delivery filters", "/v1/deliveries?event_id=42&destination_id=CRM&created_to=never", []string{"event_id", "destination_id", "created_to"}},
		{"event id not a uuid", "/v1/events/42", []string{"event_id"}},
		{"delivery id not a uuid", "/v1/deliveries/42?limit=0", []string{"delivery_id", "limit"}},
		{"non-canonical uuid", "/v1/events/urn:uuid:01929a3c-5e10-7b2a-8c4d-1f2e3d4c5b6a", []string{"event_id"}},
		{"NUL and invalid UTF-8 in filters", "/v1/events?ordering_key=%00&source_event_id=%ff", []string{"ordering_key", "source_event_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			rec, body := h.do(t, http.MethodGet, tt.target, "support-viewer", "")

			if rec.Code != http.StatusBadRequest || errorCode(body) != codeValidationFailed {
				t.Fatalf("status %d, body %v", rec.Code, body)
			}
			for _, f := range tt.fields {
				if !slices.Contains(errorFields(body), f) {
					t.Errorf("details %v do not mention %q", errorFields(body), f)
				}
			}
		})
	}
}

func TestGetEndpoints(t *testing.T) {
	id := uuid.New()
	blocker := uuid.New()
	status := 503
	attemptedAt := time.Date(2026, 10, 24, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	h := newHarness(t)
	h.store.eventDetail = store.EventDetail{
		Event: domain.Event{ID: id, Envelope: domain.Envelope{Payload: []byte(`{"a":1}`)}},
		Deliveries: []store.DeliveryView{{Delivery: domain.Delivery{ID: uuid.New(), LastStatusCode: &status,
			LastAttemptAt: &attemptedAt}, BlockedBy: &blocker}},
	}
	h.store.delivDetail = store.DeliveryDetail{
		Delivery: store.DeliveryView{Delivery: domain.Delivery{ID: id}},
		Attempts: []domain.Attempt{{ID: uuid.New(), Number: 1, Error: "timeout"}},
	}

	t.Run("event detail", func(t *testing.T) {
		rec, body := h.do(t, http.MethodGet, "/v1/events/"+id.String(), "order-service", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		data := body["data"].(map[string]any)
		d := data["deliveries"].([]any)[0].(map[string]any)
		if data["payload"].(map[string]any)["a"] != float64(1) || d["blocked_by"] != blocker.String() ||
			d["last_status_code"] != float64(503) || d["last_attempt_at"] != "2026-10-24T01:00:00Z" {
			t.Errorf("data = %v", data)
		}
	})

	t.Run("delivery detail pages attempts", func(t *testing.T) {
		rec, body := h.do(t, http.MethodGet, "/v1/deliveries/"+id.String()+"?limit=5", "oncall-operator", "")
		if rec.Code != http.StatusOK || h.store.attemptPage.Limit != 5 {
			t.Fatalf("status %d, page %+v", rec.Code, h.store.attemptPage)
		}
		attempt := body["data"].(map[string]any)["attempts"].([]any)[0].(map[string]any)
		if attempt["error"] != "timeout" || attempt["request"] != nil {
			t.Errorf("attempt = %v", attempt)
		}
		if body["meta"].(map[string]any)["next_cursor"] != nil {
			t.Error("last page has a next cursor")
		}
	})
}

func TestQueryStoreErrors(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"event not found", "/v1/events/" + uuid.NewString(), store.ErrNotFound, http.StatusNotFound, codeNotFound},
		{"delivery not found", "/v1/deliveries/" + uuid.NewString(), store.ErrNotFound, http.StatusNotFound, codeNotFound},
		{"list events failure", "/v1/events", errors.New("db down"), http.StatusInternalServerError, codeInternal},
		{"list deliveries failure", "/v1/deliveries", errors.New("db down"), http.StatusInternalServerError, codeInternal},
		{"event failure", "/v1/events/" + uuid.NewString(), errors.New("db down"), http.StatusInternalServerError, codeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.err = tt.err

			rec, body := h.do(t, http.MethodGet, tt.target, "support-viewer", "")

			if rec.Code != tt.wantStatus || errorCode(body) != tt.wantCode {
				t.Fatalf("status %d code %q, want %d %q", rec.Code, errorCode(body), tt.wantStatus, tt.wantCode)
			}
		})
	}
}
