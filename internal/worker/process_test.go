package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/config"
	"messagehub/internal/dispatch"
	"messagehub/internal/domain"
	"messagehub/internal/outbound"
	"messagehub/internal/secrets"
	"messagehub/internal/template"
)

const vendorToken = "vendor-token-abc123"

var fixedNow = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

const testConfigYAML = `version: 1
principals:
  - id: order-service
    role: source
    api_key_sha256_ref: env:HUB_KEY
policies:
  - id: std
    timeout: 1s
    retry:
      max_attempts: 3
      backoff: { initial: 5s, multiplier: 2.0, max: 1h, jitter: 0 }
      retry_on: { status: [429, "5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: none }
  - id: biz
    timeout: 1s
    retry:
      max_attempts: 3
      backoff: { initial: 5s, multiplier: 2.0, max: 1h, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success:
      status: ["2xx"]
      body: [ { path: code, op: eq, value: 0 } ]
    ordering: { mode: none }
  - id: fast
    timeout: 50ms
    retry:
      max_attempts: 3
      backoff: { initial: 5s, multiplier: 2.0, max: 1h, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: none }
destinations:
  - id: plain
    policy: std
    secrets: { TOKEN: env:VENDOR_TOKEN }
    request:
      method: POST
      url: "%[1]s/{{ event.payload.mode }}?id={{ event.id }}"
      headers:
        Authorization: "Bearer {{ secret.TOKEN }}"
        Content-Type: application/json
      body:
        order: "{{ event.payload.order }}"
  - id: biz
    policy: biz
    request: { method: POST, url: "%[1]s/biz" }
  - id: slow
    policy: fast
    request: { method: POST, url: "%[1]s/slow" }
  - id: broken
    policy: std
    request:
      method: POST
      url: "%[1]s/ok"
      body: { x: "{{ event.payload.nope }}" }
  - id: dynamic
    policy: std
    request: { method: POST, url: "{{ event.payload.target }}" }
  - id: oauth
    policy: std
    auth:
      type: oauth2_client_credentials
      token_url: "%[1]s/token"
      client_id_ref: env:CLIENT_ID
      client_secret_ref: env:CLIENT_SECRET
    request: { method: PUT, url: "%[1]s/ok" }
routes:
  - id: all
    event_type: order.paid
    destinations: [plain, biz, slow, broken, dynamic, oauth]
`

func newVendor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/busy":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/biz":
			_, _ = w.Write([]byte(`{"code":1001,"access_token":"echoed"}`))
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeDispatcher struct {
	mu          sync.Mutex
	completions []dispatch.Completion
	applied     bool
	err         error
	failFirst   int // calls that fail before err applies
}

func (f *fakeDispatcher) Claim(context.Context, int) ([]dispatch.Claimed, error) { return nil, nil }

func (f *fakeDispatcher) Complete(_ context.Context, c dispatch.Completion) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions = append(f.completions, c)
	if len(f.completions) <= f.failFirst {
		return false, errors.New("transient")
	}
	return f.applied, f.err
}

func (f *fakeDispatcher) last(t *testing.T) dispatch.Completion {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.completions) == 0 {
		t.Fatal("no completion recorded")
	}
	return f.completions[len(f.completions)-1]
}

type fakeEvents struct {
	payload map[string]any
	err     error
}

func (f fakeEvents) LoadEvent(_ context.Context, id uuid.UUID) (template.Event, error) {
	if f.err != nil {
		return template.Event{}, f.err
	}
	return template.Event{
		ID: id.String(), Source: "order-service", SourceEventID: "o-1", EventType: "order.paid",
		OccurredAt: fixedNow, ReceivedAt: fixedNow, Payload: f.payload,
	}, nil
}

type fakeTokens struct {
	header string
	err    error
}

func (f fakeTokens) Authorization(string, string, config.Auth) (string, error) {
	return f.header, f.err
}

type harness struct {
	w    *Worker
	disp *fakeDispatcher
}

