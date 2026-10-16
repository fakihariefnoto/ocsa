package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

// fixtureScan builds a store with two projects, one of them spanning two
// folders, so the browsers have something to group.
func fixtureScan(t *testing.T) *storage.Scan {
	t.Helper()
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	dirA := p.Data + "/projects/alpha"
	dirB := p.Data + "/projects/beta"
	dirGone := p.Data + "/projects/gone"
	blob := strings.Repeat("x", 400)

	stmts := []string{
		`CREATE TABLE project(id text primary key, worktree text, name text,
			time_created integer, time_updated integer, time_archived integer)`,
		`CREATE TABLE session_v2(id text primary key, project_id text, directory text, title text,
			model text, cost real, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message(id text primary key, session_id text, type text, seq integer,
			time_created integer, time_updated integer, data text)`,
		`INSERT INTO project VALUES ('p1','` + dirA + `','alpha',0,0,0)`,
		`INSERT INTO project VALUES ('p2','` + dirB + `','beta',0,0,0)`,
		`INSERT INTO session_v2 VALUES ('s1','p1','` + dirA + `','big alpha session','m',0,0,0)`,
		`INSERT INTO session_v2 VALUES ('s2','p1','` + dirB + `','small alpha session','m',0,0,0)`,
		`INSERT INTO session_v2 VALUES ('s3','p2','` + dirGone + `','beta session','m',0,0,0)`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// s1 gets 3x the payload of s2 and s3.
	for i := 0; i < 3; i++ {
		if _, err := conn.SQL.Exec(
			`INSERT INTO session_message VALUES ('m1_`+string(rune('a'+i))+
				`','s1','user',`+string(rune('0'+i))+`,0,0,?)`, blob); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"s2", "s3"} {
		if _, err := conn.SQL.Exec(
			`INSERT INTO session_message VALUES ('m_`+id+`','`+id+`','user',0,0,0,?)`, blob); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()

	s, err := storage.Run(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newModel(s *storage.Scan) Model {
	m := New(s.Paths)
	m.scan = s
	m.width, m.height = 110, 40
	return m
}

func press(t *testing.T, m Model, key string) Model {
	t.Helper()
	var msg tea.KeyMsg
	if len(key) == 1 {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	} else {
		switch key {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			t.Fatalf("unknown key %q", key)
		}
	}
	out, _ := m.Update(msg)
	return out.(Model)
}

// TestPreviewRendersWithoutATerminal keeps the dashboard honest: every view
// reads fields off the scan, and a nil map or empty slice must not panic.
func TestPreviewRendersWithoutATerminal(t *testing.T) {
	s := fixtureScan(t)
	out := Preview(s, PreviewOptions{Screen: "dash", Width: 112, Height: 44})
	for _, want := range []string{"OCSA", "DATABASE", "GENERATIONS", "RECLAIMABLE", "SESSIONS", "ACTIONS"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard is missing %q\n%s", want, out)
		}
	}
}

// TestMenuIsNavigable is the point of the interactive dashboard: the arrows
// move a cursor and enter runs what is under it.
func TestMenuIsNavigable(t *testing.T) {
	m := newModel(fixtureScan(t))

	if m.cursor != 0 {
		t.Fatalf("cursor starts at %d, want 0", m.cursor)
	}
	m = press(t, m, "down")
	m = press(t, m, "down")
	if m.cursor != 2 {
		t.Fatalf("after two downs cursor is %d, want 2", m.cursor)
	}
	m = press(t, m, "up")
	if m.cursor != 1 {
		t.Fatalf("after an up cursor is %d, want 1", m.cursor)
	}

	// Walk to Browse projects and run it with enter.
	for i, it := range m.menu() {
		if it.id == "p" {
			m.cursor = i
		}
	}
	m = press(t, m, "enter")
	if m.screen != scrProjects {
		t.Fatalf("enter on Browse projects opened screen %v, want projects", m.screen)
	}
}

// TestSingleKeyShortcutsStillWork: the menu is an addition, not a replacement.
func TestSingleKeyShortcutsStillWork(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want screen
	}{
		{"p", scrProjects},
		{"s", scrSessions},
	} {
		m := press(t, newModel(fixtureScan(t)), tc.key)
		if m.screen != tc.want {
			t.Errorf("key %q opened screen %v, want %v", tc.key, m.screen, tc.want)
		}
	}
}

// TestMutatingActionsConfirmFirst: nothing may write without a confirmation,
// and cancelling must return to where the reader was.
func TestMutatingActionsConfirmFirst(t *testing.T) {
	for _, id := range []string{"v", "i", "a", "f", "c", "k"} {
		m := newModel(fixtureScan(t))
		out, _ := m.activate(id)
		m = out.(Model)
		if m.screen != scrConfirm {
			t.Errorf("action %q did not ask for confirmation (screen %v)", id, m.screen)
			continue
		}
		if m.run == nil {
			t.Errorf("action %q has no run function", id)
		}
		if m.prev != scrDash {
			t.Errorf("action %q would return to screen %v, want dashboard", id, m.prev)
		}
		// n cancels back to the previous screen.
		if m = press(t, m, "n"); m.screen != scrDash {
			t.Errorf("action %q: cancel went to screen %v, want dashboard", id, m.screen)
		}
	}
}

// TestProjectsBrowserShowsUsagePerFolder covers the feature request: storage
// usage per project, and drilling into one shows its sessions per folder.
func TestProjectsBrowserShowsUsagePerFolder(t *testing.T) {
	s := fixtureScan(t)
	out := Preview(s, PreviewOptions{Screen: "projects", Width: 112, Height: 44})
	for _, want := range []string{"PROJECTS", "alpha", "beta", "SIZE", "SESS"} {
		if !strings.Contains(out, want) {
			t.Errorf("projects view is missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "gone") {
		t.Errorf("projects view does not flag a missing folder\n%s", out)
	}
	if !strings.Contains(out, "MB") && !strings.Contains(out, "KB") {
		t.Errorf("projects view shows no storage usage\n%s", out)
	}
}

// TestSessionsGroupedByFolder checks the folder grouping, including subtotals,
// which is what makes a cross-folder session list readable.
func TestSessionsGroupedByFolder(t *testing.T) {
	s := fixtureScan(t)

	flat := Preview(s, PreviewOptions{Screen: "sessions", Width: 112, Height: 44})
	if strings.Contains(flat, "sessions") == false {
		t.Errorf("flat session list is empty\n%s", flat)
	}

	grouped := Preview(s, PreviewOptions{Screen: "grouped", Width: 112, Height: 44})
	if !strings.Contains(grouped, "grouped by folder") {
		t.Errorf("grouped view does not say it is grouped\n%s", grouped)
	}
	if strings.Count(grouped, "projects/") < 2 {
		t.Errorf("grouped view does not show one header per folder\n%s", grouped)
	}
	if !strings.Contains(grouped, "sessions") {
		t.Errorf("grouped view is missing per-folder subtotals\n%s", grouped)
	}

	// Filtering to one project must drop the others.
	filtered := Preview(s, PreviewOptions{Screen: "sessions", ProjectID: "p1", Width: 112, Height: 44})
	if strings.Contains(filtered, "beta session") {
		t.Errorf("filter to p1 leaked a p2 session\n%s", filtered)
	}
	if !strings.Contains(filtered, "alpha") {
		t.Errorf("filter to p1 lost its own sessions\n%s", filtered)
	}
}

// TestGroupingToggles covers the in-view toggle.
func TestGroupingToggles(t *testing.T) {
	m := newModel(fixtureScan(t))
	m = press(t, m, "s")
	if m.grouped {
		t.Fatal("sessions start grouped, want flat")
	}
	m = press(t, m, "g")
	if !m.grouped {
		t.Error("g did not turn grouping on")
	}
	if out := m.viewSessions(); !strings.Contains(out, "grouped by folder") {
		t.Errorf("grouped view not rendered\n%s", out)
	}
	m = press(t, m, "g")
	if m.grouped {
		t.Error("g did not turn grouping off")
	}
}

// TestClampOnEmptyList: a terminal with no height, or a list with no rows, must
// not panic in the scroll arithmetic.
func TestClampOnEmptyList(t *testing.T) {
	s := fixtureScan(t)
	m := newModel(s)
	m.height = 0
	m.clamp(0)
	m.clamp(10)
	if m.offset != 0 {
		t.Errorf("offset drifted to %d", m.offset)
	}
	m.screen = scrSessions
	if out := m.viewSessions(); !strings.Contains(out, "SESSIONS") {
		t.Errorf("sessions view did not render at zero height:\n%s", out)
	}
}

// TestNoRenderedLineOverflowsTheFrame is the alignment regression. Long lines
// used to be left to lipgloss's word wrapper, which broke them at the left
// margin with no hanging indent, so a wrapped warning or a wide action row read
// as a new entry. Every line now has to fit the width it was given.
func TestNoRenderedLineOverflowsTheFrame(t *testing.T) {
	s := fixtureScan(t)
	for _, width := range []int{60, 70, 80, 100, 120, 160} {
		for _, screen := range []string{"dash", "projects", "sessions", "grouped"} {
			out := Preview(s, PreviewOptions{Screen: screen, Width: width, Height: 40})
			for i, line := range strings.Split(out, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("screen %q at width %d: line %d is %d cells wide\n%q",
						screen, width, i, got, line)
				}
			}
		}
	}
}

// TestReclaimAmountsShareAColumn checks the numbers in RECLAIMABLE are
// right-aligned to one column, which is what makes them comparable at a glance.
func TestReclaimAmountsShareAColumn(t *testing.T) {
	out := Preview(fixtureScan(t), PreviewOptions{Screen: "dash", Width: 120, Height: 40})
	rows := section(out, "RECLAIMABLE")
	if len(rows) < 3 {
		t.Fatalf("expected several reclaimable rows, got %d in:\n%s", len(rows), out)
	}
	var ends []int
	for _, row := range rows {
		body := strings.TrimRight(strings.TrimRight(row, " "), "│")
		body = strings.TrimRight(body, " ")
		ends = append(ends, lipgloss.Width(body))
	}
	for _, e := range ends[1:] {
		if e != ends[0] {
			t.Errorf("amounts are not in one column: right edges %v\n%s", ends, out)
			break
		}
	}
}

// section returns the content rows following a heading, up to the next blank
// content line. The box pads every row to the same total width, so alignment
// has to be measured inside a row, not by total line length.
func section(out, heading string) []string {
	lines := strings.Split(out, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, heading) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var rows []string
	for _, l := range lines[start+1:] {
		body := strings.TrimSpace(strings.Trim(l, "│╭╰─ "))
		if body == "" {
			break
		}
		rows = append(rows, l)
	}
	return rows
}

// TestTableHeadersStayOnOneLine catches what the overflow test cannot: wrapping
// produces lines that are all short enough, so a header that broke in half
// ("UPDATED" on its own row) passed that check but looked like corruption.
func TestTableHeadersStayOnOneLine(t *testing.T) {
	s := fixtureScan(t)
	cases := []struct {
		screen   string
		anchor   string
		mustHave []string
	}{
		{"sessions", "UPDATED", []string{"SIZE", "TITLE", "FOLDER", "UPDATED"}},
		{"grouped", "UPDATED", []string{"SIZE", "TITLE", "FOLDER", "UPDATED"}},
		{"projects", "LAST USED", []string{"SIZE", "SESS", "PROJECT", "FOLDER", "LAST USED"}},
	}
	for _, width := range []int{72, 84, 100, 120} {
		for _, tc := range cases {
			out := Preview(s, PreviewOptions{Screen: tc.screen, Width: width, Height: 40})
			var header string
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, tc.anchor) {
					header = line
					break
				}
			}
			if header == "" {
				t.Errorf("%s at %d: no header row found", tc.screen, width)
				continue
			}
			for _, want := range tc.mustHave {
				if !strings.Contains(header, want) {
					t.Errorf("%s at width %d: header lost %q, so it wrapped:\n%s",
						tc.screen, width, want, header)
				}
			}
		}
	}
}

func TestEscReturnsToDashboard(t *testing.T) {
	m := press(t, newModel(fixtureScan(t)), "p")
	if m.screen != scrProjects {
		t.Fatal("p did not open projects")
	}
	if m = press(t, m, "esc"); m.screen != scrDash {
		t.Errorf("esc left us on screen %v", m.screen)
	}
}
