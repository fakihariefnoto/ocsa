// Package ui is the interactive front end: a dashboard over a scan, with the
// mutating actions behind one keypress and a confirmation.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fakih/ocsa/internal/actions"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/report"
	"github.com/fakih/ocsa/internal/storage"
)

type screen int

const (
	scrDash screen = iota
	scrSessions
	scrConfirm
	scrRunning
	scrDone
)

// Styles are the whole visual language, defined once.
var (
	colAccent = lipgloss.Color("39")
	colOK     = lipgloss.Color("42")
	colWarn   = lipgloss.Color("214")
	colBad    = lipgloss.Color("203")
	colDim    = lipgloss.Color("241")
	colText   = lipgloss.Color("252")

	stTitle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stSub   = lipgloss.NewStyle().Foreground(colDim)
	stLabel = lipgloss.NewStyle().Foreground(colDim)
	stText  = lipgloss.NewStyle().Foreground(colText)
	stValue = lipgloss.NewStyle().Foreground(colText)
	stOK    = lipgloss.NewStyle().Foreground(colOK)
	stWarn  = lipgloss.NewStyle().Foreground(colWarn)
	stBad   = lipgloss.NewStyle().Foreground(colBad)
	stKey   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(colDim).Padding(0, 1)
	stSel     = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stFill    = lipgloss.NewStyle().Foreground(colAccent)
	stEmpty   = lipgloss.NewStyle().Foreground(colDim)
	stGoodBar = lipgloss.NewStyle().Foreground(colOK)
)

// Model is the bubbletea state.
type Model struct {
	p    paths.Paths
	scan *storage.Scan
	err  error

	screen  screen
	cursor  int
	offset  int
	width   int
	height  int
	spinner spinner.Model

	confirmLabel string
	confirmNote  string
	run          func() (actions.Result, error)
	result       actions.Result
	runErr       error
	started      time.Time
	status       string
}

type scanMsg struct {
	scan *storage.Scan
	err  error
}

type actionMsg struct {
	res actions.Result
	err error
}

type tickMsg time.Time

// Preview renders the dashboard for a scan without a terminal, for tests and
// for eyeballing the layout over a pipe.
func Preview(s *storage.Scan) string {
	m := New(s.Paths)
	m.scan = s
	m.width = 110
	return m.viewDash()
}

// New builds the initial model and kicks off a scan.
func New(p paths.Paths) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)
	return Model{p: p, spinner: sp, started: time.Now()}
}

func scanCmd(p paths.Paths) tea.Cmd {
	return func() tea.Msg {
		s, err := storage.Run(p)
		return scanMsg{s, err}
	}
}

