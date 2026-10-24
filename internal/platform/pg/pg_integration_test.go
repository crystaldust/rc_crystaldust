//go:build integration

package pg_test

import (
	"context"
	"testing"

	"messagehub/internal/platform/pg"
	"messagehub/internal/testsupport/pgtest"
)

func TestDatabaseLifecycle(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.Start(t) // already migrated once

	t.Run("migrate up again is a no-op", func(t *testing.T) {
		applied, err := pg.MigrateUp(ctx, dsn)
		if err != nil {
			t.Fatalf("MigrateUp error: %v", err)
		}
		if applied != 0 {
			t.Errorf("applied %d migrations on the second run, want 0", applied)
		}
	})

	t.Run("pool connects and reset empties tables", func(t *testing.T) {
		pool, err := pg.NewPool(ctx, dsn)
		if err != nil {
			t.Fatalf("NewPool error: %v", err)
		}
		defer pool.Close()
		if _, err := pool.Exec(ctx, `INSERT INTO events (id, source, source_event_id, event_type,
			occurred_at, routing_status, matched_route_ids, routing_config_hash)
			VALUES (gen_random_uuid(), 'order-service', 'evt-1', 'order.paid', now(),
			'routed', '{order-paid}', 'hash')`); err != nil {
			t.Fatalf("seed event: %v", err)
		}

		pgtest.Reset(t, pool)

		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&n); err != nil {
			t.Fatalf("count events: %v", err)
		}
		if n != 0 {
			t.Errorf("events has %d rows after Reset, want 0", n)
		}
	})

	t.Run("direct connection answers", func(t *testing.T) {
		conn, err := pg.ConnectDirect(ctx, dsn)
		if err != nil {
			t.Fatalf("ConnectDirect error: %v", err)
		}
		defer conn.Close(ctx)
		if err := conn.Ping(ctx); err != nil {
			t.Fatalf("Ping error: %v", err)
		}
	})
}
