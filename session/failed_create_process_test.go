package session_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/sqlitelog"
)

// Explicit fake compute: models a successful suspended Create followed by lost
// Resume response. Real placement writes authority before the simulated resume.
type failedCreateOwnerBackend struct{ *governedDeleteBackend }

func (*failedCreateOwnerBackend) ExecutionRequirementRuntime() {}
func (b *failedCreateOwnerBackend) Create(_ context.Context, s *api.SessionSpec) (api.Incarnation, error) {
	in := api.Incarnation{ID: s.SessionUID, Runtime: "fixture", ComputeInstance: "created-original-uid", ExecutionRequirement: append([]byte(nil), s.ExecutionRequirement...)}
	if s.RetainCompute == nil {
		return in, errors.New("missing durable pre-resume owner")
	}
	if err := s.RetainCompute(in); err != nil {
		return in, err
	}
	return in, errors.New("injected lost exact Resume reply")
}

// Two fresh executables and real SQLite/public Delete path. Abrupt cut is only
// after failed Create and failed public cleanup, not an arbitrary write cut or
// native compute proof. Child 0 deliberately exits 73 after assertions.
func TestFailedCreateOriginalOwnershipSurvivesAbruptProcessExit(t *testing.T) {
	path := os.Getenv("SESSIONS_FAILED_CREATE_DB")
	if path == "" {
		path = filepath.Join(t.TempDir(), "journal.db")
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		for phase := 0; phase < 2; phase++ {
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestFailedCreateOriginalOwnershipSurvivesAbruptProcessExit$", "-test.timeout=20s", "-test.v")
			command.Env = append(os.Environ(), "SESSIONS_FAILED_CREATE_DB="+path, "SESSIONS_FAILED_CREATE_PHASE="+strconv.Itoa(phase))
			output, err := command.CombinedOutput()
			t.Logf("fresh phase %d: %s", phase, output)
			if phase == 0 {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 73 {
					t.Fatalf("did not reach deliberate exit after durable ownership: %v", err)
				}
			} else if err != nil {
				t.Fatalf("fresh recovery: %v", err)
			}
		}
		return
	}
	phase, err := strconv.Atoi(os.Getenv("SESSIONS_FAILED_CREATE_PHASE"))
	if err != nil || phase < 0 || phase > 1 {
		t.Fatal("invalid child phase")
	}
	const uid = "failed-create-owned"
	const raw = `{"opaque":"ceiling"}`
	store := openStore(t, path)
	_, base := deletionRegistry(t)
	cleanup := &governedDeleteBackend{deleteBackend: base}
	var backend placement.Backend = cleanup
	if phase == 0 {
		if err = store.CreateOwnedSession(sqlitelog.SessionMeta{UID: uid, Harness: "echo", ExecutionRequirement: raw}, api.CallerIdentity{Issuer: "issuer", Subject: "owner"}, false); err != nil {
			t.Fatal(err)
		}
		cleanup.failure = errors.New("injected cleanup transport unavailable")
		backend = &failedCreateOwnerBackend{governedDeleteBackend: cleanup}
	}
	p := placement.New(backend, nil)
	registry, err := placement.NewRegistry("echo", map[string]*placement.Placer{"echo": p})
	if err != nil {
		t.Fatal(err)
	}
	if phase == 0 {
		if _, err = p.Exec(t.Context(), store.Session(uid), uid, nil, 0, placement.WithHarness("echo"), placement.WithExecutionRequirement([]byte(raw))); err == nil {
			t.Fatal("failed Create succeeded")
		}
		if len(cleanup.instances) != 1 || cleanup.instances[0] != "created-original-uid" {
			t.Fatal("immediate cleanup authority missing", cleanup.instances)
		}
		cleanup.instances = nil
		cleanup.requirements = nil
	}
	client := serveRegistry(t, store, registry)
	_, err = client.DeleteSession(t.Context(), &v1.DeleteSessionRequest{Uid: uid})
	if (err != nil) != (phase == 0) {
		t.Fatal("wrong cleanup result", phase, err)
	}
	if len(cleanup.instances) != 1 || cleanup.instances[0] != "created-original-uid" || len(cleanup.requirements) != 1 || cleanup.requirements[0] != raw {
		t.Fatal("original cleanup authority lost", cleanup.instances, cleanup.requirements)
	}
	info, err := store.SessionInfo(uid)
	if err != nil || info.DeletionState != api.DeletionPending || info.ExecutionRequirement != raw {
		t.Fatal("durable session intent lost", info, err)
	}
	records, err := store.Session(uid).Read(1)
	if err != nil {
		t.Fatal(err)
	}
	owned, deletes := 0, 0
	for _, r := range records {
		if r.Event.ExecutionStart != nil {
			t.Fatal("failed Create manufactured a turn", r)
		}
		if l := r.Event.Lifecycle; l != nil {
			if l.Kind == api.LifecycleComputeOwned {
				owned++
			}
			if l.Kind == api.LifecycleDelete {
				deletes++
			}
		}
	}
	if owned != 1 || deletes != 1 {
		t.Fatal("retry duplicated authority or intent", owned, deletes)
	}
	if err = store.Session(uid).Verify(); err != nil {
		t.Fatal(err)
	}
	if phase == 0 {
		os.Exit(73)
	}
}
