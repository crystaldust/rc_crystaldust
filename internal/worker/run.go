// Package worker claims and delivers deliveries (Phase 4). It depends on
// the dispatch.Dispatcher interface, never on how deliveries are
// dispatched (ADR-0009 M1), and reads the config through Runtime on every
// delivery.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"messagehub/internal/app"
	"messagehub/internal/config"
	"messagehub/internal/dispatch"
	"messagehub/internal/outbound"
	"messagehub/internal/secrets"
)

const (
	// claimBatchSize caps one Claim call; with several workers it also
	// bounds how far max_in_flight can be exceeded (dispatch.claimSQL).
	claimBatchSize = 32
	// shutdownMargin leaves the serve command time to exit after the
	// worker has abandoned unfinished deliveries.
	shutdownMargin = time.Second
)

// sender sends one vendor request (outbound.Client).
type sender interface {
	Send(ctx context.Context, req outbound.Request, timeout time.Duration) (outbound.Response, error)
}

// authorizer returns the outbound Authorization header (outbound.TokenProvider).
type authorizer interface {
	Authorization(destinationID, configHash string, auth config.Auth) (string, error)
}

// Worker processes claimed deliveries with bounded concurrency.
type Worker struct {
	config       func() *config.Config
	secrets      secrets.Resolver
	dispatcher   dispatch.Dispatcher
	events       EventLoader
	client       sender
	tokens       authorizer
	log          *slog.Logger
	now          func() time.Time
	rnd          func() float64
	concurrency  int
	drainTimeout time.Duration
}

// Run processes deliveries until ctx is cancelled, then waits for those in
// flight (bounded by HUB_SHUTDOWN_TIMEOUT); any left unfinished are taken
// over by another worker once their lease expires.
func Run(ctx context.Context, rt *app.Runtime) error {
	transport := outbound.NewTransport()
	defer transport.CloseIdleConnections()
	w := &Worker{
		config:       rt.Config.Current,
		secrets:      rt.Secrets,
		dispatcher:   dispatch.NewPostgres(rt.Pool, rt.Settings.WorkerID, rt.Config.Current, rt.Log, rt.Now),
		events:       pgEvents{pool: rt.Pool},
		client:       outbound.NewClient(transport),
		tokens:       outbound.NewTokenProvider(rt.Secrets, transport),
		log:          rt.Log,
		now:          rt.Now,
		rnd:          rand.Float64,
		concurrency:  rt.Settings.WorkerConcurrency,
		drainTimeout: max(rt.Settings.ShutdownTimeout-shutdownMargin, 0),
	}
	return w.Loop(ctx, dispatch.Wakeups(ctx, rt.Settings.DatabaseListenURL, dispatch.PollInterval, rt.Log))
}

// Loop claims on every wakeup and whenever a slot frees up, until ctx is
// cancelled. Each delivery runs in its own goroutine and completes on its
// own, so a slow destination never holds up another (§8).
func (w *Worker) Loop(ctx context.Context, wake <-chan struct{}) error {
	// Deliveries outlive the shutdown signal until drainTimeout.
	deliverCtx, abandon := context.WithCancel(context.WithoutCancel(ctx))
	defer abandon()
	slots := make(chan struct{}, w.concurrency)
	freed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			w.drain(&wg, abandon)
			return ctx.Err()
		case <-wake:
		case <-freed:
		}
		w.claimAvailable(ctx, deliverCtx, slots, freed, &wg)
	}
}

// claimAvailable claims until the slots are full or nothing is due.
func (w *Worker) claimAvailable(ctx, deliverCtx context.Context, slots, freed chan struct{}, wg *sync.WaitGroup) {
	for ctx.Err() == nil {
		want := min(cap(slots)-len(slots), claimBatchSize)
		if want == 0 {
			return
		}
		claimed, err := w.dispatcher.Claim(ctx, want)
		if err != nil {
			if ctx.Err() == nil {
				w.log.Error("dispatch.claim_failed", "error", err)
			}
			return
		}
		for _, c := range claimed {
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					<-slots
					notify(freed)
				}()
				defer w.recoverDelivery(c)
				w.Process(deliverCtx, c)
			}()
		}
		if len(claimed) < want {
			return
		}
	}
}

// recoverDelivery keeps a bug triggered by one delivery from stopping every
// other one. The delivery stays IN_FLIGHT and is reclaimed when its lease
// expires.
func (w *Worker) recoverDelivery(c dispatch.Claimed) {
	if r := recover(); r != nil {
		w.log.Error("delivery.panic", "delivery_id", c.DeliveryID, "destination_id", c.DestinationID,
			"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	}
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// drain waits for in-flight deliveries, then abandons the rest: their
// requests are cancelled and their leases expire for another worker.
func (w *Worker) drain(wg *sync.WaitGroup, abandon context.CancelFunc) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(w.drainTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		w.log.Warn("worker.drain_timeout", "timeout_ms", w.drainTimeout.Milliseconds())
		abandon()
		<-done
	}
}
