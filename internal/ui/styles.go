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

// lipglossWidth hard-wraps a body to the frame width. Wrapping, not clipping,
// so a narrow terminal never loses the tail of a line.
func lipglossWidth(body string, w int) string {
	if w <= 0 {
		return body
	}
	return lipgloss.NewStyle().Width(w).Render(body)
}
