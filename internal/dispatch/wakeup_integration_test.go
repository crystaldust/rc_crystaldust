//go:build integration

package dispatch

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"messagehub/internal/platform/pg"
)

const noPoll = time.Hour

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func expectSignal(t *testing.T, ch <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed", what)
		}
	case <-time.After(within):
		t.Fatalf("%s: no signal within %v", what, within)
	}
}

func notify(t *testing.T, f fixture) {
	t.Helper()
	f.exec(t, `SELECT pg_notify($1, '')`, pg.DeliveryReadyChannel)
}

// waitListening waits for the initial signal sent once LISTEN succeeded.
func waitListening(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	expectSignal(t, ch, 10*time.Second, "initial LISTEN signal")
}

func TestWakeupsDeliverNotificationsQuickly(t *testing.T) {
	// Arrange
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := Wakeups(ctx, testDSN, noPoll, discardLog())
	waitListening(t, ch)

	// Act
	start := time.Now()
	notify(t, f)

	// Assert
	expectSignal(t, ch, 100*time.Millisecond, "notification")
	t.Logf("notification latency %v", time.Since(start))
}

func TestWakeupsCoalesceBurstsOfNotifications(t *testing.T) {
	// Arrange
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := Wakeups(ctx, testDSN, noPoll, discardLog())
	waitListening(t, ch)

	// Act: several notifications arrive while nobody reads.
	for range 5 {
		notify(t, f)
	}
	time.Sleep(200 * time.Millisecond)

	// Assert: one pending signal stands for all of them.
	expectSignal(t, ch, time.Second, "coalesced signal")
	select {
	case <-ch:
		t.Fatal("got a second signal; bursts must coalesce")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWakeupsReconnectAfterConnectionLoss(t *testing.T) {
	// Arrange
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := Wakeups(ctx, testDSN, noPoll, discardLog())
	waitListening(t, ch)

	// Act
	f.exec(t, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE pid <> pg_backend_pid() AND query ILIKE 'LISTEN%'`)

	// Assert: the reconnect signals once, then notifications flow again.
	expectSignal(t, ch, 10*time.Second, "reconnect")
	notify(t, f)
	expectSignal(t, ch, time.Second, "notification after reconnect")
}

func TestWakeupsPollWhenListenIsUnavailable(t *testing.T) {
	// Arrange: a DSN nobody listens on, so only the poll timer fires.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}

	// Act
	ch := Wakeups(ctx, "postgres://hub:hub@127.0.0.1:1/messagehub?connect_timeout=1", 50*time.Millisecond,
		slog.New(slog.NewTextHandler(logs, nil)))

	// Assert
	expectSignal(t, ch, time.Second, "first poll")
	expectSignal(t, ch, time.Second, "second poll")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "dispatch.listen_lost") {
		if time.Now().After(deadline) {
			t.Fatal("listen failure was not logged")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWakeupsCloseAfterCancel(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	ch := Wakeups(ctx, testDSN, noPoll, discardLog())
	waitListening(t, ch)

	// Act
	cancel()

	// Assert
	select {
	case _, ok := <-ch:
		if ok {
			<-ch // a final buffered signal may precede the close
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}
