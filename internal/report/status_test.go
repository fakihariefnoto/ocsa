package report

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

func statusFixture(t *testing.T) (*storage.Scan, paths.Paths) {
	t.Helper()
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-time.Hour).UnixMilli()
	old := int64(1000)
	stmts := []string{
		`CREATE TABLE session(id text primary key, project_id text, time_created integer, time_updated integer)`,
		`CREATE TABLE message(id text primary key, session_id text, time_created integer,
			time_updated integer, data text)`,
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			model text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`INSERT INTO session VALUES ('s1','p',` + strconv.FormatInt(old, 10) + `,` + strconv.FormatInt(old, 10) + `)`,
		`INSERT INTO message VALUES ('m1','s1',` + strconv.FormatInt(old, 10) + `,` + strconv.FormatInt(old, 10) + `,'frozen')`,
		`INSERT INTO session_v2 VALUES ('s1','p','` + p.Data + `','live','m',0,` +
			strconv.FormatInt(recent, 10) + `,` + strconv.FormatInt(recent, 10) + `)`,
		`INSERT INTO session_message VALUES ('sm1','s1','user',0,` + strconv.FormatInt(recent, 10) + `,` +
			strconv.FormatInt(recent, 10) + `,'hello')`,
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
	return s, p
}

// TestStatusVerdicts covers the three states a reader can find themselves in.
func TestStatusVerdicts(t *testing.T) {
	s, p := statusFixture(t)

	cases := []struct {
		name    string
		act     db.Activity
		holder  string
		want    string
		notWant string
	}{
		{
			name: "nothing has it open",
			act:  db.Activity{Open: false},
			want: "free: safe to run now",
		},
		{
			name: "open but idle",
			act:  db.Activity{Open: true, Holders: []db.Holder{{PID: 42, Command: "opencode"}}},
			want: "idle: safe to run now",
		},
		{
			name: "mid-write",
			act:  db.Activity{Open: true, WriteLockHeld: true},
			want: "writing: a claim would wait for a gap",
		},
		{
			name:   "another ocsa holds the lock",
			act:    db.Activity{Open: true},
			holder: "1234",
			want:   "busy: another ocsa holds the lock",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := BuildStatus(p, s, tc.act, tc.holder)
			if got := st.Summary(); !strings.Contains(got, tc.want) {
				t.Errorf("summary %q does not contain %q", got, tc.want)
			}
			// SafeToRun means "a claim can start right now": no writer, and no
			// other ocsa holding the lock. A mid-write database is not unsafe
			// to touch, but it is not immediately runnable either.
			wantSafe := tc.holder == "" && !tc.act.WriteLockHeld
			if st.SafeToRun != wantSafe {
				t.Errorf("SafeToRun = %v, want %v", st.SafeToRun, wantSafe)
			}
		})
	}
}

// TestStatusTextNamesTheHolder: the point of the command is to say who is in
// the way, by name and pid, not to say "busy".
func TestStatusTextNamesTheHolder(t *testing.T) {
	s, p := statusFixture(t)
	var buf bytes.Buffer
	act := db.Activity{
		Open:    true,
		Holders: []db.Holder{{PID: 59073, Command: "opencode"}},
	}
	Status(&buf, p, s, act, "")
	out := buf.String()

	for _, want := range []string{"WHO HAS IT OPEN", "opencode (pid 59073)", "RIGHT NOW", "idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
	// It must not imply the tool will stop OpenCode.
	if strings.Contains(strings.ToLower(out), "will stop") ||
		strings.Contains(strings.ToLower(out), "will kill") {
		t.Errorf("status implies it terminates OpenCode:\n%s", out)
	}
}

// TestStatusReclaimableIsTheFreelist: "reclaimable" must mean what a vacuum
// returns, not "everything that is not live" - indexes and kv state are in use.
func TestStatusReclaimableIsTheFreelist(t *testing.T) {
	s, p := statusFixture(t)
	comp := s.Composition()
	st := BuildStatus(p, s, db.Activity{}, "")

	if st.Reclaimable != comp.Free {
		t.Errorf("reclaimable %d, want the freelist %d (total %d, live %d, frozen %d)",
			st.Reclaimable, comp.Free, comp.Total, comp.Live, comp.Frozen)
	}
	if st.Reclaimable == comp.Total-comp.Live-comp.Frozen && comp.Other > 0 {
		t.Error("reclaimable includes in-use bytes such as indexes")
	}
}
