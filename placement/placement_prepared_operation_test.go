package placement_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/sqlitelog"
)

// Fake compute with real SQLite journal/wire/CAS. This proves pre-RPC operation
// durability and cleanup-only replay; it does not prove kernel enforcement.
type preparedOperationRuntime struct {
	*governedLifecycleRuntime
	operation                  []byte
	prepares, creates, retires int
	inspect                    func() error
	retireErr                  error
}

func (b *preparedOperationRuntime) PrepareExecutionOperation(uid string, raw []byte) ([]byte, error) {
	b.prepares++
	return append([]byte(nil), b.operation...), nil
}
func (b *preparedOperationRuntime) Create(ctx context.Context, s *api.SessionSpec) (api.Incarnation, error) {
	b.creates++
	if !bytes.Equal(s.ComputeOperation, b.operation) {
		return api.Incarnation{}, errors.New("original operation missing")
	}
	if b.inspect != nil {
		if e := b.inspect(); e != nil {
			return api.Incarnation{}, e
		}
	}
	return api.Incarnation{}, errors.New("injected lost committed Create response")
}
func (b *preparedOperationRuntime) RetireExecutionOperation(ctx context.Context, raw []byte) error {
	b.retires++
	if !bytes.Equal(raw, b.operation) {
		return errors.New("retirement changed original operation")
	}
	return b.retireErr
}
func preparedRuntime(op []byte) *preparedOperationRuntime {
	return &preparedOperationRuntime{governedLifecycleRuntime: &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"})}, operation: append([]byte(nil), op...)}
}

func TestPreparedOperationDurableBeforeRPCAndFreshRegistryRetiresWithoutUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	store, e := sqlitelog.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"opaque":"ceiling"}`)
	op := []byte(`{"backend":"opaque-original-op"}`)
	if e = store.CreateOwnedSession(sqlitelog.SessionMeta{UID: "owned", Harness: "echo", ExecutionRequirement: string(raw)}, api.CallerIdentity{Issuer: "issuer", Subject: "owner"}, false); e != nil {
		t.Fatal(e)
	}
	b := preparedRuntime(op)
	defer b.Close()
	b.inspect = func() error {
		// Independent reader opened in the simulated RPC proves durable commit,
		// rather than trusting the Placer's transient local operation variable.
		reader, e := sqlitelog.Open(path)
		if e != nil {
			return e
		}
		defer reader.Close()
		recs, e := reader.Session("owned").Read(1)
		if e != nil {
			return e
		}
		if len(recs) != 1 || recs[0].Event.Kind != api.EventLifecycle || recs[0].Event.Lifecycle.Kind != api.LifecycleComputePrepared {
			return errors.New("RPC preceded durable operation")
		}
		l := recs[0].Event.Lifecycle
		if !bytes.Equal(l.ComputeOperation, op) || !bytes.Equal(l.ComputeRequirement, raw) || l.ComputeInstance != "" || l.ComputeRuntime != "" {
			return errors.New("operation lost roundtrip or became execution authority")
		}
		return nil
	}
	p := placement.New(b, nil)
	for i := 0; i < 2; i++ {
		_, e = p.Exec(t.Context(), store.Session("owned"), "owned", nil, 0, placement.WithHarness("echo"), placement.WithExecutionRequirement(raw))
		if e == nil {
			t.Fatal("uncertain create dispatched")
		}
	}
	if b.prepares != 1 || b.creates != 2 || b.retires != 2 {
		t.Fatal("retry did not retain same original operation", b.prepares, b.creates)
	}
	if _, e = store.DeleteSession(t.Context(), "owned"); e != nil {
		t.Fatal(e)
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	store, e = sqlitelog.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	fresh := preparedRuntime(op)
	defer fresh.Close()
	fresh.retireErr = errors.New("injected original-owner unavailable")
	reg, e := placement.NewRegistry("echo", map[string]*placement.Placer{"echo": placement.New(fresh, nil)})
	if e != nil {
		t.Fatal(e)
	}
	retire := func() error {
		return reg.WithSessionGuard("owned", func() error {
			return reg.RetireGovernedUnderDeleteGuard(t.Context(), store.Session("owned"), "owned", "echo", raw)
		})
	}
	if e = retire(); !errors.Is(e, fresh.retireErr) {
		t.Fatal("cleanup fault lost", e)
	}
	fresh.retireErr = nil
	if e = retire(); e != nil {
		t.Fatal("fresh registry lost original operation", e)
	}
	if fresh.retires != 2 || fresh.creates != 0 || fresh.prepares != 0 {
		t.Fatal("retirement created or prepared", fresh.retires, fresh.creates, fresh.prepares)
	}
	if e = store.Session("owned").Verify(); e != nil {
		t.Fatal(e)
	}
}

func TestPreparedOperationCASFailureCannotInvokeCreatingRPC(t *testing.T) {
	store, e := sqlitelog.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	log := store.Session("owned")
	f, e := log.NewFence()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = log.Append(0, f, api.Event{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleCancel}}); e != nil {
		t.Fatal(e)
	}
	b := preparedRuntime([]byte("opaque"))
	defer b.Close()
	_, e = placement.New(b, nil).Exec(t.Context(), log, "owned", nil, 0, placement.WithHarness("echo"), placement.WithExecutionRequirement([]byte(`{"opaque":true}`)))
	if e == nil || b.creates != 0 {
		t.Fatal("stale CAS reached creating RPC", e, b.creates)
	}
}
