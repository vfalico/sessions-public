package session_test

import (
	"context"
	"fmt"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/sqlitelog"
	"path/filepath"
	"testing"
	"time"
)

type governedDeleteBackend struct {
	*deleteBackend
	requirements []string
	instances    []string
	failure      error
}

func (b *governedDeleteBackend) Stop(_ context.Context, in api.Incarnation) error {
	b.requirements = append(b.requirements, string(in.ExecutionRequirement))
	b.instances = append(b.instances, in.ComputeInstance)
	return b.failure
}

// Real generated public transport and SQLite; backend is explicit fake compute.
func TestGovernedPublicDeleteRetiresOnlyPersistedExactInstance(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "no-instance", "pre-resume-owner"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.db")
			store := openStore(t, path)
			_, base := deletionRegistry(t)
			b := &governedDeleteBackend{deleteBackend: base}
			registry, err := placement.NewRegistry("echo", map[string]*placement.Placer{"echo": placement.New(b, nil)})
			if err != nil {
				t.Fatal(err)
			}
			uid := "governed-" + scenario
			err = store.CreateOwnedSession(sqlitelog.SessionMeta{UID: uid, Harness: "echo", ExecutionRequirement: `{"opaque":true}`}, api.CallerIdentity{Issuer: "issuer", Subject: "owner"}, false)
			if err != nil {
				t.Fatal(err)
			}
			log := store.Session(uid)
			if scenario != "no-instance" {
				fence, err := log.NewFence()
				if err != nil {
					t.Fatal(err)
				}
				count := int64(0)
				event := api.Event{Kind: api.EventExecutionStart, ExecutionID: "recorded-execution", SchemaVersion: 1, Timestamp: time.Now(), ExecutionStart: &api.ExecutionStart{Harness: "echo", ComputeInstance: "original-actor-uid", InputCount: &count}}
				if scenario == "pre-resume-owner" {
					event = api.Event{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleComputeOwned, ComputeID: uid, ComputeRuntime: "fixture", ComputeHarness: "echo", ComputeInstance: "original-actor-uid", ComputeRequirement: []byte(`{"opaque":true}`)}}
				}
				_, err = log.Append(0, fence, event)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "failure" {
				b.failure = fmt.Errorf("exact retirement pending")
			}
			client := serveRegistry(t, store, registry)
			for attempt := 0; attempt < 2; attempt++ {
				result, err := client.DeleteSession(t.Context(), &v1.DeleteSessionRequest{Uid: uid})
				if scenario == "success" || scenario == "pre-resume-owner" {
					if err != nil || result.GetDeletionState() != v1.DeletionState_DELETION_PENDING {
						t.Fatal(result, err)
					}
				} else if err == nil {
					t.Fatal("cleanup falsely completed", result)
				}
			}
			if scenario == "no-instance" {
				if len(b.instances) != 0 {
					t.Fatal("guessed compute identity", b.instances)
				}
			} else if len(b.instances) != 2 || b.instances[0] != "original-actor-uid" || b.instances[1] != "original-actor-uid" {
				t.Fatal(b.instances)
			}
			for _, raw := range b.requirements {
				if raw != `{"opaque":true}` {
					t.Fatal("durable cleanup lost requirement", raw)
				}
			}
			info, err := store.SessionInfo(uid)
			if err != nil || info.DeletionState != api.DeletionPending {
				t.Fatal(info, err)
			}
			records, err := log.Read(1)
			if err != nil {
				t.Fatal(err)
			}
			deletes := 0
			for _, r := range records {
				if r.Event.Lifecycle != nil && r.Event.Lifecycle.Kind == api.LifecycleDelete {
					deletes++
				}
			}
			if deletes != 1 {
				t.Fatal("retry wrote another intent", deletes)
			}
			if err := log.Verify(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
