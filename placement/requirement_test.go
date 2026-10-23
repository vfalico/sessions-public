package placement_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/sqlitelog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
)

func TestRequirementRefusesUncontainedHostCallbacksBeforeCompute(t *testing.T) {
	for _, kind := range []string{"model", "stream", "tool"} {
		t.Run(kind, func(t *testing.T) {
			store, err := sqlitelog.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			log := store.Session("governed")
			var model controller.ModelFunc
			var opts []placement.Option
			called := false
			switch kind {
			case "model":
				model = func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
					called = true
					return api.ModelResponse{}, nil
				}
			case "stream":
				opts = append(opts, placement.WithStreamingModel(func(context.Context, api.ModelRequest, func(string)) (api.ModelResponse, error) {
					called = true
					return api.ModelResponse{}, nil
				}))
			case "tool":
				opts = append(opts, placement.WithToolExecutor(func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
					called = true
					return api.ToolResult{}, nil
				}))
			}
			// Nil backend makes any accidental Describe/Create a visible panic.
			p := placement.New(nil, model, opts...)
			_, err = p.Exec(t.Context(), log, "governed", nil, 0, placement.WithExecutionRequirement([]byte(`{"opaque":true}`)))
			if err == nil || !strings.Contains(err.Error(), "host model/tool callbacks") {
				t.Fatal(err)
			}
			head, e := log.Head()
			if e != nil || head != 0 || called {
				t.Fatal("effect before refusal", head, e, called)
			}
		})
	}
}

// Fake runtime checks lifecycle dispatch only; no isolation is claimed.
type governedLifecycleRuntime struct {
	*local.Backend
	uid              string
	stopped          []api.Incarnation
	stopErr          error
	cleanupCancelled bool
}

func (*governedLifecycleRuntime) ExecutionRequirementRuntime() {}
func (b *governedLifecycleRuntime) Create(ctx context.Context, spec *api.SessionSpec) (api.Incarnation, error) {
	inc, err := b.Backend.Create(ctx, spec)
	inc.ComputeInstance = b.uid
	return inc, err
}
func (b *governedLifecycleRuntime) Stop(ctx context.Context, inc api.Incarnation) error {
	b.stopped = append(b.stopped, inc)
	b.cleanupCancelled = ctx.Err() != nil
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("cleanup requires deadline")
	}
	return b.stopErr
}

type lifecycleHarness struct {
	scenario string
	cancel   context.CancelFunc
}

func (h lifecycleHarness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{ID: "lifecycle", Version: "1", Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}, nil
}
func (h lifecycleHarness) Run(ctx context.Context, _ *api.Start, sink api.EventSink) error {
	if h.cancel != nil {
		h.cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	if h.scenario == "turn-failure" || h.scenario == "retirement-failure" {
		return fmt.Errorf("fixture native failure")
	}
	return sink.Output(ctx, "ok")
}
func TestGovernedFailedTurnRetiresExactCompute(t *testing.T) {
	for _, scenario := range []string{"success", "turn-failure", "cancelled", "dial-failure", "retirement-failure", "missing-identity", "policy-free"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h := lifecycleHarness{scenario: scenario}
			if scenario == "cancelled" {
				h.cancel = cancel
			}
			backend := &governedLifecycleRuntime{Backend: local.New(h), uid: "exact-incarnation-7"}
			defer backend.Close()
			if scenario == "missing-identity" {
				backend.uid = ""
			}
			if scenario == "retirement-failure" {
				backend.stopErr = errors.New("retirement fixture unavailable")
			}
			var options []placement.Option
			if scenario == "dial-failure" {
				options = append(options, placement.WithDialer(func(api.Incarnation) (api.Harness, func() error, error) {
					return nil, nil, fmt.Errorf("fixture dial failure")
				}))
			}
			placer := placement.New(backend, nil, options...)
			store, err := sqlitelog.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var execOpts []placement.ExecOption
			if scenario != "policy-free" {
				execOpts = append(execOpts, placement.WithExecutionRequirement([]byte(`{"opaque":"test"}`)))
			}
			_, err = placer.Exec(ctx, store.Session("owned"), "owned", nil, 0, execOpts...)
			expectedStop := scenario == "turn-failure" || scenario == "cancelled" || scenario == "dial-failure" || scenario == "retirement-failure"
			if expectedStop {
				if err == nil || len(backend.stopped) != 1 || backend.cleanupCancelled {
					t.Fatal("retirement missing", err, backend.stopped, backend.cleanupCancelled)
				}
				if string(backend.stopped[0].ExecutionRequirement) != `{"opaque":"test"}` {
					t.Fatal("failed launch lost original governed intent")
				}
				if backend.stopped[0].ID != "owned" || backend.stopped[0].ComputeInstance != "exact-incarnation-7" {
					t.Fatal("wrong retirement authority", backend.stopped)
				}
			} else if len(backend.stopped) != 0 {
				t.Fatal("unexpected teardown", backend.stopped)
			}
			if scenario == "missing-identity" && (err == nil || !strings.Contains(err.Error(), "ownership retained")) {
				t.Fatal("identity omitted", err)
			}
			if scenario == "retirement-failure" && !errors.Is(err, backend.stopErr) {
				t.Fatal("retirement error swallowed", err)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
				t.Fatal("cancellation lost", err)
			}
			if (scenario == "success" || scenario == "policy-free") && err != nil {
				t.Fatal(err)
			}
		})
	}
}
