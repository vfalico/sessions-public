package ateadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/runtime/substrate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Protocol mapping only; no claim of native enforcement or server-side lease.
func TestExactCreateIdentityAndResumePreconditionMapping(t *testing.T) {
	f := &fakeControl{actor: runningActor()}
	ad := FromClient(f)
	ref := substrate.ActorRef{Atespace: "space", Name: "owned"}
	raw := []byte(`{"opaque":"ceiling"}`)
	info, err := ad.CreateActorWithRequirementIdentity(t.Context(), ref, substrate.ObjectRef{Atespace: "tmpl", Name: "echo"}, raw)
	if err != nil || info.UID != "uid-owned" || info.Status != substrate.StatusSuspended || info.ExecutionRequirement != string(raw) {
		t.Fatal("creation identity lost", info, err)
	}
	raw[0] = '!'
	if info.ExecutionRequirement != `{"opaque":"ceiling"}` || string(f.lastCreate.ExecutionRequirement) != `{"opaque":"ceiling"}` {
		t.Fatal("request aliases source")
	}
	if len(f.calls) != 1 {
		t.Fatal("identity rediscovered by name", f.calls)
	}
	if _, err = ad.ResumeActorExact(t.Context(), ref, info.UID); err != nil {
		t.Fatal(err)
	}
	if f.lastResume.GetActorUid() != info.UID || f.lastResume.GetActor().GetName() != "owned" || f.lastResume.GetActor().GetAtespace() != "space" {
		t.Fatal("UID precondition dropped", f.lastResume)
	}
	calls := len(f.calls)
	if _, err = ad.ResumeActorExact(t.Context(), ref, ""); err == nil || len(f.calls) != calls {
		t.Fatal("empty identity dispatched")
	}
}
func TestExactCreateUncertainReplyDoesNotRediscoverIdentity(t *testing.T) {
	for _, code := range []codes.Code{codes.DeadlineExceeded, codes.Unavailable, codes.AlreadyExists} {
		t.Run(code.String(), func(t *testing.T) {
			f := &fakeControl{createErr: status.Error(code, "injected reply loss")}
			info, err := FromClient(f).CreateActorWithRequirementIdentity(context.Background(), substrate.ActorRef{Atespace: "space", Name: "owned"}, substrate.ObjectRef{Name: "echo"}, []byte("opaque"))
			if err == nil || info.UID != "" || len(f.calls) != 1 {
				t.Fatal("uncertain response became authority", info, err, f.calls)
			}
			if code == codes.AlreadyExists && !errors.Is(err, substrate.ErrActorExists) {
				t.Fatal("lost conflict", err)
			}
		})
	}
}
