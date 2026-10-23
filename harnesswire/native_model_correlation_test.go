package harnesswire

import (
	"context"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"testing"
	"time"
)

func TestNativeGrantMustMatchCallAndSession(t *testing.T) {
	for _, mode := range []string{"wrong-call", "wrong-session", "no-native-provider"} {
		t.Run(mode, func(t *testing.T) {
			server := &cancellationConnectServer{sent: make(chan *v1.Event, 4)}
			results := make(chan *v1.ControllerFrame, 1)
			done := make(chan struct{})
			defer close(done)
			calls := 0
			sink := &streamSink{stream: server, results: results, done: done, session: "s", executionID: "e", nativeModel: func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				calls++
				return api.ModelResponse{}, nil
			}}
			if mode == "no-native-provider" {
				sink.nativeModel = nil
			}
			finished := make(chan error, 1)
			go func() { _, err := sink.Model(t.Context(), api.ModelRequest{Model: "test"}); finished <- err }()
			var ev *v1.Event
			select {
			case ev = <-server.sent:
			case <-time.After(time.Second):
				t.Fatal("model not requested")
			}
			call, session := ev.GetModel().GetId(), "s"
			if mode == "wrong-call" {
				call = "other"
			}
			if mode == "wrong-session" {
				session = "other"
			}
			results <- &v1.ControllerFrame{ExecutionId: "e", Session: session, Frame: &v1.ControllerFrame_NativeModelCallId{NativeModelCallId: call}}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("uncorrelated grant accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("invalid grant blocked")
			}
			if calls != 0 || len(server.sent) != 0 {
				t.Fatal("invalid grant ran provider", calls)
			}
		})
	}
}
func TestNativeProviderReceivesCancellation(t *testing.T) {
	server := &cancellationConnectServer{sent: make(chan *v1.Event, 4)}
	results := make(chan *v1.ControllerFrame, 1)
	done := make(chan struct{})
	defer close(done)
	started := make(chan struct{})
	finished := make(chan error, 1)
	sink := &streamSink{stream: server, results: results, done: done, session: "s", executionID: "e", nativeModel: func(ctx context.Context, _ api.ModelRequest) (api.ModelResponse, error) {
		close(started)
		<-ctx.Done()
		return api.ModelResponse{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _, err := sink.Model(ctx, api.ModelRequest{Model: "test"}); finished <- err }()
	ev := <-server.sent
	results <- &v1.ControllerFrame{ExecutionId: "e", Session: "s", Frame: &v1.ControllerFrame_NativeModelCallId{NativeModelCallId: ev.GetModel().GetId()}}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider never started")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancellation hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("provider cancellation blocked")
	}
}
