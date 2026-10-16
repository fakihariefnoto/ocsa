package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
)

// TestPerSessionBytesCountsOneGeneration is the regression for the bug that
// made every session look twice its real size: v1 message/part and v2
// session_message carry the same message ids, so summing both double counts.
func TestPerSessionBytesCountsOneGeneration(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}

	blob := "x"
	for i := 0; i < 2000; i++ { // 2000 bytes of payload per table
		blob += "x"
	}
	stmts := []string{
		`CREATE TABLE session(id text primary key, project_id text, directory text, title text,
			version text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			version text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE message(id text primary key, session_id text, time_created integer,
			time_updated integer, data text)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`CREATE TABLE part(id text primary key, message_id text, session_id text,
			time_created integer, time_updated integer, data text)`,
		`CREATE TABLE project(id text primary key, worktree text, name text,
			time_created integer, time_updated integer)`,
		`INSERT INTO project VALUES ('p1','` + p.Data + `','proj',0,0)`,
		`INSERT INTO session_v2 VALUES ('s1','p1','` + p.Data + `','t','1.0',0,0,0)`,
		`INSERT INTO session VALUES ('s1','p1','` + p.Data + `','t','1.0',0,0,0)`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	// Same id in both generations, and one row of each.
	same := "msg_1"
	for _, q := range []string{
		`INSERT INTO message VALUES ('` + same + `','s1',0,0,'` + blob + `')`,
		`INSERT INTO session_message VALUES ('` + same + `','s1','user',0,0,0,'` + blob + `')`,
		`INSERT INTO part VALUES ('part_1','` + same + `','s1',0,0,'` + blob + `')`,
	} {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()

	s, err := Run(p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(s.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(s.Sessions))
	}
	got := s.Sessions[0].Bytes
	// Only the 2001 bytes of session_message may be counted. Counting the
	// other two tables as well would give ~6003.
	if got != 2001 {
		t.Errorf("session bytes = %d, want 2001 (one generation only)", got)
	}
	if s.Sessions[0].Messages != 1 {
		t.Errorf("message count = %d, want 1", s.Sessions[0].Messages)
	}
}

// TestDeadDirectoryDetection pins the "sessions in directories that are gone"
// warning, which is how stale sessions get found.
func TestDeadDirectoryDetection(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			version text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`INSERT INTO session_v2 VALUES ('s1','p','/definitely/not/here','gone','1.0',0,0,0)`,
		`INSERT INTO session_v2 VALUES ('s2','p','` + p.Data + `','here','1.0',0,0,0)`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	conn.Close()

	s, err := Run(p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	gone := map[string]bool{}
	for _, si := range s.Sessions {
		gone[si.ID] = si.DirGone
	}
	if !gone["s1"] {
		t.Error("session in a missing directory not flagged")
	}
	if gone["s2"] {
		t.Error("session in an existing directory flagged as gone")
	}
}

// TestFrozenGenerationIsNotCountedAsFreeSpace: a frozen generation holds real
// rows, so it must be reported separately and never added to the reclaimable
// total, which is what a vacuum can actually return.
func TestFrozenGenerationIsNotCountedAsFreeSpace(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	old := int64(1000)                               // long ago, in milliseconds
	recent := time.Now().Add(-time.Hour).UnixMilli() // the generation still in use
	stmts := []string{
		`CREATE TABLE session(id text primary key, project_id text, time_created integer, time_updated integer)`,
		`CREATE TABLE message(id text primary key, session_id text, time_created integer,
			time_updated integer, data text)`,
		`CREATE TABLE part(id text primary key, message_id text, session_id text,
			time_created integer, time_updated integer, data text)`,
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			model text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`INSERT INTO session VALUES ('s1','p',` + itoa(old) + `,` + itoa(old) + `)`,
		`INSERT INTO message VALUES ('m1','s1',` + itoa(old) + `,` + itoa(old) + `,'payload')`,
		`INSERT INTO part VALUES ('p1','m1','s1',` + itoa(old) + `,` + itoa(old) + `,'payload')`,
		`INSERT INTO session_v2 VALUES ('s1','p','` + p.Data + `','live','m',0,` +
			itoa(recent) + `,` + itoa(recent) + `)`,
		`INSERT INTO session_message VALUES ('sm1','s1','user',0,` + itoa(recent) + `,` +
			itoa(recent) + `,'hello')`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	conn.Close()

	s, err := Run(p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if s.Reclaim.LegacyBytes <= 0 {
		t.Errorf("frozen v1 tables not reported as legacy (%+v)", s.Generations)
	}
	if s.Reclaim.Total >= s.Reclaim.LegacyBytes {
		t.Errorf("legacy bytes leaked into the reclaimable total: total %d, legacy %d",
			s.Reclaim.Total, s.Reclaim.LegacyBytes)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestScanOnMissingDatabase covers the first-run case: no install yet.
func TestScanOnMissingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := paths.Paths{}.WithDataDir(dir)
	if _, err := Run(p); err == nil {
		t.Error("expected an error for a missing database")
	}
}

// TestCompositionSegmentsSumToTheFile: the bar is only honest if its parts add
// up to the file it claims to describe.
func TestCompositionSegmentsSumToTheFile(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	old := int64(1000)
	recent := time.Now().Add(-time.Hour).UnixMilli()
	stmts := []string{
		`CREATE TABLE project(id text primary key, worktree text, name text,
			time_created integer, time_updated integer)`,
		`CREATE TABLE session(id text primary key, project_id text, time_created integer, time_updated integer)`,
		`CREATE TABLE message(id text primary key, session_id text, time_created integer,
			time_updated integer, data text)`,
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			model text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`INSERT INTO project VALUES ('p1','` + p.Data + `','demo',0,0)`,
		`INSERT INTO session VALUES ('s1','p1',` + itoa(old) + `,` + itoa(old) + `)`,
		`INSERT INTO message VALUES ('m1','s1',` + itoa(old) + `,` + itoa(old) + `,'dead weight')`,
		`INSERT INTO session_v2 VALUES ('s1','p1','` + p.Data + `','live','m',0,` +
			itoa(recent) + `,` + itoa(recent) + `)`,
		`INSERT INTO session_message VALUES ('sm1','s1','user',0,` + itoa(recent) + `,` +
			itoa(recent) + `,'hello')`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// Free pages, so the reclaimable segment is non-zero too.
	if _, err := conn.SQL.Exec(`CREATE TABLE junk(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.SQL.Exec(`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n < 500)
		INSERT INTO junk SELECT randomblob(2000) FROM c`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.SQL.Exec(`DELETE FROM junk`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	s, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Composition()

	var sum int64
	for _, seg := range c.Segments() {
		sum += seg.Bytes
	}
	if sum != c.Total {
		t.Errorf("segments sum to %d, file is %d", sum, c.Total)
	}
	if c.Total != s.Stats.FileBytes() {
		t.Errorf("composition total %d != file %d", c.Total, s.Stats.FileBytes())
	}
	if c.Frozen <= 0 {
		t.Error("the frozen generation is not reported")
	}
	if c.Live <= 0 {
		t.Error("the live generation is not reported")
	}
	if c.Free <= 0 {
		t.Error("free pages are not reported")
	}
	if ratio := c.Ratio(); ratio <= 1 {
		t.Errorf("ratio %.2f should exceed 1 when a frozen generation is present", ratio)
	}
	if share := c.DiskShare(); share <= 0 {
		t.Error("no disk context; the comparable-size line would be missing")
	}
	for _, seg := range c.Segments() {
		if pct := seg.Percent(c.Total); pct < 0 || pct > 100 {
			t.Errorf("segment %q percent out of range: %.2f", seg.Key, pct)
		}
	}
}
