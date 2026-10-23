package main

import (
	"context"
	"encoding/json"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harnesswire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Real provider HTTP and harness gRPC transports, but no compute isolation.
func TestNativeChatProviderHTTPAndReplay(t *testing.T) {
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("provider configuration lost")
		}
		var body struct {
			Model    string                           `json:"model"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "native-test" || len(body.Messages) != 1 || body.Messages[0].Content != "hello" {
			t.Error("invalid provider request", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"native HTTP answer"}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`))
		if err != nil {
			t.Error(err)
		}
	}))
	defer provider.Close()
	t.Setenv("HARNESS_KIND", "chat")
	t.Setenv("HARNESS_MODEL", "native-test")
	t.Setenv("HARNESS_MODEL_PLACEMENT", "native")
	t.Setenv("HARNESS_MODEL_BASE_URL", provider.URL)
	t.Setenv("HARNESS_MODEL_API_KEY", "fixture-key")
	t.Setenv("HARNESS_MODEL_PATH", "/v1/chat/completions")
	harness, err := selectHarness()
	if err != nil {
		t.Fatal(err)
	}
	server, err := configuredServer(harness)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	v1.RegisterHarnessServer(g, server)
	go g.Serve(listener)
	defer g.Stop()
	conn, err := grpc.NewClient("passthrough:///native", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
	log := eventlog.AsStore(eventlog.New())
	c, err := controller.New(log, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Exec(t.Context(), remote, []api.Message{*api.TextMessage("user", "hello")}, 0); err != nil {
		t.Fatal(err)
	}
	live, err := c.Outputs()
	if err != nil || len(live) != 1 || live[0] != "native HTTP answer" || requests != 1 {
		t.Fatal(live, requests, err)
	}
	c2, err := controller.New(log, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := c2.Replay(t.Context(), remote)
	if err != nil || len(replay) != 1 || replay[0] != live[0] || requests != 1 {
		t.Fatal("replay called provider", replay, requests, err)
	}
}
