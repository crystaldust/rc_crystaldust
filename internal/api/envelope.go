package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"messagehub/internal/domain"
	"messagehub/internal/store"
)

// Error codes (docs/design/domain-model.md §12).
const (
	codeValidationFailed = "validation_failed"
	codeUnauthenticated  = "unauthenticated"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeInvalidState     = "invalid_state"
	codePayloadTooLarge  = "payload_too_large"
	codeInternal         = "internal"
)

// response is the unified envelope {success, data, error, meta}.
type response struct {
	Success bool       `json:"success"`
	Data    any        `json:"data"`
	Error   *errorBody `json:"error"`
	Meta    any        `json:"meta"`
}

type errorBody struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []errorDetail `json:"details"`
}

type errorDetail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// pageMeta is the meta of paginated responses.
type pageMeta struct {
	NextCursor *string `json:"next_cursor"`
	Limit      int     `json:"limit"`
}

var emptyMeta = struct{}{}

func writeJSON(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The client may have gone away; nothing useful can be done about it.
	_ = json.NewEncoder(w).Encode(body)
}

func writeData(w http.ResponseWriter, status int, data, meta any) {
	if meta == nil {
		meta = emptyMeta
	}
	writeJSON(w, status, response{Success: true, Data: data, Meta: meta})
}

func writeError(w http.ResponseWriter, status int, code, message string, details []errorDetail) {
	if details == nil {
		details = []errorDetail{}
	}
	writeJSON(w, status, response{Error: &errorBody{Code: code, Message: message, Details: details}, Meta: emptyMeta})
}

func writeValidation(w http.ResponseWriter, verr *domain.ValidationError) {
	details := make([]errorDetail, len(verr.Problems))
	for i, p := range verr.Problems {
		details[i] = errorDetail{Field: p.Field, Reason: p.Reason}
	}
	writeError(w, http.StatusBadRequest, codeValidationFailed, "request validation failed", details)
}

func writeNotFound(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotFound, codeNotFound, what+" not found", nil)
}

// writeInternal logs the cause and returns a generic message: internal
// details never reach the response (coding-conventions §3).
func writeInternal(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	log.Error("request.failed", slog.String("request_id", requestID(r.Context())),
		slog.String("route", r.Pattern), slog.String("error", err.Error()))
	writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
}

// writeStoreError maps store errors; what names the resource for 404.
func writeStoreError(w http.ResponseWriter, r *http.Request, log *slog.Logger, what string, err error) {
	var serr *store.StateError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeNotFound(w, what)
	case errors.As(err, &serr):
		writeError(w, http.StatusConflict, codeInvalidState, serr.Error(),
			[]errorDetail{{Field: "status", Reason: fmt.Sprintf("is %s", serr.Current)}})
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, codeValidationFailed, "a value in the request cannot be stored", nil)
	default:
		writeInternal(w, r, log, err)
	}
}
