package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"messagehub/internal/domain"
)

// Page sizes for list endpoints and attempt pages (OpenAPI Limit parameter).
const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// EventFilter selects events; zero fields do not filter (OpenAPI listEvents).
type EventFilter struct {
	SourceEventID string
	EventType     string
	OrderingKey   string
	RoutingStatus domain.RoutingStatus
	Status        domain.DeliveryStatus // events having a delivery in this status
	ReceivedFrom  *time.Time            // inclusive
	ReceivedTo    *time.Time            // exclusive
	Cursor        *Cursor
	Limit         int
}

// DeliveryFilter selects deliveries; zero fields do not filter.
type DeliveryFilter struct {
	EventID       *uuid.UUID
	DestinationID string
	Status        domain.DeliveryStatus
	CreatedFrom   *time.Time // inclusive
	CreatedTo     *time.Time // exclusive
	Cursor        *Cursor
	Limit         int
}

// AttemptPage selects one page of a delivery's attempts.
type AttemptPage struct {
	Cursor *Cursor
	Limit  int
}

// where accumulates AND conditions. Only placeholder numbers are formatted
// into the SQL text; every value travels as a bind argument.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(condFormat string, arg any) {
	w.args = append(w.args, arg)
	w.conds = append(w.conds, fmt.Sprintf(condFormat, len(w.args)))
}

// addBefore adds the keyset condition (t, id) < (cursor.Time, cursor.ID),
// written so the leading time column can bound an index scan.
func (w *where) addBefore(timeCol, idCol string, c *Cursor) {
	if c == nil {
		return
	}
	w.args = append(w.args, c.Time, c.ID)
	t, id := len(w.args)-1, len(w.args)
	w.conds = append(w.conds, fmt.Sprintf("%s <= $%d AND (%s < $%d OR %s < $%d)", timeCol, t, timeCol, t, idCol, id))
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

func normalizeLimit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	return min(n, MaxLimit)
}

// pageOf trims the extra row fetched to detect a next page.
func pageOf[T any](items []T, limit int, cursorOf func(T) Cursor) Page[T] {
	if len(items) <= limit {
		return Page[T]{Items: items}
	}
	items = items[:limit]
	next := cursorOf(items[limit-1])
	return Page[T]{Items: items, NextCursor: &next}
}

// GetEvent returns an event with its payload and deliveries.
func (s *Store) GetEvent(ctx context.Context, scope Scope, id uuid.UUID) (EventDetail, error) {
	w := &where{}
	w.add("e.id = $%d", id)
	if scope.Source != "" {
		w.add("e.source = $%d", scope.Source)
	}
	row := s.pool.QueryRow(ctx, `SELECT `+eventColumns+`, p.payload::text
		FROM events e LEFT JOIN event_payloads p ON p.event_id = e.id`+w.sql(), w.args...)
	ev, payload, err := scanEventWithPayload(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return EventDetail{}, ErrNotFound
	}
	if err != nil {
		return EventDetail{}, fmt.Errorf("get event: %w", err)
	}
	ev.Payload = payload
	rows, err := s.pool.Query(ctx, `SELECT `+deliveryColumns+` FROM deliveries d
		WHERE d.event_id = $1 ORDER BY d.created_at, d.id`, id)
	if err != nil {
		return EventDetail{}, fmt.Errorf("get event deliveries: %w", err)
	}
	deliveries, err := s.collectDeliveryViews(ctx, scope, rows)
	if err != nil {
		return EventDetail{}, fmt.Errorf("get event deliveries: %w", err)
	}
	return EventDetail{Event: ev, Deliveries: deliveries}, nil
}

// emptyPayload stands in for a payload already purged by retention (Phase 5).
const emptyPayload = "{}"

func scanEventWithPayload(row pgx.Row) (domain.Event, []byte, error) {
	var payload *string
	var ev domain.Event
	err := scanEventInto(row, &ev, &payload)
	if err != nil {
		return domain.Event{}, nil, err
	}
	if payload == nil {
		return ev, []byte(emptyPayload), nil
	}
	return ev, []byte(*payload), nil
}

