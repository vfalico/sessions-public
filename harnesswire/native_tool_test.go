package harnesswire_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/sqlitelog"
)

type nativeToolHarness struct {
	call api.ToolCall
	got  *api.ToolResult
}

func (nativeToolHarness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{ID: "native-tools", Version: "1", Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}, nil
}
func (h nativeToolHarness) Run(ctx context.Context, _ *api.Start, sink api.EventSink) error {
	r, e := sink.ToolCall(ctx, h.call)
	if h.got != nil {
		*h.got = r
	}
	return e
}
func nativeCall() api.ToolCall {
	return api.ToolCall{ID: "call-native-1", Tool: "http", Args: map[string]any{"url": "http://example.invalid/"}, Mediation: api.MediationControllerMediated, IdempotencyKey: "native-key-1"}
}

func TestNativeToolRecordedBeforeGrantReplayNoIO(t *testing.T) {
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	log := store.Session("native-tool")
	calls, hosts := 0, 0
	native := func(_ context.Context, call api.ToolCall) (api.ToolResult, error) {
		recs, e := log.Read(1)
		if e != nil {
			return api.ToolResult{}, e
		}
		if len(recs) == 0 || recs[len(recs)-1].Event.Kind != api.EventToolCall || !recs[len(recs)-1].Event.ToolCall.Native {
			t.Error("native IO before durable intent")
		}
		calls++
		if !call.Native || call.ID != "call-native-1" {
			t.Error("native identity lost", call)
		}
		return api.ToolResult{ID: "executor-cannot-change-id", Output: map[string]any{"ok": "native"}}, nil
	}
	host := func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		hosts++
		return api.ToolResult{}, errors.New("host must never execute")
	}
	var got api.ToolResult
	remote := dial(t, harnesswire.NewNativeToolServer(nativeToolHarness{nativeCall(), &got}, nil, native))
	c, e := controller.New(log, nil, controller.WithSessionUID("native-tool"), controller.WithToolExecutor(host))
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Exec(t.Context(), remote, nil, 0); e != nil {
		t.Fatal(e)
	}
	if calls != 1 || hosts != 0 || got.ID != "call-native-1" || got.Output["ok"] != "native" {
		t.Fatal(calls, hosts, got)
	}
	before, e := log.Read(1)
	if e != nil {
		t.Fatal(e)
	}
	c2, e := controller.New(log, nil, controller.WithSessionUID("native-tool"), controller.WithToolExecutor(host))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c2.Replay(t.Context(), remote); e != nil {
		t.Fatal(e)
	}
	after, e := log.Read(1)
	if e != nil || !reflect.DeepEqual(before, after) || calls != 1 || hosts != 0 {
		t.Fatal("replay performed IO or wrote journal", e, calls, hosts)
	}
	// Same ID/key/args with a different placement must diverge before any executor.
	ordinary := dial(t, harnesswire.NewServer(nativeToolHarness{nativeCall(), nil}))
	c3, e := controller.New(log, nil, controller.WithSessionUID("native-tool"), controller.WithToolExecutor(host))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c3.Replay(t.Context(), ordinary); !errors.Is(e, controller.ErrReplayDiverged) {
		t.Fatal("placement drift accepted", e)
	}
	if hosts != 0 || calls != 1 {
		t.Fatal("drift executed", calls, hosts)
	}
	if e = log.Verify(); e != nil {
		t.Fatal(e)
	}
}
func TestNativeToolTerminalIntentRecoveryAndReplay(t *testing.T) {
	log := eventlog.AsStore(eventlog.New())
	calls := 0
	native := func(context.Context, api.ToolCall) (api.ToolResult, error) {
		calls++
		if calls == 1 {
			return api.ToolResult{}, fmt.Errorf("offline")
		}
		return api.ToolResult{Output: map[string]any{"recovered": true}}, nil
	}
	remote := dial(t, harnesswire.NewNativeToolServer(nativeToolHarness{nativeCall(), nil}, nil, native))
	c, e := controller.New(log, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Exec(t.Context(), remote, nil, 0); e == nil {
		t.Fatal("failure hidden")
	}
	c2, e := controller.New(log, nil)
	if e != nil {
		t.Fatal(e)
	}
	if resumed, e := c2.Resume(t.Context(), remote); e != nil || !resumed || calls != 2 {
		t.Fatal("native recovery", resumed, calls, e)
	}
	c3, e := controller.New(log, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c3.Replay(t.Context(), remote); e != nil || calls != 2 {
		t.Fatal("replay executed", e, calls)
	}
}
func TestNativeToolRejectsApprovalAndMissingIDsBeforeIO(t *testing.T) {
	for _, mode := range []string{"approval", "no-id", "no-key"} {
		t.Run(mode, func(t *testing.T) {
			call := nativeCall()
			switch mode {
			case "approval":
				call.Mediation = api.MediationRequiresApproval
			case "no-id":
				call.ID = ""
			case "no-key":
				call.IdempotencyKey = ""
			}
			count := 0
			remote := dial(t, harnesswire.NewNativeToolServer(nativeToolHarness{call, nil}, nil, func(context.Context, api.ToolCall) (api.ToolResult, error) { count++; return api.ToolResult{}, nil }))
			log := eventlog.AsStore(eventlog.New())
			c, e := controller.New(log, nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = c.Exec(t.Context(), remote, nil, 0); e == nil || count != 0 {
				t.Fatal("invalid native authority reached executor", e, count)
			}
			recs, e := log.Read(1)
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range recs {
				if r.Event.ToolCall != nil {
					t.Fatal("invalid call recorded")
				}
			}
		})
	}
}
