package substrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

// Explicit Control doubles: these simulate response/caller cuts, not kernel
// enforcement. Their assertions concern durable ownership and warm admission.
type uncertainCreateControl struct {
	*coldVerifiedControl
	loseReply bool
}

func (c *uncertainCreateControl) CreateActorWithRequirementIdentity(ctx context.Context, ref substrate.ActorRef, template substrate.ObjectRef, raw []byte) (substrate.ActorInfo, error) {
	in, err := c.coldVerifiedControl.CreateActorWithRequirementIdentity(ctx, ref, template, raw)
	if err == nil && c.loseReply {
		return substrate.ActorInfo{}, errors.New("injected lost committed Create reply")
	}
	return in, err
}

func TestUncertainCreateCannotWarmAdoptWithoutDurableOriginalOwner(t *testing.T) {
	for _, cut := range []string{"lost-create-reply", "crash-before-retention"} {
		t.Run(cut, func(t *testing.T) {
			raw := []byte(`{"opaque":"ceiling"}`)
			m := &mockControl{info: substrate.ActorInfo{Status: substrate.StatusRunning}}
			c := &uncertainCreateControl{coldVerifiedControl: &coldVerifiedControl{coldRequirementControl: &coldRequirementControl{mockControl: m}}, loseReply: cut == "lost-create-reply"}
			b := substrate.New(c, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
			retained := 0
			if cut == "lost-create-reply" {
				in, err := b.Create(t.Context(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, RetainCompute: func(api.Incarnation) error { retained++; return nil }})
				if err == nil || in.Address != "" || retained != 0 {
					t.Fatal("uncertain create unexpectedly dispatched", in, err, retained)
				}
			} else {
				// Simulate abrupt caller exit after authenticated server Create commits but
				// before RetainCompute can append. The server-authored UID exists, caller
				// journal does not. This is not a native abrupt-process experiment.
				if _, err := c.CreateActorWithRequirementIdentity(t.Context(), substrate.ActorRef{Atespace: "space", Name: "owned"}, substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, raw); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := m.actors["owned"]; !ok {
				t.Fatal("fixture did not create compute")
			}
			// Independent owner action can have left this name running. Neither matching
			// policy nor current exact enforcement proves this caller owns that UID.
			m.actors["owned"] = substrate.StatusRunning
			verifiedBefore := c.callsVerified
			in, err := b.Create(t.Context(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, RetainCompute: func(api.Incarnation) error { retained++; return nil }})
			if err == nil || in.Address != "" || c.callsVerified != verifiedBefore || retained != 0 {
				t.Fatalf("unowned warm adoption: handle=%+v error=%v verifications=%d retained=%d", in, err, c.callsVerified-verifiedBefore, retained)
			}
		})
	}
}
