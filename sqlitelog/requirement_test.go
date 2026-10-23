package sqlitelog_test

import (
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/sqlitelog"
	"path/filepath"
	"testing"
)

func TestOwnedCreationPersistsImmutableCompleteRequirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.db")
	s := mustOpen(t, path)
	raw := `{"opaque":{"large":9007199254740993}}`
	m := sqlitelog.SessionMeta{UID: "governed", Harness: "static", ExecutionRequirement: raw}
	if err := s.CreateOwnedSession(m, api.CallerIdentity{Issuer: "issuer", Subject: "owner"}, false); err != nil {
		t.Fatal(err)
	}
	info, err := s.SessionInfo(m.UID)
	if err != nil || info.ExecutionRequirement != raw {
		t.Fatal(info, err)
	}
	m.ExecutionRequirement = `{"changed":true}`
	if err = s.PutSession(m); err == nil {
		t.Fatal("changed authority accepted")
	}
	info, err = s.SessionInfo(m.UID)
	if err != nil || info.ExecutionRequirement != raw {
		t.Fatal("stored authority changed", info, err)
	}
}
