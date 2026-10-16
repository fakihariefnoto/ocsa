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

func sampleSegments() []stackSegment {
	return []stackSegment{
		{bytes: 177, style: stLive, glyph: glyphBlock},
		{bytes: 379, style: stFrozen, glyph: glyphBlock},
		{bytes: 8, style: stOther, glyph: glyphBlock},
		{bytes: 3, style: stReclaim, glyph: glyphBlock},
	}
}

// TestStackedBarFillsExactlyItsWidth: rounding each segment independently
// leaves the bar a cell short or long, which is visible as a gap against the
// text beside it.
func TestStackedBarFillsExactlyItsWidth(t *testing.T) {
	for _, width := range []int{1, 7, 22, 40, 100} {
		bar := stackedBar(sampleSegments(), width)
		if got := cells(bar); got != width {
			t.Errorf("width %d: bar is %d cells:\n%q", width, got, plain(bar))
		}
	}
}

// TestAllocationShowsEveryPresentSegment: a category that exists but gets no
// cells means the bar misrepresents the file. This is asserted on the
// allocation rather than the glyphs, because every segment shares one glyph now
// and colour is not observable in a test.
func TestAllocationShowsEveryPresentSegment(t *testing.T) {
	cellsPerSeg := allocateCells(sampleSegments(), 40)
	for i, n := range cellsPerSeg {
		if n < 1 {
			t.Errorf("segment %d got %d cells; a present category must be visible", i, n)
		}
	}

	// Proportions must follow the bytes, not the order.
	big := allocateCells([]stackSegment{
		{bytes: 900, style: stLive, glyph: glyphBlock},
		{bytes: 100, style: stFrozen, glyph: glyphBlock},
	}, 40)
	if big[0] <= big[1] {
		t.Errorf("a 9:1 split came out %v", big)
	}
	if big[0]+big[1] != 40 {
		t.Errorf("split does not fill the bar: %v", big)
	}

	// A segment of zero bytes must not take a cell from a real one.
	withEmpty := allocateCells([]stackSegment{
		{bytes: 500, style: stLive, glyph: glyphBlock},
		{bytes: 0, style: stFrozen, glyph: glyphBlock},
		{bytes: 500, style: stOther, glyph: glyphBlock},
	}, 20)
	if withEmpty[1] != 0 {
		t.Errorf("an empty segment got %d cells", withEmpty[1])
	}
}

// TestStackedBarIsOneUnbrokenRun is the regression for the bar that looked
// broken: mixing shade glyphs (█▓▒░) read as a fade or a rendering fault. It is
// one glyph now, and colour makes the parts.
func TestStackedBarIsOneUnbrokenRun(t *testing.T) {
	bar := plain(stackedBar(sampleSegments(), 40))
	if strings.Trim(bar, glyphBlock) != "" {
		t.Errorf("bar contains something other than %q:\n%s", glyphBlock, bar)
	}
	if len([]rune(bar)) != 40 {
		t.Errorf("bar is %d cells, want 40 unbroken", len([]rune(bar)))
	}
}

func TestStackedBarHandlesEmptyTotals(t *testing.T) {
	bar := stackedBar([]stackSegment{
		{0, stLive, glyphBlock},
		{0, stFrozen, glyphBlock},
	}, 10)
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
