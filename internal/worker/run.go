// Package worker claims and delivers deliveries. Phase 4 owns this package
// and replaces the placeholder Run.
package worker

import (
	"context"

	"messagehub/internal/app"
)

// Run processes deliveries until ctx is cancelled.
func Run(ctx context.Context, rt *app.Runtime) error {
	return app.ErrRoleNotImplemented
}
