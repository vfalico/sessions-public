package substrate_test

import (
	"context"
	"errors"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
	"reflect"
	"testing"
)

type verifiedWarmControl struct {
	*mockControl
	verifyErr           error
	verifiedUID         string
	verifiedRequirement []byte
	verifiedRef         substrate.ActorRef
}

func (c *verifiedWarmControl) VerifyActorExecution(_ context.Context, ref substrate.ActorRef, uid string, raw []byte) error {
	c.calls = append(c.calls, "verify:"+ref.Name)
	c.verifiedUID = uid
	c.verifiedRequirement = append([]byte(nil), raw...)
	c.verifiedRef = ref
	return c.verifyErr
}
func TestGovernedWarmAttachmentRequiresCurrentExactProof(t *testing.T) {
	for _, scenario := range []string{"active", "inactive", "unsupported", "suspended", "missing-uid", "changed-requirement"} {
		t.Run(scenario, func(t *testing.T) {
			raw := []byte(`{"opaque":true}`)
			m := &mockControl{info: substrate.ActorInfo{ExecutionRequirement: string(raw), Worker: "worker"}}
			m.exists("owned", substrate.StatusRunning)
			c := &verifiedWarmControl{mockControl: m}
			var ctl substrate.ControlClient = c
			switch scenario {
			case "inactive":
				c.verifyErr = errors.New("service unavailable or inactive")
			case "unsupported":
				ctl = m
			case "suspended":
				m.exists("owned", substrate.StatusSuspended)
			case "missing-uid":
				m.getNoUID = true
			case "changed-requirement":
				m.info.ExecutionRequirement = `{"opaque":false}`
			}
			inc, err := substrate.New(ctl, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo", Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}).Create(context.Background(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ExpectedComputeInstance: m.uid("owned")})
			if scenario == "active" {
				if err != nil || inc.ID != "owned" {
					t.Fatal(inc, err)
				}
				if c.verifiedUID != m.uid("owned") || !reflect.DeepEqual(raw, c.verifiedRequirement) || c.verifiedRef.Name != "owned" {
					t.Fatal("verification lost exact identity", c)
				}
			} else if err == nil {
				t.Fatal("unproved warm attachment admitted")
			}
			want := []string{"get:owned"}
			if scenario == "active" || scenario == "inactive" {
				want = append(want, "verify:owned")
			}
			if !reflect.DeepEqual(m.calls, want) {
				t.Fatal("read-only verification caused resume/create", m.calls)
			}
		})
	}
}
