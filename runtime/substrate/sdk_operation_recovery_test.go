package substrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

// Explicit owner-Control double; owner transaction concurrency has separate
// actual PostgreSQL tests. This suite proves host ordering, not kernel gates.
type operationControl struct {
	*coldVerifiedControl
	original   substrate.ActorInfo
	operation  string
	cancelled  bool
	loseReply  bool
	creates    int
	terminated string
	retireErr  error
}

func (c *operationControl) CreateActorWithRequirementOperation(ctx context.Context, r substrate.ActorRef, t substrate.ObjectRef, raw []byte, op string, lookup, retire bool) (substrate.ActorInfo, error) {
	if c.operation != "" && c.operation != op {
		return substrate.ActorInfo{}, errors.New("changed operation")
	}
	c.operation = op
	if retire && c.original.UID == "" {
		c.cancelled = true
		return substrate.ActorInfo{}, nil
	}
	if c.cancelled {
		return substrate.ActorInfo{}, errors.New("operation cancelled")
	}
	if c.original.UID != "" {
		return c.original, nil
	}
	if lookup {
		return substrate.ActorInfo{}, substrate.ErrActorNotFound
	}
	in, err := c.coldVerifiedControl.CreateActorWithRequirementIdentity(ctx, r, t, raw)
	if err != nil {
		return in, err
	}
	c.original = in
	c.creates++
	// A real Create returns suspended state and must not dispatch a workload.
	c.mockControl.actors[r.Name] = substrate.StatusSuspended
	if c.loseReply {
		c.loseReply = false
		return substrate.ActorInfo{}, errors.New("injected lost committed Create reply")
	}
	return in, nil
}
func (c *operationControl) TerminateActor(ctx context.Context, r substrate.ActorRef, uid string) error {
	c.terminated = uid
	if c.retireErr != nil {
		return c.retireErr
	}
	// Exact owner delete is idempotent for missing target, refuses replacement.
	if current, exists := c.mockControl.actors[r.Name]; exists {
		_ = current
		if c.mockControl.uid(r.Name) != uid {
			return errors.New("replacement not owned")
		}
		delete(c.mockControl.actors, r.Name)
	}
	return nil
}
func operationBackend(c *operationControl) *substrate.Backend {
	return substrate.New(c, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
}
func newOperationControl() *operationControl {
	m := &mockControl{info: substrate.ActorInfo{Status: substrate.StatusRunning}}
	return &operationControl{coldVerifiedControl: &coldVerifiedControl{coldRequirementControl: &coldRequirementControl{mockControl: m}}}
}

func TestOriginalOperationRecoversLostCreateAndResumeReplies(t *testing.T) {
	for _, cut := range []string{"lost-create-reply", "caller-before-retention", "lost-resume-reply"} {
		t.Run(cut, func(t *testing.T) {
			c := newOperationControl()
			b := operationBackend(c)
			raw := []byte(`{"opaque":"ceiling"}`)
			op, err := b.PrepareExecutionOperation("owned", raw)
			if err != nil {
				t.Fatal(err)
			}
			var retained api.Incarnation
			retain := func(in api.Incarnation) error {
				if in.Address != "" || in.ComputeInstance == "" {
					t.Fatal("retention is dispatch", in)
				}
				retained = in
				return nil
			}
			spec := &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ComputeOperation: op, RetainCompute: retain}
			switch cut {
			case "lost-create-reply":
				c.loseReply = true
			case "caller-before-retention":
				spec.RetainCompute = func(api.Incarnation) error { return errors.New("injected caller cut before durable retention") }
			case "lost-resume-reply":
				// Resume commits compute but its caller sees a transient error; original
				// operation remains sufficient to resolve UID after caller reconstruction.
				c.resumeErr = errors.New("lost Resume reply")
			}
			in, err := b.Create(t.Context(), spec)
			if err == nil || in.Address != "" {
				t.Fatal("cut dispatched", in, err)
			}
			if c.creates != 1 {
				t.Fatal("fixture did not create exactly once", c.creates)
			}
			if cut == "lost-resume-reply" {
				c.mockControl.actors["owned"] = substrate.StatusRunning
			}
			c.resumeErr = nil
			// Fresh backend object; only original operation and durably retained UID
			// (when present) cross the caller reconstruction boundary.
			fresh := operationBackend(c)
			spec = &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ComputeOperation: append([]byte(nil), op...), ExpectedComputeInstance: retained.ComputeInstance, RetainCompute: retain}
			in, err = fresh.Create(t.Context(), spec)
			if err != nil || in.Address == "" || in.ComputeInstance != c.original.UID || c.creates != 1 || retained.ComputeInstance != c.original.UID {
				t.Fatal("original operation recovery failed", in, err, c.creates, retained)
			}
		})
	}
}

