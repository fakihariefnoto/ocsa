package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

// View implements tea.Model.
func (m Model) View() string {
	if m.err != nil {
		return stBad.Render("scan failed: "+m.err.Error()) + "\n\n" + stSub.Render("q to quit") + "\n"
	}
	if m.scan == nil {
		return "\n  " + m.spinner.View() + " scanning " + m.p.Data + "…\n"
	}
	switch m.screen {
	case scrProjects:
		return m.viewProjects()
	case scrSessions:
		return m.viewSessions()
	case scrConfirm:
		return m.viewConfirm()
	case scrRunning:
		return m.viewRunning()
	case scrDone:
		return m.viewDone()
	default:
		return m.viewDash()
	}
}

// frame wraps a body in the box and appends the footer.
func (m Model) frame(body string) string {
	head := stTitle.Render("OCSA") + stSub.Render("  "+m.p.Data)
	if m.status != "" {
		head += "   " + stOK.Render(m.status)
	}
	inner := body
	if m.width > 6 {
		inner = lipglossWidth(body, min(m.width-4, 104))
	}
	return head + "\n" + stBox.Render(inner) + "\n" + m.footer()
}

func (m Model) footer() string {
	switch m.screen {
	case scrDash:
		return " " + stKey.Render("[↑↓]") + " " + stSub.Render("choose") + "   " +
			stKey.Render("[enter]") + " " + stSub.Render("run") + "   " +
			stKey.Render("[p]") + " " + stSub.Render("projects") + "   " +
			stKey.Render("[s]") + " " + stSub.Render("sessions") + "   " +
			stKey.Render("[q]") + " " + stSub.Render("quit")
	case scrProjects:
		return " " + stKey.Render("[↑↓]") + " " + stSub.Render("move") + "   " +
			stKey.Render("[enter]") + " " + stSub.Render("open its sessions") + "   " +
			stKey.Render("[esc]") + " " + stSub.Render("back")
	case scrSessions:
		mode := "by size"
		if m.grouped {
			mode = "by folder"
		}
		return " " + stKey.Render("[↑↓]") + " " + stSub.Render("move") + "   " +
			stKey.Render("[g]") + " " + stSub.Render("group "+mode) + "   " +
			stKey.Render("[enter]") + " " + stSub.Render("filter to project") + "   " +
			stKey.Render("[esc]") + " " + stSub.Render("back")
	case scrConfirm:
		return " " + stKey.Render("[y]") + " " + stSub.Render("run") + "   " +
			stKey.Render("[n]") + " " + stSub.Render("cancel")
	default:
		return " " + stKey.Render("[esc]") + " " + stSub.Render("back") + "   " +
			stKey.Render("[enter]") + " " + stSub.Render("rescan") + "   " +
			stKey.Render("[q]") + " " + stSub.Render("quit")
	}
}

