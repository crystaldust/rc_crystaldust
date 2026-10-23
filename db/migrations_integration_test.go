//go:build integration

package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	postgresImage = "postgres:16-alpine"

	pgUniqueViolation = "23505"
	pgCheckViolation  = "23514"

	seedEventID    = "00000000-0000-7000-8000-000000000001"
	seedDeliveryID = "00000000-0000-7000-8000-000000000002"
)

var schemaTables = []string{"events", "event_payloads", "deliveries", "attempts", "operator_actions"}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	conn := startPostgres(ctx, t)
	setupGoose(t)

	if err := goose.Up(conn, MigrationsDir); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	t.Run("all tables exist after up", func(t *testing.T) {
		for _, table := range schemaTables {
			if !tableExists(ctx, t, conn, table) {
				t.Errorf("table %s does not exist", table)
			}
		}
	})

	seed(ctx, t, conn)

	t.Run("constraints reject invalid rows", func(t *testing.T) {
		tests := []struct {
			name     string
			query    string
			wantCode string
		}{
			{
				name: "duplicate source and source_event_id",
				query: `INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
					routing_status, matched_route_ids, routing_config_hash)
					VALUES (gen_random_uuid(), 'order-service', 'evt-1', 'order.paid', now(),
					'routed', '{order-paid}', 'hash')`,
				wantCode: pgUniqueViolation,
			},
			{
				name: "unrouted with matched routes",
				query: `INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
					routing_status, matched_route_ids, routing_config_hash)
					VALUES (gen_random_uuid(), 'order-service', 'evt-unrouted', 'order.paid', now(),
					'unrouted', '{order-paid}', 'hash')`,
				wantCode: pgCheckViolation,
			},
			{
				name: "routed without matched routes",
				query: `INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
					routing_status, matched_route_ids, routing_config_hash)
					VALUES (gen_random_uuid(), 'order-service', 'evt-routed', 'order.paid', now(),
					'routed', '{}', 'hash')`,
				wantCode: pgCheckViolation,
			},
			{
				name: "unknown routing status",
				query: `INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
					routing_status, matched_route_ids, routing_config_hash)
					VALUES (gen_random_uuid(), 'order-service', 'evt-bad-routing', 'order.paid', now(),
					'dropped', '{}', 'hash')`,
				wantCode: pgCheckViolation,
			},
			{
				name: "invalid delivery status",
				query: `INSERT INTO deliveries (id, event_id, destination_id, policy_id, status, ingest_seq)
					VALUES (gen_random_uuid(), '` + seedEventID + `', 'erp', 'default', 'DONE', 1)`,
				wantCode: pgCheckViolation,
			},
			{
				name: "duplicate event and destination",
				query: `INSERT INTO deliveries (id, event_id, destination_id, policy_id, ingest_seq)
					VALUES (gen_random_uuid(), '` + seedEventID + `', 'crm', 'default', 1)`,
				wantCode: pgUniqueViolation,
			},
			{
				name: "negative attempt count",
				query: `INSERT INTO deliveries (id, event_id, destination_id, policy_id, ingest_seq, attempt_count)
					VALUES (gen_random_uuid(), '` + seedEventID + `', 'erp', 'default', 1, -1)`,
				wantCode: pgCheckViolation,
			},
			{
				name: "invalid attempt outcome",
				query: `INSERT INTO attempts (id, delivery_id, number, trigger, started_at, outcome, config_hash)
					VALUES (gen_random_uuid(), '` + seedDeliveryID + `', 1, 'auto', now(), 'ok', 'hash')`,
				wantCode: pgCheckViolation,
			},
			{
				name: "invalid attempt trigger",
				query: `INSERT INTO attempts (id, delivery_id, number, trigger, started_at, outcome, config_hash)
					VALUES (gen_random_uuid(), '` + seedDeliveryID + `', 1, 'manual', now(), 'success', 'hash')`,
				wantCode: pgCheckViolation,
			},
			{
				name: "invalid operator action",
				query: `INSERT INTO operator_actions (id, delivery_id, action, actor, reason, from_status)
					VALUES (gen_random_uuid(), '` + seedDeliveryID + `', 'delete', 'ops', 'why', 'DEAD')`,
				wantCode: pgCheckViolation,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Act
				_, err := conn.ExecContext(ctx, tt.query)

				// Assert
				assertPgErrorCode(t, err, tt.wantCode)
			})
		}
	})

	t.Run("attempts with any started_at land in default partition", func(t *testing.T) {
		for _, startedAt := range []string{"1999-01-01T00:00:00Z", "2026-10-24T12:00:00Z", "2099-12-31T23:59:59Z"} {
			// Act
			var partition string
			err := conn.QueryRowContext(ctx, `INSERT INTO attempts
				(id, delivery_id, number, trigger, started_at, outcome, config_hash)
				VALUES (gen_random_uuid(), $1, 1, 'auto', $2, 'success', 'hash')
				RETURNING tableoid::regclass::text`, seedDeliveryID, startedAt).Scan(&partition)

			// Assert
			if err != nil {
				t.Fatalf("insert attempt at %s: %v", startedAt, err)
			}
			if partition != "attempts_default" {
				t.Errorf("attempt at %s landed in %s, want attempts_default", startedAt, partition)
			}
		}
	})

	t.Run("down removes all tables", func(t *testing.T) {
		if err := goose.Down(conn, MigrationsDir); err != nil {
			t.Fatalf("goose down: %v", err)
		}
		for _, table := range append(schemaTables, "attempts_default") {
			if tableExists(ctx, t, conn, table) {
				t.Errorf("table %s still exists after down", table)
			}
		}
	})

	t.Run("up again succeeds", func(t *testing.T) {
		if err := goose.Up(conn, MigrationsDir); err != nil {
			t.Fatalf("goose up again: %v", err)
		}
		for _, table := range schemaTables {
			if !tableExists(ctx, t, conn, table) {
				t.Errorf("table %s does not exist after second up", table)
			}
		}
	})
}

