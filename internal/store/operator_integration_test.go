//go:build integration

package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
)

// deliveryIn stores one event with an inventory delivery in status st.
func deliveryIn(t *testing.T, f fixture, st domain.DeliveryStatus) uuid.UUID {
	t.Helper()
	in := f.input(t, "order-service", uuid.NewString(), "", "inventory")
	f.mustIngest(t, in)
	id := in.Deliveries[0].ID
	if _, err := f.pool.Exec(context.Background(), `UPDATE deliveries SET status = $2,
		attempt_count = 8, lease_owner = CASE WHEN $2 = 'IN_FLIGHT' THEN 'w1' END,
		completed_at = CASE WHEN $2 IN ('SUCCEEDED','DEAD','SKIPPED') THEN now() END
		WHERE id = $1`, id, string(st)); err != nil {
		t.Fatalf("prepare delivery: %v", err)
	}
	return id
}

func operatorInput(id uuid.UUID) OperatorInput {
	return OperatorInput{ActionID: domain.NewID(), DeliveryID: id, Actor: "oncall-operator", Reason: "vendor outage resolved"}
}

func TestReplay(t *testing.T) {
	for _, from := range []domain.DeliveryStatus{domain.StatusDead, domain.StatusSucceeded, domain.StatusSkipped} {
		t.Run("from "+string(from), func(t *testing.T) {
			// Arrange
			f := newFixture(t)
			id := deliveryIn(t, f, from)
			notified := listen(t)
			in := operatorInput(id)

			// Act
			action, err := f.store.Replay(context.Background(), in)

			// Assert
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if action.FromStatus != from || action.ToStatus != domain.StatusPending || action.Action != ActionReplay ||
				action.ID != in.ActionID || action.CreatedAt.IsZero() {
				t.Errorf("action = %+v", action)
			}
			if n := f.count(t, `SELECT count(*) FROM deliveries WHERE id = $1 AND status = 'PENDING'
				AND replay_count = 1 AND attempt_count = 0 AND completed_at IS NULL AND lease_owner IS NULL`, id); n != 1 {
				t.Error("delivery not reset for a new round")
			}
			if n := f.count(t, `SELECT count(*) FROM operator_actions WHERE delivery_id = $1 AND action = 'replay'
				AND actor = 'oncall-operator' AND reason = 'vendor outage resolved' AND from_status = $2`, id, string(from)); n != 1 {
				t.Error("audit row missing")
			}
			if !notified(500 * time.Millisecond) {
				t.Error("replay sent no notification")
			}
		})
	}
}

func TestSkip(t *testing.T) {
	f := newFixture(t)
	id := deliveryIn(t, f, domain.StatusDead)

	action, err := f.store.Skip(context.Background(), operatorInput(id))

	if err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if action.FromStatus != domain.StatusDead || action.ToStatus != domain.StatusSkipped {
		t.Errorf("action = %+v", action)
	}
	if n := f.count(t, `SELECT count(*) FROM deliveries WHERE id = $1 AND status = 'SKIPPED' AND completed_at IS NOT NULL`, id); n != 1 {
		t.Error("delivery not skipped")
	}
	if n := f.count(t, `SELECT count(*) FROM operator_actions WHERE delivery_id = $1 AND action = 'skip'`, id); n != 1 {
		t.Error("audit row missing")
	}
}

func TestOperatorRejectsInvalidStates(t *testing.T) {
	tests := []struct {
		name string
		from domain.DeliveryStatus
		skip bool
	}{
		{"replay pending", domain.StatusPending, false},
		{"replay in flight", domain.StatusInFlight, false},
		{"replay retry scheduled", domain.StatusRetryScheduled, false},
		{"skip pending", domain.StatusPending, true},
		{"skip succeeded", domain.StatusSucceeded, true},
		{"skip skipped", domain.StatusSkipped, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			f := newFixture(t)
			id := deliveryIn(t, f, tt.from)
			op := f.store.Replay
			if tt.skip {
				op = f.store.Skip
			}

			// Act
			_, err := op(context.Background(), operatorInput(id))

			// Assert
			var serr *StateError
			if !errors.As(err, &serr) || !errors.Is(err, ErrInvalidState) || serr.Current != tt.from {
				t.Fatalf("error = %v, want StateError with current %s", err, tt.from)
			}
			if n := f.count(t, `SELECT count(*) FROM operator_actions`); n != 0 {
				t.Errorf("audit rows = %d, want 0", n)
			}
			if n := f.count(t, `SELECT count(*) FROM deliveries WHERE id = $1 AND status = $2`, id, string(tt.from)); n != 1 {
				t.Error("delivery status changed")
			}
		})
	}
}

func TestOperatorUnknownDelivery(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Replay(context.Background(), operatorInput(uuid.New())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestConcurrentReplayOnlyOneSucceeds(t *testing.T) {
	// Arrange
	f := newFixture(t)
	id := deliveryIn(t, f, domain.StatusDead)
	const operators = 10
	var wg sync.WaitGroup
	errs := make(chan error, operators)

	// Act
	for range operators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.store.Replay(context.Background(), operatorInput(id))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	// Assert
	ok, invalid := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrInvalidState):
			invalid++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || invalid != operators-1 {
		t.Errorf("succeeded %d, invalid %d; want 1 and %d", ok, invalid, operators-1)
	}
	if n := f.count(t, `SELECT replay_count FROM deliveries WHERE id = $1`, id); n != 1 {
		t.Errorf("replay_count = %d, want 1", n)
	}
}
