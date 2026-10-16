// Package storage turns a database handle and a data directory into a scan:
// what is on disk, what is reclaimable, and who is responsible for it.
package storage

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
)

// Artifact is one thing on disk under the data directory.
type Artifact struct {
	Name   string
	Path   string
	Detail string
	Bytes  int64
	Exists bool
}

// Reclaim is the reclaim model: what could be returned to the filesystem.
type Reclaim struct {
	DBFree           int64
	DBLive           int64
	SnapshotTotal    int64
	SnapshotPrunable int64
	LogTotal         int64
	LogReclaimable   int64
	ToolOutput       int64
	OrphanBytes      int64
	LegacyBytes      int64 // frozen generation: reachable only by deleting rows
	Total            int64
}

// SessionInfo is one OpenCode session with an estimated payload size.
type SessionInfo struct {
	ID        string
	ProjectID string
	Title     string
	Directory string
	Model     string
	Bytes     int64
	Messages  int64
	Cost      float64
	Updated   time.Time
	DirGone   bool
}

// ProjectInfo aggregates sessions by project.
type ProjectInfo struct {
	ID       string
	Name     string
	Worktree string
	Sessions int
	Bytes    int64
	LastUsed time.Time
	DirGone  bool
	Archived bool
}

// Orphan reports rows whose parent no longer exists.
type Orphan struct {
	What  string
	Count int64
	Bytes int64
}

// Generation is one representation of the conversation data. OpenCode carries
// two: the v1 session/message/part trio and the current v2 session_v2/
// session_message pair. Only one is being written at any time; the other is
// baggage that a vacuum cannot help with, because it is not free space.
type Generation struct {
	Name      string
	Tables    []string
	Rows      int64
	Bytes     int64
	LastWrite time.Time
	Frozen    bool
}

// Scan is the complete picture of one installation.
type Scan struct {
	Paths       paths.Paths
	Stats       db.Stats
	TableSizes  []db.TableSize
	Counts      map[string]int64
	Integrity   string
	FKIssues    int
	Artifacts   []Artifact
	Reclaim     Reclaim
	Sessions    []SessionInfo
	Projects    []ProjectInfo
	Orphans     []Orphan
	Generations []Generation
	Warnings    []string
	DirTotal    int64
	WALBytes    int64
	SHMBytes    int64
	DiskFree    int64
	DiskTotal   int64
	Scanned     time.Time
}

// OrphanQueries are the integrity checks for rows with missing parents.
var OrphanQueries = []struct{ What, Query string }{
	{"message rows with a missing session (v1)",
		"SELECT COUNT(*), COALESCE(SUM(LENGTH(data)),0) FROM message WHERE session_id NOT IN (SELECT id FROM session)"},
	{"part rows with a missing session (v1)",
		"SELECT COUNT(*), COALESCE(SUM(LENGTH(data)),0) FROM part WHERE session_id NOT IN (SELECT id FROM session)"},
	{"part rows with a missing message",
		"SELECT COUNT(*), COALESCE(SUM(LENGTH(data)),0) FROM part WHERE message_id NOT IN (SELECT id FROM message)"},
	{"session_message rows with a missing session (v2)",
		"SELECT COUNT(*), COALESCE(SUM(LENGTH(data)),0) FROM session_message WHERE session_id NOT IN (SELECT id FROM session_v2)"},
	{"sessions with a missing project",
		"SELECT COUNT(*), 0 FROM session_v2 WHERE project_id NOT IN (SELECT id FROM project)"},
}

// snapshotKeepRecent is how many of the newest snapshots the reclaim estimate
// treats as untouchable.
const snapshotKeepRecent = 5

// SnapshotKeepRecent exposes the retention the reclaim estimate assumes.
func SnapshotKeepRecent() int { return snapshotKeepRecent }

