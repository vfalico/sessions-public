package agentsessionsd

import "github.com/agent-primitives/sessions/placement"

// WithOwnedRuntimes registers operator-configured compute owners as static
// harness routes. The caller owns backend connection lifecycle. Entries get no
// host model, stream or tool callbacks: native effects must remain inside the
// selected runtime. Configured ownership alone is not enforcement evidence.
func WithOwnedRuntimes(entries map[string]placement.Backend) Option {
	frozen := make(map[string]placement.Backend, len(entries))
	for name, backend := range entries {
		frozen[name] = backend
	}
	return func(o *options) { o.owned = frozen }
}
