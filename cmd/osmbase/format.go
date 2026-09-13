package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// humanBytes renders a byte count the way a person reads one.
//
// Binary units, because that is what a file size is and what every tool
// reporting one on this platform means. The exact count is printed beside it
// by bytesExact where it matters: "125.6 GiB" is what tells a reader the
// archive is enormous, and the exact number is what goes in a bug report.
func humanBytes(n int64) string {
	const unit = 1024
	if n < 0 {
		return fmt.Sprintf("%d bytes", n)
	}
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// bytesExact renders a size both ways: readable first, exact in brackets.
func bytesExact(n int64) string {
	if n < 1024 {
		return humanBytes(n)
	}
	return fmt.Sprintf("%s (%d bytes)", humanBytes(n), n)
}

// formatCoord renders a longitude or latitude for output.
//
// Seven decimal places is about a centimetre on the ground, which is finer
// than any tile geometry this reads -- a zoom 15 tile at extent 4096 resolves
// to roughly 30 cm -- and is the precision a PMTiles header stores its own
// bounds at. Trailing zeros are trimmed so that a round number reads as one
// and the JSON stays small; the result is still an exact decimal, never
// exponential, because JSON consumers and human eyes both dislike 1.5e-05.
func formatCoord(v float64) string {
	s := strconv.FormatFloat(v, 'f', 7, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	// FormatFloat renders negative zero as "-0", which is arithmetically
	// right and reads as a typo on a map.
	if s == "-0" {
		return "0"
	}
	return s
}

// table lays out rows in aligned columns.
//
// text/tabwriter would do this, and was what this used first. It was replaced
// for one reason: a blank separator row inside a tabwriter block is a row of
// empty cells, and tabwriter pads it, so every separator came out as a line of
// trailing spaces. That is invisible on a terminal and very visible in a bug
// report pasted into an issue tracker, which is what the inspect output is
// for. Here a row of no cells is an empty line, and the last column of every
// row is never padded.
//
// It also right-aligns chosen columns, which tabwriter can only do for all of
// them or none, and a table of feature counts wants its numbers aligned and
// its layer names not.
type table struct {
	rows  [][]string
	right map[int]bool
}

// rightAlign marks a column as right-aligned. Columns are numbered from zero.
func (t *table) rightAlign(columns ...int) {
	if t.right == nil {
		t.right = map[int]bool{}
	}
	for _, c := range columns {
		t.right[c] = true
	}
}

func (t *table) row(cells ...string) { t.rows = append(t.rows, cells) }

// blank adds an empty line, which separates sections without breaking the
// alignment of the columns around it.
func (t *table) blank() { t.rows = append(t.rows, nil) }

func (t *table) write(w io.Writer) {
	// Widths are measured over rows that actually have columns, so a blank
	// separator or a full-width note neither widens a column nor is padded to
	// one.
	var widths []int
	for _, r := range t.rows {
		if len(r) < 2 {
			continue
		}
		for i, cell := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if n := len([]rune(cell)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, r := range t.rows {
		var b strings.Builder
		for i, cell := range r {
			if i > 0 {
				b.WriteString("  ")
			}
			pad := 0
			if i < len(widths) {
				pad = widths[i] - len([]rune(cell))
			}
			if t.right[i] {
				// Padding on the LEFT, so a right-aligned column lines up
				// even when it is the last one on the line.
				b.WriteString(strings.Repeat(" ", pad))
				b.WriteString(cell)
				continue
			}
			b.WriteString(cell)
			// A left-aligned last cell is not padded: padding it is what puts
			// trailing spaces on every line.
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		fmt.Fprintln(w, b.String())
	}
}
