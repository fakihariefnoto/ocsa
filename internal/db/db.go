// Package db opens and interrogates an OpenCode SQLite store.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Stats is the page-level view of the database file.
type Stats struct {
	PageSize    int64
	PageCount   int64
	Freelist    int64
	AutoVacuum  int
	JournalMode string
}

// FileBytes is the size of the file on disk.
func (s Stats) FileBytes() int64 { return s.PageSize * s.PageCount }

// LiveBytes is the part of the file holding actual rows.
func (s Stats) LiveBytes() int64 { return s.PageSize * (s.PageCount - s.Freelist) }

// FreeBytes is the part of the file sitting in the freelist, reclaimable by
// VACUUM with no data loss.
func (s Stats) FreeBytes() int64 { return s.PageSize * s.Freelist }

// TableSize is one row of the dbstat breakdown.
type TableSize struct {
	Name  string
	Bytes int64
	Pages int64
}

// DB is a handle on an OpenCode database.
type DB struct {
	SQL  *sql.DB
	Path string
	rw   bool
}

// Open connects to path. Read-only handles are safe against a live OpenCode.
// The file must already exist: the tool must never invent a database next to a
// typo in --data-dir.
func Open(path string, rw bool) (*DB, error) {
	mode := "ro"
	if rw {
		mode = "rw"
	}
	return open(path, mode)
}

// Create opens path read-write, creating it when missing. Used by tests and
// fixture builders only.
func Create(path string) (*DB, error) { return open(path, "rwc") }

func open(path, mode string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=%s", path, mode)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1) // one writer/lock; avoids self-deadlock on PRAGMA
	if _, err := conn.Exec("PRAGMA busy_timeout=300000"); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{SQL: conn, Path: path, rw: mode != "ro"}, nil
}

// Close releases the handle.
func (d *DB) Close() error { return d.SQL.Close() }

// RW reports whether the handle can write.
func (d *DB) RW() bool { return d.rw }

func (d *DB) pragmaInt(name string) (int64, error) {
	var v int64
	err := d.SQL.QueryRow("PRAGMA " + name).Scan(&v)
	return v, err
}

// Stats reads the page-level counters.
func (d *DB) Stats() (Stats, error) {
	var s Stats
	var err error
	if s.PageSize, err = d.pragmaInt("page_size"); err != nil {
		return s, err
	}
	if s.PageCount, err = d.pragmaInt("page_count"); err != nil {
		return s, err
	}
	if s.Freelist, err = d.pragmaInt("freelist_count"); err != nil {
		return s, err
	}
	av, err := d.pragmaInt("auto_vacuum")
	if err != nil {
		return s, err
	}
	s.AutoVacuum = int(av)
	if err := d.SQL.QueryRow("PRAGMA journal_mode").Scan(&s.JournalMode); err != nil {
		return s, err
	}
	return s, nil
}

// Integrity runs PRAGMA integrity_check and returns its verdict.
func (d *DB) Integrity() (string, error) {
	rows, err := d.SQL.Query("PRAGMA integrity_check")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var verdicts []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		verdicts = append(verdicts, v)
	}
	if len(verdicts) == 0 {
		return "unknown", rows.Err()
	}
	if verdicts[0] == "ok" && len(verdicts) > 1 {
		return strings.Join(verdicts, "; "), nil
	}
	return verdicts[0], rows.Err()
}

// ForeignKeyViolations counts rows returned by PRAGMA foreign_key_check.
func (d *DB) ForeignKeyViolations() (int, error) {
	rows, err := d.SQL.Query("PRAGMA foreign_key_check")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// HasTable reports whether name exists in the schema.
func (d *DB) HasTable(name string) (bool, error) {
	var n int
	err := d.SQL.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	return n > 0, err
}

// Count returns the row count of a table, or 0 if the table is absent.
func (d *DB) Count(table string) (int64, error) {
	ok, err := d.HasTable(table)
	if err != nil || !ok {
		return 0, err
	}
	var n int64
	err = d.SQL.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&n)
	return n, err
}

