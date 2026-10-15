package ui

import (
	"strings"
	"testing"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

func fixtureScan(t *testing.T) *storage.Scan {
	t.Helper()
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE project(id text primary key, worktree text, name text,
			time_created integer, time_updated integer, time_archived integer)`,
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			model text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`INSERT INTO project VALUES ('p1','` + p.Data + `','demo',0,0,0)`,
		`INSERT INTO session_v2 VALUES ('s1','p1','` + p.Data + `','a session','model',0,0,0)`,
		`INSERT INTO session_message VALUES ('m1','s1','user',0,0,0,'hello')`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	conn.Close()

	s, err := storage.Run(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPreviewRendersWithoutATerminal keeps the dashboard honest: every view
// reads fields off the scan, and a nil map or empty slice must not panic.
func TestPreviewRendersWithoutATerminal(t *testing.T) {
	s := fixtureScan(t)
	out := Preview(s)
	for _, want := range []string{"OCSA", "DATABASE", "RECLAIMABLE", "ARTIFACTS", "SESSIONS"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard is missing %q\n%s", want, out)
		}
	}
}

// TestPreviewHandlesEmptyInstall covers a fresh install with no sessions,
// projects or artifacts, where every section is empty.
func TestPreviewHandlesEmptyInstall(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.SQL.Exec(`CREATE TABLE session_v2(id text primary key, project_id text,
		directory text, title text, model text, cost real, time_created integer, time_updated integer)`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	s, err := storage.Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if out := Preview(s); !strings.Contains(out, "OCSA") {
		t.Errorf("empty install did not render:\n%s", out)
	}
}

// TestViewSessionsPaginates exercises the scroll clamp, which divides by the
// terminal height and would panic on a zero-height terminal.
func TestViewSessionsPaginates(t *testing.T) {
	s := fixtureScan(t)
	m := New(s.Paths)
	m.scan = s
	m.width, m.height = 100, 0
	m.screen = scrSessions
	m.clamp()
	if out := m.viewSessions(); !strings.Contains(out, "SESSIONS") {
		t.Errorf("sessions view did not render:\n%s", out)
	}
}
