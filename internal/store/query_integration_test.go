//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
)

func TestGetEvent(t *testing.T) {
	// Arrange
	f := newFixture(t)
	in := f.input(t, "order-service", "evt-1", "sku-1", "inventory", "data-warehouse")
	f.mustIngest(t, in)
	ctx := context.Background()

	t.Run("returns payload, metadata and deliveries", func(t *testing.T) {
		got, err := f.store.GetEvent(ctx, Scope{}, in.EventID)
		if err != nil {
			t.Fatalf("GetEvent: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(got.Event.Payload, &payload); err != nil || payload["sku"] != "sku-1" {
			t.Errorf("payload = %s (%v)", got.Event.Payload, err)
		}
		if got.Event.Metadata["trace_id"] != "abc123" || got.Event.OrderingKey != "sku-1" || got.Event.Source != "order-service" {
			t.Errorf("event = %+v", got.Event)
		}
		if got.Event.IngestSeq == 0 || got.Event.RoutingConfigHash != f.cfg.Hash() {
			t.Errorf("hub fields not filled: %+v", got.Event)
		}
		if len(got.Deliveries) != 2 || got.Deliveries[0].ID != in.Deliveries[0].ID {
			t.Errorf("deliveries = %+v, want ingest order", got.Deliveries)
		}
	})

	t.Run("own source can read it", func(t *testing.T) {
		if _, err := f.store.GetEvent(ctx, Scope{Source: "order-service"}, in.EventID); err != nil {
			t.Fatalf("GetEvent: %v", err)
		}
	})

	t.Run("other source and unknown id are not found", func(t *testing.T) {
		if _, err := f.store.GetEvent(ctx, Scope{Source: "billing-service"}, in.EventID); !errors.Is(err, ErrNotFound) {
			t.Errorf("other source: error = %v, want ErrNotFound", err)
		}
		if _, err := f.store.GetEvent(ctx, Scope{}, uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown id: error = %v, want ErrNotFound", err)
		}
	})
}

// seedEvents stores three events: routed with ordering key, unrouted, and
// routed from another source. It returns their inputs in that order.
func seedEvents(t *testing.T, f fixture) []IngestInput {
	t.Helper()
	routed := f.input(t, "order-service", "evt-a", "sku-1", "inventory")
	unrouted := f.input(t, "order-service", "evt-b", "")
	unrouted.Envelope.EventType = "order.refunded"
	other := f.input(t, "billing-service", "evt-c", "", "data-warehouse")
	other.Envelope.EventType = "payment.succeeded"
	for _, in := range []IngestInput{routed, unrouted, other} {
		f.mustIngest(t, in)
	}
	return []IngestInput{routed, unrouted, other}
}

func TestListEventsFilters(t *testing.T) {
	// Arrange
	f := newFixture(t)
	seeded := seedEvents(t, f)
	f.setStatus(t, seeded[2].Deliveries[0].ID, domain.StatusDead)
	future := time.Now().Add(time.Hour)
	tests := []struct {
		name   string
		scope  Scope
		filter EventFilter
		want   []uuid.UUID
	}{
		{"no filter, newest first", Scope{}, EventFilter{}, ids(seeded[2], seeded[1], seeded[0])},
		{"source scope", Scope{Source: "billing-service"}, EventFilter{}, ids(seeded[2])},
		{"source_event_id", Scope{}, EventFilter{SourceEventID: "evt-b"}, ids(seeded[1])},
		{"event_type", Scope{}, EventFilter{EventType: "order.refunded"}, ids(seeded[1])},
		{"ordering_key", Scope{}, EventFilter{OrderingKey: "sku-1"}, ids(seeded[0])},
		{"routing_status", Scope{}, EventFilter{RoutingStatus: domain.RoutingUnrouted}, ids(seeded[1])},
		{"delivery status", Scope{}, EventFilter{Status: domain.StatusDead}, ids(seeded[2])},
		{"received_from in the future", Scope{}, EventFilter{ReceivedFrom: &future}, nil},
		{"received_to in the future", Scope{}, EventFilter{ReceivedTo: &future}, ids(seeded[2], seeded[1], seeded[0])},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			page, err := f.store.ListEvents(context.Background(), tt.scope, tt.filter)

			// Assert
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			got := make([]uuid.UUID, len(page.Items))
			for i, e := range page.Items {
				got[i] = e.ID
			}
			if !equalIDs(got, tt.want) {
				t.Errorf("events = %v, want %v", got, tt.want)
			}
			if page.NextCursor != nil {
				t.Error("single page has a next cursor")
			}
		})
	}
}