func startPostgres(ctx context.Context, t *testing.T) *sql.DB {
	t.Helper()
	ctr, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("messagehub"),
		postgres.WithUsername("hub"),
		postgres.WithPassword("hub"),
		postgres.BasicWaitStrategies(),
	)
	if ctr != nil {
		t.Cleanup(func() {
			if err := ctr.Terminate(context.Background()); err != nil {
				t.Errorf("terminate postgres container: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	return conn
}

func setupGoose(t *testing.T) {
	t.Helper()
	goose.SetBaseFS(Migrations)
	t.Cleanup(func() { goose.SetBaseFS(nil) })
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
}

// seed inserts one routed event and one delivery that the constraint cases collide with.
func seed(ctx context.Context, t *testing.T, conn *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
			routing_status, matched_route_ids, routing_config_hash)
			VALUES ('` + seedEventID + `', 'order-service', 'evt-1', 'order.paid', now(),
			'routed', '{order-paid}', 'hash')`,
		`INSERT INTO event_payloads (event_id, payload) VALUES ('` + seedEventID + `', '{"order_id": 1}')`,
		`INSERT INTO deliveries (id, event_id, destination_id, policy_id, ingest_seq)
			SELECT '` + seedDeliveryID + `', id, 'crm', 'default', ingest_seq
			FROM events WHERE id = '` + seedEventID + `'`,
		`INSERT INTO events (id, source, source_event_id, event_type, occurred_at,
			routing_status, routing_config_hash)
			VALUES (gen_random_uuid(), 'order-service', 'evt-unrouted-ok', 'order.refunded', now(),
			'unrouted', 'hash')`,
	}
	for _, stmt := range statements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
}

func tableExists(ctx context.Context, t *testing.T, conn *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	err := conn.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists)
	if err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

func assertPgErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with SQLSTATE %s, got nil", wantCode)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, wantCode)
	}
}
