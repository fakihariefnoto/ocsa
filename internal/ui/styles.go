package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Styles are the whole visual language, defined once.
var (
	colAccent = lipgloss.Color("39")
	colOK     = lipgloss.Color("42")
	colWarn   = lipgloss.Color("214")
	colBad    = lipgloss.Color("203")
	colDim    = lipgloss.Color("241")
	colText   = lipgloss.Color("252")

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stSub      = lipgloss.NewStyle().Foreground(colDim)
	stLabel    = lipgloss.NewStyle().Foreground(colDim)
	stText     = lipgloss.NewStyle().Foreground(colText)
	stValue    = lipgloss.NewStyle().Foreground(colText)
	stOK       = lipgloss.NewStyle().Foreground(colOK)
	stWarn     = lipgloss.NewStyle().Foreground(colWarn)
	stBad      = lipgloss.NewStyle().Foreground(colBad)
	stKey      = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stSel      = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stCursor   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stFill     = lipgloss.NewStyle().Foreground(colAccent)
	stEmpty    = lipgloss.NewStyle().Foreground(colDim)
	stGoodBar  = lipgloss.NewStyle().Foreground(colOK)
	stRow      = lipgloss.NewStyle().Foreground(colText)
	stRowAlt   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	stGroup    = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("24")).Bold(true)
	stBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(colDim).Padding(0, 1)
)

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
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ago renders a timestamp as a rough age, which is what a storage reader wants
// to know: how long since anything touched this.
func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

// bar draws live bytes inside total bytes.
func bar(live, total int64, width int) string {
	if total <= 0 || width <= 0 {
		return stEmpty.Render(strings.Repeat("░", max(width, 0)))
	}
	fill := int(float64(live) / float64(total) * float64(width))
	if fill < 1 && live > 0 {
		fill = 1
	}
	if fill > width {
		fill = width
	}
	return stFill.Render(strings.Repeat("█", fill)) + stEmpty.Render(strings.Repeat("░", width-fill))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// plural renders "1 session" / "3 sessions".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func integrityStyle(v string) string {
	if v == "ok" {
		return stOK.Render(v)
	}
	return stBad.Render(v)
}

// lipglossWidth hard-wraps a body to the frame width. Wrapping, not clipping,
// so a narrow terminal never loses the tail of a line.
func lipglossWidth(body string, w int) string {
	if w <= 0 {
		return body
	}
	return lipgloss.NewStyle().Width(w).Render(body)
}

// fit truncates to at most w visible cells, ellipsis included. Budgets are
// computed from it so no line is left to lipgloss's wrapper, which breaks a
// long line at the left margin with no hanging indent.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// wrapWords breaks text into lines of at most width cells. The first line is
// prefixed with firstIndent and continuations with hangIndent, so a wrapped
// line reads as a continuation rather than a new entry.
func wrapWords(text string, width int, firstIndent, hangIndent string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{firstIndent}
	}
	indent := firstIndent
	cur := indent
	hasWord := false
	var lines []string
	for _, word := range words {
		add := word
		if hasWord {
			add = " " + word
		}
		if hasWord && lipgloss.Width(cur)+lipgloss.Width(add) > width {
			lines = append(lines, cur)
			indent = hangIndent
			cur = indent
			hasWord = false
			add = word
		}
		cur += add
		hasWord = true
	}
	lines = append(lines, cur)
	return lines
}

// contentWidth is the usable width inside the box: the frame width less the
// border and padding, capped so the dashboard does not sprawl on a wide screen.
func (m Model) contentWidth() int {
	w := m.width - 4
	if w > 104 {
		w = 104
	}
	if w < 32 {
		w = 32
	}
	return w
}
