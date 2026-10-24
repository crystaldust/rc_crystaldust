// Package store is the PostgreSQL data access of the api role: ingest,
// queries and operator actions (docs/design/domain-model.md §3, §5.2, §6,
// §13.4). Every state change is a conditional update (ADR-0009 M2). SQL is
// hand written and always parameterised. It depends on domain, config and
// platform/pg; the api package depends on it through small interfaces.
package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

var (
	// ErrNotFound means the row does not exist or is outside the caller's Scope.
	ErrNotFound = errors.New("not found")
	// ErrInvalidState means the delivery's status does not allow the action.
	ErrInvalidState = errors.New("invalid delivery state")
	// ErrInvalidInput means PostgreSQL rejected a value (SQLSTATE class 22,
	// data exception, e.g. 22P05 NUL in jsonb, 22021 bad encoding, 22003
	// numeric overflow). The API validates these first; this is the backstop.
	ErrInvalidInput = errors.New("value rejected by the database")
)

// dataExceptionClass is the SQLSTATE class of invalid input values.
const dataExceptionClass = "22"

// classify turns a PostgreSQL data exception into ErrInvalidInput.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, dataExceptionClass) {
		return fmt.Errorf("%w (SQLSTATE %s): %w", ErrInvalidInput, pgErr.Code, err)
	}
	return err
}

// StateError is an ErrInvalidState that carries the status found.
type StateError struct {
	Action  string
	Current domain.DeliveryStatus
}

func (e *StateError) Error() string {
	return fmt.Sprintf("cannot %s a delivery in status %s", e.Action, e.Current)
}

func (e *StateError) Unwrap() error { return ErrInvalidState }

// Store runs queries on the shared pool. currentConfig returns the config
// in effect (ConfigHolder.Current); it is read per call, never cached.
type Store struct {
	pool          *pgxpool.Pool
	currentConfig func() *config.Config
}

// New returns a Store.
func New(pool *pgxpool.Pool, currentConfig func() *config.Config) *Store {
	return &Store{pool: pool, currentConfig: currentConfig}
}

// Scope limits reads to one source's data; the zero value reads everything.
type Scope struct {
	Source string
}

// DeliveryView is a delivery plus the derived blocked_by (§6).
type DeliveryView struct {
	domain.Delivery
	BlockedBy *uuid.UUID
}

// EventDetail is an event with its payload and deliveries.
type EventDetail struct {
	Event      domain.Event
	Deliveries []DeliveryView
}

// DeliveryDetail is a delivery with one page of its attempts, newest first.
type DeliveryDetail struct {
	Delivery   DeliveryView
	Attempts   []domain.Attempt
	NextCursor *Cursor
}

// Page is one page of a list; NextCursor is nil on the last page.
type Page[T any] struct {
	Items      []T
	NextCursor *Cursor
}

const eventColumns = `e.id, e.source, e.source_event_id, e.event_type, e.occurred_at,
	e.received_at, e.ingest_seq, e.idempotency_key, e.ordering_key, e.metadata,
	e.routing_status, e.matched_route_ids, e.routing_config_hash`

func scanEvent(row pgx.Row) (domain.Event, error) {
	var e domain.Event
	err := scanEventInto(row, &e)
	return e, err
}

// scanEventInto scans eventColumns into e, followed by any extra columns.
func scanEventInto(row pgx.Row, e *domain.Event, extra ...any) error {
	var (
		idempotencyKey, orderingKey *string
		routingStatus               string
	)
	dest := append([]any{&e.ID, &e.Source, &e.SourceEventID, &e.EventType, &e.OccurredAt,
		&e.ReceivedAt, &e.IngestSeq, &idempotencyKey, &orderingKey, &e.Metadata,
		&routingStatus, &e.MatchedRouteIDs, &e.RoutingConfigHash}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	e.IdempotencyKey = deref(idempotencyKey)
	e.OrderingKey = deref(orderingKey)
	e.RoutingStatus = domain.RoutingStatus(routingStatus)
	return nil
}

const deliveryColumns = `d.id, d.event_id, d.destination_id, d.policy_id, d.status,
	d.ordering_key, d.ingest_seq, d.attempt_count, d.replay_count, d.scheduled_at,
	d.lease_owner, d.lease_expires_at, d.last_attempt_at, d.last_outcome,
	d.last_status_code, d.last_error, d.created_at, d.updated_at, d.completed_at`

func scanDelivery(row pgx.Row) (domain.Delivery, error) {
	var (
		d                                       domain.Delivery
		status                                  string
		orderingKey, leaseOwner, outcome, lastE *string
	)
	err := row.Scan(&d.ID, &d.EventID, &d.DestinationID, &d.PolicyID, &status,
		&orderingKey, &d.IngestSeq, &d.AttemptCount, &d.ReplayCount, &d.ScheduledAt,
		&leaseOwner, &d.LeaseExpiresAt, &d.LastAttemptAt, &outcome,
		&d.LastStatusCode, &lastE, &d.CreatedAt, &d.UpdatedAt, &d.CompletedAt)
	if err != nil {
		return domain.Delivery{}, err
	}
	d.Status = domain.DeliveryStatus(status)
	d.OrderingKey = deref(orderingKey)
	d.LeaseOwner = deref(leaseOwner)
	d.LastOutcome = domain.Outcome(deref(outcome))
	d.LastError = deref(lastE)
	return d, nil
}

// nullIfEmpty maps an absent optional string to SQL NULL (§13.1).
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// timeOrNil passes an optional time filter to SQL.
func timeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