// ListEvents returns one page of events, newest first.
func (s *Store) ListEvents(ctx context.Context, scope Scope, f EventFilter) (Page[domain.Event], error) {
	query, args, limit := eventsQuery(scope, f)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[domain.Event]{}, fmt.Errorf("list events: %w", classify(err))
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Event, error) { return scanEvent(r) })
	if err != nil {
		return Page[domain.Event]{}, fmt.Errorf("list events: %w", err)
	}
	return pageOf(events, limit, func(e domain.Event) Cursor { return Cursor{Time: e.ReceivedAt, ID: e.ID} }), nil
}

// eventsQuery builds the ListEvents SQL; it fetches limit+1 rows to detect
// a next page.
func eventsQuery(scope Scope, f EventFilter) (query string, args []any, limit int) {
	w := &where{}
	addIf(w, scope.Source != "", "e.source = $%d", scope.Source)
	addIf(w, f.SourceEventID != "", "e.source_event_id = $%d", f.SourceEventID)
	addIf(w, f.EventType != "", "e.event_type = $%d", f.EventType)
	addIf(w, f.OrderingKey != "", "e.ordering_key = $%d", f.OrderingKey)
	addIf(w, f.RoutingStatus != "", "e.routing_status = $%d", string(f.RoutingStatus))
	addIf(w, f.Status != "", "EXISTS (SELECT 1 FROM deliveries d WHERE d.event_id = e.id AND d.status = $%d)", string(f.Status))
	addIf(w, f.ReceivedFrom != nil, "e.received_at >= $%d", timeOrNil(f.ReceivedFrom))
	addIf(w, f.ReceivedTo != nil, "e.received_at < $%d", timeOrNil(f.ReceivedTo))
	w.addBefore("e.received_at", "e.id", f.Cursor)
	limit = normalizeLimit(f.Limit)
	w.args = append(w.args, limit+1)
	query = `SELECT ` + eventColumns + ` FROM events e` + w.sql() +
		fmt.Sprintf(" ORDER BY e.received_at DESC, e.id DESC LIMIT $%d", len(w.args))
	return query, w.args, limit
}

func addIf(w *where, ok bool, condFormat string, arg any) {
	if ok {
		w.add(condFormat, arg)
	}
}

// ListDeliveries returns one page of deliveries, newest first.
func (s *Store) ListDeliveries(ctx context.Context, scope Scope, f DeliveryFilter) (Page[DeliveryView], error) {
	query, args, limit := deliveriesQuery(scope, f)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[DeliveryView]{}, fmt.Errorf("list deliveries: %w", classify(err))
	}
	views, err := s.collectDeliveryViews(ctx, scope, rows)
	if err != nil {
		return Page[DeliveryView]{}, fmt.Errorf("list deliveries: %w", err)
	}
	return pageOf(views, limit, func(v DeliveryView) Cursor { return Cursor{Time: v.CreatedAt, ID: v.ID} }), nil
}

// deliveriesQuery builds the ListDeliveries SQL; it fetches limit+1 rows.
func deliveriesQuery(scope Scope, f DeliveryFilter) (query string, args []any, limit int) {
	w := &where{}
	from := " FROM deliveries d"
	if scope.Source != "" {
		from += " JOIN events e ON e.id = d.event_id"
		w.add("e.source = $%d", scope.Source)
	}
	if f.EventID != nil {
		w.add("d.event_id = $%d", *f.EventID)
	}
	addIf(w, f.DestinationID != "", "d.destination_id = $%d", f.DestinationID)
	addIf(w, f.Status != "", "d.status = $%d", string(f.Status))
	addIf(w, f.CreatedFrom != nil, "d.created_at >= $%d", timeOrNil(f.CreatedFrom))
	addIf(w, f.CreatedTo != nil, "d.created_at < $%d", timeOrNil(f.CreatedTo))
	w.addBefore("d.created_at", "d.id", f.Cursor)
	limit = normalizeLimit(f.Limit)
	w.args = append(w.args, limit+1)
	query = `SELECT ` + deliveryColumns + from + w.sql() +
		fmt.Sprintf(" ORDER BY d.created_at DESC, d.id DESC LIMIT $%d", len(w.args))
	return query, w.args, limit
}

