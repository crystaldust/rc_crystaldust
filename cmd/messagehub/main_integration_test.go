//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"messagehub/internal/app"
	"messagehub/internal/testsupport/pgtest"
)

func TestMigrateAndRuntimeAgainstDatabase(t *testing.T) {
	dsn := pgtest.Start(t)

	t.Run("migrate up on a migrated database applies nothing", func(t *testing.T) {
		code, stdout, stderr := runCLI(map[string]string{app.EnvDatabaseURL: dsn}, "migrate", "up")
		if code != exitOK || !strings.Contains(stdout, "applied 0 migrations") {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})

	t.Run("build runtime connects and loads the config", func(t *testing.T) {
		// Arrange
		env := map[string]string{app.EnvConfigPath: exampleConfig, app.EnvDatabaseURL: dsn}
		for _, n := range requiredEnv(t) {
			env[n] = fakeHash
		}
		settings, err := app.LoadSettings(lookupFrom(env))
		if err != nil {
			t.Fatalf("LoadSettings: %v", err)
		}
		log := slog.New(slog.NewJSONHandler(io.Discard, nil))

		// Act
		rt, closeRuntime, err := buildRuntime(context.Background(), settings, lookupFrom(env), log)

		// Assert
		if err != nil {
			t.Fatalf("buildRuntime: %v", err)
		}
		defer closeRuntime()
		if err := rt.Pool.Ping(context.Background()); err != nil {
			t.Errorf("pool ping: %v", err)
		}
		if rt.Config.Current().Hash() == "" {
			t.Error("runtime has no config")
		}
	})
}
