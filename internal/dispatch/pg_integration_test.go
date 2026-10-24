//go:build integration

package dispatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
)

func TestConcurrentClaimsTakeEachDeliveryOnce(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 1000, "bulk", "", domain.StatusPending)
	var (
		mu   sync.Mutex
		seen = map[uuid.UUID]int{}
		wg   sync.WaitGroup
	)

	// Act
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range claimAll(t, f.dispatcher(fmt.Sprintf("w%d", i)), 32) {
				mu.Lock()
				seen[c.DeliveryID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Assert
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("delivery %s claimed %d times", id, n)
		}
	}
	if len(seen) != 1000 {
		t.Fatalf("claimed %d distinct deliveries, want 1000", len(seen))
	}
	if n := f.count(t, `SELECT count(*) FROM deliveries WHERE status = 'IN_FLIGHT' AND lease_owner IS NOT NULL`); n != 1000 {
		t.Fatalf("IN_FLIGHT = %d, want 1000", n)
	}
}

func TestClaimSetsLeaseAndSkipsFutureDeliveries(t *testing.T) {
	// Arrange
	f := newFixture(t)
	due := f.seed(t, 1, "bulk", "", domain.StatusRetryScheduled)
	future := f.seed(t, 1, "bulk", "", domain.StatusRetryScheduled)
	f.exec(t, `UPDATE deliveries SET scheduled_at = now() + interval '1 hour' WHERE id = $1`, future[0])
	f.seed(t, 1, "bulk", "", domain.StatusSucceeded)

	// Act
	got := claimAll(t, f.dispatcher("w1"), 10)

	// Assert
	if len(got) != 1 || got[0].DeliveryID != due[0] || got[0].Reclaimed {
		t.Fatalf("claimed %+v, want only the due delivery", got)
	}
	lease := time.Until(got[0].LeaseExpiresAt)
	if lease < 25*time.Second || lease > LeaseMargin+2*time.Second {
		t.Fatalf("lease = %v, want timeout (1s) + margin (%v)", lease, LeaseMargin)
	}
}

