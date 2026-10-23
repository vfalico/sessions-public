package main

import (
	"context"
	"encoding/json"
	"github.com/agent-primitives/sessions/api"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedListenerScope(t *testing.T) {
	for _, args := range [][]string{nil, {"-addr", "127.0.0.1:8080"}, {"--addr=[::1]:0"}} {
		if err := checkListenerArgs(args); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, args := range [][]string{{"-addr", ":8080"}, {"-addr", "0.0.0.0:8080"}, {"-addr", "localhost:8080"}, {"--addr=10.0.0.1:8080"}, {"-harness", "x=y"}, {"-registry-addr=127.0.0.1:9090"}, {"--tool-executors=x"}} {
		if err := checkListenerArgs(args); err == nil {
			t.Fatal("unsafe listener/dispatch accepted", args)
		}
	}
}
func TestPrivateRotatingControlToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	if err := os.WriteFile(p, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := tokenCredentials{p}
	got, err := c.GetRequestMetadata(context.Background())
	if err != nil || got["authorization"] != "Bearer first" || !c.RequireTransportSecurity() {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(p, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = c.GetRequestMetadata(context.Background())
	if err != nil || got["authorization"] != "Bearer second" {
		t.Fatal("token did not rotate", got, err)
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetRequestMetadata(context.Background()); err == nil {
		t.Fatal("unsafe token accepted")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err = (tokenCredentials{link}).GetRequestMetadata(context.Background()); err == nil {
		t.Fatal("symlink token accepted")
	}
}

func TestOwnedConfigClosedAndExplicit(t *testing.T) {
	valid := config{Name: "native", Atespace: "work", TemplateAtespace: "templates", Template: "native", Router: "127.0.0.1:80", ControlAddress: "127.0.0.1:443", ControlServerName: "control.internal", ControlTokenFile: "/private/token", Descriptor: api.Descriptor{ID: "native", Version: "1", Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}}
	for _, mode := range []string{"valid", "builtin", "missing-template", "implicit-resumability", "unknown", "trailing", "unsafe-mode"} {
		t.Run(mode, func(t *testing.T) {
			c := valid
			switch mode {
			case "builtin":
				c.Name = "echo"
			case "missing-template":
				c.Template = ""
			case "implicit-resumability":
				c.Descriptor.Capabilities.Resumability = ""
			}
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unknown" {
				raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
			}
			if mode == "trailing" {
				raw = append(raw, []byte(` {}`)...)
			}
			path := filepath.Join(t.TempDir(), "runtime.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "unsafe-mode" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			_, err = load(path)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
		})
	}
}
