# Owned Substrate Sessions daemon

Build from this integration module:

```sh
go build ./cmd/agentsessions-substrate
./agentsessions-substrate -runtime-config /private/runtime.json -- -addr 127.0.0.1:8080 -journal /private/journal.db
```

The private mode-0600 runtime JSON explicitly names `name`, `atespace`,
`templateAtespace`, `template`, `router`, `controlAddress`, `controlServerName`,
`controlTokenFile`, optional `controlCAFile`, and `descriptor` (the ordinary
Sessions api.Descriptor including an explicit resumability contract). No
implicit runtime image, template, policy or enforcer is selected. The template
must already exist and its declared descriptor must match the native harness.

Control uses TLS verification and a rotating private token file. The Sessions
listener is explicitly restricted to a literal loopback address: its normal
plaintext API has no authentication. Remote harness flags, dynamic registry
listener and host tool-executor dispatch are refused. Existing journal dynamic
registrations retain normal stock-daemon loading rules; use a dedicated journal
for this owned host, not an existing remote-harness journal.

Owned entries never inherit host model, streaming or tool callbacks. A native
harness which asks the host to invoke a model gets a normal execution failure,
not a host callback or a panic. The native chat template must explicitly configure `HARNESS_KIND=chat`,
`HARNESS_MODEL_PLACEMENT=native`, `HARNESS_MODEL`, and `HARNESS_MODEL_BASE_URL`
inside the sandbox (optional `HARNESS_MODEL_PATH` and `HARNESS_MODEL_API_KEY`).
No host provider credential is included in a grant. The native model exchange
journals intent before a correlated live grant; replay serves the completion
without running the provider. Host/native placement participates in the model
input hash. The owned route may be named `chat` only when the daemon's `-model`
is unset, so no host-chat callback is substituted. Native tools and full public
Kaimahi/real-compute acceptance remain unfinished.

The neutral core's `daemon.WithOwnedRuntimes` is an explicit integration point;
Substrate code remains in this separate integration module. Wire tests use a
local backend double, real daemon listener and SQLite, and prove carriage plus
callback refusal only. They do not prove native enforcement.
