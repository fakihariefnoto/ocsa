package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fakih/ocsa/internal/actions"
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
	for _, want := range []string{"OCSA", "DATABASE", "live", "frozen", "reclaimable",
		"RECLAIMABLE", "SESSIONS", "ACTIONS"} {
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

// TestClaimIsTheFirstAction: the point of the button is that it is the obvious
// one, so it must be reachable with a single enter from the dashboard.
func TestClaimIsTheFirstAction(t *testing.T) {
	m := newModel(fixtureScan(t))
	items := m.menu()
	if items[0].id != "claim" {
		t.Fatalf("first action is %q, want claim", items[0].id)
	}
	if m.cursor != 0 {
		t.Fatal("cursor does not start on the first action")
	}
	m = press(t, m, "enter")
	if m.screen != scrConfirm {
		t.Fatalf("enter opened screen %v, want the confirmation", m.screen)
	}
	if !m.isClaim {
		t.Fatal("the claim flag is not set, so the confirm screen would run the wrong thing")
	}
	if len(m.claimPlan.Steps) == 0 {
		t.Fatal("no plan was built to confirm")
	}
}

// TestClaimConfirmationShowsThePlan: the user has to be able to see what will
// happen, including what will be skipped and why, before agreeing to it.
func TestClaimConfirmationShowsThePlan(t *testing.T) {
	s := fixtureScan(t)
	if err := os.MkdirAll(s.Paths.Snapshot+"/snap1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Paths.Snapshot+"/snap1/obj", make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, err := storage.Run(s.Paths)
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(fresh)
	out, _ := m.activate("claim")
	m = out.(Model)
	view := m.viewConfirm()

	for _, want := range []string{"CLAIM STORAGE", "STEP", "FREES", "PROJECTED", "rewrite"} {
		if !strings.Contains(strings.ToLower(view), strings.ToLower(want)) {
			t.Errorf("confirmation is missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "nothing happens until you press") {
		t.Errorf("confirmation does not say it is inert until confirmed:\n%s", view)
	}

	// Every step must be visible with its reasoning, skipped ones included:
	// a silently skipped step is the thing this screen exists to prevent.
	for _, st := range m.claimPlan.Steps {
		if !strings.Contains(view, st.Label) {
			t.Errorf("step %q is not shown in the confirmation", st.Label)
		}
		if st.Status == actions.StepSkipped && st.Reason != "" &&
			!strings.Contains(view, st.Reason) {
			t.Errorf("skipped step %q does not say why", st.Label)
		}
	}
}

// fixtureScanWithSnapshots adds snapshot directories, which is the only way the
// snapshot step can have bytes to project.
func fixtureScanWithSnapshots(t *testing.T) *storage.Scan {
	t.Helper()
	s := fixtureScan(t)
	for i := 0; i < 8; i++ {
		dir := filepath.Join(s.Paths.Snapshot, "snap"+string(rune('a'+i)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "obj"), make([]byte, 64<<10), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := storage.Run(s.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Reclaim.SnapshotPrunable == 0 {
		t.Fatal("fixture has no prunable snapshots, so the toggle cannot be tested")
	}
	return fresh
}

// TestClaimSnapshotToggleIsOptIn covers the one destructive step.
func TestClaimSnapshotToggleIsOptIn(t *testing.T) {
	m := newModel(fixtureScanWithSnapshots(t))
	out, _ := m.activate("claim")
	m = out.(Model)

	statusOf := func(plan actions.Plan) string {
		for _, st := range plan.Steps {
			if st.ID == "snapshots" {
				return string(st.Status)
			}
		}
		return "missing"
	}
	if got := statusOf(m.claimPlan); got != string(actions.StepSkipped) {
		t.Errorf("snapshots are %q by default, want skipped", got)
	}
	before := m.claimPlan.Projected

	m = press(t, m, "s")
	if !m.claimOpts.IncludeSnapshots {
		t.Fatal("s did not opt in")
	}
	if got := statusOf(m.claimPlan); got != string(actions.StepPlanned) {
		t.Errorf("after opting in, snapshots are %q, want planned", got)
	}
	if m.claimPlan.Projected <= before {
		t.Error("opting in did not raise the projection")
	}
	if !strings.Contains(m.viewConfirm(), "breaks revert") {
		t.Error("opting in does not warn about what it costs")
	}

	m = press(t, m, "s")
	if m.claimOpts.IncludeSnapshots {
		t.Error("s did not toggle back off")
	}
}

// TestClaimCancelChangesNothing: n must return where it came from and run no
// action at all.
func TestClaimCancelChangesNothing(t *testing.T) {
	m := newModel(fixtureScan(t))
	out, _ := m.activate("claim")
	m = out.(Model)
	if m.runClaimCmdForTest() != nil {
		t.Fatal("a cancelable claim already has a run function")
	}
	m = press(t, m, "n")
	if m.screen != scrDash {
		t.Errorf("cancel went to screen %v, want the dashboard", m.screen)
	}
	if m.isClaim {
		t.Error("claim flag survived a cancel")
	}
}

// TestPlanRendersInTheResultScreen: after running, the per-step outcome has to
// be visible, not just a total.
func TestPlanRendersInTheResultScreen(t *testing.T) {
	m := newModel(fixtureScan(t))
	out, _ := m.activate("claim")
	m = out.(Model)
	m.claimResult = &actions.ClaimResult{
		Result: actions.Result{
			Action: "claim storage", Before: 1024, After: 512, Reclaimed: 512,
			Verified: true, Elapsed: time.Second,
		},
		Outcomes: []actions.StepOutcome{
			{Step: actions.Step{ID: "vacuum", Label: "Rewrite the database"}, Bytes: 512, Note: "rewritten"},
			{Step: actions.Step{ID: "log", Label: "Trim the log"}, Err: "permission denied"},
		},
		Activity: actions.ActivityReport{Summary: "opencode (pid 1)", SafeIfRun: true},
	}
	m.screen = scrDone
	view := m.viewDone()
	for _, want := range []string{"STEPS", "Rewrite the database", "Trim the log", "permission denied", "OPENCODE AT THE TIME"} {
		if !strings.Contains(view, want) {
			t.Errorf("result screen is missing %q:\n%s", want, view)
		}
	}
}
