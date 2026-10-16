package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

func cells(s string) int { return len([]rune(plain(s))) }

// TestStackedBarFillsExactlyItsWidth: rounding each segment independently
// leaves the bar a cell short or long, which is visible as a gap or an overflow
// against the text beside it.
func TestStackedBarFillsExactlyItsWidth(t *testing.T) {
	for _, width := range []int{1, 7, 22, 40, 100} {
		segments := []stackSegment{
			{bytes: 177, style: stLive, glyph: glyphLive},
			{bytes: 379, style: stFrozen, glyph: glyphFrozen},
			{bytes: 8, style: stOther, glyph: glyphOther},
			{bytes: 3, style: stReclaim, glyph: glyphReclaim},
		}
		bar := stackedBar(segments, width)
		if got := cells(bar); got != width {
			t.Errorf("width %d: bar is %d cells:\n%q", width, got, plain(bar))
		}
	}
}

// TestStackedBarShowsEveryPresentSegment: a category that exists but gets no
// cell means the bar misrepresents the file.
func TestStackedBarShowsEveryPresentSegment(t *testing.T) {
	segments := []stackSegment{
		{bytes: 10000, style: stLive, glyph: glyphLive},
		{bytes: 20, style: stOther, glyph: glyphOther},
	}
	bar := plain(stackedBar(segments, 30))
	if !strings.Contains(bar, glyphLive) {
		t.Errorf("live segment missing:\n%s", bar)
	}
	if !strings.Contains(bar, glyphOther) {
		t.Errorf("a present segment got no cells, so the bar hides it:\n%s", bar)
	}
}

func TestStackedBarHandlesEmptyTotals(t *testing.T) {
	bar := stackedBar([]stackSegment{{0, stLive, glyphLive}, {0, stFrozen, glyphFrozen}}, 10)
	if cells(bar) != 10 {
		t.Errorf("empty bar is %d cells, want 10", cells(bar))
	}
}

func TestFitNeverExceedsItsBudget(t *testing.T) {
	for _, w := range []int{1, 2, 5, 20} {
		for _, s := range []string{"", "abc", "a much longer string than the budget allows"} {
			if got := lipgloss.Width(fit(s, w)); got > w {
				t.Errorf("fit(%q, %d) produced %d cells", s, w, got)
			}
		}
	}
}

// TestWrapWordsKeepsHangingIndent: a continuation line must be indented, or a
// wrapped warning reads as a new entry in the list.
func TestWrapWordsKeepsHangingIndent(t *testing.T) {
	lines := wrapWords("the quick brown fox jumps over the lazy dog again and again", 30, "! ", "  ")
	if len(lines) < 2 {
		t.Fatalf("nothing wrapped: %v", lines)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("continuation line lost its indent: %q", l)
		}
	}
	for _, l := range lines {
		if lipgloss.Width(l) > 30 {
			t.Errorf("line exceeds the budget: %q (%d)", l, lipgloss.Width(l))
		}
	}
}
