//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/app"
	"messagehub/internal/config"
	"messagehub/internal/dispatch"
	"messagehub/internal/domain"
	"messagehub/internal/platform/pg"
	"messagehub/internal/secrets"
	"messagehub/internal/template"
	"messagehub/internal/testsupport/pgtest"
)

var testDSN string

func TestMain(m *testing.M) {
	dsn, stop, err := pgtest.StartShared(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	testDSN = dsn
	code := m.Run()
	stop()
	os.Exit(code)
}

const e2eToken = "e2e-secret-token-abc123"

var e2eEnv = map[string]string{
	"HUB_KEY":       strings.Repeat("ab", 32),
	"VENDOR_TOKEN":  e2eToken,
	"CLIENT_ID":     "client-1",
	"CLIENT_SECRET": "client-secret-abc123",
}

const e2eConfigYAML = `version: 1
principals:
  - { id: order-service, role: source, api_key_sha256_ref: env:HUB_KEY }
policies:
  - id: quick
    timeout: 2s
    retry:
      max_attempts: 4
      backoff: { initial: 100ms, multiplier: 2.0, max: 5s, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: none }
  - id: quick-biz
    timeout: 2s
    retry:
      max_attempts: 4
      backoff: { initial: 100ms, multiplier: 2.0, max: 5s, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success:
      status: ["2xx"]
      body: [ { path: code, op: eq, value: 0 } ]
    ordering: { mode: none }
  - id: patient
    timeout: 4s
    retry:
      max_attempts: 4
      backoff: { initial: 100ms, multiplier: 2.0, max: 10s, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: none }
destinations:
  - id: ok
    policy: quick
    secrets: { TOKEN: env:VENDOR_TOKEN }
    request:
      method: POST
      url: "%[1]s/ok?api_key={{ secret.TOKEN }}&order={{ event.payload.order }}"
      headers:
        Authorization: "Bearer {{ secret.TOKEN }}"
        X-Custom: "t={{ secret.TOKEN }}"
        Content-Type: application/json
      body:
        order: "{{ event.payload.order }}"
        auth: { token: "{{ secret.TOKEN }}" }
        note: "{{ secret.TOKEN | sha256 }}"
  - { id: flaky, policy: quick, request: { method: POST, url: "%[1]s/flaky" } }
  - { id: down, policy: quick, request: { method: POST, url: "%[1]s/down" } }
  - { id: biz, policy: quick-biz, request: { method: POST, url: "%[1]s/biz" } }
  - { id: slow, policy: patient, request: { method: POST, url: "%[1]s/slow" } }
  - id: oauth
    policy: quick
    auth:
      type: oauth2_client_credentials
      token_url: "%[1]s/token"
      client_id_ref: env:CLIENT_ID
      client_secret_ref: env:CLIENT_SECRET
    request: { method: PUT, url: "%[1]s/ok-oauth" }
routes:
  - id: all
    event_type: order.paid
    destinations: [ok, flaky, down, biz, slow, oauth]
`

// vendor simulates the external systems.
type vendor struct {
	srv        *httptest.Server
	flakyCalls atomic.Int32
	tokenDown  atomic.Bool
	slowDelay  time.Duration
	mu         sync.Mutex
	oauthAuth  string
}

func newE2EVendor(t *testing.T) *vendor {
	t.Helper()
	v := &vendor{slowDelay: 2 * time.Second}
	v.srv = httptest.NewServer(http.HandlerFunc(v.handle))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *vendor) handle(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	switch r.URL.Path {
	case "/ok":
		w.WriteHeader(http.StatusOK)
	case "/flaky":
		if v.flakyCalls.Add(1) <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	case "/down":
		w.WriteHeader(http.StatusServiceUnavailable)
	case "/biz":
		_, _ = w.Write([]byte(`{"code":1001}`))
	case "/slow":
		select {
		case <-time.After(v.slowDelay):
		case <-r.Context().Done():
		}
	case "/token":
		if v.tokenDown.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-e2e","token_type":"Bearer","expires_in":3600}`))
	case "/ok-oauth":
		v.mu.Lock()
		v.oauthAuth = r.Header.Get("Authorization")
		v.mu.Unlock()
	}
}

type e2e struct {
	pool   *pgxpool.Pool
	vendor *vendor
	rt     *app.Runtime
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	pool, err := pg.NewPool(context.Background(), testDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	pgtest.Reset(t, pool)
	v := newE2EVendor(t)
	path := filepath.Join(t.TempDir(), "hub.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(e2eConfigYAML, v.srv.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := secrets.NewEnvResolver(func(k string) (string, bool) { s, ok := e2eEnv[k]; return s, ok })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	holder, err := app.NewConfigHolder(path, resolver, logger, config.WithTemplateValidator(template.ValidateField))
	if err != nil {
		t.Fatal(err)
	}
	rt := &app.Runtime{
		Settings: app.Settings{DatabaseListenURL: testDSN, WorkerConcurrency: 8, ShutdownTimeout: 2 * time.Second},
		Config:   holder, Secrets: resolver, Pool: pool, Log: logger, Now: time.Now,
	}
	return &e2e{pool: pool, vendor: v, rt: rt}
}

// startWorker runs the worker role; the returned func stops it and waits.
func (e *e2e) startWorker(t *testing.T, id string, shutdown time.Duration) func() {
	t.Helper()
	rt := *e.rt
	rt.Settings.WorkerID, rt.Settings.ShutdownTimeout = id, shutdown
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, &rt) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("worker %s returned %v", id, err)
		}
	}
	t.Cleanup(stop)
	return stop
}