func TestConcurrentCompleteAppliesOnce(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 1, "bulk", "", domain.StatusPending)
	d := f.dispatcher("w1")
	c := claimAll(t, d, 1)[0]
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		applied int
	)

	// Act
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := d.Complete(context.Background(), successCompletion(c))
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Assert
	if applied != 1 {
		t.Fatalf("applied %d times, want 1", applied)
	}
	if n := f.count(t, `SELECT count(*) FROM attempts WHERE delivery_id = $1`, c.DeliveryID); n != 2 {
		t.Fatalf("attempts = %d, want both recorded", n)
	}
	var count int
	var completed *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT attempt_count, completed_at FROM deliveries WHERE id = $1`,
		c.DeliveryID).Scan(&count, &completed); err != nil {
		t.Fatal(err)
	}
	if f.status(t, c.DeliveryID) != domain.StatusSucceeded || count != 1 || completed == nil {
		t.Fatalf("status = %s, attempt_count = %d, completed_at = %v", f.status(t, c.DeliveryID), count, completed)
	}
}

func TestCompleteRetrySchedulesNextAttempt(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 1, "bulk", "", domain.StatusPending)
	d := f.dispatcher("w1")
	c := claimAll(t, d, 1)[0]
	delay := time.Hour
	comp := successCompletion(c)
	comp.Trigger, comp.Outcome, comp.Error, comp.RetryDelay = domain.TriggerRetry, domain.OutcomeRetryable, "HTTP 500", &delay

	// Act
	ok, err := d.Complete(context.Background(), comp)

	// Assert
	if err != nil || !ok {
		t.Fatalf("applied = %v, err = %v", ok, err)
	}
	var (
		ahead, nextMatches bool
		lastErr            string
		completed          *time.Time
	)
	// The delay is applied to the database clock, and the attempt's
	// next_attempt_at is the same instant as scheduled_at.
	if err := f.pool.QueryRow(context.Background(), `SELECT
			d.scheduled_at BETWEEN now() + interval '59 minutes' AND now() + interval '61 minutes',
			a.next_attempt_at = d.scheduled_at, d.last_error, d.completed_at
		FROM deliveries d JOIN attempts a ON a.delivery_id = d.id WHERE d.id = $1`, c.DeliveryID).
		Scan(&ahead, &nextMatches, &lastErr, &completed); err != nil {
		t.Fatal(err)
	}
	if !ahead || !nextMatches || lastErr != "HTTP 500" || completed != nil || f.status(t, c.DeliveryID) != domain.StatusRetryScheduled {
		t.Fatalf("scheduled in an hour = %v, next_attempt_at matches = %v, last_error = %q, completed_at = %v",
			ahead, nextMatches, lastErr, completed)
	}
	if got := claimAll(t, d, 1); len(got) != 0 {
		t.Fatalf("claimed %d before scheduled_at", len(got))
	}
}

func TestCompleteWithoutAttemptWritesNoAttempt(t *testing.T) {
	// Arrange: giving up after a reclaim spent the budget sends nothing.
	f := newFixture(t)
	f.seed(t, 1, "bulk", "", domain.StatusPending)
	d := f.dispatcher("w1")
	c := claimAll(t, d, 1)[0]
	comp := Completion{Claimed: c, Trigger: domain.TriggerGiveUp, Outcome: domain.OutcomeRetryable,
		Error: "retry budget exhausted", AttemptedAt: time.Now()}

	// Act
	ok, err := d.Complete(context.Background(), comp)

	// Assert
	if err != nil || !ok || f.status(t, c.DeliveryID) != domain.StatusDead {
		t.Fatalf("applied = %v, err = %v, status = %s", ok, err, f.status(t, c.DeliveryID))
	}
	if n := f.count(t, `SELECT count(*) FROM attempts WHERE delivery_id = $1`, c.DeliveryID); n != 0 {
		t.Fatalf("attempts = %d, want none", n)
	}
}

func TestCompleteRejectsIllegalTrigger(t *testing.T) {
	// Arrange
	f := newFixture(t)
	comp := Completion{Trigger: domain.TriggerReplay}

	// Act
	_, err := f.dispatcher("w1").Complete(context.Background(), comp)

	// Assert
	if err == nil {
		t.Fatal("want ErrInvalidTransition")
	}
}

func TestExpiredLeaseIsReclaimed(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 1, "bulk", "", domain.StatusPending)
	first := f.dispatcher("w1")
	c := claimAll(t, first, 1)[0]
	f.exec(t, `UPDATE deliveries SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, c.DeliveryID)

	// Act
	got, err := f.dispatcher("w2").Claim(context.Background(), 5)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Reclaimed || got[0].AttemptCount != 1 {
		t.Fatalf("reclaimed %+v, want one with attempt_count 1", got)
	}
	var owner, attemptErr, outcome string
	if err := f.pool.QueryRow(context.Background(), `SELECT d.lease_owner, a.error, a.outcome
		FROM deliveries d JOIN attempts a ON a.delivery_id = d.id WHERE d.id = $1`, c.DeliveryID).
		Scan(&owner, &attemptErr, &outcome); err != nil {
		t.Fatal(err)
	}
	if owner != "w2" || attemptErr != ReclaimError || outcome != string(domain.OutcomeRetryable) {
		t.Fatalf("owner = %s, attempt = %s / %s", owner, outcome, attemptErr)
	}

	// The original worker's late result is recorded but not applied.
	ok, err := first.Complete(context.Background(), successCompletion(c))
	if err != nil || ok {
		t.Fatalf("late complete applied = %v, err = %v", ok, err)
	}
	if n := f.count(t, `SELECT count(*) FROM attempts WHERE delivery_id = $1`, c.DeliveryID); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