func runCmd(fn func() (actions.Result, error)) tea.Cmd {
	return func() tea.Msg {
		res, err := fn()
		return actionMsg{res, err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(scanCmd(m.p), m.spinner.Tick)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case scanMsg:
		m.scan, m.err = msg.scan, msg.err
		return m, nil

	case actionMsg:
		m.result, m.runErr = msg.res, msg.err
		m.screen = scrDone
		return m, nil

	case tickMsg:
		if m.screen == scrRunning {
			return m, tick()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c", "q":
		if m.screen != scrConfirm && m.screen != scrRunning {
			return m, tea.Quit
		}
	case "esc":
		if m.screen == scrSessions || m.screen == scrDone {
			m.screen = scrDash
			return m, nil
		}
		if m.screen == scrConfirm {
			m.screen = scrDash
			return m, nil
		}
	}

	switch m.screen {
	case scrDash:
		return m.dashKey(k)
	case scrSessions:
		return m.sessionsKey(k)
	case scrConfirm:
		switch k.String() {
		case "y", "enter":
			m.screen = scrRunning
			m.started = time.Now()
			return m, tea.Batch(runCmd(m.run), tick())
		case "n":
			m.screen = scrDash
		}
	case scrDone:
		if k.String() == "enter" || k.String() == "r" {
			m.screen = scrDash
			return m, scanCmd(m.p)
		}
	}
	return m, nil
}

func (m Model) dashKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "s":
		m.screen = scrSessions
		m.cursor, m.offset = 0, 0
	case "v":
		m.prompt("vacuum the database in place",
			"rewrites "+paths.ByteSize(m.scan.Stats.FileBytes())+
				" down to about "+paths.ByteSize(m.scan.Stats.LiveBytes())+
				". Safe while OpenCode runs.",
			func() (actions.Result, error) {
				return actions.Vacuum(m.p, actions.VacuumOptions{Yes: true})
			})
	case "i":
		m.prompt("return free pages incrementally", "no full rewrite, milliseconds.", func() (actions.Result, error) {
			return actions.Incremental(m.p)
		})
	case "a":
		m.prompt("set auto_vacuum = incremental (and VACUUM)",
			"after this the file reclaims itself on demand. Suggest running [i] now and then.",
			func() (actions.Result, error) {
				return actions.AutoVacuum(m.p, "incremental", actions.VacuumOptions{Yes: true})
			})
	case "f":
		m.prompt("set auto_vacuum = full (and VACUUM)",
			"zero maintenance: SQLite returns pages on commit. Slightly more work on deletes.",
			func() (actions.Result, error) {
				return actions.AutoVacuum(m.p, "full", actions.VacuumOptions{Yes: true})
			})
	case "c":
		m.prompt("truncate the log and prune old tool output",
			paths.ByteSize(m.scan.Reclaim.LogTotal)+" log, "+
				paths.ByteSize(m.scan.Reclaim.ToolOutput)+" tool output.",
			func() (actions.Result, error) {
				return actions.Clean(m.p, actions.CleanOptions{
					Yes: true, Log: true, ToolOutput: true, ToolOutAge: 7 * 24 * time.Hour,
				})
			})
	case "k":
		m.prompt("prune snapshots", "deletes all but the newest "+
			fmt.Sprint(storage.SnapshotKeepRecent())+
			" snapshot directories. This breaks revert for old sessions.",
			func() (actions.Result, error) {
				return actions.Clean(m.p, actions.CleanOptions{
					Yes: true, Snapshots: true, SnapshotKeep: storage.SnapshotKeepRecent(),
				})
			})
	case "r":
		path, err := writeReport(m.scan)
		if err != nil {
			m.status = "report failed: " + err.Error()
		} else {
			m.status = "wrote " + path
		}
	}
	return m, nil
}

// writeReport drops a markdown report in ./reports for tracking over time.
func writeReport(s *storage.Scan) (string, error) {
	if err := os.MkdirAll("reports", 0o755); err != nil {
		return "", err
	}
	name := filepath.Join("reports", s.Scanned.Format("2006-01-02")+".md")
	return name, os.WriteFile(name, []byte(report.Markdown(s)), 0o644)
}

func (m *Model) prompt(label, note string, fn func() (actions.Result, error)) {
	m.screen = scrConfirm
	m.confirmLabel = label
	m.confirmNote = note
	m.run = fn
}

func (m Model) sessionsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.scan.Sessions)
	switch k.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = n - 1
	}
	m.clamp()
	return m, nil
}

