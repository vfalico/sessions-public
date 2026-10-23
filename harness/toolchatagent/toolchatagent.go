// Package toolchatagent implements a bounded stateless native-tool conversation.
// The owning harnesswire server supplies native provider/tool callbacks. This
// package neither selects compute nor claims that a callback is contained.
package toolchatagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/agent-primitives/sessions/api"
)

const ToolName = "http_get"
const MaxResponseBytes = 1 << 20

// Harness performs at most eight model/tool rounds per turn. Tool placement and
// invocation are explicit server configuration, not model-controlled settings.
type Harness struct{ Model string }

func (h Harness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{ID: "toolchat", Version: "1", Models: []string{h.Model}, Tools: []api.ToolSpec{{Name: ToolName, Description: "Read an HTTP URL", Mediation: api.MediationControllerMediated}}, Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}, nil
}
func (h Harness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	var config struct {
		SystemPrompt string `json:"system_prompt"`
	}
	if len(start.Config) > 0 {
		if bytes.Equal(bytes.TrimSpace(start.Config), []byte("null")) {
			return errors.New("toolchat: configuration must be object")
		}
		d := json.NewDecoder(bytes.NewReader(start.Config))
		d.DisallowUnknownFields()
		if err := d.Decode(&config); err != nil {
			return errors.New("toolchat: invalid configuration")
		}
		if d.Decode(&struct{}{}) != io.EOF {
			return errors.New("toolchat: trailing configuration")
		}
	}
	messages := []api.Message{}
	if config.SystemPrompt != "" {
		messages = append(messages, *api.TextMessage("system", config.SystemPrompt))
	}
	for _, event := range start.History {
		switch event.Kind {
		case api.EventInput, api.EventOutput:
			if event.Message != nil {
				messages = append(messages, *event.Message)
			}
		case api.EventToolResult:
			if event.Result != nil {
				messages = append(messages, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: event.Result}}})
			}
		}
	}
	messages = append(messages, start.Inputs...)
	tools := []api.ToolDefinition{{Name: ToolName, Description: "Read a URL using HTTP GET, without credentials or redirects", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}, "required": []any{"url"}, "additionalProperties": false}}}
	for round := 0; round < 8; round++ {
		response, err := sink.Model(ctx, api.ModelRequest{Model: h.Model, Messages: messages, Tools: tools})
		if err != nil {
			return err
		}
		messages = append(messages, response.Message)
		calls := 0
		for _, part := range response.Message.Parts {
			if part.ToolCall == nil {
				continue
			}
			calls++
			call := *part.ToolCall
			if call.Tool != ToolName || call.ID == "" {
				return errors.New("toolchat: unsupported model tool call")
			}
			call.Mediation = api.MediationControllerMediated
			call.IdempotencyKey = start.ExecutionID + "/" + call.ID
			result, err := sink.ToolCall(ctx, call)
			if err != nil {
				return err
			}
			messages = append(messages, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &result}}})
		}
		if calls == 0 {
			return nil
		}
	}
	return errors.New("toolchat: model/tool round limit exceeded")
}

// Fetch performs one native HTTP GET. No proxy, redirects, ambient credentials,
// cookies, or retries are enabled. Network policy is the owning runtime's job;
// this helper does not infer a policy or exempt any destination.
func Fetch(ctx context.Context, call api.ToolCall) (api.ToolResult, error) {
	if call.Tool != ToolName || len(call.Args) != 1 {
		return api.ToolResult{}, errors.New("toolchat: unsupported tool arguments")
	}
	raw, ok := call.Args["url"].(string)
	if !ok || len(raw) > 8192 {
		return api.ToolResult{}, errors.New("toolchat: invalid URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return api.ToolResult{}, errors.New("toolchat: invalid URL")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return api.ToolResult{}, errors.New("toolchat: invalid request")
	}
	response, err := client.Do(req)
	if err != nil {
		return api.ToolResult{}, fmt.Errorf("toolchat: HTTP request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return api.ToolResult{}, errors.New("toolchat: HTTP status refused")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(data) > MaxResponseBytes {
		return api.ToolResult{}, errors.New("toolchat: response exceeds limit or failed")
	}
	return api.ToolResult{ID: call.ID, Content: []api.Part{{Text: &api.TextPart{Text: string(data)}}}}, nil
}
