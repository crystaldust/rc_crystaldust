// Package api serves the HTTP API (ingest and query). Phase 3 owns this
// package and replaces the placeholder Run.
package api

import (
	"context"

	"messagehub/internal/app"
)

// Run serves the API until ctx is cancelled.
func Run(ctx context.Context, rt *app.Runtime) error {
	return app.ErrRoleNotImplemented
}
