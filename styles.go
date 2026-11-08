package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// A quiet palette: one accent, one ink, and a few semantic notes. Everything
// else is space and rules. The UI should read well on both light and dark
// terminals without depending on a filled background.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#4f46e5", Dark: "#a5b4fc"}
	colDim    = lipgloss.AdaptiveColor{Light: "#71767f", Dark: "#737781"}
	colFaint  = lipgloss.AdaptiveColor{Light: "#d8dbe1", Dark: "#2c2f36"}
	colGood   = lipgloss.AdaptiveColor{Light: "#087f5b", Dark: "#63d3a6"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#a05a00", Dark: "#f0b45f"}
	colBad    = lipgloss.AdaptiveColor{Light: "#c92a2a", Dark: "#ff8a8a"}
)

var (
	styAccent = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styDim    = lipgloss.NewStyle().Foreground(colDim)
	styFaint  = lipgloss.NewStyle().Foreground(colFaint)
	styGood   = lipgloss.NewStyle().Foreground(colGood)
	styWarn   = lipgloss.NewStyle().Foreground(colWarn)
	styBad    = lipgloss.NewStyle().Foreground(colBad)
	styStrong = lipgloss.NewStyle().Bold(true)
	styKey    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
)

func accent(s string) string { return styAccent.Render(s) }
func dim(s string) string    { return styDim.Render(s) }
func bold(s string) string   { return styStrong.Render(s) }
func keycap(k string) string { return styKey.Render(k) }

// spinner is intentionally small and quiet so it does not steal attention
// from the filename it sits next to.
func spinner(frame int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[frame%len(frames)]
}

// thinBar is the progress meter used on the operation screens. A full block
// reads as work done; the faint track shows how much is left.
func thinBar(fraction float64, width int) string {
	if width < 3 {
		return ""
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	return styAccent.Render(strings.Repeat("━", filled)) +
		styFaint.Render(strings.Repeat("─", width-filled))
}

// indeterminateBar is a small travelling segment for work whose total is not
// known yet (collecting a file list, measuring a folder).
func indeterminateBar(frame, width int) string {
	if width < 6 {
		return ""
	}
	seg := width / 4
	if seg < 3 {
		seg = 3
	}
	pos := (frame * 2) % (width + seg)
	start := pos - seg
	if start < 0 {
		start = 0
	}
	end := pos
	if end > width {
		end = width
	}
	if end < start {
		end = start
	}
	return styFaint.Render(strings.Repeat("─", start)) +
		styAccent.Render(strings.Repeat("━", end-start)) +
		styFaint.Render(strings.Repeat("─", width-end))
}

// clip shortens s to width and marks the cut with an ellipsis. It measures
// terminal cells, not runes, so wide CJK characters cannot break a column.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	target := width - 1
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > target {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// pad right-pads a styled string using its visible width.
func pad(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func padLeft(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

// elidePath keeps the informative tail of a long path.
func elidePath(p string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(p) <= width {
		return p
	}
	if width < 6 {
		return clip(p, width)
	}
	prefix := "…" + string(filepath.Separator)
	avail := width - lipgloss.Width(prefix)
	r := []rune(p)
	start := len(r)
	w := 0
	for start > 0 {
		rw := lipgloss.Width(string(r[start-1]))
		if w+rw > avail {
			break
		}
		w += rw
		start--
	}
	tail := string(r[start:])
	if i := strings.IndexAny(tail, `/\`); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return prefix + tail
}

func shortPath(p string) string {
	home := homeDir()
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// wrapHard breaks a string at width without trying to find spaces. It is used
// for paths and archive names, which have no useful word boundaries. Width is
// measured in terminal cells so wide glyphs stay inside the pane.
func wrapHard(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if rw == 0 {
			b.WriteRune(r)
			continue
		}
		if w+rw > width && b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
			w = 0
		}
		if rw > width {
			out = append(out, string(r))
			continue
		}
		b.WriteRune(r)
		w += rw
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	cur := ""
	for _, w := range words {
		if lipgloss.Width(w) > width {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			out = append(out, wrapHard(w, width)...)
			continue
		}
		if cur == "" {
			cur = w
			continue
		}
		if lipgloss.Width(cur)+1+lipgloss.Width(w) <= width {
			cur += " " + w
			continue
		}
		out = append(out, cur)
		cur = w
	}
	if cur != "" {
		out = append(out, cur)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func keyHints(hints [][2]string, width int) string {
	sep := styFaint.Render("  ·  ")
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		part := keycap(h[0]) + " " + styDim.Render(h[1])
		candidate := append(append([]string{}, parts...), part)
		line := " " + strings.Join(candidate, sep)
		if width > 0 && len(parts) > 0 && lipgloss.Width(line) > width {
			break
		}
		parts = candidate
	}
	return " " + strings.Join(parts, sep)
}

func visibleLen(s string) int { return lipgloss.Width(s) }

func countNoun(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
