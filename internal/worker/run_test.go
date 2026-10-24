package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"messagehub/internal/dispatch"
	"messagehub/internal/outbound"
)

// queueDispatcher hands out queued claims and records completions.
type queueDispatcher struct {
	fakeDispatcher
	mu       sync.Mutex
	queue    []dispatch.Claimed
	claimErr error
}

func (q *queueDispatcher) Claim(_ context.Context, limit int) ([]dispatch.Claimed, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	n := min(limit, len(q.queue))
	out := q.queue[:n]
	q.queue = q.queue[n:]
	return out, nil
}

func (q *queueDispatcher) done() int {
	q.fakeDispatcher.mu.Lock()
	defer q.fakeDispatcher.mu.Unlock()
	return len(q.completions)
}

// gateSender blocks every request until release is closed (or its context
// ends) and tracks the highest concurrency seen.
type gateSender struct {
	release  chan struct{}
	inFlight atomic.Int32
	peak     atomic.Int32
	canceled atomic.Int32
}

func (g *gateSender) Send(ctx context.Context, _ outbound.Request, _ time.Duration) (outbound.Response, error) {
	n := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-g.release:
		return outbound.Response{StatusCode: 200, BodyComplete: true}, nil
	case <-ctx.Done():
		g.canceled.Add(1)
		return outbound.Response{}, ctx.Err()
	}
}

func loopWorker(t *testing.T, disp dispatch.Dispatcher, client sender, concurrency int, drain time.Duration) *Worker {
	t.Helper()
	h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok", "order": "o-1"}}, fakeTokens{}, tokenEnv)
	w := h.w
	w.dispatcher, w.client = disp, client
	w.concurrency, w.drainTimeout = concurrency, drain
	return w
}

func queued(n int) []dispatch.Claimed {
	out := make([]dispatch.Claimed, n)
	for i := range out {
		out[i] = claimed("plain", 0)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLoopProcessesClaimsWithinConcurrency(t *testing.T) {
	// Arrange
	disp := &queueDispatcher{fakeDispatcher: fakeDispatcher{applied: true}, queue: queued(7)}
	gate := &gateSender{release: make(chan struct{})}
	w := loopWorker(t, disp, gate, 3, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	result := make(chan error, 1)

	// Act
	go func() { result <- w.Loop(ctx, wake) }()
	wake <- struct{}{}
	waitFor(t, "three requests in flight", func() bool { return gate.inFlight.Load() == 3 })
	close(gate.release)
	waitFor(t, "all deliveries completed", func() bool { return disp.done() == 7 })
	cancel()

	// Assert
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Loop returned %v", err)
	}
	if peak := gate.peak.Load(); peak > 3 {
		t.Fatalf("peak concurrency = %d, want <= 3", peak)
	}
}

func TestLoopWaitsForInFlightOnShutdown(t *testing.T) {
	// Arrange
	disp := &queueDispatcher{fakeDispatcher: fakeDispatcher{applied: true}, queue: queued(2)}
	gate := &gateSender{release: make(chan struct{})}
	w := loopWorker(t, disp, gate, 4, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() { result <- w.Loop(ctx, wake) }()
	wake <- struct{}{}
	waitFor(t, "requests in flight", func() bool { return gate.inFlight.Load() == 2 })

	// Act
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(gate.release)

	// Assert
	<-result
	if disp.done() != 2 || gate.canceled.Load() != 0 {
		t.Fatalf("completed = %d, canceled = %d; want both finished normally", disp.done(), gate.canceled.Load())
	}
}

func TestLoopAbandonsDeliveriesAfterDrainTimeout(t *testing.T) {
	// Arrange
	disp := &queueDispatcher{fakeDispatcher: fakeDispatcher{applied: true}, queue: queued(1)}
	gate := &gateSender{release: make(chan struct{})}
	w := loopWorker(t, disp, gate, 4, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() { result <- w.Loop(ctx, wake) }()
	wake <- struct{}{}
	waitFor(t, "request in flight", func() bool { return gate.inFlight.Load() == 1 })

	// Act
	cancel()

	// Assert
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return after the drain timeout")
	}
	if gate.canceled.Load() != 1 {
		t.Fatalf("canceled = %d, want the request abandoned", gate.canceled.Load())
	}
}

func TestLoopKeepsRunningAfterClaimError(t *testing.T) {
	// Arrange
	disp := &queueDispatcher{claimErr: errors.New("db down")}
	w := loopWorker(t, disp, &gateSender{release: make(chan struct{})}, 1, time.Second)
	w.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	result := make(chan error, 1)

	// Act
	go func() { result <- w.Loop(ctx, wake) }()
	wake <- struct{}{}
	wake <- struct{}{}
	cancel()

	// Assert
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Loop returned %v", err)
	}
}

type panicSender struct{}

func (panicSender) Send(context.Context, outbound.Request, time.Duration) (outbound.Response, error) {
	panic("boom")
}

func TestLoopSurvivesAPanickingDelivery(t *testing.T) {
	// Arrange
	disp := &queueDispatcher{fakeDispatcher: fakeDispatcher{applied: true}, queue: queued(2)}
	w := loopWorker(t, disp, panicSender{}, 1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	result := make(chan error, 1)

	// Act
	go func() { result <- w.Loop(ctx, wake) }()
	wake <- struct{}{}
	waitFor(t, "both deliveries taken", func() bool {
		disp.mu.Lock()
		defer disp.mu.Unlock()
		return len(disp.queue) == 0
	})
	cancel()

	// Assert: the loop is still alive and stops cleanly; nothing completed.
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Loop returned %v", err)
	}
	if disp.done() != 0 {
		t.Fatalf("completions = %d, want none after panics", disp.done())
	}
}
