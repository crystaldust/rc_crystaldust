package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/secrets"
	"messagehub/internal/store"
)

// HTTP server limits.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	readyTimeout      = 2 * time.Second
	// requestTimeout bounds each handler, including its database queries;
	// it is below writeTimeout so the error response can still be written.
	requestTimeout = 10 * time.Second
	maxHeaderBytes = 16 * 1024

	// maxIngestBodyBytes leaves room for the envelope around the largest
	// payload (domain.MaxPayloadBytes = 256 KiB).
	maxIngestBodyBytes   = 512 * 1024
	maxOperatorBodyBytes = 8 * 1024
)

// The store as the handlers see it; *store.Store implements it and tests
// use a fake.
type ingestStore interface {
	Ingest(ctx context.Context, in store.IngestInput) (store.IngestResult, error)
}

type queryStore interface {
	GetEvent(ctx context.Context, scope store.Scope, id uuid.UUID) (store.EventDetail, error)
	ListEvents(ctx context.Context, scope store.Scope, f store.EventFilter) (store.Page[domain.Event], error)
	GetDelivery(ctx context.Context, scope store.Scope, id uuid.UUID, page store.AttemptPage) (store.DeliveryDetail, error)
	ListDeliveries(ctx context.Context, scope store.Scope, f store.DeliveryFilter) (store.Page[store.DeliveryView], error)
}

type operatorStore interface {
	Replay(ctx context.Context, in store.OperatorInput) (store.OperatorAction, error)
	Skip(ctx context.Context, in store.OperatorInput) (store.OperatorAction, error)
}

type dataStore interface {
	ingestStore
	queryStore
	operatorStore
}

// deps are the handler dependencies. config is read on every request.
type deps struct {
	config   func() *config.Config
	resolver secrets.Resolver
	store    dataStore
	ready    func(ctx context.Context) error
	log      *slog.Logger
	now      func() time.Time
}

type server struct {
	deps
	auth *authenticator
}

var (
	rolesSource   = []config.Role{config.RoleSource}
	rolesOperator = []config.Role{config.RoleOperator}
	rolesAll      = []config.Role{config.RoleSource, config.RoleViewer, config.RoleOperator}
)

// newHandler registers the endpoints of api/openapi.yaml.
func newHandler(d deps) http.Handler {
	s := &server{deps: d, auth: newAuthenticator(d.config, d.resolver, d.log)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", s.auth.require(rolesSource, s.ingest))
	mux.HandleFunc("GET /v1/events", s.auth.require(rolesAll, s.listEvents))
	mux.HandleFunc("GET /v1/events/{event_id}", s.auth.require(rolesAll, s.getEvent))
	mux.HandleFunc("GET /v1/deliveries", s.auth.require(rolesAll, s.listDeliveries))
	mux.HandleFunc("GET /v1/deliveries/{delivery_id}", s.auth.require(rolesAll, s.getDelivery))
	mux.HandleFunc("POST /v1/deliveries/{delivery_id}/replay", s.auth.require(rolesOperator, s.replay))
	mux.HandleFunc("POST /v1/deliveries/{delivery_id}/skip", s.auth.require(rolesOperator, s.skip))
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeNotFound(w, "route") })
	return withObservability(mux, d.log, d.now)
}

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// scopeFor limits a source principal to its own data (§9).
func scopeFor(p config.Principal) store.Scope {
	if p.Role == config.RoleSource {
		return store.Scope{Source: p.ID}
	}
	return store.Scope{}
}
