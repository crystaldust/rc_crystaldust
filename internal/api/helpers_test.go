package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/apikey"
	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/secrets"
	"messagehub/internal/store"
)

const exampleConfig = "../../config/examples/hub.example.yaml"

// testEnv is the example config with a freshly generated key per principal.
type testEnv struct {
	cfg  *config.Config
	env  map[string]string
	keys map[string]string // principal id -> API key
	logs *bytes.Buffer
	log  *slog.Logger
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvFrom(t, readExample(t))
}

func readExample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	return string(raw)
}

func newTestEnvFrom(t *testing.T, yaml string) *testEnv {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	logs := &bytes.Buffer{}
	e := &testEnv{cfg: cfg, env: map[string]string{}, keys: map[string]string{}, logs: logs,
		log: slog.New(slog.NewJSONHandler(logs, nil))}
	for _, name := range cfg.RequiredEnv() {
		e.env[name] = "fake-secret-value"
	}
	for _, p := range cfg.Principals() {
		e.addKey(t, p)
	}
	return e
}

// addKey generates a key for p and stores its hash in p's variable,
// replacing any previous hash.
func (e *testEnv) addKey(t *testing.T, p config.Principal) string {
	t.Helper()
	key, hash, err := apikey.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	e.env[strings.TrimPrefix(p.APIKeySHA256Ref, config.EnvRefPrefix)] = hash
	e.keys[p.ID] = key
	return key
}

func (e *testEnv) resolver() secrets.Resolver {
	return secrets.NewEnvResolver(func(name string) (string, bool) {
		v, ok := e.env[name]
		return v, ok
	})
}

func (e *testEnv) currentConfig() *config.Config { return e.cfg }

// discardLog is a logger for tests that do not inspect logs.
var discardLog = slog.New(slog.NewJSONHandler(io.Discard, nil))

// fakeStore records calls and returns canned results.
type fakeStore struct {
	ingestIn     store.IngestInput
	ingestRes    store.IngestResult
	ingestErr    error
	scope        store.Scope
	eventFilter  store.EventFilter
	events       store.Page[domain.Event]
	eventDetail  store.EventDetail
	delivFilter  store.DeliveryFilter
	deliveries   store.Page[store.DeliveryView]
	delivDetail  store.DeliveryDetail
	attemptPage  store.AttemptPage
	operatorIn   store.OperatorInput
	action       store.OperatorAction
	err          error // returned by every query and operator call
	panicOnQuery bool
}

func (f *fakeStore) Ingest(_ context.Context, in store.IngestInput) (store.IngestResult, error) {
	f.ingestIn = in
	return f.ingestRes, f.ingestErr
}

func (f *fakeStore) GetEvent(_ context.Context, scope store.Scope, _ uuid.UUID) (store.EventDetail, error) {
	f.scope = scope
	return f.eventDetail, f.err
}

func (f *fakeStore) ListEvents(_ context.Context, scope store.Scope, flt store.EventFilter) (store.Page[domain.Event], error) {
	if f.panicOnQuery {
		panic("boom")
	}
	f.scope, f.eventFilter = scope, flt
	return f.events, f.err
}

func (f *fakeStore) GetDelivery(_ context.Context, scope store.Scope, _ uuid.UUID, page store.AttemptPage) (store.DeliveryDetail, error) {
	f.scope, f.attemptPage = scope, page
	return f.delivDetail, f.err
}

func (f *fakeStore) ListDeliveries(_ context.Context, scope store.Scope, flt store.DeliveryFilter) (store.Page[store.DeliveryView], error) {
	f.scope, f.delivFilter = scope, flt
	return f.deliveries, f.err
}

func (f *fakeStore) Replay(_ context.Context, in store.OperatorInput) (store.OperatorAction, error) {
	f.operatorIn = in
	return f.action, f.err
}

func (f *fakeStore) Skip(_ context.Context, in store.OperatorInput) (store.OperatorAction, error) {
	f.operatorIn = in
	return f.action, f.err
}

// harness is a handler over the example config and a fake store.
type harness struct {
	*testEnv
	store    *fakeStore
	readyErr error
	handler  http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{testEnv: newTestEnv(t), store: &fakeStore{}}
	h.handler = newHandler(deps{
		config:   h.currentConfig,
		resolver: h.resolver(),
		store:    h.store,
		ready:    func(context.Context) error { return h.readyErr },
		log:      h.log,
		now:      time.Now,
	})
	return h
}

// do sends a request as principal (empty: no Authorization header).
func (h *harness) do(t *testing.T, method, target, principal, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if principal != "" {
		req.Header.Set("Authorization", "Bearer "+h.keys[principal])
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON: %q", rec.Body.String())
	}
	return rec, decoded
}

// errorCode returns error.code of an error envelope.
func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// errorFields returns the field of every error detail.
func errorFields(body map[string]any) []string {
	e, _ := body["error"].(map[string]any)
	details, _ := e["details"].([]any)
	out := make([]string, 0, len(details))
	for _, d := range details {
		m, _ := d.(map[string]any)
		f, _ := m["field"].(string)
		out = append(out, f)
	}
	return out
}
