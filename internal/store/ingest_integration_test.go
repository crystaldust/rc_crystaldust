//go:build integration

package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"messagehub/internal/domain"
	"messagehub/internal/platform/pg"
)

func TestIngestNewEvent(t *testing.T) {
	// Arrange
	f := newFixture(t)
	in := f.input(t, "order-service", "evt-1", "sku-1", "inventory", "data-warehouse")

	// Act
	res := f.mustIngest(t, in)

	// Assert
	if res.Duplicate || res.EventID != in.EventID || res.RoutingStatus != domain.RoutingRouted {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Deliveries) != 2 || res.Deliveries[0].Status != domain.StatusPending {
		t.Fatalf("deliveries = %+v", res.Deliveries)
	}
	if n := f.count(t, `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM event_payloads WHERE event_id = $1`, in.EventID); n != 1 {
		t.Errorf("payloads = %d, want 1", n)
	}
	// ordering_key and ingest_seq are copied from the event (§6).
	if n := f.count(t, `SELECT count(*) FROM deliveries d JOIN events e ON e.id = d.event_id
		WHERE d.ordering_key = 'sku-1' AND d.ingest_seq = e.ingest_seq AND d.status = 'PENDING'`); n != 2 {
		t.Errorf("deliveries with copied ordering fields = %d, want 2", n)
	}
}

func TestIngestStoresAbsentOptionalFieldsAsNull(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "order-service", "evt-null", "", "data-warehouse")
	in.Envelope.Metadata = nil

	f.mustIngest(t, in)

	if n := f.count(t, `SELECT count(*) FROM events WHERE idempotency_key IS NULL
		AND ordering_key IS NULL AND metadata = '{}'::jsonb`); n != 1 {
		t.Errorf("events with NULL optional fields = %d, want 1", n)
	}
}

func TestIngestDuplicateReturnsOriginal(t *testing.T) {
	// Arrange
	f := newFixture(t)
	first := f.mustIngest(t, f.input(t, "order-service", "evt-1", "", "inventory"))
	again := f.input(t, "order-service", "evt-1", "", "data-warehouse", "inventory")

	// Act
	res := f.mustIngest(t, again)

	// Assert: original id and deliveries, nothing re-routed.
	if !res.Duplicate || res.EventID != first.EventID {
		t.Fatalf("result = %+v, want duplicate of %s", res, first.EventID)
	}
	if len(res.Deliveries) != 1 || res.Deliveries[0].ID != first.Deliveries[0].ID {
		t.Errorf("deliveries = %+v, want the original one", res.Deliveries)
	}
	if n := f.count(t, `SELECT count(*) FROM deliveries`); n != 1 {
		t.Errorf("deliveries = %d, want 1", n)
	}
}

func TestIngestSameSourceEventIDFromAnotherSourceIsNew(t *testing.T) {
	f := newFixture(t)
	f.mustIngest(t, f.input(t, "order-service", "evt-1", ""))

	res := f.mustIngest(t, f.input(t, "billing-service", "evt-1", ""))

	if res.Duplicate {
		t.Fatal("event from another source was treated as a duplicate")
	}
}

func TestIngestConcurrentDuplicates(t *testing.T) {
	// Arrange
	f := newFixture(t)
	const submitters = 100
	results := make(chan IngestResult, submitters)
	errs := make(chan error, submitters)
	var wg sync.WaitGroup

	// Act
	for range submitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.store.Ingest(context.Background(), f.input(t, "order-service", "evt-race", "", "inventory"))
			if err != nil {
				errs <- err
				return
			}
			results <- res
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	// Assert
	for err := range errs {
		t.Errorf("Ingest error: %v", err)
	}
	fresh := 0
	var eventID string
	for res := range results {
		if !res.Duplicate {
			fresh++
		}
		if eventID == "" {
			eventID = res.EventID.String()
		} else if res.EventID.String() != eventID {
			t.Errorf("results disagree on the event id: %s vs %s", res.EventID, eventID)
		}
	}
	if fresh != 1 {
		t.Errorf("non-duplicate results = %d, want 1", fresh)
	}
	if n := f.count(t, `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM deliveries`); n != 1 {
		t.Errorf("deliveries = %d, want 1", n)
	}
}

// listen opens a direct connection listening on the delivery-ready channel.
func listen(t *testing.T) func(wait time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pg.ConnectDirect(ctx, testDSN)
	if err != nil {
		t.Fatalf("ConnectDirect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "LISTEN "+pg.DeliveryReadyChannel); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}
	return func(wait time.Duration) bool {
		waitCtx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		_, err := conn.WaitForNotification(waitCtx)
		return err == nil
	}
}

func TestIngestNotify(t *testing.T) {
	const wait = 500 * time.Millisecond

	t.Run("unrouted event stores no delivery and sends no notification", func(t *testing.T) {
		f := newFixture(t)
		notified := listen(t)

		res := f.mustIngest(t, f.input(t, "order-service", "evt-unrouted", ""))

		if res.RoutingStatus != domain.RoutingUnrouted || len(res.Deliveries) != 0 || len(res.MatchedRouteIDs) != 0 {
			t.Fatalf("result = %+v", res)
		}
		if n := f.count(t, `SELECT count(*) FROM events WHERE routing_status = 'unrouted'`); n != 1 {
			t.Errorf("unrouted events = %d, want 1", n)
		}
		if notified(wait) {
			t.Error("unrouted ingest sent a notification")
		}
	})

	t.Run("routed event sends exactly one notification", func(t *testing.T) {
		f := newFixture(t)
		notified := listen(t)

		f.mustIngest(t, f.input(t, "order-service", "evt-routed", "", "inventory", "data-warehouse"))

		if !notified(wait) {
			t.Fatal("routed ingest sent no notification")
		}
		if notified(wait) {
			t.Error("routed ingest sent more than one notification")
		}
	})
}

func TestIngestRejectedValuesAreInputErrors(t *testing.T) {
	// The API validates these first; the store maps what slips through to
	// ErrInvalidInput (SQLSTATE class 22) instead of an internal error.
	tests := []struct {
		name  string
		apply func(*IngestInput)
	}{
		{"NUL in a text column", func(in *IngestInput) { in.Envelope.SourceEventID = "a\x00b" }},
		{"NUL escape in jsonb", func(in *IngestInput) { in.Envelope.Payload = []byte(`{"a":"\u0000"}`) }},
		{"numeric overflow in jsonb", func(in *IngestInput) { in.Envelope.Payload = []byte(`{"n":1e999999999}`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			in := f.input(t, "order-service", "evt-bad", "", "inventory")
			tt.apply(&in)

			_, err := f.store.Ingest(context.Background(), in)

			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestIngestNilRouteIDsStoredAsEmpty(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "order-service", "evt-nil", "")
	in.RouteIDs = nil

	res := f.mustIngest(t, in)

	if res.MatchedRouteIDs == nil || res.RoutingStatus != domain.RoutingUnrouted {
		t.Errorf("result = %+v", res)
	}
}
