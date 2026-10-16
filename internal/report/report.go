// Package report renders a scan or an action result as text, markdown or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/fakih/ocsa/internal/actions"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

// Text writes a plain aligned summary, the same view the TUI opens on.
func Text(w io.Writer, s *storage.Scan) {
	kv := func(k, v string) { fmt.Fprintf(w, "%-14s %s\n", k, v) }

	kv("data", s.Paths.Data)
	kv("database", s.Paths.DB)

	fmt.Fprintln(w, "\nDATABASE")
	db := s.Stats
	comp := s.Composition()
	fmt.Fprintf(w, "  %s on disk\n", paths.ByteSize(comp.Total))
	for _, seg := range comp.Segments() {
		fmt.Fprintf(w, "  %-14s %10s  %5.1f%%\n", seg.Label,
			paths.ByteSize(seg.Bytes), seg.Percent(comp.Total))
	}
	var scale []string
	if ratio := comp.Ratio(); ratio > 0 {
		scale = append(scale, fmt.Sprintf("%.1fx the live data", ratio))
	}
	if share := comp.DiskShare(); share > 0 {
		scale = append(scale, fmt.Sprintf("%.1f%% of the %s free on disk",
			share, paths.ByteSize(comp.DiskFree)))
	}
	if len(scale) > 0 {
		fmt.Fprintf(w, "  %s\n", strings.Join(scale, " | "))
	}
	fmt.Fprintf(w, "  pages %d x %d   auto_vacuum %s   journal %s   integrity %s   wal %s\n",
		db.PageCount, db.PageSize, modeName(db.AutoVacuum), db.JournalMode,
		s.Integrity, paths.ByteSize(s.WALBytes))

	if len(s.TableSizes) > 0 {
		fmt.Fprintln(w, "\nTABLES  (including indexes)")
		for _, t := range s.TableSizes {
			if t.Bytes < 16*1024 {
				continue
			}
			fmt.Fprintf(w, "  %-46s %10s\n", t.Name, paths.ByteSize(t.Bytes))
		}
	}

	if len(s.Generations) > 0 {
		fmt.Fprintln(w, "\nGENERATIONS  (one representation is written, the other is baggage)")
		for _, g := range s.Generations {
			state := "live"
			if g.Frozen {
				state = "FROZEN"
			}
			fmt.Fprintf(w, "  %-14s %10s  %6d rows  last write %s  %s\n",
				g.Name, paths.ByteSize(g.Bytes), g.Rows,
				g.LastWrite.Format("2006-01-02"), state)
		}
	}

	fmt.Fprintln(w, "\nRECLAIMABLE")
	r := s.Reclaim
	rows := []struct {
		name string
		val  int64
	}{
		{"database free pages", r.DBFree},
		{"snapshot/ (all but newest " + itoa(storage.SnapshotKeepRecent()) + ")", r.SnapshotPrunable},
		{"log (keeping 1 MB)", r.LogReclaimable},
		{"orphan rows", r.OrphanBytes},
	}
	for _, row := range rows {
		fmt.Fprintf(w, "  %-46s %10s\n", row.name, paths.ByteSize(row.val))
	}
	fmt.Fprintf(w, "  %-46s %10s\n", "TOTAL", paths.ByteSize(r.Total))
	if r.LegacyBytes > 0 {
		fmt.Fprintf(w, "  %-46s %10s\n", "frozen generation (needs row deletion)", paths.ByteSize(r.LegacyBytes))
	}
	fmt.Fprintf(w, "  (%s snapshots total, %s tool-output, %s data dir)\n",
		paths.ByteSize(r.SnapshotTotal), paths.ByteSize(r.ToolOutput), paths.ByteSize(s.DirTotal))

	if len(s.Sessions) > 0 {
		fmt.Fprintln(w, "\nBIGGEST SESSIONS")
		sess := append([]storage.SessionInfo(nil), s.Sessions...)
		sort.Slice(sess, func(i, j int) bool { return sess[i].Bytes > sess[j].Bytes })
		n := 10
		if len(sess) < n {
			n = len(sess)
		}
		for _, si := range sess[:n] {
			gone := ""
			if si.DirGone {
				gone = "  [directory gone]"
			}
			fmt.Fprintf(w, "  %10s  %-38s %s%s\n",
				paths.ByteSize(si.Bytes), truncate(si.Title, 38), si.Directory, gone)
		}
	}

	if len(s.Projects) > 0 {
		fmt.Fprintln(w, "\nPROJECTS")
		for i, p := range s.Projects {
			if i >= 10 {
				break
			}
			name := p.Name
			if name == "" {
				name = p.Worktree
			}
			fmt.Fprintf(w, "  %10s  %3d sessions  %s\n",
				paths.ByteSize(p.Bytes), p.Sessions, truncate(name, 52))
		}
	}

	if len(s.Warnings) > 0 {
		fmt.Fprintln(w, "\nWARNINGS")
		for _, warn := range s.Warnings {
			fmt.Fprintf(w, "  - %s\n", warn)
		}
	}
}