func TestOriginalOperationRetirementCancelsUncommittedCreate(t *testing.T) {
	c := newOperationControl()
	b := operationBackend(c)
	raw := []byte(`{"opaque":true}`)
	op, err := b.PrepareExecutionOperation("owned", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.RetireExecutionOperation(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	spec := &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ComputeOperation: op, RetainCompute: func(api.Incarnation) error { return nil }}
	in, err := b.Create(t.Context(), spec)
	if err == nil || in.Address != "" || c.creates != 0 {
		t.Fatal("delayed Create passed retired operation", in, err, c.creates)
	}
	if err = b.RetireExecutionOperation(t.Context(), op); err != nil {
		t.Fatal("retirement retry", err)
	}
}

func TestOriginalOperationCannotAdoptReplacementOrChangedPolicy(t *testing.T) {
	c := newOperationControl()
	b := operationBackend(c)
	raw := []byte(`{"opaque":true}`)
	op, err := b.PrepareExecutionOperation("owned", raw)
	if err != nil {
		t.Fatal(err)
	}
	c.loseReply = true
	spec := &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ComputeOperation: op, RetainCompute: func(api.Incarnation) error { return nil }}
	if _, err = b.Create(t.Context(), spec); err == nil {
		t.Fatal("lost reply not injected")
	}
	c.mockControl.exists("owned", substrate.StatusRunning)
	c.mockControl.uids["owned"] = "replacement"
	in, err := b.Create(t.Context(), spec)
	if err == nil || in.Address != "" || c.callsVerified != 0 {
		t.Fatal("adopted replacement", in, err)
	}
	spec.ExecutionRequirement = []byte(`{"opaque":false}`)
	if in, err = b.Create(t.Context(), spec); err == nil || in.Address != "" {
		t.Fatal("changed input admitted", in, err)
	}
}

func TestOriginalOperationRetirementRecoversUIDAfterLostReplyAndDeletion(t *testing.T) {
	c := newOperationControl()
	b := operationBackend(c)
	raw := []byte(`{"opaque":true}`)
	op, e := b.PrepareExecutionOperation("owned", raw)
	if e != nil {
		t.Fatal(e)
	}
	c.loseReply = true
	spec := &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ComputeOperation: op, RetainCompute: func(api.Incarnation) error { return nil }}
	if _, e = b.Create(t.Context(), spec); e == nil {
		t.Fatal("lost create reply not injected")
	}
	c.retireErr = errors.New("injected owner cleanup failure")
	if e = b.RetireExecutionOperation(t.Context(), op); !errors.Is(e, c.retireErr) {
		t.Fatal("lost original cleanup error", e)
	}
	if c.terminated != c.original.UID || c.creates != 1 {
		t.Fatal("cleanup lost original UID", c.terminated, c.original, c.creates)
	}
	// Fresh backend/process model, same immutable operation authority.
	c.retireErr = nil
	if e = operationBackend(c).RetireExecutionOperation(t.Context(), append([]byte(nil), op...)); e != nil {
		t.Fatal(e)
	}
	if e = operationBackend(c).RetireExecutionOperation(t.Context(), op); e != nil {
		t.Fatal("post-deletion retry", e)
	}
	if c.creates != 1 {
		t.Fatal("retirement created compute", c.creates)
	}
}
