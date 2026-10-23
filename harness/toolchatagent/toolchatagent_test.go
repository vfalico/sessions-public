package toolchatagent_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
)

func TestFetchRefusesRedirectsCredentialsAndOversize(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); w.Write([]byte("ok")) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	oversize := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", toolchatagent.MaxResponseBytes+1)))
	}))
	defer oversize.Close()
	for _, raw := range []string{redirect.URL, oversize.URL, "file:///etc/passwd", "http://user:secret@127.0.0.1/", target.URL + "/#fragment"} {
		_, err := toolchatagent.Fetch(t.Context(), api.ToolCall{Tool: toolchatagent.ToolName, Args: map[string]any{"url": raw}})
		if err == nil {
			t.Fatal("unsafe request accepted", raw)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("redirect/credential destination reached", reached.Load())
	}
	r, err := toolchatagent.Fetch(t.Context(), api.ToolCall{ID: "call", Tool: toolchatagent.ToolName, Args: map[string]any{"url": target.URL}})
	if err != nil || r.ID != "call" || len(r.Content) != 1 || r.Content[0].Text.Text != "ok" || reached.Load() != 1 {
		t.Fatal(r, err, reached.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := toolchatagent.Fetch(ctx, api.ToolCall{Tool: toolchatagent.ToolName, Args: map[string]any{"url": target.URL}}); err == nil {
		t.Fatal("cancellation hidden")
	}
	if reached.Load() != 1 {
		t.Fatal("cancelled fetch reached receiver")
	}
}

type configSink struct {
	api.EventSink
	calls int
}

func (s *configSink) Model(context.Context, api.ModelRequest) (api.ModelResponse, error) {
	s.calls++
	return api.ModelResponse{}, errors.New("unexpected model")
}
func TestInvalidToolchatConfigRefusesBeforeModel(t *testing.T) {
	for _, raw := range []string{`{"unknown":true}`, `{"system_prompt":"a"} {}`, `{`, `null`} {
		sink := &configSink{}
		err := (toolchatagent.Harness{Model: "fixture"}).Run(t.Context(), &api.Start{Config: []byte(raw)}, sink)
		if err == nil || sink.calls != 0 {
			t.Fatal("invalid config reached model", raw, err, sink.calls)
		}
	}
}
