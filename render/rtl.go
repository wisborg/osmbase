package render

import "unicode"

// Visual puts a label's text into the order it is drawn in, and joins its
// Arabic letters.
//
// Every face here draws letters one after another, left to right, and the
// text in the tiles is in LOGICAL order -- the order it is read in. For
// Hebrew and Arabic that is right to left, and drawn as it comes a name reads
// backwards: Syria's own name on a map of the Middle East was its letters in
// reverse. So the runs of right-to-left text are turned round -- the whole
// label's, when it starts right to left, with the numbers and Latin words in
// it kept reading left to right, and brackets mirrored.
//
// Arabic letters also change shape by where they fall in a word, and a font
// draws the shape it is asked for. Unicode has every shape as a character of
// its own, the presentation forms, and each letter is replaced by the one its
// neighbours call for -- initial, medial, final or isolated -- with lam and
// alef as their one ligature. It is the shaping a map's names need, not a
// shaper: vowel marks are left where they are, and scripts that reorder
// letters, Devanagari among them, are drawn as they come.
//
// A label with no right-to-left letter in it is returned as it is.
func Visual(s string) string {
	rs := []rune(s)
	rtl := false
	for _, r := range rs {
		if isRTL(r) {
			rtl = true
			break
		}
	}
	if !rtl {
		return s
	}
	return string(reorder(joinArabic(rs)))
}

// isRTL reports whether a letter is written right to left: Hebrew, Arabic,
// Syriac, Thaana, N'Ko and their presentation forms.
func isRTL(r rune) bool {
	switch {
	case r >= 0x0590 && r <= 0x08FF:
		return true
	case r >= 0xFB1D && r <= 0xFDFF, r >= 0xFE70 && r <= 0xFEFF:
		return true
	}
	return false
}

// class is a run's direction: strong left to right, strong right to left, or
// neutral until its neighbours decide.
type class int

const (
	neutral class = iota
	ltr
	rtl
)

func classOf(r rune) class {
	switch {
	case isRTL(r) && !isMark(r):
		return rtl
	case unicode.IsLetter(r), unicode.IsDigit(r):
		// Numbers read left to right in a right-to-left name as anywhere.
		return ltr
	}
	return neutral
}

// isMark is an Arabic or Hebrew combining mark: it takes its letter's place.
func isMark(r rune) bool { return unicode.Is(unicode.Mn, r) }

// reorder is a simplified bidi: runs of one direction, neutrals taking the
// direction either side of them when both agree and the label's own
// otherwise, and the label's direction its first strong letter's.
func reorder(rs []rune) []rune {
	type run struct {
		c     class
		runes []rune
	}
	var runs []run
	base := neutral
	for _, r := range rs {
		c := classOf(r)
		if isMark(r) && len(runs) > 0 {
			c = runs[len(runs)-1].c
		}
		if base == neutral && c != neutral {
			base = c
		}
		if n := len(runs); n > 0 && runs[n-1].c == c {
			runs[n-1].runes = append(runs[n-1].runes, r)
			continue
		}
		runs = append(runs, run{c: c, runes: []rune{r}})
	}
	if base == neutral {
		base = ltr
	}
	for i := range runs {
		if runs[i].c != neutral {
			continue
		}
		before, after := base, base
		if i > 0 {
			before = runs[i-1].c
		}
		if i+1 < len(runs) {
			after = runs[i+1].c
		}
		if before == after {
			runs[i].c = before
		} else {
			runs[i].c = base
		}
	}
	// Resolved, a neutral belongs to the run either side of it: merge them,
	// so two Hebrew words and the space between are one run, turned round
	// as one, and not two turned round in place.
	merged := runs[:0]
	for _, r := range runs {
		if n := len(merged); n > 0 && merged[n-1].c == r.c {
			merged[n-1].runes = append(merged[n-1].runes, r.runes...)
			continue
		}
		merged = append(merged, r)
	}
	runs = merged
	reverse := func(rs []rune, mirror bool) {
		for a, b := 0, len(rs)-1; a < b; a, b = a+1, b-1 {
			rs[a], rs[b] = rs[b], rs[a]
		}
		if mirror {
			for i, r := range rs {
				if m, ok := mirrored[r]; ok {
					rs[i] = m
				}
			}
		}
	}
	if base == rtl {
		for a, b := 0, len(runs)-1; a < b; a, b = a+1, b-1 {
			runs[a], runs[b] = runs[b], runs[a]
		}
	}
	var out []rune
	for _, r := range runs {
		if r.c == rtl {
			// Marks go after their letter in logical order; drawn right
			// to left, they must still follow it, so a letter and its marks
			// are turned round as one.
			reverse(r.runes, true)
			fixMarks(r.runes)
		}
		out = append(out, r.runes...)
	}
	return out
}

// fixMarks puts each run of combining marks back after the letter it belongs
// to, after reverse has put them before it.
func fixMarks(rs []rune) {
	for i := 0; i < len(rs); {
		j := i
		for j < len(rs) && isMark(rs[j]) {
			j++
		}
		if j > i && j < len(rs) {
			// rs[i:j] are marks that belonged to rs[j]: move it in front.
			letter := rs[j]
			copy(rs[i+1:j+1], rs[i:j])
			rs[i] = letter
			i = j + 1
			continue
		}
		i = j + 1
	}
}

var mirrored = map[rune]rune{'(': ')', ')': '(', '[': ']', ']': '[', '{': '}', '}': '{', '<': '>', '>': '<'}

// forms are an Arabic letter's presentation forms: isolated, final, initial,
// medial. A letter with no initial form joins only to the letter before it.
type forms [4]rune

