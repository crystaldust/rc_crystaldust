package api

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
	"messagehub/internal/store"
)

// JSON views of the OpenAPI schemas (api/openapi.yaml components.schemas).
// Absent optional fields render as null, metadata as {} (§13.4). Times are
// rendered in UTC.

type ingestDeliveryJSON struct {
	DeliveryID    string `json:"delivery_id"`
	DestinationID string `json:"destination_id"`
	Status        string `json:"status"`
}

type ingestResultJSON struct {
	EventID         string               `json:"event_id"`
	Duplicate       bool                 `json:"duplicate"`
	RoutingStatus   string               `json:"routing_status"`
	MatchedRouteIDs []string             `json:"matched_route_ids"`
	Deliveries      []ingestDeliveryJSON `json:"deliveries"`
}

func ingestResultView(r store.IngestResult) ingestResultJSON {
	deliveries := make([]ingestDeliveryJSON, len(r.Deliveries))
	for i, d := range r.Deliveries {
		deliveries[i] = ingestDeliveryJSON{DeliveryID: d.ID.String(), DestinationID: d.DestinationID, Status: string(d.Status)}
	}
	return ingestResultJSON{
		EventID:         r.EventID.String(),
		Duplicate:       r.Duplicate,
		RoutingStatus:   string(r.RoutingStatus),
		MatchedRouteIDs: nonNil(r.MatchedRouteIDs),
		Deliveries:      deliveries,
	}
}

type eventJSON struct {
	ID                string            `json:"id"`
	Source            string            `json:"source"`
	SourceEventID     string            `json:"source_event_id"`
	EventType         string            `json:"event_type"`
	OccurredAt        time.Time         `json:"occurred_at"`
	ReceivedAt        time.Time         `json:"received_at"`
	IngestSeq         int64             `json:"ingest_seq"`
	IdempotencyKey    *string           `json:"idempotency_key"`
	OrderingKey       *string           `json:"ordering_key"`
	Metadata          map[string]string `json:"metadata"`
	RoutingStatus     string            `json:"routing_status"`
	MatchedRouteIDs   []string          `json:"matched_route_ids"`
	RoutingConfigHash string            `json:"routing_config_hash"`
}

func eventView(e domain.Event) eventJSON {
	metadata := e.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	return eventJSON{
		ID:                e.ID.String(),
		Source:            e.Source,
		SourceEventID:     e.SourceEventID,
		EventType:         e.EventType,
		OccurredAt:        e.OccurredAt.UTC(),
		ReceivedAt:        e.ReceivedAt.UTC(),
		IngestSeq:         e.IngestSeq,
		IdempotencyKey:    optional(e.IdempotencyKey),
		OrderingKey:       optional(e.OrderingKey),
		Metadata:          metadata,
		RoutingStatus:     string(e.RoutingStatus),
		MatchedRouteIDs:   nonNil(e.MatchedRouteIDs),
		RoutingConfigHash: e.RoutingConfigHash,
	}
}

type eventDetailJSON struct {
	eventJSON
	Payload    json.RawMessage `json:"payload"`
	Deliveries []deliveryJSON  `json:"deliveries"`
}

func eventDetailView(d store.EventDetail) eventDetailJSON {
	return eventDetailJSON{eventJSON: eventView(d.Event), Payload: d.Event.Payload, Deliveries: mapAll(d.Deliveries, deliveryView)}
}

