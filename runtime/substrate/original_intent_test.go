package substrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

func TestOriginalGovernedIntentCannotDowngradeStop(t *testing.T) {
	for _, scenario := range []string{"exact-empty-metadata", "exact-current-metadata", "replacement", "missing-current-uid", "missing-original-uid", "unsupported", "cleanup-failure"} {
		t.Run(scenario, func(t *testing.T) {
			m := &mockControl{}
			m.exists("owned", substrate.StatusRunning)
			c := &exactStopControl{mockControl: m}
			var control substrate.ControlClient = c
			in := api.Incarnation{ID: "owned", ComputeInstance: m.uid("owned"), ExecutionRequirement: []byte(`{"opaque":true}`)}
			failure := errors.New("original cleanup uncertain")
			switch scenario {
			case "exact-current-metadata":
				m.info.ExecutionRequirement = string(in.ExecutionRequirement)
			case "replacement":
				in.ComputeInstance = "prior-uid"
			case "missing-current-uid":
				m.getNoUID = true
			case "missing-original-uid":
				in.ComputeInstance = ""
			case "unsupported":
				control = m
			case "cleanup-failure":
				c.stopErr = failure
			}
			b := substrate.New(control, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
			err := b.Stop(context.Background(), in)
			shouldTerminate := strings.HasPrefix(scenario, "exact-") || scenario == "cleanup-failure"
			if shouldTerminate {
				if c.terminated != in.ComputeInstance {
					t.Fatal("did not terminate exact original UID", c.terminated, err)
				}
			} else if c.terminated != "" {
				t.Fatal("terminated without original identity/capability", c.terminated)
			}
			if strings.HasPrefix(scenario, "exact-") {
				if err != nil {
					t.Fatal("exact destructive termination failed", err)
				}
			} else if err == nil {
				t.Fatal("unsafe or uncertain cleanup accepted")
			}
			if scenario == "cleanup-failure" && !errors.Is(err, failure) {
				t.Fatal("cleanup cause lost", err)
			}
			if strings.Join(m.calls, ",") != "get:owned" {
				t.Fatal("snapshot/name delete used", m.calls)
			}
		})
	}
}

func TestGovernedCreateRetainsDetachedOriginalIntent(t *testing.T) {
	raw := []byte(`{"opaque":true}`)
	m := &mockControl{info: substrate.ActorInfo{ExecutionRequirement: string(raw)}}
	m.exists("owned", substrate.StatusRunning)
	c := &verifiedWarmControl{mockControl: m}
	b := substrate.New(c, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
	in, err := b.Create(context.Background(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ExpectedComputeInstance: m.uid("owned")})
	if err != nil {
		t.Fatal(err)
	}
	if string(in.ExecutionRequirement) != string(raw) {
		t.Fatal("original requirement missing")
	}
	raw[0] = '!'
	if string(in.ExecutionRequirement) != `{"opaque":true}` {
		t.Fatal("incarnation aliases caller bytes")
	}
}
