// Package ui is the interactive front end: a dashboard over a scan, a navigable
// action menu, and browsers for projects and sessions, with the mutating
// actions behind a confirmation.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
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
	scrProjects
	scrSessions
	scrConfirm
	scrRunning
	scrDone
)

// menuItem is one entry in the dashboard's action list.
type menuItem struct {
	id    string
	label string
	hint  string
	key   string
}

// Model is the bubbletea state.
type Model struct {
	p    paths.Paths
	scan *storage.Scan
	err  error

	screen screen
	prev   screen
	cursor int
	offset int
	width  int
	height int

	spinner spinner.Model

	confirmLabel string
	confirmNote  string
	run          func() (actions.Result, error)
	result       actions.Result
	runErr       error
	started      time.Time
	status       string

	// claim flow: the plan shown on the confirmation is the plan that runs
	isClaim     bool
	claimPlan   actions.Plan
	claimOpts   actions.ClaimOptions
	claimResult *actions.ClaimResult

	// browsing state
	filterProject string
	grouped       bool
}

type scanMsg struct {
	scan *storage.Scan
	err  error
}

type actionMsg struct {
	res   actions.Result
	claim *actions.ClaimResult
	err   error
}

type tickMsg time.Time

// New builds the initial model and kicks off a scan.
func New(p paths.Paths) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)
	return Model{p: p, spinner: sp, started: time.Now()}
}

// ClaimOptionsForPreview lets the previewer show the destructive step too.
func ClaimOptionsForPreview(o PreviewOptions) actions.ClaimOptions {
	return actions.ClaimOptions{IncludeSnapshots: o.IncludeSnapshots}
}

// PreviewOptions selects what to render headlessly.
type PreviewOptions struct {
	Screen           string // dash, projects, sessions, grouped, confirm, claim
	ProjectID        string // filter the session list to one project
	IncludeSnapshots bool   // for the claim screen, show the opt-in step too
	Width            int
	Height           int
}

// Preview renders one screen for a scan without a terminal. Used by the
// `preview` command, by tests, and for documentation screenshots.
func Preview(s *storage.Scan, o PreviewOptions) string {
	m := New(s.Paths)
	m.scan = s
	m.width, m.height = o.Width, o.Height
	if m.width == 0 {
		m.width = 112
	}
	m.filterProject = o.ProjectID
	switch o.Screen {
	case "projects":
		m.screen = scrProjects
		return m.viewProjects()
	case "sessions":
		m.screen = scrSessions
		return m.viewSessions()
	case "grouped":
		m.screen = scrSessions
		m.grouped = true
		return m.viewSessions()
	case "claim":
		opts := ClaimOptionsForPreview(o)
		m.claimOpts = opts
		m.claimPlan = actions.BuildPlan(s, opts).Normalize()
		m.isClaim = true
		m.screen = scrConfirm
		return m.viewConfirm()
	case "confirm":
		m.prompt("vacuum the database in place", "a note about the change", nil)
		return m.viewConfirm()
	default:
		return m.viewDash()
	}
}

// menu is the dashboard's action list, in the order a reader would want them.
func (m Model) menu() []menuItem {
	return []menuItem{
		{"claim", "Claim storage", "the safe sweep: everything except snapshots", "C"},
		{"v", "Vacuum database", "rewrite in place, returns free pages", "v"},
		{"i", "Incremental vacuum", "cheap, needs auto_vacuum=incremental", "i"},
		{"a", "auto_vacuum = incremental", "makes cheap reclaiming possible", "a"},
		{"f", "auto_vacuum = full", "zero maintenance, small cost on deletes", "f"},
		{"c", "Clean log and tool output", "truncate the log, drop old spills", "c"},
		{"k", "Prune snapshots", "breaks revert for old sessions", "k"},
		{"p", "Browse projects", "storage by project and folder", "p"},
		{"s", "Browse sessions", "every session by size", "s"},
		{"r", "Write report", "reports/YYYY-MM-DD.md", "r"},
		{"q", "Quit", "", "q"},
	}
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
		return actionMsg{res: res, err: err}
	}
}

func runClaimCmd(p paths.Paths, plan actions.Plan, o actions.ClaimOptions) tea.Cmd {
	return func() tea.Msg {
		res, err := actions.Claim(p, plan, o)
		return actionMsg{res: res.Result, claim: &res, err: err}
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
		m.claimResult = msg.claim
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
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.screen != scrConfirm && m.screen != scrRunning {
			return m, tea.Quit
		}
	case "esc":
		switch m.screen {
		case scrProjects, scrSessions, scrDone:
			m.screen = scrDash
		case scrConfirm:
			m.screen = m.prev
		}
		return m, nil
	}

	switch m.screen {
	case scrDash:
		return m.dashKey(k)
	case scrProjects:
		return m.projectsKey(k)
	case scrSessions:
		return m.sessionsKey(k)
	case scrConfirm:
		switch k.String() {
		case "y", "enter":
			m.screen = scrRunning
			m.started = time.Now()
			if m.isClaim {
				return m, tea.Batch(runClaimCmd(m.p, m.claimPlan, m.claimOpts), tick())
			}
			return m, tea.Batch(runCmd(m.run), tick())
		case "n":
			m.isClaim = false
			m.screen = m.prev
		case "s":
			if m.isClaim {
				// Snapshots are the one destructive step, so they are opt-in
				// here and the plan is rebuilt so the numbers stay honest.
				m.claimOpts.IncludeSnapshots = !m.claimOpts.IncludeSnapshots
				m.claimPlan = actions.BuildPlan(m.scan, m.claimOpts).Normalize()
			}
		}
	case scrDone:
		if k.String() == "enter" {
			m.screen = scrDash
			return m, scanCmd(m.p)
		}
	}
	return m, nil
}