func newHarness(t *testing.T, events EventLoader, tokens authorizer, env map[string]string) harness {
	t.Helper()
	vendor := newVendor(t)
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfigYAML, vendor.URL)))
	if err != nil {
		t.Fatal(err)
	}
	disp := &fakeDispatcher{applied: true}
	return harness{disp: disp, w: &Worker{
		config:     func() *config.Config { return cfg },
		secrets:    secrets.NewEnvResolver(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		dispatcher: disp,
		events:     events,
		client:     outbound.NewClient(outbound.NewTransport()),
		tokens:     tokens,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:        func() time.Time { return fixedNow },
		rnd:        func() float64 { return 0.5 },
	}}
}

var tokenEnv = map[string]string{"VENDOR_TOKEN": vendorToken}

func claimed(dest string, attemptCount int) dispatch.Claimed {
	return dispatch.Claimed{DeliveryID: domain.NewID(), EventID: domain.NewID(), DestinationID: dest,
		AttemptCount: attemptCount, LeaseExpiresAt: fixedNow.Add(time.Minute)}
}

func TestProcessTriggers(t *testing.T) {
	five, thirty, ten := 5*time.Second, 30*time.Second, 10*time.Second
	tests := []struct {
		name     string
		dest     string
		mode     string
		attempts int
		events   EventLoader
		tokens   authorizer
		env      map[string]string
		trigger  domain.Trigger
		outcome  domain.Outcome
		counts   bool
		delay    *time.Duration
	}{
		{name: "success", dest: "plain", mode: "ok", trigger: domain.TriggerSucceed, outcome: domain.OutcomeSuccess, counts: true},
		{name: "5xx with budget retries", dest: "plain", mode: "fail", trigger: domain.TriggerRetry, outcome: domain.OutcomeRetryable, counts: true, delay: &five},
		{name: "5xx on last attempt gives up", dest: "plain", mode: "fail", attempts: 2, trigger: domain.TriggerGiveUp, outcome: domain.OutcomeRetryable, counts: true},
		{name: "404 is permanent", dest: "plain", mode: "missing", trigger: domain.TriggerGiveUp, outcome: domain.OutcomePermanent, counts: true},
		{name: "429 honours retry-after", dest: "plain", mode: "busy", trigger: domain.TriggerRetry, outcome: domain.OutcomeRetryable, counts: true, delay: &thirty},
		{name: "200 with business error code is permanent (PoC S3)", dest: "biz", trigger: domain.TriggerGiveUp, outcome: domain.OutcomePermanent, counts: true},
		{name: "timeout retries", dest: "slow", trigger: domain.TriggerRetry, outcome: domain.OutcomeRetryable, counts: true, delay: &five},
		{name: "render error gives up", dest: "broken", trigger: domain.TriggerGiveUp, outcome: domain.OutcomeRenderError},
		{name: "unsendable url is a render error", dest: "dynamic", trigger: domain.TriggerGiveUp, outcome: domain.OutcomeRenderError},
		{name: "unset secret is infra", dest: "plain", mode: "ok", env: map[string]string{}, trigger: domain.TriggerInfraRetry, outcome: domain.OutcomeInfraError, delay: &five},
		{name: "event load failure is infra", dest: "plain", events: fakeEvents{err: errors.New("db down")}, trigger: domain.TriggerInfraRetry, outcome: domain.OutcomeInfraError, delay: &five},
		{name: "token failure is infra", dest: "oauth", tokens: fakeTokens{err: fmt.Errorf("%w: token", outbound.ErrInfra)}, trigger: domain.TriggerInfraRetry, outcome: domain.OutcomeInfraError, delay: &five},
		{name: "infra retry keeps the count", dest: "oauth", attempts: 1, tokens: fakeTokens{err: outbound.ErrInfra}, trigger: domain.TriggerInfraRetry, outcome: domain.OutcomeInfraError, delay: &ten},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			events := tt.events
			if events == nil {
				events = fakeEvents{payload: map[string]any{"mode": tt.mode, "order": "o-1", "target": "ftp://x.example.com/"}}
			}
			tokens := tt.tokens
			if tokens == nil {
				tokens = fakeTokens{}
			}
			env := tt.env
			if env == nil {
				env = tokenEnv
			}
			h := newHarness(t, events, tokens, env)
			c := claimed(tt.dest, tt.attempts)

			// Act
			h.w.Process(context.Background(), c)

			// Assert
			got := h.disp.last(t)
			if got.Trigger != tt.trigger || got.Outcome != tt.outcome || got.CountsAttempt != tt.counts {
				t.Fatalf("got %s/%s counts=%v (error %q), want %s/%s counts=%v",
					got.Trigger, got.Outcome, got.CountsAttempt, got.Error, tt.trigger, tt.outcome, tt.counts)
			}
			if !equalDurationPtr(got.RetryDelay, tt.delay) {
				t.Fatalf("retry delay = %v, want %v", got.RetryDelay, tt.delay)
			}
			if got.Attempt == nil || got.Attempt.Number != tt.attempts+1 || got.Attempt.Outcome != tt.outcome {
				t.Fatalf("attempt = %+v", got.Attempt)
			}
		})
	}
}

