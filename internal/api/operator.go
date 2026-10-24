package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"unicode/utf8"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/store"
)

// maxReasonLen bounds OperatorActionRequest.reason (§13.4).
const maxReasonLen = 500

type operatorFunc func(ctx context.Context, in store.OperatorInput) (store.OperatorAction, error)

// replay handles POST /v1/deliveries/{delivery_id}/replay (202).
func (s *server) replay(w http.ResponseWriter, r *http.Request, p config.Principal) {
	s.operate(w, r, p, s.store.Replay, http.StatusAccepted)
}

// skip handles POST /v1/deliveries/{delivery_id}/skip (200).
func (s *server) skip(w http.ResponseWriter, r *http.Request, p config.Principal) {
	s.operate(w, r, p, s.store.Skip, http.StatusOK)
}

func (s *server) operate(w http.ResponseWriter, r *http.Request, p config.Principal, op operatorFunc, status int) {
	raw, ok := readBody(w, r, maxOperatorBodyBytes)
	if !ok {
		return
	}
	q := newParams(nil)
	id := q.pathUUID("delivery_id", r.PathValue("delivery_id"))
	reason := parseReason(q, raw)
	if verr := q.err(); verr != nil {
		writeValidation(w, verr)
		return
	}
	action, err := op(r.Context(), store.OperatorInput{ActionID: domain.NewID(), DeliveryID: id, Actor: p.ID, Reason: reason})
	if err != nil {
		writeStoreError(w, r, s.log, "delivery", err)
		return
	}
	s.log.Info("delivery.operator_action",
		slog.String("request_id", requestID(r.Context())),
		slog.String("action", string(action.Action)),
		slog.String("delivery_id", action.DeliveryID.String()),
		slog.String("actor", action.Actor),
		slog.String("from_status", string(action.FromStatus)),
		slog.String("to_status", string(action.ToStatus)))
	writeData(w, status, operatorActionView(action), nil)
}

// parseReason validates the OperatorActionRequest body {reason}.
func parseReason(q *params, raw []byte) string {
	var dup *domain.ValidationError
	if errors.As(CheckStrictJSON(raw), &dup) {
		q.problems = append(q.problems, dup.Problems...)
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		q.fail("", "body must be a JSON object")
		return ""
	}
	unknown := make([]string, 0, len(fields))
	for name := range fields {
		if name != "reason" {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		q.fail(name, "is not an allowed field")
	}
	rawReason, ok := fields["reason"]
	var reason string
	if !ok || json.Unmarshal(rawReason, &reason) != nil || string(rawReason) == "null" {
		q.fail("reason", "is required and must be a string")
		return ""
	}
	if n := utf8.RuneCountInString(reason); n < 1 || n > maxReasonLen {
		q.fail("reason", fmt.Sprintf("length must be between 1 and %d characters", maxReasonLen))
		return ""
	}
	return reason
}
