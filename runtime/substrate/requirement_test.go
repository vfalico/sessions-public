package substrate_test

import (
	"context"
	"fmt"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
	"strings"
	"testing"
)

func TestLegacyCreateCannotEraseGovernedActorRequirement(t *testing.T) {
	ctl := &mockControl{info: substrate.ActorInfo{ExecutionRequirement: `{"opaque":true}`}}
	ctl.exists("owned", substrate.StatusSuspended)
	_, err := newBackend(ctl).Create(context.Background(), &api.SessionSpec{SessionUID: "owned"})
	if err == nil || !strings.Contains(err.Error(), "mandatory execution") {
		t.Fatal("authority omitted", err)
	}
	for _, call := range ctl.calls {
		if !strings.HasPrefix(call, "get:") {
			t.Fatal("side effect before refusal", ctl.calls)
		}
	}
}

type exactStopControl struct {
	*mockControl
	terminated string
	stopErr    error
}

func (c *exactStopControl) TerminateActor(_ context.Context, _ substrate.ActorRef, uid string) error {
	c.terminated = uid
	return c.stopErr
}
func TestGovernedStopUsesExactDestructiveTermination(t *testing.T) {
	for _, scenario := range []string{"exact", "replacement", "missing-uid", "unsupported", "failure", "policy-free-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			m := &mockControl{info: substrate.ActorInfo{ExecutionRequirement: `{"opaque":true}`}}
			m.exists("owned", substrate.StatusRunning)
			ctl := &exactStopControl{mockControl: m}
			var control substrate.ControlClient = ctl
			in := api.Incarnation{ID: "owned", ComputeInstance: m.uid("owned")}
			switch scenario {
			case "policy-free-replacement":
				m.info.ExecutionRequirement = ""
				in.ComputeInstance = "original-actor-uid"
			case "replacement":
				in.ComputeInstance = "prior-actor"
			case "missing-uid":
				in.ComputeInstance = ""
			case "unsupported":
				control = m
			case "failure":
				ctl.stopErr = fmt.Errorf("retirement pending")
			}
			b := substrate.New(control, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
			err := b.Stop(t.Context(), in)
			if scenario == "exact" {
				if err != nil || ctl.terminated != in.ComputeInstance {
					t.Fatal(err, ctl.terminated)
				}
			} else if err == nil {
				t.Fatal("unsafe stop accepted", scenario)
			}
			if (scenario == "replacement" || scenario == "missing-uid" || scenario == "unsupported" || scenario == "policy-free-replacement") && ctl.terminated != "" {
				t.Fatal("terminated wrong owner")
			}
			if strings.Join(m.calls, ",") != "get:owned" {
				t.Fatal("snapshot or unguarded delete before gate", m.calls)
			}
		})
	}
}

func TestIncarnationPinsHarnessRoutingUID(t *testing.T) {
	ctl := &mockControl{info: running("worker-a")}
	backend := newBackend(ctl)
	inc, err := backend.Create(context.Background(), &api.SessionSpec{SessionUID: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	if inc.CallMetadata[substrate.TargetActorUIDHeader] == "" || inc.CallMetadata[substrate.TargetActorUIDHeader] != ctl.uid("owned") {
		t.Fatal("incarnation routing UID absent", inc)
	}
}
