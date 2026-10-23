package placement_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/sqlitelog"
)

type pendingCreateRuntime struct {
	*governedLifecycleRuntime
	failed   error
	retained bool
}

func (b *pendingCreateRuntime) Create(ctx context.Context, s *api.SessionSpec) (api.Incarnation, error) {
	in := api.Incarnation{ID: s.SessionUID, Runtime: "fixture", ComputeInstance: b.uid, ExecutionRequirement: append([]byte(nil), s.ExecutionRequirement...)}
	if s.RetainCompute == nil {
		return in, errors.New("missing pre-resume retention")
	}
	if err := s.RetainCompute(in); err != nil {
		return in, err
	}
	b.retained = true
	return in, b.failed // model Resume loss; never dispatch a harness in this double
}

// Real SQLite append/wire round-trip; explicit fake compute. Successful fresh
// registry recovery uses only pre-resume durable ownership, not a turn marker.
func TestFailedCreateDurableOwnershipRecoversWithoutExecutionStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	store, err := sqlitelog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"opaque":"network-ceiling"}`)
	if err = store.CreateOwnedSession(sqlitelog.SessionMeta{UID: "owned", Harness: "echo", ExecutionRequirement: string(raw)}, api.CallerIdentity{Issuer: "issuer", Subject: "owner"}, false); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("lost exact Resume reply")
	b := &pendingCreateRuntime{governedLifecycleRuntime: &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"}), uid: "original-7", stopErr: errors.New("cleanup unavailable")}, failed: failure}
	defer b.Close()
	p := placement.New(b, nil)
	_, err = placement.NewRegistry("echo", map[string]*placement.Placer{"echo": p})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Exec(t.Context(), store.Session("owned"), "owned", nil, 0, placement.WithHarness("echo"), placement.WithExecutionRequirement(raw))
	if !errors.Is(err, failure) || !b.retained || len(b.stopped) != 1 {
		t.Fatal("failed Create owner lost", err, b.retained, b.stopped)
	}
	records, err := store.Session("owned").Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Event.ExecutionStart != nil || records[0].Event.Lifecycle == nil || records[0].Event.Lifecycle.Kind != api.LifecycleComputeOwned {
		t.Fatal("ownership manufactured execution", records)
	}
	l := records[0].Event.Lifecycle
	if l.ComputeInstance != "original-7" || l.ComputeID != "owned" || l.ComputeRuntime != "fixture" || l.ComputeHarness != "echo" || string(l.ComputeRequirement) != string(raw) {
		t.Fatal("roundtrip lost ownership", l)
	}
	if _, err = store.DeleteSession(t.Context(), "owned"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlitelog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fresh := &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"})}
	defer fresh.Close()
	registry, err := placement.NewRegistry("echo", map[string]*placement.Placer{"echo": placement.New(fresh, nil)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = registry.WithSessionGuard("owned", func() error {
		return registry.RetireGovernedUnderDeleteGuard(ctx, store.Session("owned"), "owned", "echo", raw)
	})
	if err != nil || len(fresh.stopped) != 1 {
		t.Fatal("fresh registry lost ownership", err, fresh.stopped)
	}
	got := fresh.stopped[0]
	if got.ComputeInstance != "original-7" || got.ID != "owned" || got.Runtime != "fixture" || string(got.ExecutionRequirement) != string(raw) {
		t.Fatal("wrong retirement", got)
	}
	if err = store.Session("owned").Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedCreateRetentionCASPreventsResume(t *testing.T) {
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	log := store.Session("owned")
	fence, err := log.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = log.Append(0, fence, api.Event{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleCancel}}); err != nil {
		t.Fatal(err)
	}
	b := &pendingCreateRuntime{governedLifecycleRuntime: &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"}), uid: "original-8"}, failed: errors.New("unexpected resume")}
	defer b.Close()
	p := placement.New(b, nil)
	_, err = p.Exec(t.Context(), log, "owned", nil, 0, placement.WithExecutionRequirement([]byte(`{"opaque":true}`)))
	if err == nil || b.retained || len(b.stopped) != 1 {
		t.Fatal("CAS failed to prevent resume/retain cleanup", err, b.retained, b.stopped)
	}
	records, err := log.Read(1)
	if err != nil || len(records) != 1 {
		t.Fatal("conflicting ownership appended", err, records)
	}
}

// Fake running compute after a successful retention callback; this tests the
// real controller CAS/cursor transition across the pre-resume host record.
type retainedSuccessRuntime struct{ *governedLifecycleRuntime }

func (b *retainedSuccessRuntime) Create(ctx context.Context, s *api.SessionSpec) (api.Incarnation, error) {
	in, err := b.governedLifecycleRuntime.Create(ctx, s)
	if err != nil {
		return in, err
	}
	in.ExecutionRequirement = append([]byte(nil), s.ExecutionRequirement...)
	owned := in
	owned.Address = ""
	if err = s.RetainCompute(owned); err != nil {
		return owned, err
	}
	return in, nil
}
func TestRetainedCreateSuccessAdvancesOriginalCASExactlyOnce(t *testing.T) {
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &retainedSuccessRuntime{governedLifecycleRuntime: &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"}), uid: "original-active"}}
	defer b.Close()
	p := placement.New(b, nil)
	_, err = p.Exec(t.Context(), store.Session("owned"), "owned", nil, 0, placement.WithExecutionRequirement([]byte(`{"opaque":true}`)))
	if err != nil {
		t.Fatal("retention cursor not transferred to controller", err)
	}
	records, err := store.Session("owned").Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 3 || records[0].Event.Lifecycle == nil || records[0].Event.Lifecycle.Kind != api.LifecycleComputeOwned || records[1].Event.ExecutionStart == nil {
		t.Fatal("wrong first-effect sequence", records)
	}
	if len(b.stopped) != 0 {
		t.Fatal("successful execution retired", b.stopped)
	}
	if err = store.Session("owned").Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementDoesNotSelectCopiedParentOwnership(t *testing.T) {
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.CreateOwnedSession(sqlitelog.SessionMeta{UID: "child", Harness: "echo", ExecutionRequirement: `{"opaque":true}`}, api.CallerIdentity{Issuer: "issuer", Subject: "owner"}, false); err != nil {
		t.Fatal(err)
	}
	log := store.Session("child")
	raw := []byte(`{"opaque":true}`)
	events := []api.Event{
		{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleComputeOwned, ComputeID: "parent", ComputeRuntime: "fixture", ComputeHarness: "unavailable-parent", ComputeInstance: "parent-uid", ComputeRequirement: []byte(`{"different":"parent"}`)}},
		{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleFork, Detail: "parent@1"}},
		{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleComputeOwned, ComputeID: "child", ComputeRuntime: "fixture", ComputeHarness: "echo", ComputeInstance: "child-uid", ComputeRequirement: raw}},
	}
	fence, err := log.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	head := int64(0)
	for _, ev := range events {
		r, e := log.Append(head, fence, ev)
		if e != nil {
			t.Fatal(e)
		}
		head = r.Seq
	}
	if _, err = store.DeleteSession(t.Context(), "child"); err != nil {
		t.Fatal(err)
	}
	b := &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"})}
	defer b.Close()
	registry, err := placement.NewRegistry("echo", map[string]*placement.Placer{"echo": placement.New(b, nil)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = registry.WithSessionGuard("child", func() error { return registry.RetireGovernedUnderDeleteGuard(ctx, log, "child", "echo", raw) })
	if err != nil || len(b.stopped) != 1 || b.stopped[0].ComputeInstance != "child-uid" {
		t.Fatal("copied parent cleanup selected", err, b.stopped)
	}
}

// Explicit fake runtime; no native isolation claim. A replacement must not
// rewrite the journal's original owner before the controller compares it.
func TestRetainedCreateCannotOverwritePriorExactOwner(t *testing.T) {
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &retainedSuccessRuntime{governedLifecycleRuntime: &governedLifecycleRuntime{Backend: local.New(lifecycleHarness{scenario: "success"}), uid: "original-active"}}
	defer b.Close()
	p := placement.New(b, nil)
	log := store.Session("owned")
	raw := []byte(`{"opaque":true}`)
	if _, err = p.Exec(t.Context(), log, "owned", nil, 0, placement.WithExecutionRequirement(raw)); err != nil {
		t.Fatal(err)
	}
	head, err := log.Head()
	if err != nil {
		t.Fatal(err)
	}
	b.uid = "replacement-active"
	_, err = p.Exec(t.Context(), log, "owned", nil, head, placement.WithExecutionRequirement(raw))
	if err == nil {
		t.Fatal("REPLACEMENT_REWROTE_ORIGINAL_OWNER: second execution succeeded")
	}
	if next, _ := log.Head(); next != head {
		t.Fatal("replacement changed original journal", head, next)
	}
	if len(b.stopped) != 1 || b.stopped[0].ComputeInstance != "replacement-active" {
		t.Fatal("failed create did not retain exact reported cleanup", b.stopped)
	}
}
