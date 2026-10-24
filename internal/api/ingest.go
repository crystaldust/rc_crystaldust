package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/routing"
	"messagehub/internal/store"
)

// ingest handles POST /v1/events (§3): read with a size limit, reject
// duplicate keys, validate, route, persist, answer 202 (new) or 200
// (duplicate).
func (s *server) ingest(w http.ResponseWriter, r *http.Request, p config.Principal) {
	raw, ok := readBody(w, r, maxIngestBodyBytes)
	if !ok {
		return
	}
	env, err := parseIngestBody(raw)
	var verr *domain.ValidationError
	switch {
	case domain.IsPayloadTooLarge(err):
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, err.Error(), nil)
		return
	case errors.As(err, &verr):
		writeValidation(w, verr)
		return
	case err != nil:
		writeInternal(w, r, s.log, err)
		return
	}
	cfg := s.config()
	in, err := ingestInput(cfg, env, p.ID)
	if err != nil {
		writeInternal(w, r, s.log, err)
		return
	}
	res, err := s.store.Ingest(r.Context(), in)
	if err != nil {
		writeStoreError(w, r, s.log, "event", err)
		return
	}
	s.logIngest(r, p.ID, env, res, cfg.Hash())
	status := http.StatusAccepted
	if res.Duplicate {
		status = http.StatusOK
	}
	writeData(w, status, ingestResultView(res), nil)
}

// readBody reads at most limit bytes, answering 413 above it.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", limit), nil)
		return nil, false
	case err != nil:
		writeError(w, http.StatusBadRequest, codeValidationFailed, "could not read request body", nil)
		return nil, false
	}
	return raw, true
}

// parseIngestBody runs CheckStrictJSON (duplicate keys, NUL, number range,
// nesting; §13.1) before domain.ParseEnvelope.
func parseIngestBody(raw []byte) (domain.Envelope, error) {
	if err := CheckStrictJSON(raw); err != nil {
		return domain.Envelope{}, err
	}
	return domain.ParseEnvelope(raw)
}

// ingestInput routes the event and assigns ids to the event and deliveries.
func ingestInput(cfg *config.Config, env domain.Envelope, source string) (store.IngestInput, error) {
	routeIDs, destinationIDs, err := routing.Match(cfg, env, source)
	if err != nil {
		return store.IngestInput{}, err
	}
	deliveries := make([]store.NewDelivery, 0, len(destinationIDs))
	for _, id := range destinationIDs {
		d, ok := cfg.Destination(id)
		if !ok {
			return store.IngestInput{}, fmt.Errorf("route references unknown destination %q", id)
		}
		deliveries = append(deliveries, store.NewDelivery{ID: domain.NewID(), DestinationID: id, PolicyID: d.Policy})
	}
	return store.IngestInput{
		EventID:    domain.NewID(),
		Source:     source,
		Envelope:   env,
		RouteIDs:   routeIDs,
		Deliveries: deliveries,
		ConfigHash: cfg.Hash(),
	}, nil
}

// logIngest writes the wide ingest event (coding-conventions §7); the
// payload never appears in logs.
func (s *server) logIngest(r *http.Request, source string, env domain.Envelope, res store.IngestResult, configHash string) {
	attrs := []any{
		slog.String("request_id", requestID(r.Context())),
		slog.String("event_id", res.EventID.String()),
		slog.String("source", source),
		slog.String("source_event_id", env.SourceEventID),
		slog.String("event_type", env.EventType),
		slog.String("routing_status", string(res.RoutingStatus)),
		slog.Int("delivery_count", len(res.Deliveries)),
		slog.Bool("duplicate", res.Duplicate),
		slog.String("config_hash", configHash),
	}
	switch {
	case res.Duplicate:
		s.log.Info("event.duplicate", attrs...)
	case res.RoutingStatus == domain.RoutingUnrouted:
		s.log.Warn("event.unrouted", attrs...)
	default:
		s.log.Info("event.received", attrs...)
	}
}
