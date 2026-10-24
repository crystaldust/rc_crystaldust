package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

const (
	// ReclaimError is recorded on the attempt that stands for a call whose
	// worker lost its lease; it counts against the budget (§5.3).
	ReclaimError = "lease expired; outcome of previous attempt unknown"
	// orphanCheckInterval spaces out the scan for due deliveries whose
	// destination is no longer configured.
	orphanCheckInterval = time.Minute
)

// Status lists used verbatim in SQL. They are built from domain constants
// and inlined (not bound as parameters) so the planner can match the
// partial indexes' predicates.
var (
	claimableSQL = sqlList(claimableStatuses())
	inFlightSQL  = sqlList([]string{string(domain.StatusInFlight)})
)

func sqlList(values []string) string {
	return "('" + strings.Join(values, "','") + "')"
}

// Postgres is the PostgreSQL Dispatcher. owner identifies this worker in
// lease_owner; currentConfig is read on every call, never cached.
type Postgres struct {
	pool          *pgxpool.Pool
	owner         string
	currentConfig func() *config.Config
	log           *slog.Logger
	now           func() time.Time
	lastOrphanRun atomic.Int64
}

// NewPostgres returns a dispatcher claiming as owner.
func NewPostgres(pool *pgxpool.Pool, owner string, currentConfig func() *config.Config, log *slog.Logger, now func() time.Time) *Postgres {
	return &Postgres{pool: pool, owner: owner, currentConfig: currentConfig, log: log, now: now}
}

// Claim first takes over expired leases, then claims due deliveries, up to
// limit in total. Each step is its own short transaction; no lock is held
// while the deliveries are processed (ADR-0009 P1).
func (p *Postgres) Claim(ctx context.Context, limit int) ([]Claimed, error) {
	if limit <= 0 {
		return nil, nil
	}
	cfg := p.currentConfig()
	t := targetsOf(cfg)
	p.warnOrphans(ctx, t)
	reclaimed, err := p.reclaim(ctx, t, cfg.Hash(), limit)
	if err != nil {
		return nil, err
	}
	if len(reclaimed) == limit {
		return reclaimed, nil
	}
	claimed, err := p.claim(ctx, t, limit-len(reclaimed))
	if err != nil && len(reclaimed) > 0 {
		// The reclaim is committed: hand those deliveries over anyway, or
		// they would wait for another lease expiry and be charged again.
		p.log.Error("dispatch.claim_failed", "error", err, "reclaimed", len(reclaimed))
		return reclaimed, nil
	}
	if err != nil {
		return nil, err
	}
	return append(reclaimed, claimed...), nil
}

// claimSQL picks, per destination with free capacity, its oldest due
// deliveries whose ordering predecessors are settled, and moves them to
// IN_FLIGHT with a conditional update (M2). Capacity is computed from
// committed rows only, so with several workers max_in_flight is a soft
// limit: the excess is at most (workers − 1) × batch size.
var claimSQL = `WITH caps AS (
	SELECT * FROM unnest($1::text[], $2::int[], $3::bigint[]) AS c(destination_id, max_in_flight, lease_ms)
), inflight AS (
	SELECT destination_id, count(*) AS n FROM deliveries
	WHERE status IN ` + inFlightSQL + ` AND destination_id = ANY($1::text[])
	GROUP BY destination_id
), avail AS (
	SELECT c.destination_id, c.lease_ms, least(c.max_in_flight - coalesce(i.n, 0), $4) AS free
	FROM caps c LEFT JOIN inflight i USING (destination_id)
	WHERE c.max_in_flight - coalesce(i.n, 0) > 0
), locked AS (
	SELECT x.id, x.scheduled_at, x.ingest_seq, a.lease_ms
	FROM avail a CROSS JOIN LATERAL (
		SELECT d.id, d.scheduled_at, d.ingest_seq FROM deliveries d
		WHERE d.destination_id = a.destination_id
		  AND d.status IN ` + claimableSQL + ` AND d.scheduled_at <= now()
		  AND NOT (d.ordering_key IS NOT NULL
		      AND (d.destination_id = ANY($5::text[]) OR d.destination_id = ANY($6::text[]))
		      AND EXISTS (SELECT 1 FROM deliveries p
		          WHERE p.destination_id = d.destination_id AND p.ordering_key = d.ordering_key
		            AND p.ingest_seq < d.ingest_seq
		            AND NOT (p.status = ANY(CASE WHEN d.destination_id = ANY($6::text[])
		                                         THEN $8::text[] ELSE $7::text[] END))))
		ORDER BY d.scheduled_at, d.ingest_seq
		LIMIT a.free
		FOR UPDATE OF d SKIP LOCKED
	) x
), picked AS (
	SELECT id, lease_ms FROM locked ORDER BY scheduled_at, ingest_seq LIMIT $4
)
UPDATE deliveries d
SET status = $9, lease_owner = $10,
    lease_expires_at = now() + p.lease_ms * interval '1 millisecond', updated_at = now()
FROM picked p
WHERE d.id = p.id AND d.status IN ` + claimableSQL + `
RETURNING d.id, d.event_id, d.destination_id, d.policy_id, d.attempt_count, d.replay_count, d.lease_expires_at`

