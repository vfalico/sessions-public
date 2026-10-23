package substrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

// Explicit control double; ordering and original identity are checked, not
// native activation. The concrete wire adapter is tested separately.
type retainedCreateControl struct {
	*coldVerifiedControl
	events     []string
	original   string
	resumeErr  error
	changedUID bool
}

func (c *retainedCreateControl) CreateActorWithRequirementIdentity(ctx context.Context, r substrate.ActorRef, t substrate.ObjectRef, raw []byte) (substrate.ActorInfo, error) {
	c.events = append(c.events, "create-suspended")
	info, err := c.coldVerifiedControl.CreateActorWithRequirementIdentity(ctx, r, t, raw)
	c.original = info.UID
	return info, err
}
func (c *retainedCreateControl) ResumeActorExact(ctx context.Context, r substrate.ActorRef, uid string) (substrate.ActorInfo, error) {
	c.events = append(c.events, "resume-exact")
	if uid != c.original {
		return substrate.ActorInfo{}, errors.New("lost original UID")
	}
	if c.resumeErr != nil {
		return substrate.ActorInfo{}, c.resumeErr
	}
	info, err := c.coldVerifiedControl.ResumeActorExact(ctx, r, uid)
	if c.changedUID {
		info.UID = "replacement"
	}
	return info, err
}
func TestGovernedCreateRetainsOriginalAuthorityBeforeExactResume(t *testing.T) {
	for _, scenario := range []string{"active", "resume-error", "retention-error", "no-retainer", "changed-resume-uid", "failed-proof", "cancel-before-resume"} {
		t.Run(scenario, func(t *testing.T) {
			raw := []byte(`{"opaque":"ceiling"}`)
			m := &mockControl{info: substrate.ActorInfo{Status: substrate.StatusRunning}}
			c := &retainedCreateControl{coldVerifiedControl: &coldVerifiedControl{coldRequirementControl: &coldRequirementControl{mockControl: m}}}
			failure := errors.New("injected admission failure")
			if scenario == "resume-error" {
				c.resumeErr = failure
			}
			if scenario == "failed-proof" {
				c.failure = failure
			}
			if scenario == "changed-resume-uid" {
				c.changedUID = true
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var retained api.Incarnation
			spec := &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, RetainCompute: func(in api.Incarnation) error {
				c.events = append(c.events, "retained")
				retained = in
				// Callback sees no usable handle and only original server-authored UID.
				if in.Address != "" || in.ComputeInstance != c.original || string(in.ExecutionRequirement) != string(raw) {
					t.Fatal("invalid retention authority", in)
				}
				if scenario == "cancel-before-resume" {
					cancel()
				}
				if scenario == "retention-error" {
					return failure
				}
				return nil
			}}
			if scenario == "no-retainer" {
				spec.RetainCompute = nil
			}
			b := substrate.New(c, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
			inc, err := b.Create(ctx, spec)
			if scenario == "active" {
				if err != nil || inc.Address == "" {
					t.Fatal("active handle missing", inc, err)
				}
			} else {
				if err == nil || inc.Address != "" {
					t.Fatal("unsafe failed-create handle", inc, err)
				}
				if scenario != "no-retainer" && (inc.ComputeInstance != c.original || inc.ID != "owned" || string(inc.ExecutionRequirement) != string(raw)) {
					t.Fatal("failed Create dropped original authority", inc)
				}
			}
			want := "create-suspended,retained,resume-exact"
			if scenario == "retention-error" || scenario == "cancel-before-resume" {
				want = "create-suspended,retained"
			}
			if scenario == "no-retainer" {
				want = ""
			}
			if strings.Join(c.events, ",") != want {
				t.Fatalf("ordering %v want %s", c.events, want)
			}
			if scenario != "no-retainer" && retained.ComputeInstance == "" {
				t.Fatal("owner lost")
			}
			if scenario == "resume-error" || scenario == "retention-error" || scenario == "failed-proof" {
				if !errors.Is(err, failure) {
					t.Fatal("cause lost", err)
				}
			}
		})
	}
}

func TestGovernedPriorIdentityCheckedBeforeAnyWarmVerification(t *testing.T) {
	for _, scenario := range []string{"same", "replacement", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			raw := []byte(`{"opaque":true}`)
			m := &mockControl{info: substrate.ActorInfo{ExecutionRequirement: string(raw), Worker: "worker"}}
			m.exists("owned", substrate.StatusRunning)
			expected := m.uid("owned")
			if scenario == "replacement" {
				expected = "original-not-current"
			}
			if scenario == "absent" {
				delete(m.actors, "owned")
			}
			c := &verifiedWarmControl{mockControl: m}
			b := substrate.New(c, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
			in, err := b.Create(t.Context(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, ExpectedComputeInstance: expected})
			if scenario == "same" {
				if err != nil || in.ComputeInstance != expected || c.verifiedUID != expected {
					t.Fatal(in, err, c.verifiedUID)
				}
			} else {
				if err == nil || in.ComputeInstance != "" || len(m.calls) != 1 || m.calls[0] != "get:owned" || c.verifiedUID != "" {
					t.Fatal("replacement admitted or side effect before identity", in, err, m.calls)
				}
			}
		})
	}
}