// Run performs a read-only scan. Safe while OpenCode is running.
func Run(p paths.Paths) (*Scan, error) {
	s := &Scan{Paths: p, Scanned: time.Now()}

	conn, err := db.Open(p.DB, false)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if s.Stats, err = conn.Stats(); err != nil {
		return nil, err
	}
	if s.Integrity, err = conn.Integrity(); err != nil {
		return nil, err
	}
	if s.FKIssues, err = conn.ForeignKeyViolations(); err != nil {
		return nil, err
	}
	if s.Counts, err = conn.RowCounts(); err != nil {
		return nil, err
	}
	if s.TableSizes, err = conn.TableSizes(); err != nil {
		return nil, err
	}

	scanArtifacts(s)
	if err := scanSessions(s, conn); err != nil {
		return nil, err
	}
	if err := scanProjects(s, conn); err != nil {
		return nil, err
	}
	scanGenerations(s, conn)
	scanOrphans(s, conn)
	buildReclaim(s)

	s.warn()
	return s, nil
}

func (s *Scan) warn() {
	if s.Integrity != "ok" {
		s.Warnings = append(s.Warnings, "integrity_check reports: "+s.Integrity)
	}
	if s.FKIssues > 0 {
		s.Warnings = append(s.Warnings,
			strconv.Itoa(s.FKIssues)+" foreign key violations")
	}
	if s.Stats.Freelist > 0 {
		s.Warnings = append(s.Warnings,
			"database holds "+paths.ByteSize(s.Stats.FreeBytes())+
				" in free pages - run a vacuum to return it")
	}
	if s.Stats.AutoVacuum == 0 {
		s.Warnings = append(s.Warnings,
			"auto_vacuum is off, so the file will not shrink on its own")
	}
	gone := 0
	for _, sess := range s.Sessions {
		if sess.DirGone {
			gone++
		}
	}
	if gone > 0 {
		s.Warnings = append(s.Warnings, strconv.Itoa(gone)+
			" sessions live in directories that no longer exist")
	}
	for _, o := range s.Orphans {
		s.Warnings = append(s.Warnings, o.What+" - "+strconv.FormatInt(o.Count, 10)+" rows")
	}
	for _, g := range s.Generations {
		if g.Frozen && g.Bytes > 0 {
			s.Warnings = append(s.Warnings, g.Name+" tables are frozen (last write "+
				g.LastWrite.Format("2006-01-02")+") and hold "+
				paths.ByteSize(g.Bytes)+" - not free space, only row deletion reclaims it")
		}
	}
}

func statSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return 0
	}
	return fi.Size()
}

func dirSize(p string) int64 {
	var total int64
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries are skipped
		}
		if d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

func exists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func scandir(p string) []os.DirEntry {
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil
	}
	return ents
}

func countDirs(p string) int {
	n := 0
	for _, e := range scandir(p) {
		if e.IsDir() {
			n++
		}
	}
	return n
}

