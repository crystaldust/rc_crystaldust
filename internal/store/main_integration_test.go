//go:build integration

package store

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/platform/pg"
	"messagehub/internal/testsupport/pgtest"
)

const exampleConfig = "../../config/examples/hub.example.yaml"

// testDSN is the shared container of this package (one per test binary).
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

// fixture is a clean database plus a Store on the example config.
type fixture struct {
	pool  *pgxpool.Pool
	store *Store
	cfg   *config.Config
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return newFixtureWith(t, func(s string) string { return s })
}

// newFixtureWith applies edit to the example config text before loading it.
func newFixtureWith(t *testing.T, edit func(string) string) fixture {
	t.Helper()
	pool, err := pg.NewPool(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	pgtest.Reset(t, pool)
	raw, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg, err := config.Parse([]byte(edit(string(raw))))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return fixture{pool: pool, store: New(pool, func() *config.Config { return cfg }), cfg: cfg}
}

// input builds an IngestInput for source with one delivery per destination,
// using each destination's policy from the example config.
func (f fixture) input(t *testing.T, source, sourceEventID, orderingKey string, destinations ...string) IngestInput {
	t.Helper()
	routeIDs := []string{}
	if len(destinations) > 0 {
		routeIDs = []string{"test-route"}
	}
	deliveries := make([]NewDelivery, 0, len(destinations))
	for _, id := range destinations {
		d, ok := f.cfg.Destination(id)
		if !ok {
			t.Fatalf("unknown destination %q", id)
		}
		deliveries = append(deliveries, NewDelivery{ID: domain.NewID(), DestinationID: id, PolicyID: d.Policy})
	}
	return IngestInput{
		EventID: domain.NewID(),
		Source:  source,
		Envelope: domain.Envelope{
			SourceEventID: sourceEventID,
			EventType:     "order.purchased",
			OccurredAt:    time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC),
			Payload:       json.RawMessage(`{"sku":"sku-1","amount":100}`),
			OrderingKey:   orderingKey,
			Metadata:      map[string]string{"trace_id": "abc123"},
		},
		RouteIDs:   routeIDs,
		Deliveries: deliveries,
		ConfigHash: f.cfg.Hash(),
	}
}

func (f fixture) mustIngest(t *testing.T, in IngestInput) IngestResult {
	t.Helper()
	res, err := f.store.Ingest(context.Background(), in)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return res
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// setStatus forces a delivery status; Phase 4 owns real transitions.
func (f fixture) setStatus(t *testing.T, id uuid.UUID, status domain.DeliveryStatus) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE deliveries SET status = $2 WHERE id = $1`, id, string(status)); err != nil {
		t.Fatalf("set status: %v", err)
	}
}