func (m *Model) clamp() {
	visible := m.height - 8
	if visible < 3 {
		visible = 3
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// View implements tea.Model.
func (m Model) View() string {
	if m.err != nil {
		return stBad.Render("scan failed: "+m.err.Error()) + "\n\n" + stSub.Render("q to quit") + "\n"
	}
	if m.scan == nil {
		return "\n  " + m.spinner.View() + " scanning " + m.p.Data + "…\n"
	}
	switch m.screen {
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

func (m Model) frame(title string, body string) string {
	head := stTitle.Render("OCSA") + stSub.Render("  opencode storage  ") + stSub.Render(m.p.Data)
	if title != "" {
		head = stTitle.Render(title) + stSub.Render("  "+m.p.Data)
	}
	inner := body
	if m.width > 4 {
		inner = lipgloss.NewStyle().Width(min(m.width-4, 100)).Render(body)
	}
	return head + "\n" + stBox.Render(inner) + "\n" + m.footer()
}

func (m Model) footer() string {
	switch m.screen {
	case scrDash:
		keys := [][2]string{{"v", "vacuum"}, {"i", "incremental"}, {"a", "auto=incr"},
			{"f", "auto=full"}, {"c", "clean"}, {"k", "prune snapshots"},
			{"s", "sessions"}, {"q", "quit"}}
		var parts []string
		for _, kv := range keys {
			parts = append(parts, stKey.Render("["+kv[0]+"]")+" "+stSub.Render(kv[1]))
		}
		return " " + strings.Join(parts, "  ")
	case scrSessions:
		return " " + stKey.Render("[↑↓]") + " " + stSub.Render("move") + "   " +
			stKey.Render("[esc]") + " " + stSub.Render("back")
	case scrConfirm:
		return " " + stKey.Render("[y]") + " " + stSub.Render("run") + "   " +
			stKey.Render("[n]") + " " + stSub.Render("cancel")
	default:
		return " " + stKey.Render("[esc]") + " " + stSub.Render("back") + "   " +
			stKey.Render("[q]") + " " + stSub.Render("quit")
	}
}

func (m Model) viewDash() string {
	s := m.scan
	var b strings.Builder

	// Database composition bar: live vs free pages.
	b.WriteString(stLabel.Render("DATABASE") + "\n")
	bar := m.bar(s.Stats.LiveBytes(), s.Stats.FileBytes(), 58)
	b.WriteString("  " + bar + "\n")
	b.WriteString(fmt.Sprintf("  %s %s   %s %s   %s %s   %s %s\n",
		stLabel.Render("file"), stValue.Render(paths.ByteSize(s.Stats.FileBytes())),
		stLabel.Render("live"), stValue.Render(paths.ByteSize(s.Stats.LiveBytes())),
		stLabel.Render("free"), m.health(s.Stats.FreeBytes() > 0),
		stLabel.Render("WAL"), stValue.Render(paths.ByteSize(s.WALBytes))))
	b.WriteString(fmt.Sprintf("  %s %s   %s %s   %s %s\n",
		stLabel.Render("pages"), stValue.Render(fmt.Sprint(s.Stats.PageCount)),
		stLabel.Render("auto_vacuum"), modeStyle(s.Stats.AutoVacuum),
		stLabel.Render("integrity"), integrityStyle(s.Integrity)))

	// Reclaimable.
	b.WriteString("\n" + stLabel.Render("RECLAIMABLE") + "\n")
	r := s.Reclaim
	for _, row := range []struct {
		name string
		val  int64
	}{
		{"database free pages", r.DBFree},
		{"snapshots beyond newest " + fmt.Sprint(storage.SnapshotKeepRecent()), r.SnapshotPrunable},
		{"log beyond 1 MB", r.LogReclaimable},
		{"orphan rows", r.OrphanBytes},
	} {
		b.WriteString("  " + stValue.Render(padLeft(paths.ByteSize(row.val), 10)) +
			"  " + stSub.Render(row.name) + "\n")
	}
	b.WriteString("  " + stGoodBar.Render(padLeft(paths.ByteSize(r.Total), 10)) +
		"  " + stText.Render("TOTAL reclaimable") + "\n")
	if r.LegacyBytes > 0 {
		b.WriteString("  " + stWarn.Render(padLeft(paths.ByteSize(r.LegacyBytes), 10)) +
			"  " + stSub.Render("frozen generation, row deletion only") + "\n")
	}

	// Generations: which representation OpenCode still writes.
	if len(s.Generations) > 0 {
		b.WriteString("\n" + stLabel.Render("GENERATIONS") + "\n")
		for _, g := range s.Generations {
			state := stOK.Render("live")
			if g.Frozen {
				state = stWarn.Render("frozen " + g.LastWrite.Format("2006-01-02"))
			}
			b.WriteString("  " + stValue.Render(padLeft(paths.ByteSize(g.Bytes), 10)) + "  " +
				stText.Render(padRight(g.Name, 14)) + state + "\n")
		}
	}

	// Artifacts.
	b.WriteString("\n" + stLabel.Render("ARTIFACTS") + "\n")
	for _, a := range s.Artifacts {
		if !a.Exists && a.Bytes == 0 {
			continue
		}
		b.WriteString("  " + stValue.Render(padLeft(paths.ByteSize(a.Bytes), 10)) +
			"  " + stText.Render(padRight(a.Name, 18)) + stSub.Render(a.Detail) + "\n")
	}

	// Sessions summary.
	fmt.Fprintf(&b, "\n%s  %s\n", stLabel.Render("SESSIONS"),
		stSub.Render(fmt.Sprintf("%d sessions, %d projects, biggest %s",
			len(s.Sessions), len(s.Projects), paths.ByteSize(m.biggest()))))

	// Warnings.
	if len(s.Warnings) > 0 {
		b.WriteString("\n" + stLabel.Render("WARNINGS") + "\n")
		for _, w := range s.Warnings {
			b.WriteString("  " + stWarn.Render("! ") + stSub.Render(w) + "\n")
		}
	}
	return m.frame("", strings.TrimRight(b.String(), "\n"))
}

func (m Model) biggest() int64 {
	var max int64
	for _, s := range m.scan.Sessions {
		if s.Bytes > max {
			max = s.Bytes
		}
	}
	return max
}

func (m Model) viewSessions() string {
	s := m.scan
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s  %s\n\n",
		stLabel.Render("SESSIONS"), stSub.Render(fmt.Sprintf("%d total, estimated payload bytes", len(s.Sessions)))))
	b.WriteString("  " + stLabel.Render(padLeft("SIZE", 10)+"  "+padRight("TITLE", 40)+"  "+"DIRECTORY") + "\n")

	visible := m.height - 9
	if visible < 3 {
		visible = 3
	}
	end := m.offset + visible
	if end > len(s.Sessions) {
		end = len(s.Sessions)
	}
	for i := m.offset; i < end; i++ {
		si := s.Sessions[i]
		title := padRight(trunc(si.Title, 40), 40)
		dirStyle := stSub
		if si.DirGone {
			dirStyle = stWarn
		}
		row := "  " + stValue.Render(padLeft(paths.ByteSize(si.Bytes), 10)) + "  " +
			stText.Render(title) + "  " + dirStyle.Render(trunc(si.Directory, 34))
		if i == m.cursor {
			row = stSel.Render(" ▸" + strings.TrimPrefix(row, "  "))
		}
		b.WriteString(row + "\n")
	}
	if end < len(s.Sessions) {
		b.WriteString("\n  " + stSub.Render(fmt.Sprintf("… %d more", len(s.Sessions)-end)))
	}
	return m.frame("OCSA", strings.TrimRight(b.String(), "\n"))
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(stWarn.Render("CONFIRM") + "\n\n")
	b.WriteString("  " + stText.Render(m.confirmLabel) + "\n")
	if m.confirmNote != "" {
		b.WriteString("  " + stSub.Render(m.confirmNote) + "\n")
	}
	b.WriteString("\n  " + stLabel.Render("y to proceed, n to cancel"))
	return m.frame("OCSA", b.String())
}

func (m Model) viewRunning() string {
	elapsed := time.Since(m.started).Round(100 * time.Millisecond)
	var b strings.Builder
	b.WriteString("\n  " + m.spinner.View() + " " + stText.Render(m.confirmLabel) + "\n")
	b.WriteString("  " + stSub.Render("elapsed "+elapsed.String()) + "\n\n")
	b.WriteString("  " + stLabel.Render("the database is locked for this moment only; SQLite rolls back"))
	b.WriteString("\n  " + stLabel.Render("everything if it cannot finish"))
	return m.frame("OCSA", b.String())
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
	b.WriteString(fmt.Sprintf("  %s %s → %s\n",
		stLabel.Render(r.Action),
		stValue.Render(paths.ByteSize(r.Before)),
		stGoodBar.Render(paths.ByteSize(r.After))))
	b.WriteString(fmt.Sprintf("  %s %s in %s\n",
		stLabel.Render("reclaimed"), stGoodBar.Render(paths.ByteSize(r.Reclaimed)),
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
		for k, v := range r.Removed {
			b.WriteString("  " + stValue.Render(padLeft(fmt.Sprint(v), 10)) + "  " + stSub.Render(k) + "\n")
		}
	}
	if r.Verified && r.Reclaimed > 0 && !r.DryRun {
		b.WriteString("\n  " + stSub.Render("re-run a scan with [enter]"))
	}
	return m.frame("OCSA", strings.TrimRight(b.String(), "\n"))
}

// bar draws live bytes inside total bytes.
func (m Model) bar(live, total int64, width int) string {
	if total <= 0 {
		return stEmpty.Render(strings.Repeat("░", width))
	}
	fill := int(float64(live) / float64(total) * float64(width))
	if fill < 1 && live > 0 {
		fill = 1
	}
	if fill > width {
		fill = width
	}
	return stFill.Render(strings.Repeat("█", fill)) +
		stEmpty.Render(strings.Repeat("░", width-fill))
}

func modeStyle(v int) string {
	switch v {
	case 1:
		return stOK.Render("full")
	case 2:
		return stOK.Render("incremental")
	default:
		return stWarn.Render("none")
	}
}

func integrityStyle(v string) string {
	if v == "ok" {
		return stOK.Render(v)
	}
	return stBad.Render(v)
}

func (m Model) health(bad bool) string {
	if bad {
		return stWarn.Render(paths.ByteSize(m.scan.Stats.FreeBytes()) + " (reclaimable)")
	}
	return stOK.Render("0 B")
}

func padLeft(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-len(s)) + s
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