func (m Model) dashKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.menu()
	switch k.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(items)-1 {
			m.cursor++
		}
		return m, nil
	case "g":
		m.cursor = 0
		return m, nil
	case "G":
		m.cursor = len(items) - 1
		return m, nil
	case "enter", " ":
		return m.activate(items[max(0, min(m.cursor, len(items)-1))].id)
	}
	// Single-key shortcuts still work for anyone who knows them.
	for _, it := range items {
		if k.String() == it.key {
			return m.activate(it.id)
		}
	}
	return m, nil
}

// activate runs a menu entry, opening a confirmation for anything that writes.
func (m Model) activate(id string) (tea.Model, tea.Cmd) {
	switch id {
	case "claim":
		m.claimOpts = actions.ClaimOptions{}
		m.claimPlan = actions.BuildPlan(m.scan, m.claimOpts).Normalize()
		m.isClaim = true
		m.prev = scrDash
		m.screen = scrConfirm
		return m, nil
	case "v":
		m.prompt("vacuum the database in place",
			"rewrites "+paths.ByteSize(m.scan.Stats.FileBytes())+
				" down to about "+paths.ByteSize(m.scan.Stats.LiveBytes())+
				". Safe while OpenCode runs.",
			func() (actions.Result, error) {
				return actions.Vacuum(m.p, actions.VacuumOptions{Yes: true})
			})
	case "i":
		m.prompt("return free pages incrementally",
			"no full rewrite, milliseconds. No-op unless auto_vacuum=incremental.",
			func() (actions.Result, error) { return actions.Incremental(m.p) })
	case "a":
		m.prompt("set auto_vacuum = incremental (and VACUUM)",
			"after this the file reclaims itself on demand when you run [i].",
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
	case "p":
		m.screen = scrProjects
		m.cursor, m.offset = 0, 0
	case "s":
		m.screen = scrSessions
		m.filterProject = ""
		m.cursor, m.offset = 0, 0
	case "r":
		path, err := writeReport(m.scan)
		if err != nil {
			m.status = "report failed: " + err.Error()
		} else {
			m.status = "wrote " + path
		}
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) prompt(label, note string, fn func() (actions.Result, error)) {
	m.isClaim = false
	m.prev = m.screen
	m.screen = scrConfirm
	m.confirmLabel = label
	m.confirmNote = note
	m.run = fn
}

func (m Model) projectsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.projectRows()
	switch k.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = len(rows) - 1
	case "enter", " ":
		if len(rows) > 0 {
			m.filterProject = rows[max(0, min(m.cursor, len(rows)-1))].ID
			m.screen = scrSessions
			m.cursor, m.offset = 0, 0
		}
	}
	m.clamp(len(rows))
	return m, nil
}

func (m Model) sessionsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.sessionRows()
	switch k.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "g":
		// 'g' alone cannot both mean "top" and "group"; grouping is on the
		// uppercase key here to avoid stealing navigation.
		m.grouped = !m.grouped
		m.cursor, m.offset = 0, 0
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = len(rows) - 1
	case "enter":
		m.filterProject = rows[max(0, min(m.cursor, len(rows)-1))].ProjectID
		m.cursor, m.offset = 0, 0
	}
	m.clamp(len(rows))
	return m, nil
}

// projectRows is the sorted project list the browser shows.
func (m Model) projectRows() []storage.ProjectInfo {
	if m.scan == nil {
		return nil
	}
	return m.scan.Projects
}

// sessionRows is the session list the browser shows: everything, or one
// project, optionally ordered by folder instead of by size.
func (m Model) sessionRows() []storage.SessionInfo {
	if m.scan == nil {
		return nil
	}
	var rows []storage.SessionInfo
	for _, si := range m.scan.Sessions {
		if m.filterProject != "" && si.ProjectID != m.filterProject {
			continue
		}
		rows = append(rows, si)
	}
	if m.grouped {
		sortSessionsByFolder(rows)
	} else {
		sortSessionsBySize(rows)
	}
	return rows
}

// clamp keeps the cursor inside the visible window. It takes the row count
// rather than reading state, so callers can pass a list they already built.
func (m *Model) clamp(rows int) {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if rows > 0 && m.cursor > rows-1 {
		m.cursor = rows - 1
	}
	visible := m.visibleRows()
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

func (m Model) visibleRows() int {
	n := m.height - 10
	if n < 3 {
		n = 3
	}
	return n
}

// writeReport drops a markdown report in ./reports for tracking over time.
func writeReport(s *storage.Scan) (string, error) {
	if err := os.MkdirAll("reports", 0o755); err != nil {
		return "", err
	}
	name := filepath.Join("reports", s.Scanned.Format("2006-01-02")+".md")
	return name, os.WriteFile(name, []byte(report.Markdown(s)), 0o644)
}

// runClaimCmdForTest reports the queued claim command, which must be nil until
// the user confirms. Used by the cancel test.
func (m Model) runClaimCmdForTest() tea.Cmd {
	if m.isClaim && m.screen == scrRunning {
		return runClaimCmd(m.p, m.claimPlan, m.claimOpts)
	}
	return nil
}