var arabic = map[rune]forms{
	0x0621: {0xFE80, 0, 0, 0},
	0x0622: {0xFE81, 0xFE82, 0, 0},
	0x0623: {0xFE83, 0xFE84, 0, 0},
	0x0624: {0xFE85, 0xFE86, 0, 0},
	0x0625: {0xFE87, 0xFE88, 0, 0},
	0x0626: {0xFE89, 0xFE8A, 0xFE8B, 0xFE8C},
	0x0627: {0xFE8D, 0xFE8E, 0, 0},
	0x0628: {0xFE8F, 0xFE90, 0xFE91, 0xFE92},
	0x0629: {0xFE93, 0xFE94, 0, 0},
	0x062A: {0xFE95, 0xFE96, 0xFE97, 0xFE98},
	0x062B: {0xFE99, 0xFE9A, 0xFE9B, 0xFE9C},
	0x062C: {0xFE9D, 0xFE9E, 0xFE9F, 0xFEA0},
	0x062D: {0xFEA1, 0xFEA2, 0xFEA3, 0xFEA4},
	0x062E: {0xFEA5, 0xFEA6, 0xFEA7, 0xFEA8},
	0x062F: {0xFEA9, 0xFEAA, 0, 0},
	0x0630: {0xFEAB, 0xFEAC, 0, 0},
	0x0631: {0xFEAD, 0xFEAE, 0, 0},
	0x0632: {0xFEAF, 0xFEB0, 0, 0},
	0x0633: {0xFEB1, 0xFEB2, 0xFEB3, 0xFEB4},
	0x0634: {0xFEB5, 0xFEB6, 0xFEB7, 0xFEB8},
	0x0635: {0xFEB9, 0xFEBA, 0xFEBB, 0xFEBC},
	0x0636: {0xFEBD, 0xFEBE, 0xFEBF, 0xFEC0},
	0x0637: {0xFEC1, 0xFEC2, 0xFEC3, 0xFEC4},
	0x0638: {0xFEC5, 0xFEC6, 0xFEC7, 0xFEC8},
	0x0639: {0xFEC9, 0xFECA, 0xFECB, 0xFECC},
	0x063A: {0xFECD, 0xFECE, 0xFECF, 0xFED0},
	0x0641: {0xFED1, 0xFED2, 0xFED3, 0xFED4},
	0x0642: {0xFED5, 0xFED6, 0xFED7, 0xFED8},
	0x0643: {0xFED9, 0xFEDA, 0xFEDB, 0xFEDC},
	0x0644: {0xFEDD, 0xFEDE, 0xFEDF, 0xFEE0},
	0x0645: {0xFEE1, 0xFEE2, 0xFEE3, 0xFEE4},
	0x0646: {0xFEE5, 0xFEE6, 0xFEE7, 0xFEE8},
	0x0647: {0xFEE9, 0xFEEA, 0xFEEB, 0xFEEC},
	0x0648: {0xFEED, 0xFEEE, 0, 0},
	0x0649: {0xFEEF, 0xFEF0, 0, 0},
	0x064A: {0xFEF1, 0xFEF2, 0xFEF3, 0xFEF4},
	// Persian and Urdu.
	0x067E: {0xFB56, 0xFB57, 0xFB58, 0xFB59},
	0x0686: {0xFB7A, 0xFB7B, 0xFB7C, 0xFB7D},
	0x0698: {0xFB8A, 0xFB8B, 0, 0},
	0x06A9: {0xFB8E, 0xFB8F, 0xFB90, 0xFB91},
	0x06AF: {0xFB92, 0xFB93, 0xFB94, 0xFB95},
	0x06CC: {0xFBFC, 0xFBFD, 0xFBFE, 0xFBFF},
}

// lamAlef is the ligature lam makes with each alef: isolated and final.
var lamAlef = map[rune][2]rune{
	0x0622: {0xFEF5, 0xFEF6},
	0x0623: {0xFEF7, 0xFEF8},
	0x0625: {0xFEF9, 0xFEFA},
	0x0627: {0xFEFB, 0xFEFC},
}

const tatweel = 0x0640

// joinsForward reports whether a letter connects to the one after it.
func joinsForward(r rune) bool {
	if r == tatweel {
		return true
	}
	f, ok := arabic[r]
	return ok && f[2] != 0
}

// joinsBack reports whether a letter connects to the one before it.
func joinsBack(r rune) bool {
	if r == tatweel {
		return true
	}
	f, ok := arabic[r]
	return ok && f[1] != 0
}

// joinArabic replaces each Arabic letter with the form its neighbours call
// for, in logical order. Marks are transparent: a letter joins across them.
func joinArabic(rs []rune) []rune {
	neighbour := func(i, step int) rune {
		for j := i + step; j >= 0 && j < len(rs); j += step {
			if !isMark(rs[j]) {
				return rs[j]
			}
		}
		return 0
	}
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		f, ok := arabic[r]
		if !ok {
			out = append(out, r)
			continue
		}
		prev := joinsForward(neighbour(i, -1))
		if r == 0x0644 {
			// Lam with the alef after it -- past any marks -- is one
			// glyph, and the alef is not drawn again.
			j := i + 1
			for j < len(rs) && isMark(rs[j]) {
				j++
			}
			if j < len(rs) {
				if lig, ok := lamAlef[rs[j]]; ok {
					if prev {
						out = append(out, lig[1])
					} else {
						out = append(out, lig[0])
					}
					out = append(out, rs[i+1:j]...)
					i = j
					continue
				}
			}
		}
		next := joinsBack(neighbour(i, 1)) && f[2] != 0
		prev = prev && f[1] != 0
		switch {
		case prev && next:
			out = append(out, f[3])
		case prev:
			out = append(out, f[1])
		case next:
			out = append(out, f[2])
		default:
			out = append(out, f[0])
		}
	}
	return out
}
