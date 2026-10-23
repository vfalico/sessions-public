package client_test

import (
	"bytes"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

// The API freezes authority on Session, not individual Exec requests. Real
// service+SQLite verify SDK carriage and pre-dispatch callback refusal.
func TestSDKFrozenRequirementReadbackAndExecutionRefusal(t *testing.T) {
	c := newClient(t)
	raw := []byte(`{"opaque":{"integer":9007199254740993}}`)
	created, err := c.CreateSession(t.Context(), &v1.Session{Harness: "echo", ExecutionRequirement: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, created.ExecutionRequirement) {
		t.Fatal("creation erased requirement")
	}
	uid := created.GetMetadata().GetUid()
	raw[0] = 'X'
	read, err := c.GetSession(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read.ExecutionRequirement, created.ExecutionRequirement) {
		t.Fatal("stored authority changed")
	}
	_, err = c.Exec(t.Context(), client.ExecOptions{Session: uid, Inputs: []string{"must not dispatch host model"}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal("governed callback execution did not refuse", err)
	}
}
