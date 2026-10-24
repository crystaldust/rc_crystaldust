package api

import (
	"context"
	"log/slog"
	"net/http"
)

type healthJSON struct {
	Status string `json:"status"`
}

var healthOK = healthJSON{Status: "ok"}

// healthz handles GET /healthz: the process is alive.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, healthOK, nil)
}

// readyz handles GET /readyz: the database answers within readyTimeout
// (§13.4: 503 with code internal otherwise).
func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		s.log.Warn("readiness.failed", slog.String("request_id", requestID(r.Context())), slog.String("error", err.Error()))
		writeError(w, http.StatusServiceUnavailable, codeInternal, "not ready", nil)
		return
	}
	writeData(w, http.StatusOK, healthOK, nil)
}
