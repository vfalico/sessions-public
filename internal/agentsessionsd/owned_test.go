package agentsessionsd

import (
	"bytes"
	"context"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/harness/echoagent"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/sqlitelog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnedRuntimeDoesNotInheritHostCallbacks(t *testing.T) {
	backend := local.New(echoagent.Harness{})
	defer backend.Close()
	called := false
	model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		called = true
		return api.ModelResponse{}, nil
	}
	tool := func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		called = true
		return api.ToolResult{}, nil
	}
	registry, closeAll, err := harnessRegistry("test-model", nil, model, nil, nil, map[string]controller.ToolFunc{"native": tool}, map[string]placement.Backend{"native": backend})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	// Local cannot enforce; admission must reach that refusal, not the earlier
	// callback containment refusal. This proves no ambient callbacks were bound.
	p, err := registry.For("native")
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitelog.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = p.Exec(t.Context(), store.Session("owned"), "owned", nil, 0, placement.WithExecutionRequirement([]byte(`{"opaque":true}`)))
	if err == nil || called || !strings.Contains(err.Error(), "owning backend does not support") {
		t.Fatal("owned route used host callback or admitted local enforcement", err)
	}
}
func TestOwnedRuntimeNamesAreFrozenAndCollisionRefused(t *testing.T) {
	backend := local.New(echoagent.Harness{})
	defer backend.Close()
	entries := map[string]placement.Backend{"native": backend}
	opt := WithOwnedRuntimes(entries)
	delete(entries, "native")
	var cfg options
	opt(&cfg)
	if cfg.owned["native"] != backend {
		t.Fatal("caller mutated owned registry")
	}
	for _, name := range []string{"", "echo", "chat"} {
		registry, closeAll, err := harnessRegistry("test-model", nil, echoagent.Model, nil, nil, nil, map[string]placement.Backend{name: backend})
		if closeAll != nil {
			closeAll()
		}
		if err == nil || registry != nil {
			t.Fatal("collision accepted", name)
		}
	}
}

// This backend deliberately claims enforcement capability without enforcing.
// It is solely a wiring double; never use this test as boundary acceptance.
type ownedWiringBackend struct {
	*local.Backend
	seen    []byte
	creates int
}

func (*ownedWiringBackend) ExecutionRequirementRuntime() {}
func (b *ownedWiringBackend) Create(ctx context.Context, s *api.SessionSpec) (api.Incarnation, error) {
	b.seen = append([]byte(nil), s.ExecutionRequirement...)
	b.creates++
	clone := *s
	clone.ExecutionRequirement = nil
	return b.Backend.Create(ctx, &clone)
}
func TestOwnedRuntimeThroughActualDaemonListener(t *testing.T) {
	backend := &ownedWiringBackend{Backend: local.New(echoagent.Harness{})}
	defer backend.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	addresses := make(chan string, 1)
	done := make(chan error, 1)
	journal := filepath.Join(t.TempDir(), "owned.db")
	go func() {
		done <- Run([]string{"-addr", "127.0.0.1:0", "-journal", journal}, WithOwnedRuntimes(map[string]placement.Backend{"native": backend}), func(o *options) { o.ctx = ctx; o.listening = func(addr string) { addresses <- addr } })
	}()
	var address string
	select {
	case address = <-addresses:
	case err := <-done:
		t.Fatal("daemon failed", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not listen")
	}
	c, err := client.Dial(address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw := []byte(`{ "format":"non-suite/v1", "opaque":9007199254740993 }`)
	created, err := c.CreateSession(t.Context(), &v1.Session{Harness: "native", ExecutionRequirement: raw})
	if err != nil || !bytes.Equal(created.GetExecutionRequirement(), raw) {
		t.Fatal("public creation lost authority", err)
	}
	_, err = c.Exec(t.Context(), client.ExecOptions{Session: created.GetMetadata().GetUid(), Inputs: []string{"hello"}})
	// Echo tries sink.Model. It must fail instead of inheriting the built-in echo
	// model function, even though the actual runtime marker accepted admission.
	if err == nil || backend.creates != 1 || !bytes.Equal(backend.seen, raw) {
		t.Fatal("daemon wiring/callback containment changed", backend.creates, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestOwnedChatNameOnlyWithoutHostChat(t *testing.T) {
	b := local.New(echoagent.Harness{})
	defer b.Close()
	r, closeAll, err := harnessRegistry("", nil, echoagent.Model, nil, nil, nil, map[string]placement.Backend{"chat": b})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if !r.Static("chat") {
		t.Fatal("owned chat was not statically reserved")
	}
	if _, err := r.For("chat"); err != nil {
		t.Fatal(err)
	}
}
