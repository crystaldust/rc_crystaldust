//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/app"
	"messagehub/internal/platform/pg"
	"messagehub/internal/store"
	"messagehub/internal/testsupport/pgtest"
)

const specPath = "../../api/openapi.yaml"

// contract sends real HTTP requests to the API backed by PostgreSQL and
// validates every request and response against api/openapi.yaml (B2).
type contract struct {
	t      *testing.T
	base   string
	router routers.Router
	env    *testEnv
	pool   *pgxpool.Pool
}

func newContract(t *testing.T) *contract {
	t.Helper()
	ctx := context.Background()
	pool, err := pg.NewPool(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	doc.Servers = nil // match paths only; the test server has a random port
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	env := newTestEnv(t)
	h := newHandler(deps{config: env.currentConfig, resolver: env.resolver(),
		store: store.New(pool, env.currentConfig), ready: pool.Ping, log: discardLog, now: time.Now})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &contract{t: t, base: srv.URL, router: router, env: env, pool: pool}
}

// call sends the request as principal. checkRequest is false for requests
// that deliberately break the contract (the response is still validated).
func (c *contract) call(method, path, principal, body string, checkRequest bool) (int, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if principal != "" {
		req.Header.Set("Authorization", "Bearer "+c.env.keys[principal])
	}
	route, pathParams, err := c.router.FindRoute(req)
	if err != nil {
		c.t.Fatalf("%s %s: no route in the spec: %v", method, path, err)
	}
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true, MultiError: true}
	input := &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route, Options: opts}
	if checkRequest {
		if err := openapi3filter.ValidateRequest(context.Background(), input); err != nil {
			c.t.Fatalf("%s %s: request breaks the contract: %v", method, path, err)
		}
		req.Body = io.NopCloser(strings.NewReader(body)) // ValidateRequest consumed it
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	out := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: resp.StatusCode,
		Header: resp.Header, Options: opts}
	if err := openapi3filter.ValidateResponse(context.Background(), out.SetBodyBytes(raw)); err != nil {
		c.t.Fatalf("%s %s → %d: response breaks the contract: %v\n%s", method, path, resp.StatusCode, err, raw)
	}
	var decoded map[string]any
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&decoded); err != nil {
		c.t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, decoded
}

func (c *contract) expect(status, want int, body map[string]any) map[string]any {
	c.t.Helper()
	if status != want {
		c.t.Fatalf("status = %d, want %d: %v", status, want, body)
	}
	return body
}

func (c *contract) exec(sql string, args ...any) {
	c.t.Helper()
	if _, err := c.pool.Exec(context.Background(), sql, args...); err != nil {
		c.t.Fatalf("exec %q: %v", sql, err)
	}
}

func dataOf(body map[string]any) map[string]any { return body["data"].(map[string]any) }