type deliveryJSON struct {
	ID             string     `json:"id"`
	EventID        string     `json:"event_id"`
	DestinationID  string     `json:"destination_id"`
	PolicyID       string     `json:"policy_id"`
	Status         string     `json:"status"`
	OrderingKey    *string    `json:"ordering_key"`
	AttemptCount   int        `json:"attempt_count"`
	ReplayCount    int        `json:"replay_count"`
	ScheduledAt    time.Time  `json:"scheduled_at"`
	LastAttemptAt  *time.Time `json:"last_attempt_at"`
	LastOutcome    *string    `json:"last_outcome"`
	LastStatusCode *int       `json:"last_status_code"`
	LastError      *string    `json:"last_error"`
	BlockedBy      *string    `json:"blocked_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

func deliveryView(v store.DeliveryView) deliveryJSON {
	d := v.Delivery
	return deliveryJSON{
		ID:             d.ID.String(),
		EventID:        d.EventID.String(),
		DestinationID:  d.DestinationID,
		PolicyID:       d.PolicyID,
		Status:         string(d.Status),
		OrderingKey:    optional(d.OrderingKey),
		AttemptCount:   d.AttemptCount,
		ReplayCount:    d.ReplayCount,
		ScheduledAt:    d.ScheduledAt.UTC(),
		LastAttemptAt:  utcPtr(d.LastAttemptAt),
		LastOutcome:    optional(string(d.LastOutcome)),
		LastStatusCode: d.LastStatusCode,
		LastError:      optional(d.LastError),
		BlockedBy:      uuidPtr(v.BlockedBy),
		CreatedAt:      d.CreatedAt.UTC(),
		UpdatedAt:      d.UpdatedAt.UTC(),
		CompletedAt:    utcPtr(d.CompletedAt),
	}
}

type deliveryDetailJSON struct {
	deliveryJSON
	Attempts []attemptJSON `json:"attempts"`
}

type attemptJSON struct {
	ID            string                   `json:"id"`
	DeliveryID    string                   `json:"delivery_id"`
	Number        int                      `json:"number"`
	Trigger       string                   `json:"trigger"`
	StartedAt     time.Time                `json:"started_at"`
	DurationMS    int                      `json:"duration_ms"`
	Outcome       string                   `json:"outcome"`
	StatusCode    *int                     `json:"status_code"`
	Error         *string                  `json:"error"`
	Request       *domain.RecordedRequest  `json:"request"`
	Response      *domain.RecordedResponse `json:"response"`
	ConfigHash    string                   `json:"config_hash"`
	NextAttemptAt *time.Time               `json:"next_attempt_at"`
}

func attemptView(a domain.Attempt) attemptJSON {
	return attemptJSON{
		ID:            a.ID.String(),
		DeliveryID:    a.DeliveryID.String(),
		Number:        a.Number,
		Trigger:       string(a.Trigger),
		StartedAt:     a.StartedAt.UTC(),
		DurationMS:    a.DurationMS,
		Outcome:       string(a.Outcome),
		StatusCode:    a.StatusCode,
		Error:         optional(a.Error),
		Request:       a.Request,
		Response:      a.Response,
		ConfigHash:    a.ConfigHash,
		NextAttemptAt: utcPtr(a.NextAttemptAt),
	}
}

type operatorActionJSON struct {
	ActionID   string    `json:"action_id"`
	Action     string    `json:"action"`
	DeliveryID string    `json:"delivery_id"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	CreatedAt  time.Time `json:"created_at"`
}

func operatorActionView(a store.OperatorAction) operatorActionJSON {
	return operatorActionJSON{
		ActionID:   a.ID.String(),
		Action:     string(a.Action),
		DeliveryID: a.DeliveryID.String(),
		Actor:      a.Actor,
		Reason:     a.Reason,
		FromStatus: string(a.FromStatus),
		ToStatus:   string(a.ToStatus),
		CreatedAt:  a.CreatedAt.UTC(),
	}
}

// pageMetaOf renders the cursor of the next page, if any.
func pageMetaOf(next *store.Cursor, limit int) pageMeta {
	if next == nil {
		return pageMeta{Limit: limit}
	}
	s := store.EncodeCursor(*next)
	return pageMeta{NextCursor: &s, Limit: limit}
}

func mapAll[T, U any](items []T, f func(T) U) []U {
	out := make([]U, len(items))
	for i, it := range items {
		out[i] = f(it)
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func uuidPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}