// Markdown renders a committable report.
func Markdown(s *storage.Scan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# OpenCode storage report\n\n")
	fmt.Fprintf(&b, "- scanned: %s\n", s.Scanned.Format(time.RFC3339))
	fmt.Fprintf(&b, "- data dir: `%s`\n", s.Paths.Data)
	fmt.Fprintf(&b, "- integrity: `%s`, foreign key violations: %d\n", s.Integrity, s.FKIssues)
	fmt.Fprintf(&b, "- sessions: %d, projects: %d\n\n", len(s.Sessions), len(s.Projects))

	comp := s.Composition()

	fmt.Fprintf(&b, "## Database\n\n| metric | value |\n|---|---|\n")
	fmt.Fprintf(&b, "| file | %s |\n", paths.ByteSize(s.Stats.FileBytes()))
	fmt.Fprintf(&b, "| live | %s |\n", paths.ByteSize(s.Stats.LiveBytes()))
	fmt.Fprintf(&b, "| free pages | %s |\n", paths.ByteSize(s.Stats.FreeBytes()))
	fmt.Fprintf(&b, "| WAL | %s |\n", paths.ByteSize(s.WALBytes))
	fmt.Fprintf(&b, "| page count | %d |\n", s.Stats.PageCount)
	fmt.Fprintf(&b, "| auto_vacuum | %s |\n\n", modeName(s.Stats.AutoVacuum))

	fmt.Fprintf(&b, "### What the file is made of\n\n| part | size | share |\n|---|---|---|\n")
	for _, seg := range comp.Segments() {
		note := ""
		switch seg.Key {
		case "frozen":
			note = " (a vacuum cannot free this)"
		case "reclaimable":
			note = " (a vacuum returns this)"
		}
		fmt.Fprintf(&b, "| %s%s | %s | %.1f%% |\n", seg.Label, note,
			paths.ByteSize(seg.Bytes), seg.Percent(comp.Total))
	}
	if ratio := comp.Ratio(); ratio > 0 {
		fmt.Fprintf(&b, "\nThe file is **%.1fx** the size of the data in active use.\n", ratio)
	}
	if share := comp.DiskShare(); share > 0 {
		fmt.Fprintf(&b, "It is %.1f%% of the %s free on this disk.\n",
			share, paths.ByteSize(comp.DiskFree))
	}
	b.WriteString("\n")

	if len(s.TableSizes) > 0 {
		fmt.Fprintf(&b, "## Tables by size (including indexes)\n\n| table | size |\n|---|---|\n")
		for _, t := range s.TableSizes {
			if t.Bytes < 16*1024 {
				continue
			}
			fmt.Fprintf(&b, "| `%s` | %s |\n", t.Name, paths.ByteSize(t.Bytes))
		}
		b.WriteString("\n")
	}

	if len(s.Generations) > 0 {
		fmt.Fprintf(&b, "## Generations\n\n| representation | size | rows | last write | state |\n|---|---|---|---|---|\n")
		for _, g := range s.Generations {
			state := "live"
			if g.Frozen {
				state = "frozen"
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n",
				g.Name, paths.ByteSize(g.Bytes), g.Rows, g.LastWrite.Format("2006-01-02"), state)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Reclaimable\n\n| item | size |\n|---|---|\n")
	fmt.Fprintf(&b, "| database free pages | %s |\n", paths.ByteSize(s.Reclaim.DBFree))
	fmt.Fprintf(&b, "| prunable snapshots | %s |\n", paths.ByteSize(s.Reclaim.SnapshotPrunable))
	fmt.Fprintf(&b, "| log | %s |\n", paths.ByteSize(s.Reclaim.LogReclaimable))
	fmt.Fprintf(&b, "| orphan rows | %s |\n", paths.ByteSize(s.Reclaim.OrphanBytes))
	fmt.Fprintf(&b, "| **total** | **%s** |\n\n", paths.ByteSize(s.Reclaim.Total))
	if s.Reclaim.LegacyBytes > 0 {
		fmt.Fprintf(&b, "Plus %s in the frozen generation, reclaimable only by deleting rows.\n\n",
			paths.ByteSize(s.Reclaim.LegacyBytes))
	}

	fmt.Fprintf(&b, "## Biggest sessions\n\n| size | title | directory | messages |\n|---|---|---|---|\n")
	sess := append([]storage.SessionInfo(nil), s.Sessions...)
	sort.Slice(sess, func(i, j int) bool { return sess[i].Bytes > sess[j].Bytes })
	for i, si := range sess {
		if i >= 20 {
			break
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` | %d |\n",
			paths.ByteSize(si.Bytes), escapePipes(truncate(si.Title, 60)), si.Directory, si.Messages)
	}
	b.WriteString("\n")

	if len(s.Warnings) > 0 {
		fmt.Fprintf(&b, "## Warnings\n\n")
		for _, w := range s.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Reclaim\n\n```sh\nsqlite3 %q \"PRAGMA busy_timeout=300000; VACUUM;\"\nsqlite3 %q \"PRAGMA wal_checkpoint(TRUNCATE);\"\n```\n",
		s.Paths.DB, s.Paths.DB)
	return b.String()
}

// JSON renders the scan as JSON.
func JSON(w io.Writer, s *storage.Scan) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// ResultText writes an action result.
func ResultText(w io.Writer, r actions.Result) {
	verb := r.Action
	if r.DryRun {
		verb += " (dry run)"
	}
	fmt.Fprintf(w, "%s: %s -> %s, reclaimed %s in %s\n",
		verb, paths.ByteSize(r.Before), paths.ByteSize(r.After),
		paths.ByteSize(r.Reclaimed), r.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "verified: %v\n", r.Verified)
	for _, s := range r.Steps {
		fmt.Fprintf(w, "  . %s\n", s)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "  ! %s\n", n)
	}
}

// ResultJSON renders an action result as JSON.
func ResultJSON(w io.Writer, r actions.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func modeName(v int) string {
	switch v {
	case 1:
		return "full"
	case 2:
		return "incremental"
	default:
		return "none"
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func escapePipes(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func itoa(n int) string { return fmt.Sprintf("%d", n) }
