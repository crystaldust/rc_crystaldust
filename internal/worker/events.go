package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/template"
)

// EventLoader reads the event a delivery renders against.
type EventLoader interface {
	LoadEvent(ctx context.Context, id uuid.UUID) (template.Event, error)
}

// pgEvents loads events with their payload in one query. A payload purged
// by retention reads as {} (Phase 5), so templates that need it fail with
// missing_path rather than the worker crashing.
type pgEvents struct {
	pool *pgxpool.Pool
}

const loadEventSQL = `SELECT e.id, e.source, e.source_event_id, e.event_type, e.occurred_at,
	e.received_at, coalesce(e.idempotency_key, ''), coalesce(e.ordering_key, ''), e.metadata,
	coalesce(p.payload, '{}'::jsonb)::text
FROM events e LEFT JOIN event_payloads p ON p.event_id = e.id
WHERE e.id = $1`

func (l pgEvents) LoadEvent(ctx context.Context, id uuid.UUID) (template.Event, error) {
	var (
		ev      template.Event
		eventID uuid.UUID
		payload string
	)
	err := l.pool.QueryRow(ctx, loadEventSQL, id).Scan(&eventID, &ev.Source, &ev.SourceEventID,
		&ev.EventType, &ev.OccurredAt, &ev.ReceivedAt, &ev.IdempotencyKey, &ev.OrderingKey,
		&ev.Metadata, &payload)
	if err != nil {
		return template.Event{}, fmt.Errorf("load event %s: %w", id, err)
	}
	ev.ID = eventID.String()
	if ev.Payload, err = decodePayload(payload); err != nil {
		return template.Event{}, fmt.Errorf("load event %s: %w", id, err)
	}
	return ev, nil
}

// decodePayload keeps numbers exact (json.Number) so they render as stored.
func decodePayload(s string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return payload, nil
}