type failingSender struct{ err error }

func (f failingSender) Send(context.Context, outbound.Request, time.Duration) (outbound.Response, error) {
	return outbound.Response{}, f.err
}

func TestProcessEgressFailureIsInfra(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok", "order": "o-1"}}, fakeTokens{}, tokenEnv)
	h.w.client = failingSender{err: fmt.Errorf("%w: egress proxy: refused", outbound.ErrInfra)}

	// Act
	h.w.Process(context.Background(), claimed("plain", 0))

	// Assert
	got := h.disp.last(t)
	if got.Trigger != domain.TriggerInfraRetry || got.CountsAttempt || got.Attempt.Request == nil {
		t.Fatalf("got %s counts=%v request=%v", got.Trigger, got.CountsAttempt, got.Attempt.Request)
	}
}

func TestProcessRecordIsMasked(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok", "order": "o-1"}}, fakeTokens{}, tokenEnv)

	// Act
	h.w.Process(context.Background(), claimed("plain", 0))

	// Assert
	rec := h.disp.last(t).Attempt
	if rec.Request == nil || rec.Request.Headers["Authorization"] != domain.MaskedValue {
		t.Fatalf("request = %+v", rec.Request)
	}
	if rec.Request.Body != `{"order":"o-1"}` || rec.Response == nil || rec.Response.StatusCode != 200 {
		t.Fatalf("record = %+v / %+v", rec.Request, rec.Response)
	}
	if strings.Contains(fmt.Sprint(rec), vendorToken) {
		t.Fatal("secret leaked into the attempt record")
	}
}

func TestProcessAddsOAuthHeaderAndMasksIt(t *testing.T) {
	// Arrange
	var gotAuth string
	h := newHarness(t, fakeEvents{payload: map[string]any{}}, fakeTokens{header: "Bearer at-1"}, tokenEnv)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfigYAML, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}
	h.w.config = func() *config.Config { return cfg }

	// Act
	h.w.Process(context.Background(), claimed("oauth", 0))

	// Assert
	got := h.disp.last(t)
	if got.Outcome != domain.OutcomeSuccess || gotAuth != "Bearer at-1" {
		t.Fatalf("outcome = %s, vendor saw Authorization %q", got.Outcome, gotAuth)
	}
	if got.Attempt.Request.Headers["Authorization"] != domain.MaskedValue {
		t.Fatalf("recorded Authorization = %q", got.Attempt.Request.Headers["Authorization"])
	}
}

func TestProcessResponseEchoIsMasked(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{payload: map[string]any{}}, fakeTokens{}, tokenEnv)

	// Act
	h.w.Process(context.Background(), claimed("biz", 0))

	// Assert
	resp := h.disp.last(t).Attempt.Response
	if resp == nil || strings.Contains(resp.Body, "echoed") {
		t.Fatalf("response = %+v", resp)
	}
}

