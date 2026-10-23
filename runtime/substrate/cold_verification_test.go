package substrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

type coldRequirementControl struct {
	*mockControl
}

func (c *coldRequirementControl) CreateActorWithRequirement(ctx context.Context, ref substrate.ActorRef, template substrate.ObjectRef, raw []byte) error {
	c.info.ExecutionRequirement = string(raw)
	return c.CreateActor(ctx, ref, template)
}

type coldVerifiedControl struct {
	*coldRequirementControl
	failure       error
	uid           string
	raw           []byte
	ref           substrate.ActorRef
	callsVerified int
}

func (c *coldVerifiedControl) VerifyActorExecution(_ context.Context, ref substrate.ActorRef, uid string, raw []byte) error {
	c.callsVerified++
	c.uid = uid
	c.raw = append([]byte(nil), raw...)
	c.ref = ref
	return c.failure
}

// These are explicit Control-interface doubles. Production adapter must use
// server-authored Create response and owner-checked UID resume, not Get by name.
func (c *coldVerifiedControl) CreateActorWithRequirementIdentity(ctx context.Context, ref substrate.ActorRef, template substrate.ObjectRef, raw []byte) (substrate.ActorInfo, error) {
	if err := c.CreateActorWithRequirement(ctx, ref, template, raw); err != nil {
		return substrate.ActorInfo{}, err
	}
	return substrate.ActorInfo{UID: c.mockControl.uid(ref.Name), Status: substrate.StatusSuspended, ExecutionRequirement: string(raw)}, nil
}
func (c *coldVerifiedControl) ResumeActorExact(ctx context.Context, ref substrate.ActorRef, uid string) (substrate.ActorInfo, error) {
	if uid != c.mockControl.uid(ref.Name) {
		return substrate.ActorInfo{}, errors.New("UID changed")
	}
	return c.mockControl.ResumeActor(ctx, ref)
}

func TestGovernedColdCreateRequiresCurrentExactProof(t *testing.T) {
	for _, scenario := range []string{"active", "inactive", "unsupported", "missing-uid", "resume-error", "changed-policy"} {
		t.Run(scenario, func(t *testing.T) {
			raw := []byte(`{"opaque":true}`)
			m := &mockControl{info: substrate.ActorInfo{Status: substrate.StatusRunning}}
			requirement := &coldRequirementControl{mockControl: m}
			verified := &coldVerifiedControl{coldRequirementControl: requirement}
			var control substrate.ControlClient = verified
			failure := errors.New("current proof inactive")
			switch scenario {
			case "inactive":
				verified.failure = failure
			case "unsupported":
				control = requirement
			case "missing-uid":
				control = &coldMissingUIDControl{coldVerifiedControl: verified}
			case "resume-error":
				m.resumeErr = errors.New("resume failure")
			case "changed-policy":
				m.afterGet = func(string) { m.info.ExecutionRequirement = `{"other":true}` }
			}
			// GetActor fails absent but invokes afterGet before returning. For changed
			// policy mutate the post-create Resume result using an explicit wrapper.
			if scenario == "changed-policy" {
				control = &coldChangedPolicyControl{coldVerifiedControl: verified}
			}
			b := substrate.New(control, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
			inc, err := b.Create(context.Background(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: raw, RetainCompute: func(api.Incarnation) error { return nil }})
			if scenario == "active" {
				if err != nil || inc.ComputeInstance == "" {
					t.Fatal("verified cold launch refused", inc, err)
				}
			} else if err == nil || inc.Address != "" {
				t.Fatal("unverified cold launch handle returned", inc, err)
			}
			if scenario == "active" || scenario == "inactive" {
				if verified.callsVerified != 1 || verified.uid != m.uid("owned") || verified.ref != (substrate.ActorRef{Atespace: "space", Name: "owned"}) || string(verified.raw) != string(raw) {
					t.Fatal("proof did not bind exact original target", verified.uid, verified.raw, verified.ref)
				}
			} else if verified.callsVerified != 0 {
				t.Fatal("proof called before identity/policy/activation was valid")
			}
			if scenario == "inactive" && !errors.Is(err, failure) {
				t.Fatal("proof failure cause lost", err)
			}
			if strings.Contains(strings.Join(m.calls, ","), "suspend:") || strings.Contains(strings.Join(m.calls, ","), "delete:") {
				t.Fatal("failed-create cleanup inferred from reusable name", m.calls)
			}
		})
	}
}

type coldChangedPolicyControl struct{ *coldVerifiedControl }

func (c *coldChangedPolicyControl) ResumeActorExact(ctx context.Context, ref substrate.ActorRef, _ string) (substrate.ActorInfo, error) {
	info, err := c.mockControl.ResumeActor(ctx, ref)
	info.ExecutionRequirement = `{"other":true}`
	return info, err
}

type coldMissingUIDControl struct{ *coldVerifiedControl }

func (c *coldMissingUIDControl) ResumeActorExact(ctx context.Context, ref substrate.ActorRef, _ string) (substrate.ActorInfo, error) {
	info, err := c.mockControl.ResumeActor(ctx, ref)
	info.UID = ""
	return info, err
}
