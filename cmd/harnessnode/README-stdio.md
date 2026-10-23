# Runtime stdio listener mode

`RUNTIME_CONTROL_STDIO=1` selects in-process harnesswire port 80 and HTTP readiness
port 8081 carried by runtime-owned stdin/stdout frames. No TCP listener or socket
is created for these endpoints. stdout is protocol-only; all logs go to stderr.
The runtime must reserve stdout and select the actual prepared container through
its trusted worker configuration. The transport only accepts host-initiated
connections and cannot request host-side outbound calls. Native model/tools and
subprocesses retain their actual network policy; do not add implicit endpoints.
Absent this variable the ordinary TCP/unix configuration remains unchanged.
Test `TestOrdinaryMainStdio` invokes actual main and checks readiness and Describe.
