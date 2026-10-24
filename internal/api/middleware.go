package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"messagehub/internal/domain"
)

// requestIDHeader carries the Hub-generated request id on every response.
const requestIDHeader = "X-Request-Id"

type ctxKey int

const requestInfoKey ctxKey = iota

// requestInfo is per-request state shared between the middleware and the
// auth step, which records the principal for the access log.
type requestInfo struct {
	id          string
	principalID string
}

func infoFrom(ctx context.Context) *requestInfo {
	if info, ok := ctx.Value(requestInfoKey).(*requestInfo); ok {
		return info
	}
	return &requestInfo{}
}

func requestID(ctx context.Context) string { return infoFrom(ctx).id }

// statusRecorder remembers the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// withObservability assigns a request id, bounds the request with
// requestTimeout, recovers panics as 500 and writes one access log line per
// request. Request bodies are never logged. The mux records the matched
// route on req itself, which is why req is not copied again downstream.
func withObservability(next http.Handler, log *slog.Logger, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := now()
		info := &requestInfo{id: domain.NewID().String()}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), requestInfoKey, info), requestTimeout)
		defer cancel()
		req := r.WithContext(ctx)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		rec.Header().Set(requestIDHeader, info.id)
		// Responses may echo payloads: never cache or sniff them.
		rec.Header().Set("Cache-Control", "no-store")
		rec.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			p := recover()
			if p == http.ErrAbortHandler {
				panic(p) // net/http's own signal to abort the response
			}
			if p != nil {
				if !rec.wroteHeader {
					writeInternal(rec, req, log, fmt.Errorf("panic: %v", p))
				} else {
					log.Error("request.panic", slog.String("request_id", info.id), slog.Any("panic", p))
				}
			}
			log.Info("http.request",
				slog.String("request_id", info.id),
				slog.String("method", r.Method),
				slog.String("route", req.Pattern),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", now().Sub(start).Milliseconds()),
				slog.String("principal_id", info.principalID))
		}()
		next.ServeHTTP(rec, req)
	})
}
