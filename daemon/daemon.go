// Package daemon exposes the stock Sessions daemon with explicit owned-runtime
// registration. Integrations supply compute clients without importing vendor
// code into the neutral core. Network listeners retain the stock daemon's
// plaintext, unauthenticated development contract; do not expose them remotely.
package daemon

import (
	"github.com/agent-primitives/sessions/internal/agentsessionsd"
	"github.com/agent-primitives/sessions/placement"
)

type Option = agentsessionsd.Option

var ErrUsage = agentsessionsd.ErrUsage

func Run(args []string, opts ...Option) error { return agentsessionsd.Run(args, opts...) }
func WithOwnedRuntimes(entries map[string]placement.Backend) Option {
	return agentsessionsd.WithOwnedRuntimes(entries)
}
