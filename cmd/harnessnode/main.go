// Command harnessnode is the harness packaged to run INSIDE a compute sandbox (a substrate actor, or
// any runtime). It serves a selected harness over the harnesswire gRPC Harness.Connect stream on :80 —
// the port the atenet mesh routes to — so the external agentsessions controller drives it remotely,
// exactly as it drives the in-process runtime/local harness over a unix socket. A separate HTTP
// /readyz on :8081 lets ResumeActor block until the harness is live (ActorTemplate readyz probe).
//
// This is the substrate realization of the harness half of the step-6 dial path: the transport moves
// from a unix socket to atenet-router, which forwards to this port; the harnesswire server is unchanged.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/grpc"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controlstdio"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/harness/counteragent"
	"github.com/agent-primitives/sessions/harness/echoagent"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/internal/modelconfig"
	"github.com/agent-primitives/sessions/observability"
	"github.com/agent-primitives/sessions/runtime/substrate"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	harness, err := selectHarness()
	if err != nil {
		log.Fatalf("harnessnode: configure harness: %v", err)
	}
	wireServer, err := configuredServer(harness)
	if err != nil {
		log.Fatalf("harnessnode: configure native model: %v", err)
	}
	grpcAddr := env("HARNESS_ADDR", ":"+substrate.HarnessPort) // harnesswire gRPC; atenet-router forwards here
	readyzAddr := env("HARNESS_READYZ", ":8081")               // HTTP readyz for the ActorTemplate wakeup probe

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// harnesswire gRPC server (h2c: plain TCP, no TLS — the mesh terminates/originates HTTP/2).
	// HARNESS_ADDR=unix:///path serves on a unix socket instead, for a host that registers this
	// harness by address on the same machine (agentsessionsd -harness name=unix:///path).
	var lis net.Listener
	var control *controlstdio.Mux
	var readyListener net.Listener
	if os.Getenv("RUNTIME_CONTROL_STDIO") == "1" {
		control = controlstdio.New(false, os.Stdin, os.Stdout, func() error { os.Stdin.Close(); return os.Stdout.Close() })
		defer control.Close()
		lis, err = control.Listen(80)
		if err != nil {
			log.Fatal(err)
		}
		readyListener, err = control.Listen(8081)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			if err := control.Serve(); err != nil {
				log.Printf("runtime control closed: %v", err)
			}
		}()
	}
	if control != nil {
		// Runtime-owned stdio; no workload sockets or implicit network grants.
	} else if sock, ok := strings.CutPrefix(grpcAddr, "unix://"); ok {
		ulis, lock, err := listenUnix(sock)
		if err != nil {
			log.Fatalf("harnessnode: listen %s: %v", grpcAddr, err)
		}
		// Held until the process exits, however it exits: the kernel drops the lock, and the next
		// harnessnode on this address can then reclaim the socket file.
		defer func() { _ = lock.Close() }()
		lis = ulis
	} else if lis, err = net.Listen("tcp", grpcAddr); err != nil {
		log.Fatalf("harnessnode: listen %s: %v", grpcAddr, err)
	}
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(observability.UnaryServerInterceptor(slog.Default())),
		grpc.ChainStreamInterceptor(observability.StreamServerInterceptor(slog.Default())),
	)
	v1.RegisterHarnessServer(srv, wireServer)

	// readyz on a side port so the actor is reported live once the gRPC server is accepting.
	readyz := &http.Server{
		Addr:    readyzAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
	}
	go func() {
		var err error
		if readyListener != nil {
			err = readyz.Serve(readyListener)
		} else {
			err = readyz.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("harnessnode: readyz: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
		_ = readyz.Close()
	}()

	log.Printf("harnessnode: %q harness serving harnesswire on %s, readyz on %s", env("HARNESS_KIND", "echo"), grpcAddr, readyzAddr)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("harnessnode: serve: %v", err)
	}
}

// selectHarness picks the harness this node serves, by HARNESS_KIND (default "echo"). One image
// serves both conformance tiers: "echo" and "chat" are STATELESS_REPLAY; "counter" requires a memory
// snapshot. Chat defaults to host provider placement; toolchat requires explicit
// native provider/tool placement. Both obtain the model ID from HARNESS_MODEL.
func selectHarness() (api.Harness, error) {
	switch env("HARNESS_KIND", "echo") {
	case "toolchat":
		model := os.Getenv("HARNESS_MODEL")
		if model == "" {
			return nil, errors.New("HARNESS_MODEL is required for toolchat")
		}
		return toolchatagent.Harness{Model: model}, nil
	case "chat":
		model := os.Getenv("HARNESS_MODEL")
		if model == "" {
			return nil, errors.New("HARNESS_MODEL is required when HARNESS_KIND=chat")
		}
		return chatagent.Harness{Model: model}, nil
	case "counter":
		return &counteragent.Harness{}, nil
	default:
		return echoagent.Harness{}, nil
	}
}

// Native provider configuration is explicit and local to the owning sandbox.
// A native-model grant carries no provider credential or host-side network IO.
func configuredServer(h api.Harness) (*harnesswire.Server, error) {
	mode := os.Getenv("HARNESS_MODEL_PLACEMENT")
	if mode == "" || mode == "host" {
		if os.Getenv("HARNESS_KIND") == "toolchat" {
			return nil, errors.New("toolchat requires native model/tool placement")
		}
		return harnesswire.NewServer(h), nil
	}
	if mode != "native" {
		return nil, errors.New("HARNESS_MODEL_PLACEMENT must be host or native")
	}
	if os.Getenv("HARNESS_KIND") != "chat" && os.Getenv("HARNESS_KIND") != "toolchat" {
		return nil, errors.New("native provider requires explicit chat harness")
	}
	model, base := os.Getenv("HARNESS_MODEL"), os.Getenv("HARNESS_MODEL_BASE_URL")
	if model == "" || base == "" {
		return nil, errors.New("native provider requires explicit model and base URL")
	}
	if os.Getenv("HARNESS_KIND") == "toolchat" {
		client, err := modelconfig.NewResponses(modelconfig.Config{Model: model, BaseURL: base, Path: os.Getenv("HARNESS_MODEL_PATH"), APIKey: os.Getenv("HARNESS_MODEL_API_KEY"), AuthHeader: "Authorization"})
		if err != nil {
			return nil, errors.New("invalid native Responses provider configuration")
		}
		return harnesswire.NewNativeToolServer(h, client.Model, toolchatagent.Fetch), nil
	}
	client, err := modelconfig.New(modelconfig.Config{Model: model, BaseURL: base, Path: os.Getenv("HARNESS_MODEL_PATH"), APIKey: os.Getenv("HARNESS_MODEL_API_KEY"), AuthHeader: "Authorization"})
	if err != nil {
		return nil, errors.New("invalid native provider configuration")
	}
	return harnesswire.NewNativeServer(h, client.Model), nil
}
