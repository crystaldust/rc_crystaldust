package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"messagehub/internal/api"
	"messagehub/internal/app"
	"messagehub/internal/config"
	"messagehub/internal/platform/pg"
	"messagehub/internal/secrets"
	"messagehub/internal/worker"
)

const (
	roleAPI    = "api"
	roleWorker = "worker"
	roleAll    = "all"
)

// startupTimeout bounds connecting to the database at start.
const startupTimeout = 30 * time.Second

// roleFunc runs one role until its context is cancelled.
type roleFunc func(ctx context.Context, rt *app.Runtime) error

var roles = map[string][]roleFunc{
	roleAPI:    {api.Run},
	roleWorker: {worker.Run},
	roleAll:    {api.Run, worker.Run},
}

var (
	errShutdownTimeout = errors.New("roles did not stop within the shutdown timeout")
	errRoleStopped     = errors.New("role stopped without a shutdown request")
)

// configOptions are the config loading options used by every command.
// Phase 4 adds the template validator here.
func configOptions() []config.Option {
	return nil
}

func (c cli) serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	role := fs.String("role", roleAll, "role to run: api, worker or all")
	if code, ok := c.parseFlags(fs, args); !ok {
		return code
	}
	fns, ok := roles[*role]
	if !ok {
		return c.usageError(fmt.Sprintf("unknown role %q: want api, worker or all", *role))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := c.runServe(ctx, *role, fns); err != nil {
		return c.fail(exitError, err)
	}
	return exitOK
}

func (c cli) runServe(ctx context.Context, role string, fns []roleFunc) error {
	settings, err := app.LoadSettings(c.lookup)
	if err != nil {
		return err
	}
	if err := settings.RequireDatabase(); err != nil {
		return err
	}
	log, err := newLogger(c.stdout, settings.LogLevel)
	if err != nil {
		return err
	}
	// Catch SIGHUP before the (possibly slow) startup, so an early reload
	// signal is queued instead of killing the process.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	rt, closeRuntime, err := buildRuntime(ctx, settings, c.lookup, log)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchReload(ctx, rt.Config, hup)

	log.Info("hub.starting",
		slog.String("role", role),
		slog.String("settings", settings.String()),
		slog.String("config_hash", rt.Config.Current().Hash()))
	err = runRoles(ctx, rt, fns, settings.ShutdownTimeout)
	// After a shutdown timeout a role may still hold pool connections and
	// pool.Close would wait for them; skip it so the process exits on time.
	if !errors.Is(err, errShutdownTimeout) {
		closeRuntime()
	}
	log.Info("hub.stopped", slog.String("role", role))
	return err
}

func newLogger(w io.Writer, level string) (*slog.Logger, error) {
	lvl, err := app.ParseLogLevel(level)
	if err != nil {
		return nil, err
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}

// buildRuntime loads the config, checks its secrets and opens the database
// pool. The returned func releases what was opened.
func buildRuntime(ctx context.Context, s app.Settings, lookup func(string) (string, bool), log *slog.Logger) (*app.Runtime, func(), error) {
	resolver := secrets.NewEnvResolver(lookup)
	holder, err := app.NewConfigHolder(s.ConfigPath, resolver, log, configOptions()...)
	if err != nil {
		return nil, nil, err
	}
	dbCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	pool, err := pg.NewPool(dbCtx, s.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	rt := &app.Runtime{
		Settings: s,
		Config:   holder,
		Secrets:  resolver,
		Pool:     pool,
		Log:      log,
		Now:      time.Now,
	}
	return rt, pool.Close, nil
}

type reloader interface {
	Reload() error
}

// watchReload reloads the config on every SIGHUP until ctx is cancelled.
func watchReload(ctx context.Context, r reloader, hup <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			// A failed reload keeps the old config and is already logged by
			// ConfigHolder; the process keeps serving.
			_ = r.Reload()
		}
	}
}

// runRoles runs every role concurrently. It returns when the first role
// returns or ctx is cancelled (shutdown signal): the other roles are then
// cancelled and given timeout to return. A role that returns before any
// shutdown request is a failure even when it returns nil or
// context.Canceled; after the request (or after the first role stopped),
// context.Canceled is a clean stop.
func runRoles(ctx context.Context, rt *app.Runtime, fns []roleFunc, timeout time.Duration) error {
	roleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, len(fns))
	for _, fn := range fns {
		go func() { results <- fn(roleCtx, rt) }()
	}

	var errs []error
	remaining := len(fns)
	select {
	case err := <-results:
		remaining--
		if ctx.Err() == nil && (err == nil || errors.Is(err, context.Canceled)) {
			// %v, not %w: the result must not match context.Canceled.
			err = fmt.Errorf("%w (returned %v)", errRoleStopped, err)
		}
		errs = append(errs, err)
	case <-ctx.Done():
	}
	cancel()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for ; remaining > 0; remaining-- {
		select {
		case err := <-results:
			errs = append(errs, err)
		case <-deadline.C:
			return errors.Join(append(cleanErrors(errs), errShutdownTimeout)...)
		}
	}
	return errors.Join(cleanErrors(errs)...)
}

// cleanErrors drops nils and the cancellations caused by shutdown.
func cleanErrors(errs []error) []error {
	out := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			out = append(out, err)
		}
	}
	return out
}
