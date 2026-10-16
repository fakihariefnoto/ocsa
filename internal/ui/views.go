package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/fakih/ocsa/internal/actions"
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

// frame wraps a body in the box and appends the footer. The header and footer
// are fitted to the terminal width too, so nothing wraps at the left margin.
func (m Model) frame(body string) string {
	width := m.width
	if width < 40 {
		width = 40
	}
	head := stTitle.Render("OCSA") + stSub.Render("  "+m.p.Data)
	if m.status != "" {
		head += "   " + stOK.Render(m.status)
	}
	inner := body
	if m.width > 6 {
		inner = lipglossWidth(body, min(m.width-4, 104))
	}
	return fit(head, width) + "\n" + stBox.Render(inner) + "\n" + fit(m.footer(), width)
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
	cw := m.contentWidth()
	var b strings.Builder

	// The database block: a composition bar, not a fill gauge. Four things are
	// in this file and only one of them is reclaimable, which the old two-tone
	// bar could not show.
	comp := s.Composition()
	barW := cw - lipgloss.Width(paths.ByteSize(comp.Total)) - len(" on disk") - 6
	if barW > 64 {
		barW = 64 // beyond this it stops reading as a gauge
	}
	if barW < 12 {
		barW = 12
	}
	// A blank line separates the heading from the bar, which would otherwise
	// read as part of the heading text. The total sits immediately after the
	// bar, not aligned to the far edge: pushed to the edge it looked like an
	// unrelated column two lines away from the thing it describes.
	totalText := paths.ByteSize(comp.Total) + " on disk"
	b.WriteString(stLabel.Render("DATABASE") + "\n\n")
	b.WriteString("  " + stackedBar([]stackSegment{
		{comp.Live, stLive, glyphBlock},
		{comp.Frozen, stFrozen, glyphBlock},
		{comp.Other, stOther, glyphBlock},
		{comp.Free, stReclaim, glyphBlock},
	}, barW) + "  " + stValue.Render(totalText) + "\n\n")

	// Legend: same glyph and colour as the bar, with the share so the segments
	// can be judged without measuring them by eye.
	const legendFixed = 2 + 2 + 1 + 12 + 10 + 2 + 6 + 2
	legend := func(st lipgloss.Style, glyph, label string, bytes int64, note string) {
		share := 0.0
		if comp.Total > 0 {
			share = float64(bytes) / float64(comp.Total) * 100
		}
		line := "  " + st.Render(glyph+glyph) + " " +
			stText.Render(padRight(label, 12)) +
			stValue.Render(padLeft(paths.ByteSize(bytes), 10)) + "  " +
			stSub.Render(padLeft(fmt.Sprintf("%4.1f%%", share), 6))
		if room := cw - legendFixed; room >= 8 {
			line += "  " + stSub.Render(fit(note, room))
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	frozenNote := "abandoned tables a vacuum cannot free"
	for _, g := range s.Generations {
		if g.Frozen && !g.LastWrite.IsZero() {
			frozenNote = "abandoned tables, last written " + g.LastWrite.Format("2006-01-02")
		}
	}
	legend(stLive, glyphBlock, "live", comp.Live, "the generation OpenCode writes to")
	legend(stFrozen, glyphBlock, "frozen", comp.Frozen, frozenNote)
	legend(stOther, glyphBlock, "other", comp.Other, "indexes, key/value state, schema")
	reclaimNote := "free pages a vacuum would return"
	if comp.Free == 0 {
		reclaimNote = "nothing to return; the file holds only rows"
	}
	legend(stReclaim, glyphBlock, "reclaimable", comp.Free, reclaimNote)

	// The comparable numbers: how much bigger the file is than it needs to be,
	// and how much of the disk that is.
	var scale []string
	if ratio := comp.Ratio(); ratio > 0 {
		scale = append(scale, fmt.Sprintf("%.1f× the live data", ratio))
	}
	if share := comp.DiskShare(); share > 0 {
		scale = append(scale, fmt.Sprintf("%.1f%% of the %s free on disk",
			share, paths.ByteSize(comp.DiskFree)))
	}
	if len(scale) > 0 {
		b.WriteString("  " + stSub.Render(fit(strings.Join(scale, "  ·  "), cw-2)) + "\n")
	}

	// The bookkeeping line sheds its least important parts on a narrow
	// terminal instead of wrapping, which would break the block's shape.
	head := "  " + fmt.Sprintf("%d pages × %d B", s.Stats.PageCount, s.Stats.PageSize) +
		"  ·  auto_vacuum " + modePlain(s.Stats.AutoVacuum)
	line := head + "  ·  integrity " + s.Integrity
	if lipgloss.Width(line)+15 <= cw {
		line += "  ·  WAL " + paths.ByteSize(s.WALBytes)
	}
	b.WriteString(stLabel.Render(fit(line, cw)) + "\n")

	// Reclaimable: one row per source, amounts right-aligned to one column so
	// they can be compared without reading the labels.
	r := s.Reclaim
	b.WriteString("\n" + stLabel.Render("RECLAIMABLE") + "  " +
		stGoodBar.Render(paths.ByteSize(r.Total)) + "\n")
	const amountW = 10
	labelW := cw - amountW - 4
	if labelW < 18 {
		labelW = 18
	}
	rows := []struct {
		label string
		value int64
		faint bool
	}{
		{"free pages", r.DBFree, false},
		{"snapshots, newest " + fmt.Sprint(storage.SnapshotKeepRecent()) + " kept", r.SnapshotPrunable, false},
		{"log, 1 MB kept", r.LogReclaimable, false},
		{"orphan rows", r.OrphanBytes, false},
	}
	for _, row := range rows {
		if row.value == 0 && row.label == "orphan rows" {
			continue
		}
		b.WriteString("  " + stSub.Render(padRight(fit(row.label, labelW), labelW)) +
			stValue.Render(padLeft(paths.ByteSize(row.value), amountW)) + "\n")
	}
	if r.LegacyBytes > 0 {
		b.WriteString("  " + stWarn.Render(padRight(
			fit("frozen generation, needs row deletion", labelW), labelW)) +
			stWarn.Render(padLeft(paths.ByteSize(r.LegacyBytes), amountW)) + "\n")
	}

	b.WriteString("\n" + stLabel.Render("SESSIONS") + "\n")
	b.WriteString("  " + stValue.Render(plural(len(s.Sessions), "session")+" · "+
		plural(len(s.Projects), "project")+" · "+paths.ByteSize(sessionTotal(s))+
		" of payload · biggest "+paths.ByteSize(biggestSession(s))) + "\n")

	// The action menu, navigable with the arrows. The hint column absorbs
	// whatever width is left, so the key column always lines up.
	b.WriteString("\n" + stLabel.Render("ACTIONS") + "\n")
	items := m.menu()
	const labelCol = 30
	keyW := 0
	for _, it := range items {
		if len(it.key)+2 > keyW {
			keyW = len(it.key) + 2
		}
	}
	hintW := cw - 2 - 2 - labelCol - 1 - keyW - 2
	for i, it := range items {
		marker := "  "
		label := stText.Render(padRight(fit(it.label, labelCol), labelCol))
		if i == m.cursor {
			marker = stCursor.Render("▸ ")
			label = stSelected.Render(padRight(" "+it.label+" ", labelCol))
		}
		line := "  " + marker + label + " "
		if hintW > 8 {
			line += stSub.Render(padRight(fit(it.hint, hintW), hintW))
		}
		if it.key != "" {
			line += "  " + stKey.Render(padLeft("["+it.key+"]", keyW))
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}

	warnings := 0
	for _, w := range s.Warnings {
		if strings.Contains(w, "free pages") {
			continue // already a row in RECLAIMABLE
		}
		if warnings == 0 {
			b.WriteString("\n")
		}
		warnings++
		for _, line := range wrapWords(w, cw, "  "+stWarn.Render("! "), "    ") {
			b.WriteString(line + "\n")
		}
	}
	return m.frame(strings.TrimRight(b.String(), "\n"))
}

// modePlain is the unstyled auto_vacuum mode, for the aligned key/value grid
// where a styled cell would break the column padding.
// modeStylePlain keeps the mode readable in the one-line summary.
func modeStylePlain(v int) string {
	switch v {
	case 2:
		return stOK.Render("incremental")
	case 1:
		return stOK.Render("full")
	default:
		return stWarn.Render("none")
	}
}

func modePlain(v int) string {
	switch v {
	case 1:
		return "full"
	case 2:
		return "incremental"
	default:
		return "none"
	}
}

func (m Model) viewProjects() string {
	rows := m.projectRows()
	cw := m.contentWidth()
	var b strings.Builder

	// Column widths are derived from the frame, not hardcoded: a fixed table
	// wraps its own header on a narrow terminal, which reads as corruption.
	const sizeW, sessW = 10, 5
	projW := 22
	if cw < 84 {
		projW = 16
	}
	lastW := len("LAST USED")
	folderW := cw - (2 + sizeW + 2 + sessW + 2 + projW + 2 + 2 + lastW)
	if folderW < 16 {
		folderW = 16
	}

	var totalBytes int64
	var totalSessions int
	for _, p := range rows {
		totalBytes += p.Bytes
		totalSessions += p.Sessions
	}
	b.WriteString(fmt.Sprintf("%s  %s\n\n", stLabel.Render("PROJECTS"),
		stSub.Render(plural(len(rows), "project")+" · "+plural(totalSessions, "session")+
			" · "+paths.ByteSize(totalBytes)+" of session payload")))
	b.WriteString("  " + stLabel.Render(padLeft("SIZE", sizeW)+"  "+padLeft("SESS", sessW)+"  "+
		padRight("PROJECT", projW)+"  "+padRight("FOLDER", folderW)+"  "+"LAST USED") + "\n")

	visible := m.visibleRows()
	end := min(m.offset+visible, len(rows))
	for i := m.offset; i < end; i++ {
		p := rows[i]
		gone := ""
		folderStyle := stSub
		folderRoom := folderW
		if p.DirGone {
			gone = " " + stWarn.Render("gone")
			folderStyle = stWarn
			folderRoom = folderW - len(" gone")
		}
		row := "  " + stValue.Render(padLeft(paths.ByteSize(p.Bytes), sizeW)) + "  " +
			stValue.Render(padLeft(fmt.Sprint(p.Sessions), sessW)) + "  " +
			stRow.Render(padRight(fit(projectLabel(p), projW), projW)) + "  " +
			folderStyle.Render(padRight(fit(shortenPath(p.Worktree), folderRoom), folderRoom)) + "  " +
			stSub.Render(padLeft(ago(p.LastUsed), lastW)) + gone
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
	cw := m.contentWidth()
	var b strings.Builder

	// Same principle as projects: the flexible columns take what is left, so
	// the header and the rows agree on where every column starts.
	const sizeW, updW = 10, 9
	folderW := 30
	if cw < 96 {
		folderW = 22
	}
	if cw < 76 {
		folderW = 16
	}
	if folderW > cw/3 {
		folderW = cw / 3
	}
	titleW := cw - (2 + sizeW + 2 + 2 + folderW + 2 + updW)
	if titleW < 16 {
		titleW = 16
		if shrink := cw - (2 + sizeW + 2 + 2 + titleW + 2 + updW); shrink > 10 {
			folderW = shrink
		}
	}

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

	b.WriteString("  " + stLabel.Render(padLeft("SIZE", sizeW)+"  "+
		padRight("TITLE", titleW)+"  "+padRight("FOLDER", folderW)+"  "+"UPDATED") + "\n")

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
			head := "  " + dirStyle.Render(fit(shortenPath(si.Directory), cw-22)) +
				stSub.Render("   "+paths.ByteSize(subtotal[si.Directory])+" · "+
					plural(count[si.Directory], "session"))
			b.WriteString("\n" + fit(head, cw) + "\n")
		}
		row := "  " + stValue.Render(padLeft(paths.ByteSize(si.Bytes), sizeW)) + "  " +
			stRow.Render(padRight(fit(si.Title, titleW), titleW)) + "  " +
			folderCell(si, folderW) + "  " +
			stSub.Render(padLeft(ago(si.Updated), updW))
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

// folderCell renders the folder column at a caller-chosen width, so the column
// budget and the render agree.
func folderCell(si storage.SessionInfo, width int) string {
	room := width
	if si.DirGone {
		room -= len(" gone")
	}
	label := padRight(fit(shortenPath(si.Directory), room), room)
	if si.DirGone {
		return stWarn.Render(label)
	}
	return stSub.Render(label)
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	cw := m.contentWidth()

	if m.isClaim {
		b.WriteString(stWarn.Render("CLAIM STORAGE") + "  " +
			stSub.Render("nothing happens until you press y") + "\n\n")
		b.WriteString("  " + stLabel.Render(padRight("STEP", 44)+"  "+padLeft("FREES", 10)) + "\n")
		for _, st := range m.claimPlan.Steps {
			mark := stOK.Render("+")
			label := padRight(fit(st.Label, 44), 44)
			size := padLeft(paths.ByteSize(st.Projected), 10)
			detail := st.Detail
			if st.Status == actions.StepSkipped {
				mark = stSub.Render("·")
				label = stSub.Render(label)
				size = stSub.Render(padLeft("-", 10))
				detail = st.Reason
			}
			b.WriteString("  " + mark + " " + label + "  " + stValue.Render(size) + "\n")
			for _, line := range wrapWords(detail, cw-4, "      ", "      ") {
				b.WriteString(stSub.Render(line) + "\n")
			}
		}
		b.WriteString("\n  " + stLabel.Render(padRight("PROJECTED", 44)+"  ") +
			stGoodBar.Render(padLeft(paths.ByteSize(m.claimPlan.Projected), 10)) + "\n")
		if m.claimPlan.Frozen > 0 {
			// Same columns as PROJECTED, so the numbers line up.
			b.WriteString("  " + stSub.Render(padRight(
				fit("frozen tables (needs row deletion)", 44), 44)) +
				"  " + stWarn.Render(padLeft(paths.ByteSize(m.claimPlan.Frozen), 10)) + "\n")
		}
		b.WriteString("\n  " + stKey.Render("[s]") + " " +
			stSub.Render(toggleLabel(m.claimOpts.IncludeSnapshots, m.claimPlan.Snapshots)) + "\n")
		b.WriteString("  " + stSub.Render("if OpenCode is mid-message, this waits for a gap") + "\n")
		b.WriteString("\n  " + stKey.Render("[y]") + " " + stSub.Render("claim it") + "   " +
			stKey.Render("[n]") + " " + stSub.Render("cancel"))
		return m.frame(strings.TrimRight(b.String(), "\n"))
	}

	b.WriteString(stWarn.Render("CONFIRM") + "\n\n")
	b.WriteString("  " + stText.Render(m.confirmLabel) + "\n")
	if m.confirmNote != "" {
		b.WriteString("  " + stSub.Render(m.confirmNote) + "\n")
	}
	b.WriteString("\n  " + stLabel.Render("y to proceed, n to cancel"))
	return m.frame(b.String())
}

// toggleLabel states what pressing [s] will do, and what it costs.
func toggleLabel(on bool, snapshotBytes int64) string {
	if on {
		return "include snapshots (" + paths.ByteSize(snapshotBytes) +
			") - breaks revert for old sessions"
	}
	return "include snapshots? currently skipped: they hold revert data"
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
	if m.claimResult != nil && len(m.claimResult.Outcomes) > 0 {
		b.WriteString("\n  " + stLabel.Render("STEPS") + "\n")
		for _, out := range m.claimResult.Outcomes {
			mark := stOK.Render("+")
			label := padRight(fit(out.Step.Label, 44), 44)
			if out.Err != "" {
				mark = stBad.Render("!")
				label = stBad.Render(label)
			}
			note := out.Note
			if out.Err != "" {
				note = out.Err
			}
			b.WriteString("  " + mark + " " + label +
				stValue.Render(padLeft(paths.ByteSize(out.Bytes), 10)) + "  " +
				stSub.Render(truncateLine(note, 40)) + "\n")
		}
	}
	if m.claimResult != nil {
		a := m.claimResult.Activity
		line := "would have been safe to run"
		if !a.SafeIfRun {
			line = "ran despite a writer"
		}
		b.WriteString("\n  " + stLabel.Render("OPENCODE AT THE TIME") + "  " +
			stSub.Render(a.Summary+" ("+line+")") + "\n")
	}
	if r.Verified && !r.DryRun {
		b.WriteString("\n  " + stSub.Render("press [enter] to rescan"))
	}
	return m.frame(strings.TrimRight(b.String(), "\n"))
}

// truncateLine keeps a note inside one line.
func truncateLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
