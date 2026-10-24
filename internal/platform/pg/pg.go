// Package pg opens PostgreSQL connections and applies the embedded schema
// migrations. It depends only on db (the embedded migrations) and pgx.
package pg

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"messagehub/db"
)

// NewPool opens the shared connection pool (pgxpool defaults) and checks
// that the database answers. The pool may go through pgbouncer.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// ConnectDirect opens one dedicated connection, for LISTEN (ADR-0009 P4):
// dsn must reach PostgreSQL directly, not a transaction-mode pgbouncer.
func ConnectDirect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open direct database connection: %w", err)
	}
	return conn, nil
}

// MigrateUp applies every pending embedded migration and returns how many
// were applied; running it again is a no-op. It uses a goose Provider, which
// holds no package-level state, so concurrent callers do not interfere.
func MigrateUp(ctx context.Context, dsn string) (int, error) {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return 0, fmt.Errorf("parse database url: %w", err)
	}
	sqlDB := stdlib.OpenDB(*connCfg)
	defer sqlDB.Close()
	return migrate(ctx, sqlDB)
}

func migrate(ctx context.Context, sqlDB *sql.DB) (int, error) {
	migrations, err := fs.Sub(db.Migrations, db.MigrationsDir)
	if err != nil {
		return 0, fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		return 0, fmt.Errorf("create migration provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	return len(results), nil
}
