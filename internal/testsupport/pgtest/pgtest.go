//go:build integration

// Package pgtest starts a migrated PostgreSQL container for integration
// tests. Share one container per test package from TestMain:
//
//	func TestMain(m *testing.M) {
//		dsn, stop, err := pgtest.StartShared(context.Background())
//		if err != nil { log.Fatal(err) }
//		testDSN = dsn
//		code := m.Run()
//		stop()
//		os.Exit(code)
//	}
//
// Colima users need DOCKER_HOST and TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE
// (coding-conventions.md §10).
package pgtest

import (
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"messagehub/internal/platform/pg"
)

const (
	image = "postgres:16-alpine"
	// Obviously fake credentials for a throwaway test container.
	database = "messagehub"
	username = "hub"
	password = "hub"
)

// Tables lists the business tables Reset clears, children first.
var Tables = []string{"attempts", "operator_actions", "deliveries", "event_payloads", "events"}

// StartShared starts a container and applies every migration. Call stop
// once all tests in the package have finished.
func StartShared(ctx context.Context) (dsn string, stop func(), err error) {
	ctr, err := postgres.Run(ctx, image,
		postgres.WithDatabase(database),
		postgres.WithUsername(username),
		postgres.WithPassword(password),
		postgres.BasicWaitStrategies(),
	)
	stop = func() {
		if ctr == nil {
			return
		}
		if err := ctr.Terminate(context.Background()); err != nil {
			log.Printf("terminate postgres container: %v", err)
		}
	}
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("start postgres container: %w", err)
	}
	dsn, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("postgres connection string: %w", err)
	}
	if _, err := pg.MigrateUp(ctx, dsn); err != nil {
		stop()
		return "", nil, err
	}
	return dsn, stop, nil
}

// Start starts a container for one test and terminates it on cleanup.
func Start(t *testing.T) string {
	t.Helper()
	dsn, stop, err := StartShared(context.Background())
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	t.Cleanup(stop)
	return dsn
}

// Reset empties every business table so tests sharing a container start clean.
func Reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	stmt := "TRUNCATE " + strings.Join(Tables, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("pgtest reset: %v", err)
	}
}
