package substrate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

// A control-interface adversarial observation, NOT a native immutable-API bypass.
// The UID stays fixed while the control response loses requirement metadata.
func TestGovernedStopMustNotDowngradeAfterSameUIDPolicyLoss(t *testing.T) {
	m := &mockControl{info: substrate.ActorInfo{ExecutionRequirement: `{"opaque":true}`}}
	m.exists("owned", substrate.StatusRunning)
	control := &verifiedWarmControl{mockControl: m}
	backend := substrate.New(control, "space", substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, api.Descriptor{ID: "echo"})
	in, err := backend.Create(context.Background(), &api.SessionSpec{SessionUID: "owned", ExecutionRequirement: []byte(`{"opaque":true}`), ExpectedComputeInstance: m.uid("owned")})
	if err != nil {
		t.Fatal(err)
	}
	if in.ComputeInstance != m.uid("owned") {
		t.Fatal("governed creation did not pin UID")
	}
	m.calls = nil
	m.info.ExecutionRequirement = ""
	err = backend.Stop(context.Background(), in)
	calls := strings.Join(m.calls, ",")
	if strings.Contains(calls, "suspend:") || strings.Contains(calls, "delete:") {
		t.Fatalf("SAME_UID_POLICY_LOSS_DOWNGRADED_STOP: err=%v calls=%s", err, calls)
	}
}
