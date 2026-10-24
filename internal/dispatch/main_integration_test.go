//go:build integration

package dispatch

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/platform/pg"
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

const dispatchConfigYAML = `version: 1
principals:
  - { id: src, role: source, api_key_sha256_ref: env:HUB_KEY }
policies:
  - id: plain
    timeout: 1s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: none }
  - id: block
    timeout: 1s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: per_key, on_predecessor_dead: block }
  - id: skip
    timeout: 1s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: per_key, on_predecessor_dead: skip }
destinations:
  - { id: bulk, policy: plain, max_in_flight: 1000, request: { method: POST, url: "https://x.example.com/" } }
  - { id: capped, policy: plain, max_in_flight: 2, request: { method: POST, url: "https://x.example.com/" } }
  - { id: ordered-block, policy: block, request: { method: POST, url: "https://x.example.com/" } }
  - { id: ordered-skip, policy: skip, request: { method: POST, url: "https://x.example.com/" } }
routes:
  - { id: rt, event_type: a.b, destinations: [bulk] }
`

type fixture struct {
	pool *pgxpool.Pool
	cfg  *config.Config
	logs *syncBuffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool, err := pg.NewPool(context.Background(), testDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	pgtest.Reset(t, pool)
	cfg, err := config.Parse([]byte(dispatchConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	return fixture{pool: pool, cfg: cfg, logs: &syncBuffer{}}
}

func (f fixture) dispatcher(owner string) *Postgres {
	log := slog.New(slog.NewTextHandler(io.MultiWriter(f.logs), nil))
	return NewPostgres(f.pool, owner, func() *config.Config { return f.cfg }, log, time.Now)
}

const seedSQL = `WITH ev AS (
	INSERT INTO events (id, source, source_event_id, event_type, occurred_at, ordering_key,
		routing_status, matched_route_ids, routing_config_hash)
	SELECT gen_random_uuid(), 'src', 'e-' || gen_random_uuid(), 'a.b', now(), $3,
		'routed', '{r}', 'h'
	FROM generate_series(1, $1) g
	ORDER BY g
	RETURNING id, ingest_seq
)
INSERT INTO deliveries (id, event_id, destination_id, policy_id, ordering_key, ingest_seq, status)
SELECT gen_random_uuid(), id, $2, 'plain', $3, ingest_seq, $4 FROM ev
RETURNING id`

// seed inserts n events with one delivery each, in ingest order.
func (f fixture) seed(t *testing.T, n int, dest, orderingKey string, status domain.DeliveryStatus) []uuid.UUID {
	t.Helper()
	var key *string
	if orderingKey != "" {
		key = &orderingKey
	}
	rows, err := f.pool.Query(context.Background(), seedSQL, n, dest, key, string(status))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func (f fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (f fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (f fixture) status(t *testing.T, id uuid.UUID) domain.DeliveryStatus {
	t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM deliveries WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return domain.DeliveryStatus(s)
}

func claimAll(t *testing.T, d *Postgres, limit int) []Claimed {
	t.Helper()
	var all []Claimed
	for {
		got, err := d.Claim(context.Background(), limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			return all
		}
		all = append(all, got...)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
