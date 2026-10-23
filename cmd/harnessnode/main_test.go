package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/harness/chatagent"
)

func TestSelectHarness(t *testing.T) {
	for _, tt := range []struct {
		name         string
		kind         string
		model        string
		id           string
		resumability api.Resumability
	}{
		{"default", "", "", "echo", api.ResumabilityStatelessReplay},
		{"echo", "echo", "ignored", "echo", api.ResumabilityStatelessReplay},
		{"counter", "counter", "", "counter", api.ResumabilityRequiresMemorySnapshot},
		{"existing fallback", "unknown", "", "echo", api.ResumabilityStatelessReplay},
		{"chat", "chat", "test-model", "chat", api.ResumabilityStatelessReplay},
		{"toolchat", "toolchat", "test-model", "toolchat", api.ResumabilityStatelessReplay},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HARNESS_KIND", tt.kind)
			t.Setenv("HARNESS_MODEL", tt.model)
			harness, err := selectHarness()
			if err != nil {
				t.Fatal(err)
			}
			desc, err := harness.Describe(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if desc.ID != tt.id || desc.Capabilities.Resumability != tt.resumability {
				t.Fatalf("descriptor = %+v, want %s with %s", desc, tt.id, tt.resumability)
			}
			if tt.kind == "chat" {
				chat, ok := harness.(chatagent.Harness)
				if !ok || chat.Model != tt.model {
					t.Fatalf("harness = %+v, want chat with configured model", harness)
				}
				if !desc.Capabilities.ForkSafe || !reflect.DeepEqual(desc.Models, []string{tt.model}) {
					t.Fatalf("chat descriptor = %+v", desc)
				}
			}
		})
	}
}

func TestSelectChatRequiresModel(t *testing.T) {
	t.Setenv("HARNESS_KIND", "chat")
	t.Setenv("HARNESS_MODEL", "")
	harness, err := selectHarness()
	if err == nil || !strings.Contains(err.Error(), "HARNESS_MODEL is required") {
		t.Fatalf("error = %v, want missing HARNESS_MODEL", err)
	}
	if harness != nil {
		t.Fatal("invalid chat configuration returned a fallback harness")
	}
}

func TestNativeProviderConfigurationIsExplicit(t *testing.T) {
	for _, mode := range []string{"legacy", "host", "native", "bad-mode", "missing-base", "non-chat", "keyed-insecure"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HARNESS_KIND", "chat")
			t.Setenv("HARNESS_MODEL", "test")
			t.Setenv("HARNESS_MODEL_BASE_URL", "http://127.0.0.1:1")
			t.Setenv("HARNESS_MODEL_API_KEY", "")
			t.Setenv("HARNESS_MODEL_PLACEMENT", "native")
			switch mode {
			case "legacy":
				t.Setenv("HARNESS_MODEL_PLACEMENT", "")
			case "host":
				t.Setenv("HARNESS_MODEL_PLACEMENT", "host")
			case "bad-mode":
				t.Setenv("HARNESS_MODEL_PLACEMENT", "typo")
			case "missing-base":
				t.Setenv("HARNESS_MODEL_BASE_URL", "")
			case "non-chat":
				t.Setenv("HARNESS_KIND", "echo")
			case "keyed-insecure":
				t.Setenv("HARNESS_MODEL_API_KEY", "fixture")
				t.Setenv("HARNESS_MODEL_BASE_URL", "http://example.invalid")
			}
			h, err := selectHarness()
			if err != nil {
				t.Fatal(err)
			}
			_, err = configuredServer(h)
			valid := mode == "legacy" || mode == "host" || mode == "native"
			if (err == nil) != valid {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestToolchatRequiresExplicitNativePlacement(t *testing.T) {
	for _, mode := range []string{"", "host", "native"} {
		t.Run("placement-"+mode, func(t *testing.T) {
			t.Setenv("HARNESS_KIND", "toolchat")
			t.Setenv("HARNESS_MODEL", "fixture")
			t.Setenv("HARNESS_MODEL_PLACEMENT", mode)
			t.Setenv("HARNESS_MODEL_BASE_URL", "http://127.0.0.1:1")
			t.Setenv("HARNESS_MODEL_API_KEY", "")
			t.Setenv("HARNESS_MODEL_PATH", "")
			h, err := selectHarness()
			if err != nil {
				t.Fatal(err)
			}
			_, err = configuredServer(h)
			if (err == nil) != (mode == "native") {
				t.Fatal(mode, err)
			}
		})
	}
}