func TestSameOwnerCannotCompleteAfterReclaimingItself(t *testing.T) {
	// Arrange: the lease token, not only the owner, fences a late result.
	f := newFixture(t)
	f.seed(t, 1, "bulk", "", domain.StatusPending)
	d := f.dispatcher("w1")
	stale := claimAll(t, d, 1)[0]
	f.exec(t, `UPDATE deliveries SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, stale.DeliveryID)
	fresh := claimAll(t, d, 1)[0]

	// Act
	staleOK, err1 := d.Complete(context.Background(), successCompletion(stale))
	freshOK, err2 := d.Complete(context.Background(), successCompletion(fresh))

	// Assert
	if err1 != nil || err2 != nil || staleOK || !freshOK {
		t.Fatalf("stale applied = %v, fresh applied = %v (%v, %v)", staleOK, freshOK, err1, err2)
	}
}

func TestMaxInFlightIsRespected(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 20, "capped", "", domain.StatusPending)

	// Act
	got := claimAll(t, f.dispatcher("w1"), 32)

	// Assert
	if len(got) != 2 {
		t.Fatalf("claimed %d, want max_in_flight 2", len(got))
	}
}

func TestMaxInFlightIsASoftLimitAcrossWorkers(t *testing.T) {
	// Arrange
	const workers, batch = 4, 32
	f := newFixture(t)
	f.seed(t, 50, "capped", "", domain.StatusPending)
	var wg sync.WaitGroup

	// Act
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimAll(t, f.dispatcher(fmt.Sprintf("w%d", i)), batch)
		}()
	}
	wg.Wait()

	// Assert: each racing worker can add at most min(batch, max_in_flight).
	n := f.count(t, `SELECT count(*) FROM deliveries WHERE status = 'IN_FLIGHT'`)
	if bound := 2 + (workers-1)*min(batch, 2); n < 2 || n > bound {
		t.Fatalf("IN_FLIGHT = %d, want 2..%d", n, bound)
	}
}

func TestBusyDestinationDoesNotStarveOthers(t *testing.T) {
	// Arrange: an old backlog on a capped destination must not fill the batch.
	f := newFixture(t)
	f.seed(t, 100, "capped", "", domain.StatusPending)
	f.exec(t, `UPDATE deliveries SET scheduled_at = now() - interval '1 hour'`)
	other := f.seed(t, 1, "bulk", "", domain.StatusPending)

	// Act
	got, err := f.dispatcher("w1").Claim(context.Background(), 3)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	ids := map[uuid.UUID]bool{}
	for _, c := range got {
		ids[c.DeliveryID] = true
	}
	if len(got) != 3 || !ids[other[0]] {
		t.Fatalf("claimed %d, other destination included = %v", len(got), ids[other[0]])
	}
}

func TestOrderedDeliveryWaitsForPredecessor(t *testing.T) {
	tests := []struct {
		name        string
		dest        string
		predecessor domain.DeliveryStatus
		wantNext    bool
	}{
		{"pending predecessor blocks", "ordered-block", domain.StatusRetryScheduled, false},
		{"in flight predecessor blocks", "ordered-block", domain.StatusInFlight, false},
		{"dead predecessor blocks in block mode", "ordered-block", domain.StatusDead, false},
		{"dead predecessor settles in skip mode", "ordered-skip", domain.StatusDead, true},
		{"skipped predecessor settles", "ordered-block", domain.StatusSkipped, true},
		{"succeeded predecessor settles", "ordered-block", domain.StatusSucceeded, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			f := newFixture(t)
			prev := f.seed(t, 1, tt.dest, "sku-1", tt.predecessor)
			f.exec(t, `UPDATE deliveries SET scheduled_at = now() + interval '1 hour' WHERE id = $1`, prev[0])
			next := f.seed(t, 1, tt.dest, "sku-1", domain.StatusPending)
			unrelated := f.seed(t, 1, tt.dest, "sku-2", domain.StatusPending)

			// Act
			got := claimAll(t, f.dispatcher("w1"), 10)

			// Assert
			ids := map[uuid.UUID]bool{}
			for _, c := range got {
				ids[c.DeliveryID] = true
			}
			if ids[next[0]] != tt.wantNext || !ids[unrelated[0]] {
				t.Fatalf("next claimed = %v (want %v), other key claimed = %v", ids[next[0]], tt.wantNext, ids[unrelated[0]])
			}
		})
	}
}

func TestOrderedDeliveryClaimsOnlyTheHeadOfAKey(t *testing.T) {
	// Arrange
	f := newFixture(t)
	ids := f.seed(t, 3, "ordered-block", "sku-1", domain.StatusPending)

	// Act
	got := claimAll(t, f.dispatcher("w1"), 10)

	// Assert
	if len(got) != 1 || got[0].DeliveryID != ids[0] {
		t.Fatalf("claimed %+v, want only the first in ingest order", got)
	}
}

// B15: ordering only sees committed predecessors. When a source breaks the
// rule to submit one key serially (§6), an event that took a smaller
// ingest_seq but commits later is invisible to the claim, so its successor
// goes first. This documents the behaviour; it is not a guarantee.
func TestOrderingCannotSeeUncommittedPredecessors(t *testing.T) {
	// Arrange
	f := newFixture(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var early uuid.UUID
	if err := tx.QueryRow(ctx, seedSQL, 1, "ordered-block", "sku-1", string(domain.StatusPending)).Scan(&early); err != nil {
		t.Fatal(err)
	}
	late := f.seed(t, 1, "ordered-block", "sku-1", domain.StatusPending)

	// Act
	got := claimAll(t, f.dispatcher("w1"), 10)

	// Assert
	if len(got) != 1 || got[0].DeliveryID != late[0] {
		t.Fatalf("claimed %+v, want the later-sequenced but committed delivery", got)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Once visible, the earlier one is claimable too: an IN_FLIGHT successor
	// never blocks its predecessor.
	if again := claimAll(t, f.dispatcher("w1"), 10); len(again) != 1 || again[0].DeliveryID != early {
		t.Fatalf("after commit claimed %+v, want the earlier delivery", again)
	}
}

func TestOrderingDoesNotApplyToUnorderedDestinations(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 3, "bulk", "sku-1", domain.StatusPending)

	// Act
	got := claimAll(t, f.dispatcher("w1"), 10)

	// Assert
	if len(got) != 3 {
		t.Fatalf("claimed %d, want all 3 (ordering mode none)", len(got))
	}
}

func TestUnknownDestinationIsNotClaimedAndIsReported(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 1, "removed", "", domain.StatusPending)

	// Act
	got := claimAll(t, f.dispatcher("w1"), 10)

	// Assert
	if len(got) != 0 {
		t.Fatalf("claimed %d deliveries of an unknown destination", len(got))
	}
	if !strings.Contains(f.logs.String(), "dispatch.unknown_destinations") || !strings.Contains(f.logs.String(), "removed") {
		t.Fatalf("logs = %s", f.logs.String())
	}
}

func TestClaimWithZeroLimitDoesNothing(t *testing.T) {
	// Arrange
	f := newFixture(t)
	f.seed(t, 1, "bulk", "", domain.StatusPending)

	// Act
	got, err := f.dispatcher("w1").Claim(context.Background(), 0)

	// Assert
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestDispatchQueriesUseIndexes(t *testing.T) {
	// Arrange: a realistic mix, mostly settled rows.
	f := newFixture(t)
	f.seed(t, 9000, "bulk", "", domain.StatusSucceeded)
	f.seed(t, 500, "bulk", "", domain.StatusPending)
	f.seed(t, 300, "ordered-block", "sku-1", domain.StatusPending)
	f.seed(t, 200, "capped", "", domain.StatusInFlight)
	f.exec(t, `ANALYZE deliveries`)
	tg := targetsOf(f.cfg)
	queries := []struct {
		name string
		sql  string
		args []any
	}{
		{"claim", claimSQL, []any{tg.ids, tg.maxInFlight, tg.leaseMS, 32, tg.orderedBlock, tg.orderedSkip,
			settledStatuses(domain.PredecessorDeadBlock), settledStatuses(domain.PredecessorDeadSkip),
			string(domain.StatusInFlight), "w1"}},
		{"reclaim", reclaimSQL, []any{tg.ids, tg.leaseMS, 32, "w1", "retryable", ReclaimError}},
		{"complete", completeSQL, []any{uuid.New(), "w1", "SUCCEEDED", 1, time.Now(), "success", 200, nil, nil,
			true, string(domain.StatusInFlight), time.Now()}},
	}
	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			// Act
			plan := explain(t, f, q.sql, q.args...)
			t.Logf("plan:\n%s", plan)

			// Assert
			if strings.Contains(plan, "Seq Scan on deliveries") {
				t.Fatalf("plan has a sequential scan on deliveries:\n%s", plan)
			}
		})
	}
}

func explain(t *testing.T, f fixture, sql string, args ...any) string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func successCompletion(c Claimed) Completion {
	code := 200
	now := time.Now()
	return Completion{
		Claimed: c, Trigger: domain.TriggerSucceed, Outcome: domain.OutcomeSuccess, StatusCode: &code,
		AttemptedAt: now, CountsAttempt: true,
		Attempt: &domain.Attempt{
			ID: domain.NewID(), DeliveryID: c.DeliveryID, Number: c.AttemptCount + 1, Trigger: domain.AttemptAuto,
			StartedAt: now, Outcome: domain.OutcomeSuccess, StatusCode: &code, ConfigHash: "h",
			Request:  &domain.RecordedRequest{Method: "POST", URL: "https://x.example.com/", Headers: map[string]string{}},
			Response: &domain.RecordedResponse{StatusCode: 200},
		},
	}
}
