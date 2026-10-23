package harnesswire_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/model/openai"
)

// exercises both effects through the public harness stream and actual HTTP.
// No compute isolation: this is transport/replay proof, NOT native enforcement.
type nativeModelToolHarness struct{ url string }

func (nativeModelToolHarness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{ID: "native-model-tool", Version: "1", Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}, nil
}
func (h nativeModelToolHarness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	messages := append([]api.Message(nil), start.Inputs...)
	first, e := sink.Model(ctx, api.ModelRequest{Model: "fixture", Messages: messages, Tools: []api.ToolDefinition{{Name: "fetch", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}}}}})
	if e != nil {
		return e
	}
	messages = append(messages, first.Message)
	for _, part := range first.Message.Parts {
		if part.ToolCall != nil {
			call := *part.ToolCall
			call.Mediation = api.MediationControllerMediated
			call.IdempotencyKey = start.ExecutionID + "/" + call.ID
			result, e := sink.ToolCall(ctx, call)
			if e != nil {
				return e
			}
			messages = append(messages, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &result}}})
		}
	}
	_, e = sink.Model(ctx, api.ModelRequest{Model: "fixture", Messages: messages})
	return e
}
func nativeModelToolHTTP(t *testing.T, production bool) {
	toolName := "fetch"
	if production {
		toolName = toolchatagent.ToolName
	}
	var requests, tools atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tools.Add(1); io.WriteString(w, "tool-native-ok") }))
	defer receiver.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if r.URL.Path != "/responses" {
			t.Error("wrong protocol", r.URL.Path)
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		if n == 1 {
			args, _ := json.Marshal(map[string]string{"url": receiver.URL})
			argstr, _ := json.Marshal(string(args))
			fmt.Fprintf(w, `{"status":"completed","output":[{"type":"function_call","id":"item","call_id":"fetch-1","name":%q,"arguments":%s,"status":"completed"}]}`, toolName, argstr)
		} else {
			var body map[string]any
			if e = json.Unmarshal(raw, &body); e != nil {
				t.Error(e)
			}
			found := false
			for _, item := range body["input"].([]any) {
				m := item.(map[string]any)
				if m["type"] == "function_call_output" && m["output"] == "tool-native-ok" {
					found = true
				}
			}
			if !found {
				t.Error("native result missing from provider input", string(raw))
			}
			io.WriteString(w, `{"status":"completed","output":[{"type":"message","id":"msg-final","role":"assistant","content":[{"type":"output_text","text":"complete"}]}]}`)
		}
	}))
	defer provider.Close()
	model, e := openai.NewResponses(openai.WithModel("fixture"), openai.WithBaseURL(provider.URL))
	if e != nil {
		t.Fatal(e)
	}
	native := func(ctx context.Context, call api.ToolCall) (api.ToolResult, error) {
		if call.Tool != "fetch" || call.Args["url"] != receiver.URL {
			return api.ToolResult{}, fmt.Errorf("unexpected tool request")
		}
		req, e := http.NewRequestWithContext(ctx, "GET", receiver.URL, nil)
		if e != nil {
			return api.ToolResult{}, e
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return api.ToolResult{}, e
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		return api.ToolResult{Content: []api.Part{{Text: &api.TextPart{Text: string(raw)}}}}, e
	}
	var h api.Harness = nativeModelToolHarness{receiver.URL}
	if production {
		h = toolchatagent.Harness{Model: "fixture"}
		native = toolchatagent.Fetch
	}
	remote := dial(t, harnesswire.NewNativeToolServer(h, func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		r, e := model.Model(ctx, req)
		if e != nil {
			t.Errorf("native provider underlying error: %v", e)
		}
		return r, e
	}, native))
	log := eventlog.AsStore(eventlog.New())
	c, e := controller.New(log, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Exec(t.Context(), remote, []api.Message{*api.TextMessage("user", "fetch the URL")}, 0); e != nil {
		t.Fatal(e)
	}
	if requests.Load() != 2 || tools.Load() != 1 {
		t.Fatal("effects", requests.Load(), tools.Load())
	}
	c2, e := controller.New(log, nil)
	if e != nil {
		t.Fatal(e)
	}
	result, e := c2.Replay(t.Context(), remote)
	if e != nil || requests.Load() != 2 || tools.Load() != 1 {
		t.Fatal("replay effects", e, result, requests.Load(), tools.Load())
	}
}

func TestNativeModelToolHTTPRoundTripReplayNoEffects(t *testing.T)    { nativeModelToolHTTP(t, false) }
func TestProductionToolchatHTTPRoundTripReplayNoEffects(t *testing.T) { nativeModelToolHTTP(t, true) }
