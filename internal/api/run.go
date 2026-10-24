// Package api serves the HTTP API of api/openapi.yaml: ingest, queries,
// operator actions and health checks (docs/design/domain-model.md §3, §9,
// §12, §13). It depends on app (Runtime), store, routing, apikey and
// domain; nothing depends on it except cmd/messagehub.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"messagehub/internal/app"
	"messagehub/internal/store"
)

// Run serves the API on rt.Settings.HTTPAddr until ctx is cancelled, then
// shuts down gracefully within rt.Settings.ShutdownTimeout.
func Run(ctx context.Context, rt *app.Runtime) error {
	h := newHandler(deps{
		config:   rt.Config.Current,
		resolver: rt.Secrets,
		store:    store.New(rt.Pool, rt.Config.Current),
		ready:    rt.Pool.Ping,
		log:      rt.Log,
		now:      rt.Now,
	})
	return serve(ctx, newHTTPServer(rt.Settings.HTTPAddr, h), rt.Settings.ShutdownTimeout, rt.Log)
}

// serve runs srv until ctx is cancelled or the listener fails.
func serve(ctx context.Context, srv *http.Server, shutdownTimeout time.Duration, log *slog.Logger) error {
	failed := make(chan error, 1)
	go func() {
		log.Info("api.listening", slog.String("addr", srv.Addr))
		failed <- srv.ListenAndServe()
	}()
	select {
	case err := <-failed:
		return fmt.Errorf("serve http on %s: %w", srv.Addr, err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Requests still running after the timeout are cut off so the
		// process can exit; Close's own error adds nothing to err.
		_ = srv.Close()
		<-failed
		return fmt.Errorf("shut down http server: %w", err)
	}
	if err := <-failed; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http on %s: %w", srv.Addr, err)
	}
	return nil
}