// submit inserts an event with one PENDING delivery and notifies workers,
// as ingest does (Phase 3 is not involved).
func (e *e2e) submit(t *testing.T, dest string, payload string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	eventID, deliveryID := domain.NewID(), domain.NewID()
	_, err := e.pool.Exec(ctx, `WITH ev AS (
		INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
			routing_status, matched_route_ids, routing_config_hash)
		VALUES ($1, 'order-service', $3, 'order.paid', now(), 'routed', '{all}', 'h')
		RETURNING id, ingest_seq
	), pl AS (
		INSERT INTO event_payloads (event_id, payload) SELECT id, $4::jsonb FROM ev
	)
	INSERT INTO deliveries (id, event_id, destination_id, policy_id, ingest_seq)
	SELECT $2, id, $5, 'quick', ingest_seq FROM ev`,
		eventID, deliveryID, "src-"+deliveryID.String(), payload, dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `SELECT pg_notify($1, '')`, pg.DeliveryReadyChannel); err != nil {
		t.Fatal(err)
	}
	return deliveryID
}

type deliveryRow struct {
	Status       domain.DeliveryStatus
	AttemptCount int
}

func (e *e2e) delivery(t *testing.T, id uuid.UUID) deliveryRow {
	t.Helper()
	var r deliveryRow
	var s string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT status, attempt_count FROM deliveries WHERE id = $1`, id).Scan(&s, &r.AttemptCount); err != nil {
		t.Fatal(err)
	}
	r.Status = domain.DeliveryStatus(s)
	return r
}

func (e *e2e) waitStatus(t *testing.T, id uuid.UUID, want domain.DeliveryStatus, within time.Duration) deliveryRow {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		r := e.delivery(t, id)
		if r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery %s is %s after %v, want %s", id, r.Status, within, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type attemptRow struct {
	Number    int
	Outcome   string
	Error     *string
	StartedAt time.Time
	Request   *string
	Response  *string
}

func (e *e2e) attempts(t *testing.T, id uuid.UUID) []attemptRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT number, outcome, error, started_at,
		request::text, response::text FROM attempts WHERE delivery_id = $1 ORDER BY started_at`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []attemptRow
	for rows.Next() {
		var a attemptRow
		if err := rows.Scan(&a.Number, &a.Outcome, &a.Error, &a.StartedAt, &a.Request, &a.Response); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func describe(attempts []attemptRow) []string {
	out := make([]string, 0, len(attempts))
	for _, a := range attempts {
		errText := ""
		if a.Error != nil {
			errText = *a.Error
		}
		out = append(out, fmt.Sprintf("#%d %s %s %q", a.Number, a.StartedAt.Format("15:04:05.000"), a.Outcome, errText))
	}
	return out
}

func TestE2ESuccessIsRecordedWithoutSecrets(t *testing.T) {
	// Arrange
	e := newE2E(t)
	e.startWorker(t, "w1", 2*time.Second)

	// Act
	id := e.submit(t, "ok", `{"order":"o-1"}`)

	// Assert
	r := e.waitStatus(t, id, domain.StatusSucceeded, 10*time.Second)
	attempts := e.attempts(t, id)
	if r.AttemptCount != 1 || len(attempts) != 1 || attempts[0].Outcome != "success" {
		t.Fatalf("delivery = %+v, attempts = %+v", r, attempts)
	}
	req := *attempts[0].Request
	if strings.Contains(req, e2eToken) {
		t.Fatalf("secret in recorded request: %s", req)
	}
	// The body is stored as a JSON string inside the record, hence the escapes.
	for _, want := range []string{`"Authorization": "***"`, `"X-Custom": "***"`, `api_key=***`, `order=o-1`,
		`\"auth\":{\"token\":\"***\"}`, `\"note\":\"***\"`, `\"order\":\"o-1\"`} {
		if !strings.Contains(req, want) {
			t.Errorf("recorded request lacks %s: %s", want, req)
		}
	}
}

func TestE2ERetriesWithBackoffThenSucceeds(t *testing.T) {
	// Arrange
	e := newE2E(t)
	e.startWorker(t, "w1", 2*time.Second)

	// Act
	id := e.submit(t, "flaky", `{}`)

	// Assert
	r := e.waitStatus(t, id, domain.StatusSucceeded, 20*time.Second)
	attempts := e.attempts(t, id)
	if r.AttemptCount != 4 || len(attempts) != 4 {
		t.Fatalf("delivery = %+v, %d attempts", r, len(attempts))
	}
	for i := 1; i < len(attempts); i++ {
		gap := attempts[i].StartedAt.Sub(attempts[i-1].StartedAt)
		if want := 100 * time.Millisecond << (i - 1); gap < want {
			t.Errorf("gap before attempt %d = %v, want >= %v; attempts: %+v", i+1, gap, want, describe(attempts))
		}
	}
}

func TestE2EPersistentFailureGoesDead(t *testing.T) {
	// Arrange
	e := newE2E(t)
	e.startWorker(t, "w1", 2*time.Second)

	// Act
	id := e.submit(t, "down", `{}`)

	// Assert
	r := e.waitStatus(t, id, domain.StatusDead, 20*time.Second)
	if r.AttemptCount != 4 || len(e.attempts(t, id)) != 4 {
		t.Fatalf("delivery = %+v, want 4 attempts", r)
	}
}

func TestE2EBusinessErrorCodeGoesDead(t *testing.T) {
	// Arrange
	e := newE2E(t)
	e.startWorker(t, "w1", 2*time.Second)

	// Act
	id := e.submit(t, "biz", `{}`)

	// Assert
	r := e.waitStatus(t, id, domain.StatusDead, 10*time.Second)
	attempts := e.attempts(t, id)
	if r.AttemptCount != 1 || len(attempts) != 1 || attempts[0].Outcome != "permanent" {
		t.Fatalf("delivery = %+v, attempts = %+v", r, attempts)
	}
}

func TestE2ESlowDestinationDoesNotDelayOthers(t *testing.T) {
	// Arrange
	e := newE2E(t)
	e.startWorker(t, "w1", 3*time.Second)
	slow := e.submit(t, "slow", `{}`)
	e.waitStatus(t, slow, domain.StatusInFlight, 5*time.Second)

	// Act
	start := time.Now()
	fast := e.submit(t, "ok", `{"order":"o-2"}`)

	// Assert
	e.waitStatus(t, fast, domain.StatusSucceeded, 5*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("fast destination took %v while the slow one was in flight", elapsed)
	}
	if e.delivery(t, slow).Status != domain.StatusInFlight {
		t.Fatal("slow delivery finished first; the test proves nothing")
	}
}

func TestE2EOAuthTokenFailureIsInfraRetry(t *testing.T) {
	// Arrange
	e := newE2E(t)
	e.vendor.tokenDown.Store(true)
	e.startWorker(t, "w1", 2*time.Second)

	// Act
	id := e.submit(t, "oauth", `{}`)

	// Assert: token endpoint down → retried without consuming budget.
	deadline := time.Now().Add(10 * time.Second)
	for len(e.attempts(t, id)) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no infra retries recorded")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, a := range e.attempts(t, id) {
		if a.Outcome != "infra_error" || a.Request != nil {
			t.Fatalf("attempt = %+v, want infra_error without a request record", a)
		}
	}
	if r := e.delivery(t, id); r.AttemptCount != 0 {
		t.Fatalf("attempt_count = %d, want 0", r.AttemptCount)
	}

	// Recovery: the next retry gets a token and delivers.
	e.vendor.tokenDown.Store(false)
	r := e.waitStatus(t, id, domain.StatusSucceeded, 10*time.Second)
	e.vendor.mu.Lock()
	auth := e.vendor.oauthAuth
	e.vendor.mu.Unlock()
	if r.AttemptCount != 1 || auth != "Bearer at-e2e" {
		t.Fatalf("delivery = %+v, vendor saw Authorization %q", r, auth)
	}
	last := e.attempts(t, id)
	if req := *last[len(last)-1].Request; strings.Contains(req, "at-e2e") {
		t.Fatalf("oauth token in recorded request: %s", req)
	}
}

func TestE2EUnfinishedDeliveryIsTakenOverAfterLeaseExpiry(t *testing.T) {
	// Arrange: worker 1 abandons a slow delivery when it shuts down.
	e := newE2E(t)
	stop := e.startWorker(t, "w1", 1200*time.Millisecond)
	id := e.submit(t, "slow", `{}`)
	e.waitStatus(t, id, domain.StatusInFlight, 5*time.Second)
	stop()
	if r := e.delivery(t, id); r.Status != domain.StatusInFlight {
		t.Fatalf("after shutdown status = %s, want IN_FLIGHT until the lease expires", r.Status)
	}
	// Fast-forward the lease (timeout + LeaseMargin) instead of waiting.
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE deliveries SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	// Act
	e.startWorker(t, "w2", 3*time.Second)

	// Assert
	r := e.waitStatus(t, id, domain.StatusSucceeded, 15*time.Second)
	reclaims := 0
	for _, a := range e.attempts(t, id) {
		if a.Error != nil && *a.Error == dispatch.ReclaimError {
			reclaims++
		}
	}
	if r.AttemptCount != 2 || reclaims != 1 {
		t.Fatalf("delivery = %+v, lease-expired attempts = %d", r, reclaims)
	}
}
