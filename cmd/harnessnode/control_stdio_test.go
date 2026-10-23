package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controlstdio"
	"github.com/agent-primitives/sessions/harnesswire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Run the actual ordinary main with stdio, not an alternate test-only server.
func TestOrdinaryMainStdio(t *testing.T) {
	if os.Getenv("POC_STDIO_HELPER") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOrdinaryMainStdio$")
	cmd.Env = append(os.Environ(), "POC_STDIO_HELPER=1", "RUNTIME_CONTROL_STDIO=1", "HARNESS_KIND=echo", "HARNESS_MODEL_PLACEMENT=")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	mux := controlstdio.New(true, output, input, func() error { input.Close(); return output.Close() })
	defer func() { mux.Close(); cancel(); cmd.Wait() }()
	go mux.Serve()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return mux.Dial(ctx, 8081) }}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://runtime-control/readyz", nil)
	reply, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reply.Body)
	reply.Body.Close()
	if err != nil || reply.StatusCode != 200 || string(data) != "ok" {
		t.Fatal(reply.StatusCode, string(data), err)
	}
	conn, err := grpc.NewClient("passthrough:///runtime-control", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return mux.Dial(ctx, 80) }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	remote := harnesswire.NewClientHarness(v1.NewHarnessClient(conn))
	desc, err := remote.Describe(ctx)
	if err != nil || desc.ID != "echo" {
		t.Fatal(desc, err)
	}
}