// claim moves claimable deliveries (claimableStatuses, taken from the
// transition table) to IN_FLIGHT.
func (p *Postgres) claim(ctx context.Context, t targets, limit int) ([]Claimed, error) {
	rows, err := p.pool.Query(ctx, claimSQL, t.ids, t.maxInFlight, t.leaseMS, limit,
		t.orderedBlock, t.orderedSkip,
		settledStatuses(domain.PredecessorDeadBlock), settledStatuses(domain.PredecessorDeadSkip),
		string(domain.StatusInFlight), p.owner)
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	claimed, err := pgx.CollectRows(rows, scanClaimed(false))
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	return claimed, nil
}

func scanClaimed(reclaimed bool) pgx.RowToFunc[Claimed] {
	return func(row pgx.CollectableRow) (Claimed, error) {
		c := Claimed{Reclaimed: reclaimed}
		err := row.Scan(&c.DeliveryID, &c.EventID, &c.DestinationID, &c.PolicyID,
			&c.AttemptCount, &c.ReplayCount, &c.LeaseExpiresAt)
		return c, err
	}
}

// reclaimSQL takes over IN_FLIGHT deliveries whose lease expired (§5.2
// Reclaim). The lost call may have reached the vendor, so it counts as an
// attempt (§5.3).
var reclaimSQL = `WITH caps AS (
	SELECT * FROM unnest($1::text[], $2::bigint[]) AS c(destination_id, lease_ms)
), expired AS (
	SELECT d.id FROM deliveries d
	WHERE d.status IN ` + inFlightSQL + ` AND d.lease_expires_at < now()
	  AND d.destination_id = ANY($1::text[])
	ORDER BY d.lease_expires_at
	LIMIT $3
	FOR UPDATE SKIP LOCKED
)
UPDATE deliveries d
SET lease_owner = $4, lease_expires_at = now() + c.lease_ms * interval '1 millisecond',
    attempt_count = d.attempt_count + 1, last_attempt_at = now(),
    last_outcome = $5, last_status_code = NULL, last_error = $6, updated_at = now()
FROM expired e JOIN caps c ON true
WHERE d.id = e.id AND c.destination_id = d.destination_id
  AND d.status IN ` + inFlightSQL + ` AND d.lease_expires_at < now()
RETURNING d.id, d.event_id, d.destination_id, d.policy_id, d.attempt_count, d.replay_count, d.lease_expires_at`

// reclaim applies IN_FLIGHT --Reclaim--> IN_FLIGHT (§5.2) to expired leases.
func (p *Postgres) reclaim(ctx context.Context, t targets, configHash string, limit int) ([]Claimed, error) {
	var out []Claimed
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, reclaimSQL, t.ids, t.leaseMS, limit, p.owner,
			string(domain.OutcomeRetryable), ReclaimError)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectRows(rows, scanClaimed(true)); err != nil {
			return err
		}
		for _, c := range out {
			if err := insertAttempt(ctx, tx, reclaimAttempt(c, configHash, p.now()), nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reclaim expired leases: %w", err)
	}
	return out, nil
}

func reclaimAttempt(c Claimed, configHash string, now time.Time) domain.Attempt {
	return domain.Attempt{
		ID:         domain.NewID(),
		DeliveryID: c.DeliveryID,
		Number:     c.AttemptCount,
		Trigger:    AttemptTrigger(c.ReplayCount),
		StartedAt:  now,
		Outcome:    domain.OutcomeRetryable,
		Error:      ReclaimError,
		ConfigHash: configHash,
	}
}

// AttemptTrigger is replay for every attempt after an operator replay (§5.3).
func AttemptTrigger(replayCount int) domain.AttemptTrigger {
	if replayCount > 0 {
		return domain.AttemptReplay
	}
	return domain.AttemptAuto
}

