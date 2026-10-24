package api

import (
	"net/http"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/store"
)

// listEvents handles GET /v1/events.
func (s *server) listEvents(w http.ResponseWriter, r *http.Request, p config.Principal) {
	q := newParams(r.URL.Query())
	f := store.EventFilter{
		SourceEventID: q.str("source_event_id", domain.MaxSourceEventIDLen, nil),
		EventType:     q.str("event_type", domain.MaxEventTypeLen, domain.EventTypePattern),
		OrderingKey:   q.str("ordering_key", domain.MaxOrderingKeyLen, nil),
		RoutingStatus: domain.RoutingStatus(q.enum("routing_status", routingStatuses)),
		Status:        domain.DeliveryStatus(q.enum("status", deliveryStatuses())),
		ReceivedFrom:  q.time("received_from"),
		ReceivedTo:    q.time("received_to"),
		Cursor:        q.cursor(),
		Limit:         q.limit(),
	}
	if verr := q.err(); verr != nil {
		writeValidation(w, verr)
		return
	}
	page, err := s.store.ListEvents(r.Context(), scopeFor(p), f)
	if err != nil {
		writeStoreError(w, r, s.log, "event", err)
		return
	}
	writeData(w, http.StatusOK, mapAll(page.Items, eventView), pageMetaOf(page.NextCursor, f.Limit))
}

// getEvent handles GET /v1/events/{event_id}.
func (s *server) getEvent(w http.ResponseWriter, r *http.Request, p config.Principal) {
	q := newParams(nil)
	id := q.pathUUID("event_id", r.PathValue("event_id"))
	if verr := q.err(); verr != nil {
		writeValidation(w, verr)
		return
	}
	detail, err := s.store.GetEvent(r.Context(), scopeFor(p), id)
	if err != nil {
		writeStoreError(w, r, s.log, "event", err)
		return
	}
	writeData(w, http.StatusOK, eventDetailView(detail), nil)
}

// listDeliveries handles GET /v1/deliveries.
func (s *server) listDeliveries(w http.ResponseWriter, r *http.Request, p config.Principal) {
	q := newParams(r.URL.Query())
	f := store.DeliveryFilter{
		EventID:       q.uuid("event_id"),
		DestinationID: q.str("destination_id", configIDMaxLen, configIDPattern),
		Status:        domain.DeliveryStatus(q.enum("status", deliveryStatuses())),
		CreatedFrom:   q.time("created_from"),
		CreatedTo:     q.time("created_to"),
		Cursor:        q.cursor(),
		Limit:         q.limit(),
	}
	if verr := q.err(); verr != nil {
		writeValidation(w, verr)
		return
	}
	page, err := s.store.ListDeliveries(r.Context(), scopeFor(p), f)
	if err != nil {
		writeStoreError(w, r, s.log, "delivery", err)
		return
	}
	writeData(w, http.StatusOK, mapAll(page.Items, deliveryView), pageMetaOf(page.NextCursor, f.Limit))
}

// configIDMaxLen is the longest id configIDPattern accepts.
const configIDMaxLen = 63

// getDelivery handles GET /v1/deliveries/{delivery_id}; cursor and limit
// page the attempts (§13.4).
func (s *server) getDelivery(w http.ResponseWriter, r *http.Request, p config.Principal) {
	q := newParams(r.URL.Query())
	id := q.pathUUID("delivery_id", r.PathValue("delivery_id"))
	page := store.AttemptPage{Cursor: q.cursor(), Limit: q.limit()}
	if verr := q.err(); verr != nil {
		writeValidation(w, verr)
		return
	}
	detail, err := s.store.GetDelivery(r.Context(), scopeFor(p), id, page)
	if err != nil {
		writeStoreError(w, r, s.log, "delivery", err)
		return
	}
	view := deliveryDetailJSON{deliveryJSON: deliveryView(detail.Delivery), Attempts: mapAll(detail.Attempts, attemptView)}
	writeData(w, http.StatusOK, view, pageMetaOf(detail.NextCursor, page.Limit))
}
