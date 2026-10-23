package harnesswire

import (
	"context"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"testing"
	"time"
)

func TestNativeToolGrantRejectsWrongAuthority(t *testing.T) {
	for _, mode := range []string{"wrong-call", "wrong-session", "wrong-execution", "unconfigured"} {
		t.Run(mode, func(t *testing.T) {
			server := &cancellationConnectServer{sent: make(chan *v1.Event, 4)}
			results := make(chan *v1.ControllerFrame, 1)
			done := make(chan struct{})
			defer close(done)
			calls := 0
			sink := &streamSink{stream: server, results: results, done: done, session: "s", executionID: "e", nativeTool: func(context.Context, api.ToolCall) (api.ToolResult, error) { calls++; return api.ToolResult{}, nil }}
			if mode == "unconfigured" {
				sink.nativeTool = nil
			}
			finished := make(chan error, 1)
			go func() {
				_, e := sink.ToolCall(t.Context(), api.ToolCall{ID: "call", Tool: "http", Mediation: api.MediationControllerMediated, IdempotencyKey: "key"})
				finished <- e
			}()
			select {
			case <-server.sent:
			case <-time.After(time.Second):
				t.Fatal("no tool request")
			}
			call, session, execution := "call", "s", "e"
			switch mode {
			case "wrong-call":
				call = "other"
			case "wrong-session":
				session = "other"
			case "wrong-execution":
				execution = "other"
			}
			results <- &v1.ControllerFrame{Session: session, ExecutionId: execution, Frame: &v1.ControllerFrame_NativeToolCallId{NativeToolCallId: call}}
			select {
			case e := <-finished:
				if e == nil {
					t.Fatal("invalid authority accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("invalid authority blocked")
			}
			if calls != 0 || len(server.sent) != 0 {
				t.Fatal("invalid authority invoked tool", calls)
			}
		})
	}
}
func TestNativeToolCancellationReachesExecutor(t *testing.T) {
	server := &cancellationConnectServer{sent: make(chan *v1.Event, 4)}
	results := make(chan *v1.ControllerFrame, 1)
	done := make(chan struct{})
	defer close(done)
	started := make(chan struct{})
	sink := &streamSink{stream: server, results: results, done: done, session: "s", executionID: "e", nativeTool: func(ctx context.Context, _ api.ToolCall) (api.ToolResult, error) {
		close(started)
		<-ctx.Done()
		return api.ToolResult{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, e := sink.ToolCall(ctx, api.ToolCall{ID: "call", Tool: "http", Mediation: api.MediationControllerMediated, IdempotencyKey: "key"})
		finished <- e
	}()
	select {
	case <-server.sent:
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	results <- &v1.ControllerFrame{Session: "s", ExecutionId: "e", Frame: &v1.ControllerFrame_NativeToolCallId{NativeToolCallId: "call"}}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool not started")
	}
	cancel()
	select {
	case e := <-finished:
		if e == nil {
			t.Fatal("cancellation hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("executor did not cancel")
	}
}
