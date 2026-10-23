# Governed suspended Create ownership

The Substrate backend requires `ExactRequirementControl` for a new governed Actor. Its Create operation returns the server-assigned UID while suspended. A name-only lookup after Create is not an ownership proof. The host's `SessionSpec.RetainCompute` callback commits a hash-chained `COMPUTE_OWNED` lifecycle record using the invocation's original CAS before the backend invokes exact-UID Resume. Retention failure prevents Resume. Policy-free callers retain the existing Create/Resume path.

The lifecycle record contains the original incarnation ID, runtime, resolved host harness registration, UID and frozen opaque requirement. It is cleanup-only authority: not an execution start, activation receipt, or permission to dispatch. Protobuf and wire conversions preserve it. The controller's existing instance comparison recognizes it, so failed first turns do not erase original instance identity. A fork still resets inherited compute ownership for the child. Workload event sinks cannot author this host lifecycle transition.

The normal public Delete path reconstructs every original ownership record independently of turn-start records. It continues to require durable DELETE intent, original requirement equality and an exact target identity. Failed cleanup remains pending. An observed replacement or missing target is not silently selected or deleted; absence currently stays an explicit cleanup error because backend retirement may still be pending. This patch does not remove native restrictions to reset a run.

The adapter consumes the companion Substrate `ResumeActorRequest.actor_uid` precondition. That companion checks UID before running fast-path success and under the existing Actor lease before resume side effects. Ship/qualify that dependency first; an old server ignoring an unknown field is not compatible proof. The core Sessions module does not import Substrate generated APIs, Guardd policy types, commands or receipts.

## Qualification and remaining gaps

Tests use explicit Control doubles for lifecycle ordering and exact wire mapping, actual SQLite and the production placement/public deletion paths for persistence. Separate fresh test executables exercise an intentional process exit after failed Create and failed cleanup, then recover the same original owner. These are not native Actor/kernel enforcement tests or Kaimahi acceptance.

An uncertain Create reply remains unresolved: the adapter does not rediscover authority from a friendly name, retry Resume, or claim an unobserved object. Compute-side idempotent creation/reconciliation and durable ownership across the response-to-retention crash window still require qualification/correction. This is not an arbitrary crash-cut proof. Cross-process concurrent placement, enforcer-loss behavior, complete native teardown and all-three final-tree public acceptance remain required. Existing unknown or missing authority stays a refusal, not completed reclamation.

## Uncertain creation cannot establish warm ownership

A matching reusable name, requirement and current enforcement proof do not
establish original ownership. Governed warm attachment requires a nonempty
`ExpectedComputeInstance` from the verified host journal and compares it before
verification or dispatch. Lost Create replies and a caller exit before durable
retention therefore cannot silently adopt compute. Policy-free attachment is
unchanged. This closes unsafe adoption, **not** the suspended resource leak:
recovering cleanup authority for an uncertain Create still needs a durable
owner-side operation mechanism. No name-only cleanup is authorized.

## Prepared owner operation before the first creating RPC

Owning runtimes may implement `api.ExecutionOperationRuntime`. Preparation is
side-effect-free and returns opaque immutable operation bytes. Placement appends
COMPUTE_PREPARED using the caller CAS before any creating RPC. Core understands
only session, harness and original requirement association; the backend owns the
operation format. The backend resolves the same owner-side operation, retains
server-assigned UID in COMPUTE_OWNED before exact Resume, and verifies current
complete protection before returning a usable handle. A lost committed Create
reply or pre-retention caller cut reuses the same operation, never name discovery.

Substrate's descriptor includes original template, exact requirement, target and
host-generated operation UUID. Cleanup uses atomic cancel-or-original retirement:
a delayed first Create cannot commit after cancellation, and a retired committed
operation cannot resume. The owner marks the original Actor DELETING under its
existing lease; then exact UID deletion completes native monotonic cleanup. A
replacement is never adopted or deleted. Both operation and original UID remain
journaled after cleanup errors and fresh-registry reconstruction. Neither prepared
nor owned records grant activation or workload dispatch. Policy-free behavior is
unchanged. This needs the separately supplied generic Substrate operation patch.

Evidence distinguishes SQLite/Control doubles from real PostgreSQL/public gRPC
and native integration. No source-level test alone qualifies a process-crash or
kernel acceptance group. Empty original-operation response means the compute
owner atomically cancelled before creation, not that a name lookup was absent.

Failed governed calls retire the original operation, including an uncertain
Create without returned UID. Cleanup failure retains both journaled operation
and owner tombstone for ordinary deletion retry. Returning enforcer availability
does not re-authorize a retired operation. This implementation chooses stop over
same-target continuation after a proof-readback outage; it never treats a
cleanup-only journal as restart authority.