// RowCounts returns counts for every table OCSA tracks, skipping absent ones.
func (d *DB) RowCounts() (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range VerifiableTables {
		n, err := d.Count(t)
		if err != nil {
			return out, err
		}
		if ok, _ := d.HasTable(t); ok {
			out[t] = n
		}
	}
	return out, nil
}

// VerifiableTables are the tables compared before and after a mutation.
var VerifiableTables = []string{
	"project", "session", "session_v2", "message", "session_message", "part",
	"todo", "permission", "session_pending", "session_inbox",
}

// TableSizes reports the footprint of each table including its indexes, which
// is what a reader actually wants: dbstat alone lists every index separately.
// Returns nil (not an error) when the build has no dbstat, so callers degrade.
func (d *DB) TableSizes() ([]TableSize, error) {
	rows, err := d.SQL.Query(`
		SELECT m.tbl_name AS tbl, SUM(d.pgsize) AS bytes, COUNT(*) AS pages
		FROM dbstat d JOIN sqlite_master m ON m.name = d.name
		GROUP BY tbl ORDER BY bytes DESC`)
	if err != nil {
		return nil, nil //nolint:nilerr // graceful degradation
	}
	defer rows.Close()
	var out []TableSize
	for rows.Next() {
		var t TableSize
		if err := rows.Scan(&t.Name, &t.Bytes, &t.Pages); err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastWrite returns the newest time_created in a table, and its row count.
// Used to tell live tables from tables the application stopped writing to.
// A table without the column yields a zero time rather than an error.
func (d *DB) LastWrite(table, column string) (time.Time, int64, error) {
	ok, err := d.HasTable(table)
	if err != nil || !ok {
		return time.Time{}, 0, err
	}
	var n int64
	if err := d.SQL.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&n); err != nil {
		return time.Time{}, 0, err
	}
	cols, err := d.Columns(table)
	if err != nil {
		return time.Time{}, n, err
	}
	if !cols[column] {
		return time.Time{}, n, nil
	}
	var maxVal sql.NullInt64
	if err := d.SQL.QueryRow("SELECT MAX(" + quoteIdent(column) + ") FROM " +
		quoteIdent(table)).Scan(&maxVal); err != nil {
		return time.Time{}, n, err
	}
	if !maxVal.Valid {
		return time.Time{}, n, nil
	}
	return time.UnixMilli(maxVal.Int64), n, nil
}

// Columns lists the column names of a table, so callers can tolerate schema
// drift between OpenCode versions instead of failing on a renamed column.
func (d *DB) Columns(table string) (map[string]bool, error) {
	ok, err := d.HasTable(table)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.SQL.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return cols, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// Checkpoint flushes the write-ahead log. (busy, log, checkpointed)
func (d *DB) Checkpoint() (int64, int64, int64, error) {
	var busy, logFrames, ckpted int64
	err := d.SQL.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &ckpted)
	if err != nil {
		// Not in WAL mode: nothing to do.
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, err
	}
	return busy, logFrames, ckpted, nil
}

// Vacuum rewrites the file in place, dropping freelist pages. Safe against a
// running OpenCode: writes are serialised by busy_timeout and SQLite rolls the
// whole operation back if it cannot acquire the lock.
func (d *DB) Vacuum() error {
	_, err := d.SQL.Exec("VACUUM")
	return err
}

// SetAutoVacuum changes the auto-vacuum mode. The value only takes effect
// after a VACUUM, so callers run Vacuum immediately afterwards.
func (d *DB) SetAutoVacuum(mode int) error {
	_, err := d.SQL.Exec(fmt.Sprintf("PRAGMA auto_vacuum=%d", mode))
	return err
}

// IncrementalVacuum returns freelist pages to the filesystem in small steps.
// Requires auto_vacuum=INCREMENTAL to have any effect.
func (d *DB) IncrementalVacuum() error {
	_, err := d.SQL.Exec("PRAGMA incremental_vacuum")
	return err
}

// Exec runs a statement, for the orphan sweeps.
func (d *DB) Exec(q string, args ...any) (int64, error) {
	res, err := d.SQL.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