// completeSQL applies the transition only while this worker still holds
// the same lease; lease_expires_at is the fencing token, so a delivery
// reclaimed by anyone, even this worker, is never overwritten by a late
// result. $9 is the retry delay in milliseconds (NULL: keep scheduled_at).
const completeSQL = `UPDATE deliveries
SET status = $3, attempt_count = attempt_count + $4,
    last_attempt_at = coalesce($5, last_attempt_at), last_outcome = $6,
    last_status_code = $7, last_error = $8,
    scheduled_at = coalesce(now() + $9::bigint * interval '1 millisecond', scheduled_at),
    lease_owner = NULL, lease_expires_at = NULL,
    completed_at = CASE WHEN $10 THEN now() END, updated_at = now()
WHERE id = $1 AND status = $11 AND lease_owner = $2 AND lease_expires_at = $12`

// Complete writes the attempt and the transition in one transaction
// (ADR-0004). The attempt is kept even when the lease was lost.
func (p *Postgres) Complete(ctx context.Context, c Completion) (bool, error) {
	to, err := domain.Transition(domain.StatusInFlight, c.Trigger)
	if err != nil {
		return false, err
	}
	counts := 0
	if c.CountsAttempt {
		counts = 1
	}
	var attemptedAt *time.Time
	if c.Attempt != nil {
		attemptedAt = &c.AttemptedAt
	}
	delayMS := millis(c.RetryDelay)
	applied := false
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, completeSQL, c.Claimed.DeliveryID, p.owner, string(to), counts,
			attemptedAt, string(c.Outcome), c.StatusCode, nullIfEmpty(c.Error), delayMS,
			to.IsTerminal(), string(domain.StatusInFlight), c.Claimed.LeaseExpiresAt)
		if err != nil {
			return err
		}
		applied = tag.RowsAffected() == 1
		if c.Attempt == nil {
			return nil
		}
		return insertAttempt(ctx, tx, *c.Attempt, delayMS)
	})
	if err != nil {
		return false, fmt.Errorf("complete delivery %s: %w", c.Claimed.DeliveryID, err)
	}
	return applied, nil
}

// insertAttemptSQL derives next_attempt_at from the same transaction time
// as deliveries.scheduled_at; $13 is the retry delay in milliseconds.
const insertAttemptSQL = `INSERT INTO attempts (id, delivery_id, number, trigger, started_at,
	duration_ms, outcome, status_code, error, request, response, config_hash, next_attempt_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now() + $13::bigint * interval '1 millisecond')`

func millis(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}
	ms := d.Milliseconds()
	return &ms
}

func insertAttempt(ctx context.Context, tx pgx.Tx, a domain.Attempt, retryDelayMS *int64) error {
	req, err := jsonOrNil(a.Request)
	if err != nil {
		return err
	}
	resp, err := jsonOrNil(a.Response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, insertAttemptSQL, a.ID, a.DeliveryID, a.Number, string(a.Trigger),
		a.StartedAt, a.DurationMS, string(a.Outcome), a.StatusCode, nullIfEmpty(a.Error),
		req, resp, a.ConfigHash, retryDelayMS)
	if err != nil {
		return fmt.Errorf("insert attempt: %w", err)
	}
	return nil
}

// jsonOrNil encodes a record for a jsonb column; nil stays SQL NULL.
func jsonOrNil[T any](v *T) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode attempt record: %w", err)
	}
	return b, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var orphanSQL = `SELECT DISTINCT destination_id FROM deliveries
WHERE status IN ` + claimableSQL + ` AND scheduled_at <= now() AND NOT (destination_id = ANY($1::text[]))`

// warnOrphans logs, at most once per orphanCheckInterval, due deliveries
// whose destination was removed from the config: they are never claimed
// and wait until it returns (alerting is Phase 5).
func (p *Postgres) warnOrphans(ctx context.Context, t targets) {
	now := p.now()
	last := p.lastOrphanRun.Load()
	if now.UnixNano()-last < int64(orphanCheckInterval) || !p.lastOrphanRun.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	rows, err := p.pool.Query(ctx, orphanSQL, t.ids)
	if err != nil {
		p.log.Warn("dispatch.orphan_check_failed", "error", err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		p.log.Warn("dispatch.orphan_check_failed", "error", err)
		return
	}
	if len(ids) > 0 {
		p.log.Warn("dispatch.unknown_destinations", "destination_ids", ids)
	}
}
