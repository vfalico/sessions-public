package harnesswire_test

import (
	"context"
	"errors"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/sqlitelog"
	"reflect"
	"testing"
)

func TestNativeModelRecordedBeforeGrantAndReplayNeverInvokes(t *testing.T) {
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	log := store.Session("native")
	calls := 0
	hostCalls := 0
	native := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		records, err := log.Read(1)
		if err != nil {
			return api.ModelResponse{}, err
		}
		if len(records) == 0 || records[len(records)-1].Event.Kind != api.EventModelCall {
			t.Error("provider ran before intent was committed")
		}
		calls++
		return api.ModelResponse{Message: *api.TextMessage("assistant", "native answer"), Usage: api.Usage{Model: "test", InputTokens: 3, OutputTokens: 2}}, nil
	}
	remote := dial(t, harnesswire.NewNativeServer(modelHarness{req: api.ModelRequest{Model: "test", Messages: []api.Message{*api.TextMessage("user", "hello")}}}, native))
	host := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		hostCalls++
		return api.ModelResponse{}, errors.New("host provider must not run")
	}
	c, err := controller.New(log, host)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Exec(t.Context(), remote, []api.Message{*api.TextMessage("user", "hello")}, 0); err != nil {
		t.Fatal(err)
	}
	live, err := c.Outputs()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || hostCalls != 0 || len(live) != 1 || live[0] != "native answer" {
		t.Fatal(calls, hostCalls, live)
	}
	c2, err := controller.New(log, host)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := c2.Replay(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || hostCalls != 0 || !reflect.DeepEqual(live, replayed) || c2.ModelInvocations() != 0 {
		t.Fatal("replay invoked provider", calls, hostCalls, replayed)
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
	// Native/host placement is included in input hash; a restart cannot silently
	// replay a native invocation as host-mediated execution.
	ordinary := dial(t, harnesswire.NewServer(modelHarness{req: api.ModelRequest{Model: "test", Messages: []api.Message{*api.TextMessage("user", "hello")}}}))
	c3, err := controller.New(log, host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c3.Replay(t.Context(), ordinary); err == nil {
		t.Fatal("placement change accepted on replay")
	}
	if hostCalls != 0 {
		t.Fatal("replay invoked host provider")
	}
}
func TestNativeProviderFailureCanRecoverWithoutHostFallback(t *testing.T) {
	log := eventlog.AsStore(eventlog.New())
	calls := 0
	provider := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		if calls == 1 {
			return api.ModelResponse{}, errors.New("offline")
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", "recovered")}, nil
	}
	remote := dial(t, harnesswire.NewNativeServer(modelHarness{req: api.ModelRequest{Model: "test"}}, provider))
	c, err := controller.New(log, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Exec(t.Context(), remote, nil, 0); err == nil {
		t.Fatal("provider failure hidden")
	}
	c2, err := controller.New(log, nil)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := c2.Resume(t.Context(), remote)
	if err != nil || !resumed || calls != 2 {
		t.Fatal(resumed, calls, err)
	}
	outputs, err := c2.Outputs()
	if err != nil || len(outputs) != 1 || outputs[0] != "recovered" {
		t.Fatal(outputs, err)
	}
	c3, err := controller.New(log, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c3.Replay(t.Context(), remote); err != nil || calls != 2 {
		t.Fatal("completed recovery called provider", calls, err)
	}
}
