// Package dispatch hands deliveries to workers (ADR-0009 M1). The worker
// depends only on the Dispatcher interface; pgDispatcher implements it on
// PostgreSQL with FOR UPDATE SKIP LOCKED to find candidates and conditional
// updates for every state change (M2). Wakeups merges LISTEN/NOTIFY with a
// polling fallback (P4, P5). It depends on domain, config and platform/pg.
package dispatch

import (
	"context"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
)

// Claimed is one delivery handed to this worker, now IN_FLIGHT under its
// lease. LeaseExpiresAt doubles as the fencing token for Complete.
type Claimed struct {
	DeliveryID     uuid.UUID
	EventID        uuid.UUID
	DestinationID  string
	PolicyID       string
	AttemptCount   int
	ReplayCount    int
	Reclaimed      bool
	LeaseExpiresAt time.Time
}

// Completion is the result of processing one Claimed delivery. Attempt is
// nil when no request was attempted (e.g. budget already spent on
// reclaim); it is written even if the lease was lost, because the call did
// happen.
//
// RetryDelay is set for Retry and InfraRetry. The next attempt is due that
// long after the database's current time, which also fills the attempt's
// next_attempt_at (Attempt.NextAttemptAt is ignored): scheduling, like
// leases, uses the database clock only, so a skewed worker clock can
// neither fire retries early nor hold them back.
type Completion struct {
	Claimed       Claimed
	Trigger       domain.Trigger
	Outcome       domain.Outcome
	StatusCode    *int
	Error         string
	AttemptedAt   time.Time
	RetryDelay    *time.Duration
	CountsAttempt bool
	Attempt       *domain.Attempt
}

// Dispatcher is the boundary between scheduling and delivery. A future MQ
// implementation replaces it without touching the worker (ADR-0009).
type Dispatcher interface {
	// Claim moves up to limit due deliveries to IN_FLIGHT for this worker,
	// first taking over deliveries whose lease expired.
	Claim(ctx context.Context, limit int) ([]Claimed, error)
	// Complete records the attempt and applies the transition if this
	// worker still holds the lease; applied reports whether it did.
	Complete(ctx context.Context, c Completion) (applied bool, err error)
}