func TestContract(t *testing.T) {
	c := newContract(t)
	const purchase = `{"source_event_id":"ord-1","event_type":"order.purchased","occurred_at":"2026-10-24T08:00:00Z",
		"ordering_key":"sku-1","idempotency_key":"pay-1","metadata":{"trace_id":"abc123"},"payload":{"sku":"sku-1","qty":2}}`

	// --- ingest
	status, body := c.call(http.MethodPost, "/v1/events", "order-service", purchase, true)
	routed := dataOf(c.expect(status, http.StatusAccepted, body))
	eventID := routed["event_id"].(string)
	deliveries := routed["deliveries"].([]any)
	if routed["routing_status"] != "routed" || len(deliveries) != 2 {
		t.Fatalf("routed ingest = %v", routed)
	}
	inventoryID := deliveries[0].(map[string]any)["delivery_id"].(string)
	warehouseID := deliveries[1].(map[string]any)["delivery_id"].(string)

	status, body = c.call(http.MethodPost, "/v1/events", "order-service", purchase, true)
	if dup := dataOf(c.expect(status, http.StatusOK, body)); dup["duplicate"] != true || dup["event_id"] != eventID {
		t.Fatalf("duplicate ingest = %v", dup)
	}
	status, body = c.call(http.MethodPost, "/v1/events", "order-service",
		`{"source_event_id":"ord-2","event_type":"order.refunded","occurred_at":"2026-10-24T08:01:00Z","payload":{}}`, true)
	if unrouted := dataOf(c.expect(status, http.StatusAccepted, body)); unrouted["routing_status"] != "unrouted" {
		t.Fatalf("unrouted ingest = %v", unrouted)
	}
	c.call(http.MethodPost, "/v1/events", "billing-service",
		`{"source_event_id":"pay-1","event_type":"payment.succeeded","occurred_at":"2026-10-24T08:02:00Z","payload":{"contact_id":"c-1"}}`, true)

	t.Run("ingest errors", func(t *testing.T) {
		status, body := c.call(http.MethodPost, "/v1/events", "support-viewer", purchase, true)
		c.expect(status, http.StatusForbidden, body)
		status, body = c.call(http.MethodPost, "/v1/events", "", purchase, false)
		c.expect(status, http.StatusUnauthorized, body)
		status, body = c.call(http.MethodPost, "/v1/events", "order-service", `{"event_type":"x"}`, false)
		c.expect(status, http.StatusBadRequest, body)
		big := `{"source_event_id":"big","event_type":"a.b","occurred_at":"2026-10-24T08:00:00Z","payload":{"x":"` +
			strings.Repeat("a", 300*1024) + `"}}`
		status, body = c.call(http.MethodPost, "/v1/events", "order-service", big, false)
		c.expect(status, http.StatusRequestEntityTooLarge, body)
	})

	t.Run("event queries", func(t *testing.T) {
		status, body := c.call(http.MethodGet, "/v1/events/"+eventID, "order-service", "", true)
		detail := dataOf(c.expect(status, http.StatusOK, body))
		if detail["payload"].(map[string]any)["sku"] != "sku-1" || len(detail["deliveries"].([]any)) != 2 {
			t.Errorf("event detail = %v", detail)
		}
		status, body = c.call(http.MethodGet, "/v1/events/"+eventID, "billing-service", "", true)
		c.expect(status, http.StatusNotFound, body)
		status, body = c.call(http.MethodGet, "/v1/events/not-a-uuid", "support-viewer", "", false)
		c.expect(status, http.StatusBadRequest, body)

		status, body = c.call(http.MethodGet, "/v1/events?routing_status=unrouted", "support-viewer", "", true)
		if items := c.expect(status, http.StatusOK, body)["data"].([]any); len(items) != 1 {
			t.Errorf("unrouted events = %v", items)
		}
		status, body = c.call(http.MethodGet, "/v1/events", "order-service", "", true)
		if items := c.expect(status, http.StatusOK, body)["data"].([]any); len(items) != 2 {
			t.Errorf("order-service sees %d events, want its own 2", len(items))
		}
	})

	t.Run("pagination walks every event", func(t *testing.T) {
		seen := 0
		path := "/v1/events?limit=1"
		for {
			status, body := c.call(http.MethodGet, path, "oncall-operator", "", true)
			c.expect(status, http.StatusOK, body)
			seen += len(body["data"].([]any))
			next, _ := body["meta"].(map[string]any)["next_cursor"].(string)
			if next == "" {
				break
			}
			path = "/v1/events?limit=1&cursor=" + next
		}
		if seen != 3 {
			t.Errorf("walked %d events, want 3", seen)
		}
	})

	t.Run("delivery queries", func(t *testing.T) {
		c.exec(`INSERT INTO attempts (id, delivery_id, number, trigger, started_at, duration_ms, outcome,
			status_code, error, request, response, config_hash, next_attempt_at)
			VALUES (gen_random_uuid(), $1, 1, 'auto', now(), 5001, 'retryable', NULL, 'timeout: no response within 5s',
			'{"method":"POST","url":"https://inventory.example.com/stock","headers":{"Authorization":"***"},"body":"{}","truncated":false}',
			NULL, $2, now())`, inventoryID, c.env.cfg.Hash())
		status, body := c.call(http.MethodGet, "/v1/deliveries/"+inventoryID+"?limit=10", "support-viewer", "", true)
		if attempts := dataOf(c.expect(status, http.StatusOK, body))["attempts"].([]any); len(attempts) != 1 {
			t.Errorf("attempts = %v", attempts)
		}
		status, body = c.call(http.MethodGet, "/v1/deliveries?destination_id=inventory&status=PENDING", "support-viewer", "", true)
		if items := c.expect(status, http.StatusOK, body)["data"].([]any); len(items) != 1 {
			t.Errorf("inventory deliveries = %v", items)
		}
		status, body = c.call(http.MethodGet, "/v1/deliveries/"+warehouseID, "billing-service", "", true)
		c.expect(status, http.StatusNotFound, body)
	})

	t.Run("operator actions", func(t *testing.T) {
		reason := `{"reason":"vendor outage resolved"}`
		status, body := c.call(http.MethodPost, "/v1/deliveries/"+inventoryID+"/replay", "oncall-operator", reason, true)
		c.expect(status, http.StatusConflict, body)
		status, body = c.call(http.MethodPost, "/v1/deliveries/"+inventoryID+"/skip", "support-viewer", reason, true)
		c.expect(status, http.StatusForbidden, body)

		c.exec(`UPDATE deliveries SET status = 'DEAD', completed_at = now() WHERE id = $1`, inventoryID)
		status, body = c.call(http.MethodPost, "/v1/deliveries/"+inventoryID+"/skip", "oncall-operator", reason, true)
		if a := dataOf(c.expect(status, http.StatusOK, body)); a["to_status"] != "SKIPPED" {
			t.Errorf("skip = %v", a)
		}
		status, body = c.call(http.MethodPost, "/v1/deliveries/"+inventoryID+"/replay", "oncall-operator", reason, true)
		if a := dataOf(c.expect(status, http.StatusAccepted, body)); a["from_status"] != "SKIPPED" || a["to_status"] != "PENDING" {
			t.Errorf("replay = %v", a)
		}
		status, body = c.call(http.MethodPost, "/v1/deliveries/"+inventoryID+"/skip", "oncall-operator", `{}`, false)
		c.expect(status, http.StatusBadRequest, body)
	})

	t.Run("health", func(t *testing.T) {
		status, body := c.call(http.MethodGet, "/healthz", "", "", true)
		c.expect(status, http.StatusOK, body)
		status, body = c.call(http.MethodGet, "/readyz", "", "", true)
		c.expect(status, http.StatusOK, body)
	})
}

// TestRunServesUntilCancelled starts the real role entry point on a Runtime.
func TestRunServesUntilCancelled(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pg.NewPool(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	env := newTestEnv(t)
	holder, err := app.NewConfigHolder(exampleConfig, env.resolver(), discardLog)
	if err != nil {
		t.Fatalf("NewConfigHolder: %v", err)
	}
	rt := &app.Runtime{
		Settings: app.Settings{HTTPAddr: "127.0.0.1:0", ShutdownTimeout: time.Second},
		Config:   holder, Secrets: env.resolver(), Pool: pool, Log: discardLog, Now: time.Now,
	}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, rt) }()
	time.Sleep(100 * time.Millisecond)

	// Act
	cancel()

	// Assert
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
