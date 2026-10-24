package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	h := newHarness(t)

	t.Run("healthz needs no key", func(t *testing.T) {
		rec, body := h.do(t, http.MethodGet, "/healthz", "", "")
		if rec.Code != http.StatusOK || body["data"].(map[string]any)["status"] != "ok" {
			t.Fatalf("status %d, body %v", rec.Code, body)
		}
	})

	t.Run("readyz reports the database", func(t *testing.T) {
		rec, _ := h.do(t, http.MethodGet, "/readyz", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("ready: status %d", rec.Code)
		}
		h.readyErr = errors.New("connection refused")
		rec, body := h.do(t, http.MethodGet, "/readyz", "", "")
		if rec.Code != http.StatusServiceUnavailable || errorCode(body) != codeInternal {
			t.Fatalf("not ready: status %d, body %v", rec.Code, body)
		}
	})
}

func TestUnknownRoute(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{"/v2/events", "/v1/events/x/y"} {
		rec, body := h.do(t, http.MethodGet, target, "support-viewer", "")
		if rec.Code != http.StatusNotFound || errorCode(body) != codeNotFound {
			t.Errorf("%s: status %d, body %v", target, rec.Code, body)
		}
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	h := newHarness(t)
	h.store.panicOnQuery = true

	rec, body := h.do(t, http.MethodGet, "/v1/events", "support-viewer", "")

	if rec.Code != http.StatusInternalServerError || errorCode(body) != codeInternal {
		t.Fatalf("status %d, body %v", rec.Code, body)
	}
	if !strings.Contains(h.logs.String(), "panic: boom") {
		t.Errorf("panic not logged: %s", h.logs)
	}
}

func TestAccessLog(t *testing.T) {
	h := newHarness(t)

	h.do(t, http.MethodGet, "/v1/events", "support-viewer", "")

	logs := h.logs.String()
	for _, want := range []string{`"msg":"http.request"`, `"route":"GET /v1/events"`, `"status":200`, `"principal_id":"support-viewer"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("access log %s lacks %s", logs, want)
		}
	}
	if strings.Contains(logs, h.keys["support-viewer"]) {
		t.Error("access log contains the API key")
	}
}

func TestServeShutsDownOnCancel(t *testing.T) {
	// Arrange
	h := newHarness(t)
	srv := newHTTPServer("127.0.0.1:0", h.handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, time.Second, discardLog) }()
	time.Sleep(50 * time.Millisecond)

	// Act
	cancel()

	// Assert
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}

func TestServeReportsListenFailure(t *testing.T) {
	srv := newHTTPServer("256.0.0.1:bad", http.NotFoundHandler())
	if err := serve(context.Background(), srv, time.Second, discardLog); err == nil {
		t.Fatal("serve succeeded on an invalid address")
	}
}

func TestObservabilityEdgeCases(t *testing.T) {
	t.Run("implicit 200 is recorded", func(t *testing.T) {
		e := newTestEnv(t)
		h := withObservability(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("{}"))
		}), e.log, time.Now)

		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

		if !strings.Contains(e.logs.String(), `"status":200`) {
			t.Errorf("access log = %s", e.logs)
		}
	})

	t.Run("panic after the response started is only logged", func(t *testing.T) {
		e := newTestEnv(t)
		h := withObservability(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			panic("late")
		}), e.log, time.Now)
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if rec.Code != http.StatusAccepted || !strings.Contains(e.logs.String(), `"msg":"request.panic"`) {
			t.Errorf("status %d, logs %s", rec.Code, e.logs)
		}
	})
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestReadBodyFailure(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/events", failingBody{})
	rec := httptest.NewRecorder()

	if _, ok := readBody(rec, req, maxIngestBodyBytes); ok || rec.Code != http.StatusBadRequest {
		t.Fatalf("ok %v, status %d; want a 400", ok, rec.Code)
	}
}

func TestResponseHardeningHeaders(t *testing.T) {
	h := newHarness(t)

	rec, _ := h.do(t, http.MethodGet, "/healthz", "", "")

	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestAbortHandlerPanicPropagates(t *testing.T) {
	h := withObservability(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), discardLog, time.Now)
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
		}
	}()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestServeCutsOffRequestsAfterShutdownTimeout(t *testing.T) {
	// Arrange: one request never finishes on its own.
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	srv := newHTTPServer("127.0.0.1:18089", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, 50*time.Millisecond, discardLog) }()
	time.Sleep(50 * time.Millisecond)
	go func() {
		resp, err := http.Get("http://127.0.0.1:18089/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	// Act
	cancel()

	// Assert
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil after a shutdown timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after the shutdown timeout")
	}
}
