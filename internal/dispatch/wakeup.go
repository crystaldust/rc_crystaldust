package dispatch

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"messagehub/internal/platform/pg"
)

const (
	// PollInterval is the fallback claim interval when no notification
	// arrives (ADR-0009 P5).
	PollInterval = time.Second
	// Reconnect backoff for the LISTEN connection.
	listenRetryInitial = 500 * time.Millisecond
	listenRetryMax     = 30 * time.Second
)

// Wakeups returns a channel that receives a signal whenever deliveries may
// have become claimable: on NOTIFY over a direct connection (ADR-0009 P4)
// and every pollInterval regardless, so a lost notification or a broken
// connection only delays claims (P5). Signals coalesce (buffer of one). The
// channel is closed after ctx is done.
func Wakeups(ctx context.Context, listenDSN string, pollInterval time.Duration, log *slog.Logger) <-chan struct{} {
	out := make(chan struct{}, 1)
	signal := func() {
		select {
		case out <- struct{}{}:
		default:
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		listenLoop(ctx, listenDSN, signal, log)
	}()
	go func() {
		defer close(out)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				<-done
				return
			case <-ticker.C:
				signal()
			}
		}
	}()
	return out
}

// listenLoop keeps one LISTEN connection open, reconnecting with capped
// exponential backoff, until ctx is done.
func listenLoop(ctx context.Context, dsn string, signal func(), log *slog.Logger) {
	delay := listenRetryInitial
	for ctx.Err() == nil {
		listened, err := listenOnce(ctx, dsn, signal)
		if ctx.Err() != nil {
			return
		}
		if listened {
			delay = listenRetryInitial
		}
		log.Warn("dispatch.listen_lost", "error", err, "retry_in_ms", delay.Milliseconds())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, listenRetryMax)
	}
}

// listenOnce connects, subscribes and forwards notifications until the
// connection fails. listened reports whether the subscription succeeded.
func listenOnce(ctx context.Context, dsn string, signal func()) (listened bool, err error) {
	conn, err := pg.ConnectDirect(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer closeQuietly(conn)
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{pg.DeliveryReadyChannel}.Sanitize()); err != nil {
		return false, err
	}
	// Anything committed while the connection was down is picked up now.
	signal()
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return true, err
		}
		signal()
	}
}

func closeQuietly(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// The connection is being discarded after a failure or shutdown; a
	// close error carries no information worth acting on.
	_ = conn.Close(ctx)
}
