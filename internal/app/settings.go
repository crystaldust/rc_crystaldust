// Package app holds the process-level runtime shared by every role: process
// settings from HUB_* environment variables, the hot-reloadable config holder
// and the Runtime handed to api.Run and worker.Run. It depends on domain,
// config and secrets; the role packages depend on it, never the reverse.
package app

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"messagehub/internal/domain"
)

// Process environment variables (runtime-foundation plan, Task 1).
const (
	EnvConfigPath        = "HUB_CONFIG_PATH"
	EnvDatabaseURL       = "HUB_DATABASE_URL"
	EnvDatabaseListenURL = "HUB_DATABASE_LISTEN_URL"
	EnvHTTPAddr          = "HUB_HTTP_ADDR"
	EnvWorkerID          = "HUB_WORKER_ID"
	EnvWorkerConcurrency = "HUB_WORKER_CONCURRENCY"
	EnvLogLevel          = "HUB_LOG_LEVEL"
	EnvShutdownTimeout   = "HUB_SHUTDOWN_TIMEOUT"
)

// Defaults and limits for optional settings.
const (
	DefaultHTTPAddr          = ":8080"
	DefaultWorkerConcurrency = 64
	MinWorkerConcurrency     = 1
	MaxWorkerConcurrency     = 4096
	DefaultLogLevel          = "info"
	DefaultShutdownTimeout   = 30 * time.Second
)

// redactedDSN replaces a connection string that cannot be safely shown.
const redactedDSN = "[redacted]"

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// Settings are the process parameters. DatabaseURL and DatabaseListenURL
// are secrets (they may carry a password): log them only via String.
type Settings struct {
	ConfigPath        string
	DatabaseURL       string
	DatabaseListenURL string
	HTTPAddr          string
	WorkerID          string
	WorkerConcurrency int
	LogLevel          string
	ShutdownTimeout   time.Duration
}

// LoadSettings reads settings through lookup (os.LookupEnv in production).
// Every problem is reported at once as *domain.ValidationError, with the
// environment variable name as the field.
func LoadSettings(lookup func(string) (string, bool)) (Settings, error) {
	// An empty value counts as unset, as in secrets.EnvResolver: manifests
	// often render optional variables as "".
	get := func(name, def string) string {
		if v, ok := lookup(name); ok && v != "" {
			return v
		}
		return def
	}
	var problems []domain.FieldError
	add := func(field, reason string) {
		problems = append(problems, domain.FieldError{Field: field, Reason: reason})
	}

	s := Settings{
		ConfigPath:  get(EnvConfigPath, ""),
		DatabaseURL: get(EnvDatabaseURL, ""),
		HTTPAddr:    get(EnvHTTPAddr, DefaultHTTPAddr),
		WorkerID:    get(EnvWorkerID, defaultWorkerID()),
		LogLevel:    get(EnvLogLevel, DefaultLogLevel),
	}
	s.DatabaseListenURL = get(EnvDatabaseListenURL, s.DatabaseURL)
	if s.ConfigPath == "" {
		add(EnvConfigPath, "is required")
	}
	if _, err := ParseLogLevel(s.LogLevel); err != nil {
		add(EnvLogLevel, err.Error())
	}
	concurrency, err := parseConcurrency(get(EnvWorkerConcurrency, strconv.Itoa(DefaultWorkerConcurrency)))
	if err != nil {
		add(EnvWorkerConcurrency, err.Error())
	}
	s.WorkerConcurrency = concurrency
	timeout, err := parseShutdownTimeout(get(EnvShutdownTimeout, DefaultShutdownTimeout.String()))
	if err != nil {
		add(EnvShutdownTimeout, err.Error())
	}
	s.ShutdownTimeout = timeout

	if len(problems) > 0 {
		return Settings{}, &domain.ValidationError{Problems: problems}
	}
	return s, nil
}

// RequireDatabase reports a missing HUB_DATABASE_URL; only the commands
// that touch the database (serve, migrate) call it.
func (s Settings) RequireDatabase() error {
	if s.DatabaseURL == "" {
		return &domain.ValidationError{Problems: []domain.FieldError{{Field: EnvDatabaseURL, Reason: "is required"}}}
	}
	return nil
}

// String renders the settings for logs with connection string passwords removed.
func (s Settings) String() string {
	return fmt.Sprintf(
		"config_path=%s database_url=%s database_listen_url=%s http_addr=%s worker_id=%s worker_concurrency=%d log_level=%s shutdown_timeout=%s",
		s.ConfigPath, RedactDSN(s.DatabaseURL), RedactDSN(s.DatabaseListenURL), s.HTTPAddr,
		s.WorkerID, s.WorkerConcurrency, s.LogLevel, s.ShutdownTimeout)
}

// LogValue makes slog render Settings through String, so logging the
// struct with slog.Any never emits a connection string password.
func (s Settings) LogValue() slog.Value { return slog.StringValue(s.String()) }

// ParseLogLevel maps debug / info / warn / error to a slog level.
func ParseLogLevel(level string) (slog.Level, error) {
	lvl, ok := logLevels[level]
	if !ok {
		return 0, fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}
	return lvl, nil
}

// RedactDSN drops the password and the whole query string from a
// postgres:// URL (query parameters such as password= or sslpassword= can
// carry credentials). Anything it cannot parse as such a URL (including the
// keyword/value form) is fully redacted, because it may embed a password in
// an unknown position.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return redactedDSN
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

func parseConcurrency(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < MinWorkerConcurrency || n > MaxWorkerConcurrency {
		return 0, fmt.Errorf("worker concurrency %q: want an integer %d-%d", v, MinWorkerConcurrency, MaxWorkerConcurrency)
	}
	return n, nil
}

func parseShutdownTimeout(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("shutdown timeout %q: want a positive duration such as 30s", v)
	}
	return d, nil
}

// defaultWorkerID is <hostname>-<pid>, unique per process on a host.
func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}
