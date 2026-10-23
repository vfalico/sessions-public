// agentsessions-substrate wires an owning Substrate backend into the ordinary
// Sessions daemon. Control uses verified TLS and a rotating token file; the
// Sessions API retains its explicitly loopback-only development contract.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/daemon"
	adapter "github.com/agent-primitives/sessions/integrations/substrate"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/substrate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type tokenCredentials struct{ path string }

func (c tokenCredentials) RequireTransportSecurity() bool { return true }
func (c tokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	raw, err := readPrivate(c.path, 16384)
	if err != nil {
		return nil, errors.New("Control token unavailable")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("invalid Control token")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}
func readPrivate(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private file changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("private file exceeds limit")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) {
		return nil, errors.New("private file changed")
	}
	return raw, nil
}

type config struct {
	Name              string         `json:"name"`
	Atespace          string         `json:"atespace"`
	TemplateAtespace  string         `json:"templateAtespace"`
	Template          string         `json:"template"`
	Router            string         `json:"router"`
	ControlAddress    string         `json:"controlAddress"`
	ControlServerName string         `json:"controlServerName"`
	ControlCAFile     string         `json:"controlCAFile"`
	ControlTokenFile  string         `json:"controlTokenFile"`
	Descriptor        api.Descriptor `json:"descriptor"`
}

func load(path string) (config, error) {
	var c config
	raw, err := readPrivate(path, 65536)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("invalid runtime configuration")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return c, errors.New("trailing runtime configuration")
	}
	if c.Name == "" || c.Name == "echo" || c.Atespace == "" || c.TemplateAtespace == "" || c.Template == "" || c.ControlServerName == "" || c.ControlTokenFile == "" || c.Descriptor.ID == "" || c.Descriptor.Version == "" {
		return c, errors.New("incomplete owned runtime configuration")
	}
	if c.Descriptor.Capabilities.Resumability != api.ResumabilityStatelessReplay && c.Descriptor.Capabilities.Resumability != api.ResumabilityRequiresMemorySnapshot {
		return c, errors.New("explicit descriptor resumability required")
	}
	if _, _, err = net.SplitHostPort(c.ControlAddress); err != nil {
		return c, errors.New("invalid Control address")
	}
	if _, _, err = net.SplitHostPort(c.Router); err != nil {
		return c, errors.New("invalid router address")
	}
	return c, nil
}
func checkListenerArgs(args []string) error {
	// The stock daemon has no authentication. Refuse alternate bind spellings,
	// remote harnesses or a dynamic registry rather than weakening ownership.
	for i, arg := range args {
		name, value, eq := strings.Cut(arg, "=")
		if name == "-harness" || name == "--harness" || name == "-registry-addr" || name == "--registry-addr" || name == "-tool-executors" || name == "--tool-executors" {
			return errors.New("owned daemon does not enable remote registry or host tool dispatch")
		}
		if name != "-addr" && name != "--addr" {
			continue
		}
		if !eq {
			if i+1 >= len(args) {
				return errors.New("missing listener")
			}
			value = args[i+1]
		}
		host, _, err := net.SplitHostPort(value)
		if err != nil {
			return errors.New("invalid Sessions listener")
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("Sessions development listener must be literal loopback")
		}
	}
	return nil
}
func run(args []string) error {
	// A mandatory -- divides integration and normal daemon flags. No hidden
	// environment profile or implicit template/enforcer default is selected.
	split := -1
	for i, arg := range args {
		if arg == "--" {
			split = i
			break
		}
	}
	if split < 0 {
		return errors.New("usage: agentsessions-substrate -runtime-config FILE -- [agentsessionsd flags]")
	}
	flags := flag.NewFlagSet("agentsessions-substrate", flag.ContinueOnError)
	path := flags.String("runtime-config", "", "private owning-runtime JSON configuration")
	if err := flags.Parse(args[:split]); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("explicit runtime configuration required")
	}
	rest := args[split+1:]
	if err := checkListenerArgs(rest); err != nil {
		return err
	}
	c, err := load(*path)
	if err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return err
	}
	if c.ControlCAFile != "" {
		raw, err := readPrivate(c.ControlCAFile, 1<<20)
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("invalid Control CA")
		}
	}
	conn, err := grpc.NewClient(c.ControlAddress, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: c.ControlServerName})), grpc.WithPerRPCCredentials(tokenCredentials{c.ControlTokenFile}))
	if err != nil {
		return err
	}
	defer conn.Close()
	backend := substrate.New(adapter.New(conn), c.Atespace, substrate.ObjectRef{Atespace: c.TemplateAtespace, Name: c.Template}, c.Descriptor, substrate.WithRouter(c.Router))
	return daemon.Run(rest, daemon.WithOwnedRuntimes(map[string]placement.Backend{c.Name: backend}))
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentsessions-substrate:", err)
		os.Exit(1)
	}
}
