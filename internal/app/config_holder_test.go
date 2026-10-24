package app

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/secrets"
)

const exampleConfig = "../../config/examples/hub.example.yaml"

func readExample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	return string(b)
}

// writeConfig writes content to a fresh file in a temp dir and returns its path.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "hub.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// fakeHash is an obviously fake value that is also a well-formed key hash,
// so it satisfies both secret presence and principal hash format checks.
var fakeHash = strings.Repeat("a", 64)

// fakeEnv sets every variable cfg references to fakeHash.
func fakeEnv(cfg *config.Config) map[string]string {
	env := make(map[string]string)
	for _, name := range cfg.RequiredEnv() {
		env[name] = fakeHash
	}
	return env
}

// fullResolver sets every variable the example config needs to a fake value.
func fullResolver(t *testing.T) secrets.Resolver {
	t.Helper()
	cfg, err := config.Load(exampleConfig)
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	return secrets.NewEnvResolver(lookupFrom(fakeEnv(cfg)))
}

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestNewConfigHolderLoadsConfig(t *testing.T) {
	path := writeConfig(t, t.TempDir(), readExample(t))

	h, err := NewConfigHolder(path, fullResolver(t), discardLogger())

	if err != nil {
		t.Fatalf("NewConfigHolder error: %v", err)
	}
	if h.Current() == nil || h.Current().Hash() == "" {
		t.Fatal("Current() has no loaded config")
	}
}

func TestNewConfigHolderRejectsInvalidStart(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		resolver func(t *testing.T) secrets.Resolver
		check    func(error) bool
	}{
		{
			name:     "invalid config",
			content:  "version: 1\n",
			resolver: fullResolver,
			check:    func(err error) bool { return errors.As(err, new(*domain.ValidationError)) },
		},
		{
			name:     "missing environment variables",
			content:  "",
			resolver: func(*testing.T) secrets.Resolver { return secrets.NewEnvResolver(lookupFrom(nil)) },
			check:    func(err error) bool { return errors.As(err, new(*secrets.MissingError)) },
		},
		{
			name:    "malformed principal key hash",
			content: "",
			resolver: func(*testing.T) secrets.Resolver {
				return secrets.NewEnvResolver(func(string) (string, bool) { return "not-a-hash", true })
			},
			check: func(err error) bool { return errors.As(err, new(*domain.ValidationError)) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.content
			if content == "" {
				content = readExample(t)
			}
			path := writeConfig(t, t.TempDir(), content)

			_, err := NewConfigHolder(path, tt.resolver(t), discardLogger())

			if !tt.check(err) {
				t.Fatalf("NewConfigHolder error = %v, want the expected type", err)
			}
		})
	}
}

func TestReloadSwitchesToNewConfig(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	original := readExample(t)
	path := writeConfig(t, dir, original)
	var logs bytes.Buffer
	h, err := NewConfigHolder(path, fullResolver(t), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewConfigHolder error: %v", err)
	}
	oldHash := h.Current().Hash()
	writeConfig(t, dir, strings.Replace(original, "max_in_flight: 20", "max_in_flight: 21", 1))

	// Act
	err = h.Reload()

	// Assert
	if err != nil {
		t.Fatalf("Reload error: %v", err)
	}
	newHash := h.Current().Hash()
	if newHash == oldHash {
		t.Fatal("Reload kept the old config hash")
	}
	if !strings.Contains(logs.String(), oldHash) || !strings.Contains(logs.String(), newHash) {
		t.Errorf("reload log %q does not record both hashes", logs.String())
	}
}

func TestReloadFailureKeepsOldConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"invalid config", "version: 1\n"},
		{"new variable missing", "ADD_SECRET"},
		{"file removed", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			dir := t.TempDir()
			original := readExample(t)
			path := writeConfig(t, dir, original)
			var logs bytes.Buffer
			h, err := NewConfigHolder(path, fullResolver(t), slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatalf("NewConfigHolder error: %v", err)
			}
			before := h.Current()
			switch tt.content {
			case "":
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove config: %v", err)
				}
			case "ADD_SECRET":
				writeConfig(t, dir, strings.Replace(original,
					"INVENTORY_TOKEN: env:INVENTORY_API_TOKEN",
					"INVENTORY_TOKEN: env:INVENTORY_API_TOKEN_V2", 1))
			default:
				writeConfig(t, dir, tt.content)
			}

			// Act
			err = h.Reload()

			// Assert
			if err == nil {
				t.Fatal("Reload succeeded, want error")
			}
			if h.Current() != before {
				t.Error("Reload replaced the config after a failure")
			}
			if !strings.Contains(logs.String(), `"level":"ERROR"`) {
				t.Errorf("failure not logged at error level: %q", logs.String())
			}
			if strings.Contains(logs.String(), fakeHash) {
				t.Error("reload log leaks a secret value")
			}
		})
	}
}

func TestConfigHolderConcurrentAccess(t *testing.T) {
	path := writeConfig(t, t.TempDir(), readExample(t))
	h, err := NewConfigHolder(path, fullResolver(t), discardLogger())
	if err != nil {
		t.Fatalf("NewConfigHolder error: %v", err)
	}
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if h.Current() == nil {
					t.Error("Current() returned nil")
				}
			}
		}()
		go func() {
			defer wg.Done()
			if err := h.Reload(); err != nil {
				t.Errorf("Reload error: %v", err)
			}
		}()
	}
	wg.Wait()
}