func ids(ins ...IngestInput) []uuid.UUID {
	out := make([]uuid.UUID, len(ins))
	for i, in := range ins {
		out[i] = in.EventID
	}
	return out
}

func equalIDs(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListEventsPagination(t *testing.T) {
	// Arrange
	f := newFixture(t)
	const total = 5
	for i := range total {
		f.mustIngest(t, f.input(t, "order-service", "evt-"+string(rune('a'+i)), ""))
	}

	t.Run("walks every page without gaps or repeats", func(t *testing.T) {
		seen := map[uuid.UUID]bool{}
		var sizes []int
		var cursor *Cursor
		var last time.Time
		for {
			page, err := f.store.ListEvents(context.Background(), Scope{}, EventFilter{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			sizes = append(sizes, len(page.Items))
			for _, e := range page.Items {
				if seen[e.ID] {
					t.Fatalf("event %s returned twice", e.ID)
				}
				if !last.IsZero() && e.ReceivedAt.After(last) {
					t.Fatalf("events not newest first")
				}
				seen[e.ID], last = true, e.ReceivedAt
			}
			if page.NextCursor == nil {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != total || len(sizes) != 3 || sizes[2] != 1 {
			t.Errorf("pages = %v, seen %d events; want [2 2 1] and %d", sizes, len(seen), total)
		}
	})

	t.Run("exactly limit rows has no next page", func(t *testing.T) {
		page, err := f.store.ListEvents(context.Background(), Scope{}, EventFilter{Limit: total})
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		if len(page.Items) != total || page.NextCursor != nil {
			t.Errorf("got %d items, next cursor %v; want %d and none", len(page.Items), page.NextCursor, total)
		}
	})

	t.Run("limit is clamped to the maximum", func(t *testing.T) {
		if _, err := f.store.ListEvents(context.Background(), Scope{}, EventFilter{Limit: MaxLimit + 50}); err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
	})
}

func TestListDeliveries(t *testing.T) {
	// Arrange
	f := newFixture(t)
	seeded := seedEvents(t, f)
	routed, other := seeded[0], seeded[2]
	f.setStatus(t, other.Deliveries[0].ID, domain.StatusDead)
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name   string
		scope  Scope
		filter DeliveryFilter
		want   []uuid.UUID
	}{
		{"no filter, newest first", Scope{}, DeliveryFilter{}, []uuid.UUID{other.Deliveries[0].ID, routed.Deliveries[0].ID}},
		{"source scope", Scope{Source: "order-service"}, DeliveryFilter{}, []uuid.UUID{routed.Deliveries[0].ID}},
		{"event_id", Scope{}, DeliveryFilter{EventID: &other.EventID}, []uuid.UUID{other.Deliveries[0].ID}},
		{"destination_id", Scope{}, DeliveryFilter{DestinationID: "inventory"}, []uuid.UUID{routed.Deliveries[0].ID}},
		{"status", Scope{}, DeliveryFilter{Status: domain.StatusDead}, []uuid.UUID{other.Deliveries[0].ID}},
		{"created_to in the past", Scope{}, DeliveryFilter{CreatedTo: &past}, nil},
		{"created_from in the past", Scope{}, DeliveryFilter{CreatedFrom: &past}, []uuid.UUID{other.Deliveries[0].ID, routed.Deliveries[0].ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := f.store.ListDeliveries(context.Background(), tt.scope, tt.filter)
			if err != nil {
				t.Fatalf("ListDeliveries: %v", err)
			}
			got := make([]uuid.UUID, len(page.Items))
			for i, d := range page.Items {
				got[i] = d.ID
			}
			if !equalIDs(got, tt.want) {
				t.Errorf("deliveries = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("pagination", func(t *testing.T) {
		first, err := f.store.ListDeliveries(context.Background(), Scope{}, DeliveryFilter{Limit: 1})
		if err != nil || len(first.Items) != 1 || first.NextCursor == nil {
			t.Fatalf("first page = %+v, %v", first, err)
		}
		second, err := f.store.ListDeliveries(context.Background(), Scope{}, DeliveryFilter{Limit: 1, Cursor: first.NextCursor})
		if err != nil || len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].ID == first.Items[0].ID {
			t.Fatalf("second page = %+v, %v", second, err)
		}
	})
}

func insertAttempt(t *testing.T, f fixture, deliveryID uuid.UUID, number int, startedAt time.Time) uuid.UUID {
	t.Helper()
	id := domain.NewID()
	_, err := f.pool.Exec(context.Background(), `INSERT INTO attempts (id, delivery_id, number, trigger,
		started_at, duration_ms, outcome, status_code, error, request, response, config_hash)
		VALUES ($1, $2, $3, 'auto', $4, 120, 'retryable', 503, NULL,
		'{"method":"POST","url":"https://x.example.com","headers":{"Authorization":"***"},"body":"{}","truncated":false}',
		'{"status_code":503,"content_type":"text/plain","body":"busy","truncated":false}', $5)`,
		id, deliveryID, number, startedAt, f.cfg.Hash())
	if err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	return id
}

func TestGetDelivery(t *testing.T) {
	// Arrange
	f := newFixture(t)
	in := f.input(t, "order-service", "evt-1", "", "inventory")
	f.mustIngest(t, in)
	deliveryID := in.Deliveries[0].ID
	base := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	var attemptIDs []uuid.UUID
	for i := 1; i <= 3; i++ {
		attemptIDs = append(attemptIDs, insertAttempt(t, f, deliveryID, i, base.Add(time.Duration(i)*time.Minute)))
	}
	ctx := context.Background()

	t.Run("pages attempts newest first", func(t *testing.T) {
		first, err := f.store.GetDelivery(ctx, Scope{}, deliveryID, AttemptPage{Limit: 2})
		if err != nil {
			t.Fatalf("GetDelivery: %v", err)
		}
		if len(first.Attempts) != 2 || first.Attempts[0].ID != attemptIDs[2] || first.NextCursor == nil {
			t.Fatalf("first page = %+v", first.Attempts)
		}
		a := first.Attempts[0]
		if a.Request == nil || a.Request.Headers["Authorization"] != domain.MaskedValue || a.Response == nil || a.Response.StatusCode != 503 {
			t.Errorf("attempt records not decoded: %+v", a)
		}
		second, err := f.store.GetDelivery(ctx, Scope{}, deliveryID, AttemptPage{Limit: 2, Cursor: first.NextCursor})
		if err != nil || len(second.Attempts) != 1 || second.Attempts[0].ID != attemptIDs[0] || second.NextCursor != nil {
			t.Fatalf("second page = %+v, %v", second.Attempts, err)
		}
	})

	t.Run("scope and unknown id", func(t *testing.T) {
		if _, err := f.store.GetDelivery(ctx, Scope{Source: "order-service"}, deliveryID, AttemptPage{}); err != nil {
			t.Errorf("own source: %v", err)
		}
		if _, err := f.store.GetDelivery(ctx, Scope{Source: "billing-service"}, deliveryID, AttemptPage{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("other source: error = %v, want ErrNotFound", err)
		}
		if _, err := f.store.GetDelivery(ctx, Scope{}, uuid.New(), AttemptPage{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown id: error = %v, want ErrNotFound", err)
		}
	})
}

func TestBlockedBy(t *testing.T) {
	// inventory uses the per_key policy (block); data-warehouse does not order.
	tests := []struct {
		name          string
		mode          string
		firstStatus   domain.DeliveryStatus
		wantSecondBy  int // index of the blocking delivery, -1 for none
		wantThirdBy   int
		wantUnordered bool
	}{
		{"pending predecessor blocks every successor", "block", domain.StatusPending, 0, 0, false},
		{"dead predecessor blocks in block mode", "block", domain.StatusDead, 0, 0, false},
		{"dead predecessor is settled in skip mode", "skip", domain.StatusDead, -1, 1, false},
		{"succeeded predecessor is settled", "block", domain.StatusSucceeded, -1, 1, false},
		{"skipped predecessor is settled", "block", domain.StatusSkipped, -1, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			f := newFixtureWith(t, func(s string) string {
				return strings.Replace(s, "on_predecessor_dead: block", "on_predecessor_dead: "+tt.mode, 1)
			})
			var inv []uuid.UUID
			for _, id := range []string{"evt-1", "evt-2", "evt-3"} {
				in := f.input(t, "order-service", id, "sku-1", "inventory", "data-warehouse")
				f.mustIngest(t, in)
				inv = append(inv, in.Deliveries[0].ID)
			}
			f.setStatus(t, inv[0], tt.firstStatus)

			// Act
			page, err := f.store.ListDeliveries(context.Background(), Scope{}, DeliveryFilter{})

			// Assert
			if err != nil {
				t.Fatalf("ListDeliveries: %v", err)
			}
			by := map[uuid.UUID]*uuid.UUID{}
			for _, d := range page.Items {
				by[d.ID] = d.BlockedBy
				if d.DestinationID == "data-warehouse" && d.BlockedBy != nil {
					t.Errorf("unordered delivery %s has blocked_by", d.ID)
				}
			}
			checkBlocked(t, "first", by[inv[0]], -1, inv)
			checkBlocked(t, "second", by[inv[1]], tt.wantSecondBy, inv)
			checkBlocked(t, "third", by[inv[2]], tt.wantThirdBy, inv)
		})
	}
}

func checkBlocked(t *testing.T, name string, got *uuid.UUID, wantIdx int, inv []uuid.UUID) {
	t.Helper()
	switch {
	case wantIdx < 0 && got != nil:
		t.Errorf("%s blocked_by = %s, want none", name, got)
	case wantIdx >= 0 && (got == nil || *got != inv[wantIdx]):
		t.Errorf("%s blocked_by = %v, want %s", name, got, inv[wantIdx])
	}
}

func TestBlockedByIgnoresClaimedAndTerminalDeliveries(t *testing.T) {
	f := newFixture(t)
	var inv []uuid.UUID
	for _, id := range []string{"evt-1", "evt-2"} {
		in := f.input(t, "order-service", id, "sku-1", "inventory")
		f.mustIngest(t, in)
		inv = append(inv, in.Deliveries[0].ID)
	}
	for _, st := range []domain.DeliveryStatus{domain.StatusInFlight, domain.StatusDead} {
		f.setStatus(t, inv[1], st)

		detail, err := f.store.GetDelivery(context.Background(), Scope{}, inv[1], AttemptPage{})

		if err != nil {
			t.Fatalf("GetDelivery: %v", err)
		}
		if detail.Delivery.BlockedBy != nil {
			t.Errorf("%s delivery has blocked_by %s", st, detail.Delivery.BlockedBy)
		}
	}
}

func TestBlockedByHidesOtherSourcesFromScopedCallers(t *testing.T) {
	// Arrange: billing-service's earlier delivery blocks order-service's.
	f := newFixture(t)
	first := f.input(t, "billing-service", "evt-1", "sku-1", "inventory")
	second := f.input(t, "order-service", "evt-2", "sku-1", "inventory")
	f.mustIngest(t, first)
	f.mustIngest(t, second)
	id := second.Deliveries[0].ID
	ctx := context.Background()

	// Act
	scoped, err := f.store.GetDelivery(ctx, Scope{Source: "order-service"}, id, AttemptPage{})
	if err != nil {
		t.Fatalf("GetDelivery scoped: %v", err)
	}
	unscoped, err := f.store.GetDelivery(ctx, Scope{}, id, AttemptPage{})
	if err != nil {
		t.Fatalf("GetDelivery unscoped: %v", err)
	}

	// Assert
	if scoped.Delivery.BlockedBy != nil {
		t.Errorf("scoped caller sees another source's delivery %s", scoped.Delivery.BlockedBy)
	}
	if unscoped.Delivery.BlockedBy == nil || *unscoped.Delivery.BlockedBy != first.Deliveries[0].ID {
		t.Errorf("unscoped blocked_by = %v, want %s", unscoped.Delivery.BlockedBy, first.Deliveries[0].ID)
	}
}
