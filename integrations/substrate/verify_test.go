package ateadapter

import (
	"context"
	"errors"
	"github.com/agent-primitives/sessions/runtime/substrate"
	atepb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"testing"
)

type verifyControl struct {
	*fakeControl
	active  bool
	err     error
	request *atepb.VerifyActorExecutionRequest
}

func (c *verifyControl) VerifyActorExecution(_ context.Context, r *atepb.VerifyActorExecutionRequest, _ ...grpc.CallOption) (*atepb.VerifyActorExecutionResponse, error) {
	c.request = r
	return &atepb.VerifyActorExecutionResponse{Active: c.active}, c.err
}
func TestVerifyActorExecutionPreservesExactIdentityAndRequiresActive(t *testing.T) {
	for _, scenario := range []string{"active", "inactive", "error", "missing-uid", "missing-requirement"} {
		t.Run(scenario, func(t *testing.T) {
			c := &verifyControl{fakeControl: &fakeControl{}, active: true}
			uid := "exact"
			raw := []byte(`{"opaque":true}`)
			switch scenario {
			case "inactive":
				c.active = false
			case "error":
				c.err = errors.New("unavailable")
			case "missing-uid":
				uid = ""
			case "missing-requirement":
				raw = nil
			}
			err := (&Adapter{ctl: c}).VerifyActorExecution(context.Background(), substrate.ActorRef{Atespace: "native", Name: "owned"}, uid, raw)
			if (err == nil) != (scenario == "active") {
				t.Fatal(scenario, err)
			}
			if uid == "" || len(raw) == 0 {
				if c.request != nil {
					t.Fatal("invalid request sent")
				}
				return
			}
			if c.request.GetActor().GetName() != "owned" || c.request.GetActor().GetAtespace() != "native" || c.request.GetActorUid() != uid || string(c.request.GetExecutionRequirement()) != string(raw) {
				t.Fatal("identity lost", c.request)
			}
			raw[0] = 'X'
			if c.request.ExecutionRequirement[0] == 'X' {
				t.Fatal("caller can mutate exact request")
			}
			if len(c.calls) != 0 {
				t.Fatal("verification performed lifecycle mutation", c.calls)
			}
		})
	}
}
