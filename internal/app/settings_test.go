package app

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"messagehub/internal/domain"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func TestLoadSettingsDefaults(t *testing.T) {
	// Arrange
	env := map[string]string{
		EnvConfigPath:  "hub.yaml",
		EnvDatabaseURL: "postgres://hub:abc123@db:5432/hub",
	}

	// Act
	s, err := LoadSettings(lookupFrom(env))

	// Assert
	if err != nil {
		t.Fatalf("LoadSettings error: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"config path", s.ConfigPath, "hub.yaml"},
		{"database url", s.DatabaseURL, env[EnvDatabaseURL]},
		{"listen url defaults to database url", s.DatabaseListenURL, env[EnvDatabaseURL]},
		{"http addr", s.HTTPAddr, DefaultHTTPAddr},
		{"worker concurrency", s.WorkerConcurrency, DefaultWorkerConcurrency},
		{"log level", s.LogLevel, DefaultLogLevel},
		{"shutdown timeout", s.ShutdownTimeout, DefaultShutdownTimeout},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if s.WorkerID == "" {
		t.Error("worker id default is empty")
	}
}

func TestLoadSettingsExplicitValues(t *testing.T) {
	env := map[string]string{
		EnvConfigPath:        "hub.yaml",
		EnvDatabaseURL:       "postgres://hub@pgbouncer/hub",
		EnvDatabaseListenURL: "postgres://hub@db/hub",
		EnvHTTPAddr:          "127.0.0.1:9000",
		EnvWorkerID:          "worker-a",
		EnvWorkerConcurrency: "8",
		EnvLogLevel:          "debug",
		EnvShutdownTimeout:   "5s",
	}

	s, err := LoadSettings(lookupFrom(env))

	if err != nil {
		t.Fatalf("LoadSettings error: %v", err)
	}
	want := Settings{
		ConfigPath:        "hub.yaml",
		DatabaseURL:       "postgres://hub@pgbouncer/hub",
		DatabaseListenURL: "postgres://hub@db/hub",
		HTTPAddr:          "127.0.0.1:9000",
		WorkerID:          "worker-a",
		WorkerConcurrency: 8,
		LogLevel:          "debug",
		ShutdownTimeout:   5 * time.Second,
	}
	if s != want {
		t.Errorf("settings = %+v, want %+v", s, want)
	}
}

func TestLoadSettingsAggregatesProblems(t *testing.T) {
	// Arrange: every optional value is invalid and the config path is missing.
	env := map[string]string{
		EnvWorkerConcurrency: "0",
		EnvLogLevel:          "verbose",
		EnvShutdownTimeout:   "abc",
	}

	// Act
	_, err := LoadSettings(lookupFrom(env))

	// Assert
	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *domain.ValidationError", err)
	}
	for _, field := range []string{EnvConfigPath, EnvWorkerConcurrency, EnvLogLevel, EnvShutdownTimeout} {
		if !verr.HasField(field) {
			t.Errorf("problems %v do not mention %s", verr.Problems, field)
		}
	}
}

func TestLoadSettingsRejectsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{"concurrency above maximum", EnvWorkerConcurrency, "4097"},
		{"concurrency not a number", EnvWorkerConcurrency, "many"},
		{"zero shutdown timeout", EnvShutdownTimeout, "0s"},
		{"negative shutdown timeout", EnvShutdownTimeout, "-1s"},
		{"empty config path", EnvConfigPath, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{EnvConfigPath: "hub.yaml", tt.field: tt.value}

			_, err := LoadSettings(lookupFrom(env))

			var verr *domain.ValidationError
			if !errors.As(err, &verr) || !verr.HasField(tt.field) {
				t.Fatalf("error = %v, want a problem on %s", err, tt.field)
			}
		})
	}
}