func TestProcessMissingDestinationGivesUp(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{}, fakeTokens{}, tokenEnv)

	// Act
	h.w.Process(context.Background(), claimed("gone", 4))

	// Assert
	got := h.disp.last(t)
	if got.Trigger != domain.TriggerGiveUp || got.Outcome != domain.OutcomePermanent || got.CountsAttempt {
		t.Fatalf("got %+v", got)
	}
	if got.Attempt == nil || got.Attempt.Number != 5 || !strings.Contains(got.Error, "gone") {
		t.Fatalf("attempt = %+v, error %q", got.Attempt, got.Error)
	}
}

func TestProcessReclaimedWithSpentBudgetGivesUpWithoutSending(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok"}}, fakeTokens{}, tokenEnv)
	c := claimed("plain", 3)
	c.Reclaimed = true

	// Act
	h.w.Process(context.Background(), c)

	// Assert
	got := h.disp.last(t)
	if got.Trigger != domain.TriggerGiveUp || got.Attempt != nil || got.CountsAttempt {
		t.Fatalf("got %+v", got)
	}
}

func TestProcessAfterReplayRecordsReplayTrigger(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok"}}, fakeTokens{}, tokenEnv)
	c := claimed("plain", 0)
	c.ReplayCount = 1

	// Act
	h.w.Process(context.Background(), c)

	// Assert
	if got := h.disp.last(t).Attempt.Trigger; got != domain.AttemptReplay {
		t.Fatalf("attempt trigger = %s, want replay", got)
	}
}

func TestProcessSurvivesCompleteFailures(t *testing.T) {
	for _, tt := range []struct {
		name    string
		applied bool
		err     error
		calls   int
	}{
		{"lease lost", false, nil, 1},
		{"write keeps failing is retried then abandoned", false, errors.New("db down"), completeAttempts},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok"}}, fakeTokens{}, tokenEnv)
			h.disp.applied, h.disp.err = tt.applied, tt.err

			// Act
			h.w.Process(context.Background(), claimed("plain", 0))

			// Assert
			if len(h.disp.completions) != tt.calls {
				t.Fatalf("completions = %d", len(h.disp.completions))
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdef", 3, "abc"},
		{"a中b", 2, "a"},
		{"code \x00\xff", 100, "code ��"},
	}
	for _, tt := range tests {
		// Act
		got := truncateText(tt.in, tt.n)

		// Assert
		if got != tt.want {
			t.Errorf("truncateText(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func equalDurationPtr(a, b *time.Duration) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func TestProcessRetriesATransientCompleteFailure(t *testing.T) {
	// Arrange
	h := newHarness(t, fakeEvents{payload: map[string]any{"mode": "ok", "order": "o-1"}}, fakeTokens{}, tokenEnv)
	h.disp.failFirst = 1

	// Act
	h.w.Process(context.Background(), claimed("plain", 0))

	// Assert
	calls := h.disp.completions
	if len(calls) != 2 || calls[0].Attempt.ID != calls[1].Attempt.ID {
		t.Fatalf("calls = %d, want a retry with the same attempt id", len(calls))
	}
}

func TestOAuthHeaderReplacesConfiguredAuthorizationInAnyCase(t *testing.T) {
	// Arrange
	r := template.Rendered{
		Actual: template.Request{Headers: map[string]string{"authorization": "Bearer static", "X-A": "1"}},
		Masked: template.Request{Headers: map[string]string{"authorization": "***", "X-A": "1"}},
	}

	// Act
	got := withAuthorization(r, "Bearer at-1")

	// Assert
	want := map[string]string{"Authorization": "Bearer at-1", "X-A": "1"}
	if fmt.Sprint(got.Actual.Headers) != fmt.Sprint(want) || r.Actual.Headers["authorization"] != "Bearer static" {
		t.Fatalf("headers = %v (input %v)", got.Actual.Headers, r.Actual.Headers)
	}
}
