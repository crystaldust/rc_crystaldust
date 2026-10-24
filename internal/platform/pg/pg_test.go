package pg

import (
	"context"
	"testing"
	"time"
)

// unreachableDSN points at a port nothing listens on; connect_timeout keeps
// the failure fast.
const unreachableDSN = "postgres://hub@127.0.0.1:1/hub?sslmode=disable&connect_timeout=1"

func TestConnectionFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tests := []struct {
		name string
		run  func() error
	}{
		{"pool with invalid dsn", func() error { _, err := NewPool(ctx, "postgres://hub@db:port/hub"); return err }},
		{"pool with unreachable database", func() error { _, err := NewPool(ctx, unreachableDSN); return err }},
		{"direct with unreachable database", func() error { _, err := ConnectDirect(ctx, unreachableDSN); return err }},
		{"migrate with invalid dsn", func() error { _, err := MigrateUp(ctx, "postgres://hub@db:port/hub"); return err }},
		{"migrate with unreachable database", func() error { _, err := MigrateUp(ctx, unreachableDSN); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Fatal("succeeded, want error")
			}
		})
	}
}
