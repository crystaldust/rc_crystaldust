package app

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"messagehub/internal/config"
	"messagehub/internal/secrets"
)

// ConfigHolder holds the current configuration and swaps it atomically on
// Reload. Callers must call Current for every request or delivery and never
// keep the returned *config.Config, so a reload takes effect at once (the
// stale-cache problem PoC S2 found in Svix).
type ConfigHolder struct {
	cur      atomic.Pointer[config.Config]
	reloadMu sync.Mutex // serialises Reload so the logged old hash is accurate
	path     string
	opts     []config.Option
	resolver secrets.Resolver
	log      *slog.Logger
}

// NewConfigHolder loads the configuration at path and checks that every
// secret it references is set. Any failure is returned so the process
// refuses to start.
func NewConfigHolder(path string, r secrets.Resolver, log *slog.Logger, opts ...config.Option) (*ConfigHolder, error) {
	h := &ConfigHolder{path: path, opts: opts, resolver: r, log: log}
	cfg, err := h.load()
	if err != nil {
		return nil, err
	}
	h.cur.Store(cfg)
	return h, nil
}

// Current returns the configuration in effect now.
func (h *ConfigHolder) Current() *config.Config { return h.cur.Load() }

// Reload loads the file again. On success the new configuration replaces
// the old one; on failure the old one stays in effect and the error is
// logged and returned.
func (h *ConfigHolder) Reload() error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	old := h.cur.Load()
	cfg, err := h.load()
	if err != nil {
		h.log.Error("config.reload_failed",
			slog.String("config_path", h.path),
			slog.String("config_hash", old.Hash()),
			slog.String("error", err.Error()))
		return err
	}
	h.cur.Store(cfg)
	h.log.Info("config.reloaded",
		slog.String("config_path", h.path),
		slog.String("old_config_hash", old.Hash()),
		slog.String("new_config_hash", cfg.Hash()))
	return nil
}

func (h *ConfigHolder) load() (*config.Config, error) {
	cfg, err := config.Load(h.path, h.opts...)
	if err != nil {
		return nil, err
	}
	if err := secrets.CheckRequired(cfg, h.resolver); err != nil {
		return nil, fmt.Errorf("config %q: %w", h.path, err)
	}
	if err := CheckPrincipalKeys(cfg, h.resolver); err != nil {
		return nil, fmt.Errorf("config %q: %w", h.path, err)
	}
	return cfg, nil
}
