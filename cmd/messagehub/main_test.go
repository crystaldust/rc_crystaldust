package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"messagehub/internal/apikey"
	"messagehub/internal/app"
	"messagehub/internal/config"
)

const exampleConfig = "../../config/examples/hub.example.yaml"

// fakeHash is an obviously fake secret value that is also a well-formed key
// hash, so it passes the principal hash format check.
var fakeHash = strings.Repeat("a", 64)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

// runCLI runs the command and returns its exit code, stdout and stderr.
func runCLI(env map[string]string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, lookupFrom(env), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func requiredEnv(t *testing.T) []string {
	t.Helper()
	cfg, err := config.Load(exampleConfig)
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	return cfg.RequiredEnv()
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"deploy"}},
		{"config without subcommand", []string{"config"}},
		{"unknown config subcommand", []string{"config", "lint"}},
		{"migrate without subcommand", []string{"migrate"}},
		{"unknown migrate subcommand", []string{"migrate", "down"}},
		{"unknown flag", []string{"config", "check", "--strict"}},
		{"unknown role", []string{"serve", "--role=admin"}},
		{"unknown serve flag", []string{"serve", "--port=1"}},
		{"unknown migrate flag", []string{"migrate", "up", "--steps=1"}},
		{"keygen with arguments", []string{"keygen", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := runCLI(nil, tt.args...)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, exitUsage, stderr)
			}
			if stderr == "" {
				t.Error("no usage message on stderr")
			}
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, stdout, _ := runCLI(nil, args...)
		if code != exitOK || !strings.Contains(stdout, "messagehub") {
			t.Errorf("%v: exit %d, stdout %q", args, code, stdout)
		}
	}
	// A subcommand's -h prints its flags and exits 0.
	if code, _, stderr := runCLI(nil, "config", "check", "-h"); code != exitOK || !strings.Contains(stderr, "-config") {
		t.Errorf("config check -h: exit %d, stderr %q", code, stderr)
	}
}

func TestConfigCheck(t *testing.T) {
	names := requiredEnv(t)
	allSet := make(map[string]string)
	for _, n := range names {
		allSet[n] = fakeHash
	}

	t.Run("missing variables exit 2 and are all listed", func(t *testing.T) {
		code, _, stderr := runCLI(nil, "config", "check", "--config", exampleConfig)
		if code != exitMissingEnv {
			t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitMissingEnv, stderr)
		}
		for _, n := range names {
			if !strings.Contains(stderr, n) {
				t.Errorf("stderr does not list %s", n)
			}
		}
	})

	t.Run("all variables set exits 0 without printing values", func(t *testing.T) {
		code, stdout, stderr := runCLI(allSet, "config", "check", "--config", exampleConfig)
		if code != exitOK {
			t.Fatalf("exit code = %d, stderr %q", code, stderr)
		}
		if !strings.Contains(stdout, "config_hash: ") {
			t.Errorf("stdout %q has no config_hash", stdout)
		}
		if strings.Contains(stdout+stderr, fakeHash) {
			t.Error("output leaks a secret value")
		}
	})

	t.Run("config path from HUB_CONFIG_PATH", func(t *testing.T) {
		env := map[string]string{app.EnvConfigPath: exampleConfig}
		for k, v := range allSet {
			env[k] = v
		}
		if code, _, stderr := runCLI(env, "config", "check"); code != exitOK {
			t.Fatalf("exit code = %d, stderr %q", code, stderr)
		}
	})

	t.Run("invalid config exits 1", func(t *testing.T) {
		path := t.TempDir() + "/hub.yaml"
		if err := os.WriteFile(path, []byte("version: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := runCLI(nil, "config", "check", "--config", path); code != exitError {
			t.Fatalf("exit code = %d, want %d", code, exitError)
		}
	})

	t.Run("malformed principal key hash exits 1", func(t *testing.T) {
		env := map[string]string{}
		for k := range allSet {
			env[k] = "not-a-hash"
		}
		code, _, stderr := runCLI(env, "config", "check", "--config", exampleConfig)
		if code != exitError || !strings.Contains(stderr, "api_key_sha256_ref") {
			t.Fatalf("exit %d, stderr %q; want %d naming the principal field", code, stderr, exitError)
		}
	})

	t.Run("no config path exits usage", func(t *testing.T) {
		if code, _, _ := runCLI(nil, "config", "check"); code != exitUsage {
			t.Fatalf("exit code = %d, want %d", code, exitUsage)
		}
	})
}

func TestConfigCheckPrintsWarnings(t *testing.T) {
	// Arrange: a second route for the same event_type and destination.
	b, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Routes()[0]
	extra := "\n  - id: duplicate-route\n    event_type: " + r.EventType +
		"\n    destinations: [" + r.Destinations[0] + "]\n"
	path := t.TempDir() + "/hub.yaml"
	if err := os.WriteFile(path, append(b, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}
	env := make(map[string]string)
	for _, n := range cfg.RequiredEnv() {
		env[n] = fakeHash
	}

	// Act
	code, stdout, stderr := runCLI(env, "config", "check", "--config", path)

	// Assert
	if code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "warning: ") {
		t.Errorf("stdout %q has no warning", stdout)
	}
}

func TestConfigEnv(t *testing.T) {
	code, stdout, stderr := runCLI(nil, "config", "env", "--config", exampleConfig)

	if code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, stderr)
	}
	got := strings.Split(strings.TrimSpace(stdout), "\n")
	want := requiredEnv(t)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("config env = %v, want %v", got, want)
	}
}