// GetDelivery returns a delivery and one page of its attempts, newest first.
func (s *Store) GetDelivery(ctx context.Context, scope Scope, id uuid.UUID, page AttemptPage) (DeliveryDetail, error) {
	w := &where{}
	from := " FROM deliveries d"
	w.add("d.id = $%d", id)
	if scope.Source != "" {
		from += " JOIN events e ON e.id = d.event_id"
		w.add("e.source = $%d", scope.Source)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+deliveryColumns+from+w.sql(), w.args...)
	if err != nil {
		return DeliveryDetail{}, fmt.Errorf("get delivery: %w", err)
	}
	views, err := s.collectDeliveryViews(ctx, scope, rows)
	if err != nil {
		return DeliveryDetail{}, fmt.Errorf("get delivery: %w", err)
	}
	if len(views) == 0 {
		return DeliveryDetail{}, ErrNotFound
	}
	attempts, err := s.listAttempts(ctx, id, page)
	if err != nil {
		return DeliveryDetail{}, err
	}
	return DeliveryDetail{Delivery: views[0], Attempts: attempts.Items, NextCursor: attempts.NextCursor}, nil
}

const attemptColumns = `a.id, a.delivery_id, a.number, a.trigger, a.started_at, a.duration_ms,
	a.outcome, a.status_code, a.error, a.request, a.response, a.config_hash, a.next_attempt_at`

func (s *Store) listAttempts(ctx context.Context, deliveryID uuid.UUID, page AttemptPage) (Page[domain.Attempt], error) {
	w := &where{}
	w.add("a.delivery_id = $%d", deliveryID)
	w.addBefore("a.started_at", "a.id", page.Cursor)
	limit := normalizeLimit(page.Limit)
	w.args = append(w.args, limit+1)
	rows, err := s.pool.Query(ctx, `SELECT `+attemptColumns+` FROM attempts a`+w.sql()+
		fmt.Sprintf(" ORDER BY a.started_at DESC, a.id DESC LIMIT $%d", len(w.args)), w.args...)
	if err != nil {
		return Page[domain.Attempt]{}, fmt.Errorf("list attempts: %w", err)
	}
	attempts, err := pgx.CollectRows(rows, scanAttempt)
	if err != nil {
		return Page[domain.Attempt]{}, fmt.Errorf("list attempts: %w", err)
	}
	return pageOf(attempts, limit, func(a domain.Attempt) Cursor { return Cursor{Time: a.StartedAt, ID: a.ID} }), nil
}

func scanAttempt(row pgx.CollectableRow) (domain.Attempt, error) {
	var (
		a                domain.Attempt
		trigger, outcome string
		errText          *string
	)
	err := row.Scan(&a.ID, &a.DeliveryID, &a.Number, &trigger, &a.StartedAt, &a.DurationMS,
		&outcome, &a.StatusCode, &errText, &a.Request, &a.Response, &a.ConfigHash, &a.NextAttemptAt)
	a.Trigger = domain.AttemptTrigger(trigger)
	a.Outcome = domain.Outcome(outcome)
	a.Error = deref(errText)
	return a, err
}

// collectDeliveryViews scans delivery rows and fills in blocked_by.
func (s *Store) collectDeliveryViews(ctx context.Context, scope Scope, rows pgx.Rows) ([]DeliveryView, error) {
	deliveries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Delivery, error) { return scanDelivery(r) })
	if err != nil {
		return nil, err
	}
	blocked, err := s.blockedBy(ctx, scope, deliveries)
	if err != nil {
		return nil, err
	}
	views := make([]DeliveryView, len(deliveries))
	for i, d := range deliveries {
		views[i] = DeliveryView{Delivery: d}
		if p, ok := blocked[d.ID]; ok {
			views[i].BlockedBy = &p
		}
	}
	return views, nil
}
