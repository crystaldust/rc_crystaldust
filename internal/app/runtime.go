package app

import (
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"messagehub/internal/secrets"
)

// ErrRoleNotImplemented is returned by a role whose Run is still a placeholder.
var ErrRoleNotImplemented = errors.New("role not implemented yet")

// Runtime is the read-only set of dependencies every role receives. Fields
// are set once by the serve command and never changed; mutable state such
// as the current config lives behind ConfigHolder.
type Runtime struct {
	Settings Settings
	Config   *ConfigHolder
	Secrets  secrets.Resolver
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Now      func() time.Time
}