func TestLoadSettingsAcceptsConcurrencyBounds(t *testing.T) {
	for _, value := range []string{"1", "4096"} {
		t.Run(value, func(t *testing.T) {
			env := map[string]string{EnvConfigPath: "hub.yaml", EnvWorkerConcurrency: value}
			if _, err := LoadSettings(lookupFrom(env)); err != nil {
				t.Fatalf("LoadSettings error: %v", err)
			}
		})
	}
}

func TestRequireDatabase(t *testing.T) {
	t.Run("missing database url is reported", func(t *testing.T) {
		s := Settings{ConfigPath: "hub.yaml"}
		var verr *domain.ValidationError
		if err := s.RequireDatabase(); !errors.As(err, &verr) || !verr.HasField(EnvDatabaseURL) {
			t.Fatalf("RequireDatabase() = %v, want a problem on %s", err, EnvDatabaseURL)
		}
	})
	t.Run("present database url passes", func(t *testing.T) {
		s := Settings{DatabaseURL: "postgres://hub@db/hub"}
		if err := s.RequireDatabase(); err != nil {
			t.Fatalf("RequireDatabase() = %v", err)
		}
	})
}

func TestSettingsStringRedactsPasswords(t *testing.T) {
	// Arrange
	s := Settings{
		ConfigPath:        "hub.yaml",
		DatabaseURL:       "postgres://hub:abc123@db:5432/hub?sslmode=disable",
		DatabaseListenURL: "postgres://hub:fake-listen-pw@db:5432/hub",
		WorkerConcurrency: 4,
	}

	// Act
	out := s.String()

	// Assert
	for _, secret := range []string{"abc123", "fake-listen-pw"} {
		if strings.Contains(out, secret) {
			t.Errorf("String() = %q leaks %q", out, secret)
		}
	}
	if !strings.Contains(out, "hub@db:5432") {
		t.Errorf("String() = %q, want the user and host kept", out)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"url with password", "postgres://hub:abc123@db/hub", "postgres://hub@db/hub"},
		{"url without password", "postgres://hub@db/hub", "postgres://hub@db/hub"},
		{"password in query", "postgres://hub@db/hub?password=abc123&sslpassword=abc456&sslmode=disable", "postgres://hub@db/hub"},
		{"empty", "", ""},
		{"keyword form with password", "host=db user=hub password=abc123", redactedDSN},
		{"unparseable", "postgres://hub:abc123@db:port/hub", redactedDSN},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactDSN(tt.dsn); got != tt.want {
				t.Errorf("RedactDSN(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestLoadSettingsEmptyValuesUseDefaults(t *testing.T) {
	env := map[string]string{
		EnvConfigPath:        "hub.yaml",
		EnvDatabaseURL:       "postgres://hub@db/hub",
		EnvDatabaseListenURL: "",
		EnvHTTPAddr:          "",
		EnvWorkerConcurrency: "",
	}

	s, err := LoadSettings(lookupFrom(env))

	if err != nil {
		t.Fatalf("LoadSettings error: %v", err)
	}
	if s.HTTPAddr != DefaultHTTPAddr || s.DatabaseListenURL != env[EnvDatabaseURL] || s.WorkerConcurrency != DefaultWorkerConcurrency {
		t.Errorf("settings = %+v, want empty values replaced by defaults", s)
	}
}

func TestSettingsLogValueRedacts(t *testing.T) {
	// Arrange
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	s := Settings{DatabaseURL: "postgres://hub:abc123@db/hub"}

	// Act
	log.Info("x", slog.Any("settings", s))

	// Assert
	if strings.Contains(buf.String(), "abc123") {
		t.Errorf("log %q leaks the password", buf.String())
	}
}

func TestParseLogLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if _, err := ParseLogLevel(level); err != nil {
			t.Errorf("ParseLogLevel(%q) error: %v", level, err)
		}
	}
	if _, err := ParseLogLevel("verbose"); err == nil {
		t.Error("ParseLogLevel(verbose) succeeded, want error")
	}
}