func TestConfigEnvInvalidConfig(t *testing.T) {
	if code, _, _ := runCLI(nil, "config", "env", "--config", "does-not-exist.yaml"); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
}

func TestKeygen(t *testing.T) {
	code, stdout, stderr := runCLI(nil, "keygen")

	if code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, stderr)
	}
	var key, hash string
	for _, line := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(line, "key: "); ok {
			key = v
		}
		if v, ok := strings.CutPrefix(line, "sha256: "); ok {
			hash = v
		}
	}
	if !strings.HasPrefix(key, apikey.Prefix) || len(key) != apikey.KeyLength {
		t.Fatalf("key line %q is malformed", key)
	}
	sum := sha256.Sum256([]byte(key))
	if hash != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %s, want hash of the key", hash)
	}
	if !strings.Contains(stderr, "only once") {
		t.Errorf("stderr %q does not warn the key is shown once", stderr)
	}
}

func TestMigrateRequiresDatabaseURL(t *testing.T) {
	code, _, stderr := runCLI(nil, "migrate", "up")
	if code != exitError || !strings.Contains(stderr, app.EnvDatabaseURL) {
		t.Fatalf("exit %d, stderr %q; want %d naming %s", code, stderr, exitError, app.EnvDatabaseURL)
	}
}

func TestMigrateReportsDatabaseFailure(t *testing.T) {
	env := map[string]string{app.EnvDatabaseURL: "postgres://hub:abc123@127.0.0.1:1/hub?connect_timeout=1"}
	code, _, stderr := runCLI(env, "migrate", "up")
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if strings.Contains(stderr, "abc123") {
		t.Errorf("stderr %q leaks the database password", stderr)
	}
}

func TestServeStartupFailures(t *testing.T) {
	names := requiredEnv(t)
	base := map[string]string{
		app.EnvConfigPath:  exampleConfig,
		app.EnvDatabaseURL: "postgres://hub:abc123@127.0.0.1:1/hub?connect_timeout=1",
	}
	for _, n := range names {
		base[n] = fakeHash
	}
	without := func(key string) map[string]string {
		out := make(map[string]string, len(base))
		for k, v := range base {
			if k != key {
				out[k] = v
			}
		}
		return out
	}
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"invalid settings", without(app.EnvConfigPath)},
		{"missing database url", without(app.EnvDatabaseURL)},
		{"missing secret", without(names[0])},
		{"database unreachable", base},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(tt.env, "serve", "--role=api")
			if code != exitError {
				t.Fatalf("exit code = %d, want %d", code, exitError)
			}
			if strings.Contains(stdout+stderr, "abc123") || strings.Contains(stdout+stderr, fakeHash) {
				t.Error("output leaks a secret")
			}
		})
	}
}

func TestRunRoles(t *testing.T) {
	errBoom := errors.New("boom")
	blockUntilDone := func(ctx context.Context, _ *app.Runtime) error {
		<-ctx.Done()
		return ctx.Err()
	}
	failNow := func(context.Context, *app.Runtime) error { return errBoom }
	neverStop := func(context.Context, *app.Runtime) error { select {} }

	t.Run("first failure stops the other roles", func(t *testing.T) {
		err := runRoles(context.Background(), nil, []roleFunc{failNow, blockUntilDone}, time.Second)
		if !errors.Is(err, errBoom) {
			t.Fatalf("runRoles error = %v, want boom", err)
		}
	})

	t.Run("shutdown signal returns cleanly", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := runRoles(ctx, nil, []roleFunc{blockUntilDone, blockUntilDone}, time.Second); err != nil {
			t.Fatalf("runRoles error = %v, want nil", err)
		}
	})

	t.Run("role ignoring shutdown times out", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := runRoles(ctx, nil, []roleFunc{blockUntilDone, neverStop}, 10*time.Millisecond)
		if !errors.Is(err, errShutdownTimeout) {
			t.Fatalf("runRoles error = %v, want errShutdownTimeout", err)
		}
	})

	t.Run("role stopping on its own is a failure", func(t *testing.T) {
		stopNil := func(context.Context, *app.Runtime) error { return nil }
		stopCanceled := func(context.Context, *app.Runtime) error { return context.Canceled }
		for _, fn := range []roleFunc{stopNil, stopCanceled} {
			err := runRoles(context.Background(), nil, []roleFunc{fn, blockUntilDone}, time.Second)
			if !errors.Is(err, errRoleStopped) {
				t.Errorf("runRoles error = %v, want errRoleStopped", err)
			}
		}
	})

	t.Run("every role name has roles", func(t *testing.T) {
		for _, name := range []string{roleAPI, roleWorker, roleAll} {
			if len(roles[name]) == 0 {
				t.Errorf("role %q has no role functions", name)
			}
		}
	})
}

type countingReloader struct{ calls chan struct{} }

func (r countingReloader) Reload() error {
	r.calls <- struct{}{}
	return errors.New("bad config")
}

func TestWatchReload(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	hup := make(chan os.Signal, 1)
	r := countingReloader{calls: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		watchReload(ctx, r, hup)
		close(done)
	}()

	// Act: a failing reload must not stop the watcher.
	hup <- os.Interrupt
	<-r.calls
	hup <- os.Interrupt
	<-r.calls
	cancel()

	// Assert
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchReload did not return after cancel")
	}
}