func (m Model) viewDash() string {
	s := m.scan
	var b strings.Builder

	// One line for the file, then one for its bookkeeping.
	b.WriteString(stLabel.Render("DATABASE") + "  " +
		bar(s.Stats.LiveBytes(), s.Stats.FileBytes(), 30) + "  " +
		stValue.Render(paths.ByteSize(s.Stats.FileBytes())) + "   " +
		stLabel.Render("live ") + stValue.Render(paths.ByteSize(s.Stats.LiveBytes())) + "   " +
		stLabel.Render("free ") + freeStyle(s.Stats.FreeBytes()) + "   " +
		stLabel.Render("WAL ") + stValue.Render(paths.ByteSize(s.WALBytes)) + "\n")
	b.WriteString("          " + stLabel.Render(fmt.Sprintf("%d pages × %d B", s.Stats.PageCount, s.Stats.PageSize)) +
		"   " + stLabel.Render("auto_vacuum ") + modeStyle(s.Stats.AutoVacuum) +
		"   " + stLabel.Render("integrity ") + integrityStyle(s.Integrity) + "\n")

	// Generations: the thing a plain size report misses.
	if len(s.Generations) > 0 {
		b.WriteString("\n" + stLabel.Render("GENERATIONS") + "\n")
		for _, g := range s.Generations {
			state := stOK.Render("live")
			if g.Frozen {
				state = stWarn.Render("FROZEN since " + g.LastWrite.Format("2006-01-02"))
			}
			b.WriteString("  " + stValue.Render(padLeft(paths.ByteSize(g.Bytes), 10)) + "  " +
				stText.Render(padRight(g.Name, 14)) + state + "\n")
		}
	}

	r := s.Reclaim
	b.WriteString("\n" + stLabel.Render("RECLAIMABLE") + "  " +
		stGoodBar.Render(paths.ByteSize(r.Total)) + stSub.Render("   free pages "+
		paths.ByteSize(r.DBFree)+" · snapshots "+paths.ByteSize(r.SnapshotPrunable)+
		" · log "+paths.ByteSize(r.LogReclaimable)))
	if r.LegacyBytes > 0 {
		b.WriteString(stSub.Render("   |   frozen " + paths.ByteSize(r.LegacyBytes) + " needs row deletion"))
	}
	b.WriteString("\n")

	b.WriteString("\n" + stLabel.Render("SESSIONS") + "  " + stValue.Render(
		plural(len(s.Sessions), "session")+" · "+plural(len(s.Projects), "project")+" · "+
			paths.ByteSize(sessionTotal(s))+" of payload · biggest "+
			paths.ByteSize(biggestSession(s))) + "\n")

	// The action menu, navigable with the arrows.
	b.WriteString("\n" + stLabel.Render("ACTIONS") + "\n")
	items := m.menu()
	for i, it := range items {
		marker := "  "
		label := stText.Render(padRight(it.label, 30))
		if i == m.cursor {
			marker = stCursor.Render("▸ ")
			label = stSelected.Render(padRight(" "+it.label+" ", 30))
		}
		line := "  " + marker + label + " " + stSub.Render(it.hint)
		if it.key != "" {
			line += "  " + stKey.Render("["+it.key+"]")
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}

	for _, w := range s.Warnings {
		if strings.Contains(w, "free pages") {
			continue // already shown on the reclaimable line
		}
		b.WriteString("\n  " + stWarn.Render("! ") + stSub.Render(w))
	}
	return m.frame(strings.TrimRight(b.String(), "\n"))
}

func freeStyle(free int64) string {
	if free == 0 {
		return stOK.Render("0 B")
	}
	return stWarn.Render(paths.ByteSize(free))
}

func (m Model) viewProjects() string {
	rows := m.projectRows()
	var b strings.Builder

	var totalBytes int64
	var totalSessions int
	for _, p := range rows {
		totalBytes += p.Bytes
		totalSessions += p.Sessions
	}
	b.WriteString(fmt.Sprintf("%s  %s\n\n", stLabel.Render("PROJECTS"),
		stSub.Render(plural(len(rows), "project")+" · "+plural(totalSessions, "session")+
			" · "+paths.ByteSize(totalBytes)+" of session payload")))
	b.WriteString("  " + stLabel.Render(padLeft("SIZE", 10)+"  "+padLeft("SESS", 5)+"  "+
		padRight("PROJECT", 22)+"  "+padRight("FOLDER", 40)+"  "+"LAST USED") + "\n")

	visible := m.visibleRows()
	end := min(m.offset+visible, len(rows))
	for i := m.offset; i < end; i++ {
		p := rows[i]
		gone := ""
		folderStyle := stSub
		if p.DirGone {
			gone = " " + stWarn.Render("gone")
			folderStyle = stWarn
		}
		label := projectLabel(p)
		row := "  " + stValue.Render(padLeft(paths.ByteSize(p.Bytes), 10)) + "  " +
			stValue.Render(padLeft(fmt.Sprint(p.Sessions), 5)) + "  " +
			stRow.Render(padRight(trunc(label, 22), 22)) + "  " +
			folderStyle.Render(padRight(trunc(shortenPath(p.Worktree), 40), 40)) + "  " +
			stSub.Render(ago(p.LastUsed)) + gone
		if i == m.cursor {
			row = " " + stCursor.Render("▸") + strings.TrimPrefix(row, "  ")
		}
		b.WriteString(strings.TrimRight(row, " ") + "\n")
	}
	if end < len(rows) {
		b.WriteString("\n  " + stSub.Render(fmt.Sprintf("… %d more", len(rows)-end)))
	}
	if len(rows) == 0 {
		b.WriteString("\n  " + stSub.Render("no projects recorded"))
	}
	return m.frame(strings.TrimRight(b.String(), "\n"))
}

func (m Model) viewSessions() string {
	rows := m.sessionRows()
	var b strings.Builder

	var total int64
	for _, si := range rows {
		total += si.Bytes
	}
	scope := "all projects"
	if m.filterProject != "" {
		for _, p := range m.scan.Projects {
			if p.ID == m.filterProject {
				scope = projectLabel(p)
				break
			}
		}
	}
	order := "largest first"
	if m.grouped {
		order = "grouped by folder"
	}
	b.WriteString(fmt.Sprintf("%s  %s\n\n", stLabel.Render("SESSIONS"),
		stSub.Render(scope+" · "+plural(len(rows), "session")+" · "+
			paths.ByteSize(total)+" · "+order)))

	// Subtotals per folder make the grouped view readable without arithmetic.
	subtotal := map[string]int64{}
	count := map[string]int{}
	for _, si := range rows {
		subtotal[si.Directory] += si.Bytes
		count[si.Directory]++
	}

	b.WriteString("  " + stLabel.Render(padLeft("SIZE", 10)+"  "+padRight("TITLE", 46)+"  "+
		padRight("FOLDER", 30)+"  "+"UPDATED") + "\n")

	visible := m.visibleRows()
	end := min(m.offset+visible, len(rows))
	currentDir := ""
	for i := m.offset; i < end; i++ {
		si := rows[i]
		if m.grouped && si.Directory != currentDir {
			currentDir = si.Directory
			dirStyle := stGroup
			if si.DirGone {
				dirStyle = stWarn
			}
			b.WriteString("\n  " + dirStyle.Render(shortenPath(si.Directory)) +
				stSub.Render("   "+paths.ByteSize(subtotal[si.Directory])+" · "+
					plural(count[si.Directory], "session")) + "\n")
		}
		row := "  " + stValue.Render(padLeft(paths.ByteSize(si.Bytes), 10)) + "  " +
			stRow.Render(padRight(trunc(si.Title, 46), 46)) + "  " +
			folderCell(si) + "  " + stSub.Render(ago(si.Updated))
		if i == m.cursor {
			row = " " + stCursor.Render("▸") + strings.TrimPrefix(row, "  ")
		}
		b.WriteString(strings.TrimRight(row, " ") + "\n")
	}
	if end < len(rows) {
		b.WriteString("\n  " + stSub.Render(fmt.Sprintf("… %d more", len(rows)-end)))
	}
	if len(rows) == 0 {
		b.WriteString("\n  " + stSub.Render("no sessions"))
	}
	return m.frame(strings.TrimRight(b.String(), "\n"))
}

func folderCell(si storage.SessionInfo) string {
	label := padRight(trunc(shortenPath(si.Directory), 30), 30)
	if si.DirGone {
		return stWarn.Render(label)
	}
	return stSub.Render(label)
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(stWarn.Render("CONFIRM") + "\n\n")
	b.WriteString("  " + stText.Render(m.confirmLabel) + "\n")
	if m.confirmNote != "" {
		b.WriteString("  " + stSub.Render(m.confirmNote) + "\n")
	}
	b.WriteString("\n  " + stLabel.Render("y to proceed, n to cancel"))
	return m.frame(b.String())
}

func (m Model) viewRunning() string {
	elapsed := time.Since(m.started).Round(100 * time.Millisecond)
	var b strings.Builder
	b.WriteString("\n  " + m.spinner.View() + " " + stText.Render(m.confirmLabel) + "\n")
	b.WriteString("  " + stSub.Render("elapsed "+elapsed.String()) + "\n\n")
	b.WriteString("  " + stLabel.Render("the database is locked for this moment only; SQLite rolls back"))
	b.WriteString("\n  " + stLabel.Render("everything if it cannot finish"))
	return m.frame(b.String())
}

func (m Model) viewDone() string {
	r := m.result
	var b strings.Builder
	if m.runErr != nil {
		b.WriteString(stBad.Render("FAILED: "+m.runErr.Error()) + "\n")
	} else if r.DryRun {
		b.WriteString(stWarn.Render("DRY RUN") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  %s %s → %s\n", stLabel.Render(r.Action),
		stValue.Render(paths.ByteSize(r.Before)), stGoodBar.Render(paths.ByteSize(r.After))))
	b.WriteString(fmt.Sprintf("  %s %s in %s\n", stLabel.Render("reclaimed"),
		stGoodBar.Render(paths.ByteSize(r.Reclaimed)),
		stValue.Render(r.Elapsed.Round(time.Millisecond).String())))
	verdict := stOK.Render("verified")
	if !r.Verified {
		verdict = stWarn.Render("not verified")
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", stLabel.Render("integrity"), verdict))

	for _, s := range r.Steps {
		b.WriteString("  " + stOK.Render("·") + " " + stSub.Render(s) + "\n")
	}
	for _, n := range r.Notes {
		b.WriteString("  " + stWarn.Render("!") + " " + stSub.Render(n) + "\n")
	}
	if len(r.Removed) > 0 {
		b.WriteString("\n  " + stLabel.Render("removed") + "\n")
		keys := make([]string, 0, len(r.Removed))
		for k := range r.Removed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("  " + stValue.Render(padLeft(paths.ByteSize(r.Removed[k]), 10)) + "  " +
				stSub.Render(k) + "\n")
		}
	}
	if r.Verified && !r.DryRun {
		b.WriteString("\n  " + stSub.Render("press [enter] to rescan"))
	}
	return m.frame(strings.TrimRight(b.String(), "\n"))
}

// projectLabel is the friendliest name a project has: its name, else the last
// path element of its worktree.
func projectLabel(p storage.ProjectInfo) string {
	if p.Name != "" {
		return p.Name
	}
	base := filepath.Base(strings.TrimRight(p.Worktree, "/"))
	if base == "." || base == "/" {
		return p.Worktree
	}
	return base
}

// shortenPath keeps the tail of a path, which is the part that identifies it.
func shortenPath(p string) string {
	if p == "" {
		return "-"
	}
	if len(p) <= 44 {
		return p
	}
	parts := strings.Split(p, "/")
	var out string
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := parts[i]
		if out != "" {
			candidate += "/" + out
		}
		if len(candidate)+2 > 44 {
			break
		}
		out = candidate
	}
	if out == "" {
		return "…" + p[len(p)-40:]
	}
	return "…/" + out
}

func sortSessionsBySize(rows []storage.SessionInfo) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Bytes != rows[j].Bytes {
			return rows[i].Bytes > rows[j].Bytes
		}
		return rows[i].Title < rows[j].Title
	})
}

func sortSessionsByFolder(rows []storage.SessionInfo) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Directory != rows[j].Directory {
			return rows[i].Directory < rows[j].Directory
		}
		return rows[i].Bytes > rows[j].Bytes
	})
}

func sessionTotal(s *storage.Scan) int64 {
	var total int64
	for _, si := range s.Sessions {
		total += si.Bytes
	}
	return total
}

func biggestSession(s *storage.Scan) int64 {
	var biggest int64
	for _, si := range s.Sessions {
		if si.Bytes > biggest {
			biggest = si.Bytes
		}
	}
	return biggest
}