func countFiles(p string) int {
	n := 0
	for _, e := range scandir(p) {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

func scanArtifacts(s *Scan) {
	p := s.Paths
	s.WALBytes = statSize(p.DB + "-wal")
	s.SHMBytes = statSize(p.DB + "-shm")

	entries := []Artifact{
		{"opencode.db", p.DB, "SQLite store, live " + paths.ByteSize(s.Stats.LiveBytes()), statSize(p.DB), false},
		{"opencode.db-wal", p.DB + "-wal", "write-ahead log", s.WALBytes, false},
		{"opencode.db-shm", p.DB + "-shm", "shared memory index", s.SHMBytes, false},
		{"snapshot/", p.Snapshot, strconv.Itoa(countDirs(p.Snapshot)) + " worktree snapshots", dirSize(p.Snapshot), false},
		{"log/", p.Log, "append-only, never rotated", dirSize(p.Log), false},
		{"tool-output/", p.ToolOutput, strconv.Itoa(countFiles(p.ToolOutput)) + " spilled tool results", dirSize(p.ToolOutput), false},
		{"shell/", p.Shell, "shell session state", dirSize(p.Shell), false},
		{"repos/", p.Repos, "repository cache", dirSize(p.Repos), false},
	}
	for i := range entries {
		entries[i].Exists = exists(entries[i].Path)
	}
	s.Artifacts = entries
	s.DirTotal = dirSize(p.Data)
	s.DiskFree, s.DiskTotal = diskSpace(p.Data)
}

func scanSessions(s *Scan, conn *db.DB) error {
	type agg struct {
		bytes int64
		rows  int64
	}
	sums := map[string]*agg{}

	// Only the live generation may be counted. session_message and the v1
	// message table hold the same messages (same ids), so summing both would
	// double count every conversation.
	tables := []string{"session_message"}
	if ok, err := conn.HasTable("session_message"); err != nil {
		return err
	} else if !ok {
		tables = []string{"part", "message"} // pre-v2 schema
	}

	for _, table := range tables {
		ok, err := conn.HasTable(table)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		rows, err := conn.SQL.Query(
			"SELECT session_id, SUM(LENGTH(data)), COUNT(*) FROM " + table + " GROUP BY session_id")
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var b, n int64
			if err := rows.Scan(&id, &b, &n); err != nil {
				rows.Close()
				return err
			}
			a := sums[id]
			if a == nil {
				a = &agg{}
				sums[id] = a
			}
			a.bytes += b
			a.rows += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	ok, err := conn.HasTable("session_v2")
	if err != nil || !ok {
		return err
	}
	cols, err := conn.Columns("session_v2")
	if err != nil {
		return err
	}
	// Schema drift: read whatever columns exist rather than failing on a
	// rename. Missing text columns become empty, missing numbers become zero.
	text := func(name string) string {
		if cols[name] {
			return "COALESCE(" + name + ",'')"
		}
		return "''"
	}
	num := func(name string) string {
		if cols[name] {
			return "COALESCE(" + name + ",0)"
		}
		return "0"
	}
	order := "rowid"
	if cols["time_updated"] {
		order = "time_updated"
	}
	q := "SELECT id, " + text("project_id") + ", " + text("title") + ", " +
		text("directory") + ", " + text("model") + ", " + num("cost") + ", " +
		num("time_updated") + " FROM session_v2 ORDER BY " + order + " DESC"

	rows, err := conn.SQL.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()

	dirCache := map[string]bool{}
	for rows.Next() {
		var si SessionInfo
		var updated int64
		if err := rows.Scan(&si.ID, &si.ProjectID, &si.Title, &si.Directory,
			&si.Model, &si.Cost, &updated); err != nil {
			return err
		}
		if a := sums[si.ID]; a != nil {
			si.Bytes = a.bytes
			si.Messages = a.rows
		}
		si.Updated = time.UnixMilli(updated)
		si.DirGone = !dirExistsCached(dirCache, si.Directory)
		s.Sessions = append(s.Sessions, si)
	}
	return rows.Err()
}

func scanProjects(s *Scan, conn *db.DB) error {
	ok, err := conn.HasTable("project")
	if err != nil || !ok {
		return err
	}
	byID := map[string]*ProjectInfo{}
	cols, err := conn.Columns("project")
	if err != nil {
		return err
	}
	text := func(name string) string {
		if cols[name] {
			return "COALESCE(" + name + ",'')"
		}
		return "''"
	}
	num := func(name string) string {
		if cols[name] {
			return "COALESCE(" + name + ",0)"
		}
		return "0"
	}
	rows, err := conn.SQL.Query("SELECT id, " + text("name") + ", " + text("worktree") +
		", " + num("time_updated") + ", " + num("time_archived") + " FROM project")
	if err != nil {
		return err
	}
	for rows.Next() {
		var pi ProjectInfo
		var updated, archived int64
		if err := rows.Scan(&pi.ID, &pi.Name, &pi.Worktree, &updated, &archived); err != nil {
			rows.Close()
			return err
		}
		pi.LastUsed = time.UnixMilli(updated)
		pi.Archived = archived > 0
		pi.DirGone = !exists(pi.Worktree)
		byID[pi.ID] = &pi
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, sess := range s.Sessions {
		pi := byID[sess.ProjectID]
		if pi == nil {
			continue
		}
		pi.Sessions++
		pi.Bytes += sess.Bytes
		if sess.Updated.After(pi.LastUsed) {
			pi.LastUsed = sess.Updated
		}
	}
	for _, pi := range byID {
		s.Projects = append(s.Projects, *pi)
	}
	sort.Slice(s.Projects, func(i, j int) bool {
		if s.Projects[i].Bytes != s.Projects[j].Bytes {
			return s.Projects[i].Bytes > s.Projects[j].Bytes
		}
		return s.Projects[i].Sessions > s.Projects[j].Sessions
	})
	return nil
}

func dirExistsCached(cache map[string]bool, dir string) bool {
	if dir == "" {
		return false
	}
	if v, ok := cache[dir]; ok {
		return v
	}
	v := exists(dir)
	cache[dir] = v
	return v
}

// scanGenerations works out which representation OpenCode is still writing and
// which it has abandoned. A frozen generation is not free space, so much the
// opposite: only deleting its rows reclaims it, and that needs care.
func scanGenerations(s *Scan, conn *db.DB) {
	bytesByTable := map[string]int64{}
	for _, t := range s.TableSizes {
		bytesByTable[t.Name] = t.Bytes
	}

	gens := []Generation{
		{Name: "v1 (legacy)", Tables: []string{"session", "message", "part"}},
		{Name: "v2 (current)", Tables: []string{"session_v2", "session_message"}},
	}
	const frozenAfter = 7 * 24 * time.Hour
	for i := range gens {
		var newest time.Time
		for _, table := range gens[i].Tables {
			last, rows, err := conn.LastWrite(table, "time_created")
			if err != nil {
				continue
			}
			gens[i].Rows += rows
			gens[i].Bytes += bytesByTable[table]
			if last.After(newest) {
				newest = last
			}
		}
		gens[i].LastWrite = newest
	}

	// Frozen is relative to the other generations, not to the wall clock: a
	// generation has been abandoned when it stopped being written a while
	// before the newest write anywhere. Comparing against time.Now() called a
	// fresh install's current tables "frozen" because their timestamps were
	// epoch, which is exactly backwards.
	var newest time.Time
	for _, g := range gens {
		if g.LastWrite.After(newest) {
			newest = g.LastWrite
		}
	}
	for i := range gens {
		gens[i].Frozen = gens[i].Rows > 0 && !newest.IsZero() &&
			newest.Sub(gens[i].LastWrite) > frozenAfter
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i].Bytes > gens[j].Bytes })
	s.Generations = gens
}

func scanOrphans(s *Scan, conn *db.DB) {
	for _, oq := range OrphanQueries {
		rows, err := conn.SQL.Query(oq.Query)
		if err != nil {
			continue // table absent in this schema version
		}
		var o Orphan
		o.What = oq.What
		if rows.Next() {
			if err := rows.Scan(&o.Count, &o.Bytes); err != nil {
				rows.Close()
				continue
			}
		}
		rows.Close()
		if o.Count > 0 {
			s.Orphans = append(s.Orphans, o)
		}
	}
}

func buildReclaim(s *Scan) {
	r := &s.Reclaim
	r.DBFree = s.Stats.FreeBytes()
	r.DBLive = s.Stats.LiveBytes()

	type snap struct {
		size int64
		mod  time.Time
	}
	var snaps []snap
	for _, e := range scandir(s.Paths.Snapshot) {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(s.Paths.Snapshot, e.Name())
		size := dirSize(p)
		r.SnapshotTotal += size
		mod := time.Time{}
		if fi, err := e.Info(); err == nil {
			mod = fi.ModTime()
		}
		snaps = append(snaps, snap{size, mod})
	}
	// Keep the newest few; everything older is reported as prunable.
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].mod.After(snaps[j].mod) })
	for i, sn := range snaps {
		if i >= snapshotKeepRecent {
			r.SnapshotPrunable += sn.size
		}
	}

	r.LogTotal = dirSize(s.Paths.Log)
	const keepLog = 1 << 20
	if r.LogTotal > keepLog {
		r.LogReclaimable = r.LogTotal - keepLog
	}
	r.ToolOutput = dirSize(s.Paths.ToolOutput)

	for _, o := range s.Orphans {
		r.OrphanBytes += o.Bytes
	}

	// A frozen generation is real, reachable data: reclaimable only by
	// deleting rows, so it is reported but never counted as free space.
	for _, g := range s.Generations {
		if g.Frozen {
			r.LegacyBytes += g.Bytes
		}
	}

	r.Total = r.DBFree + r.SnapshotPrunable + r.LogReclaimable + r.OrphanBytes
}
