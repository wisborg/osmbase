package render

import "strings"

// PlainCredit turns an archive's attribution string into text that can be
// drawn.
//
// It is EXPORTED, and that is the point of it living here rather than in each
// consumer. The string comes out of the archive's own metadata and the
// archives this reads were authored for web maps -- Protomaps writes its
// attribution as HTML, because in a browser the credit is a link somebody can
// follow. Neither a PNG nor a video has a browser, so drawn verbatim the
// obligation that should read
//
//	(c) OpenStreetMap
//
// reads as an anchor tag instead. That happened, in a rendered frame, before
// anybody noticed.
//
// Every consumer has to solve it and the answer is the same for all of them,
// so solving it twice would be two answers to one question about a legal
// obligation -- which is the worst category of thing to have two of.
func PlainCredit(s string) string {
	plain := collapseSpace(decodeEntities(stripTags(s)))
	if plain == "" {
		return collapseSpace(s)
	}
	return plain
}

// stripTags removes anything that looks like an HTML tag, keeping the text
// around it.
//
// An unterminated '<' is kept literally rather than swallowing the rest of
// the string: a credit reading "Contains data < 2024" is likelier in a
// manifest than a truncated tag, and dropping everything after it would lose
// the part that names the source.
func stripTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for {
		open := strings.IndexByte(s, '<')
		if open < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := strings.IndexByte(s[open:], '>')
		if end < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:open])
		// Whether the tag was a word boundary is the one judgement in here,
		// and it follows what a browser would have shown rather than what is
		// convenient. An inline element joins the text either side of it, so
		// "<a ...>OpenStreetMap</a>, ODbL" has no space before the comma and
		// must not gain one; a break or a block element separates, so
		// "a<br>b" is two words and joining them would invent one. The list
		// is short because the only markup that reaches here is a credit line
		// from a manifest.
		if breaksLine(s[open+1 : open+end]) {
			b.WriteByte(' ')
		}
		s = s[open+end+1:]
	}
}

// breaksLine reports whether a tag's contents name an element that separates
// the text around it.
func breaksLine(tag string) bool {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "/")
	name, _, _ := strings.Cut(tag, " ")
	switch strings.ToLower(strings.TrimSuffix(name, "/")) {
	case "br", "p", "div", "li", "ul", "ol", "tr", "td":
		return true
	}
	return false
}

// creditEntities are the named references that occur in the attribution
// strings these archives actually carry, plus the numeric forms of the two
// quote characters.
//
// A slice rather than a map: the table is scanned in order and a map would
// scan it in a different order every run, which is the sort of thing that
// decides nothing until two entries overlap -- and rendering has to be
// reproducible frame for frame.
var creditEntities = []struct{ ref, text string }{
	{"&copy;", "©"},
	{"&#169;", "©"},
	{"&lt;", "<"},
	{"&gt;", ">"},
	{"&quot;", "\""},
	{"&#34;", "\""},
	{"&#39;", "'"},
	{"&apos;", "'"},
	{"&nbsp;", " "},
	// LAST, and the ordering is the whole reason this is a table scanned in
	// one left-to-right pass rather than a sequence of strings.ReplaceAll
	// calls. Replacing "&amp;" first would turn "&amp;copy;" -- a literal
	// "&copy;" that somebody escaped -- into "&copy;" and then into "©",
	// decoding the same text twice.
	{"&amp;", "&"},
}

// decodeEntities replaces the references above, in a single pass so that no
// replacement's output is fed back into the table.
func decodeEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		matched := false
		for _, e := range creditEntities {
			if strings.HasPrefix(s[i:], e.ref) {
				b.WriteString(e.text)
				i += len(e.ref)
				matched = true
				break
			}
		}
		if !matched {
			// An entity this does not know stays as it was written. It is
			// visible, which is the point: a credit with "&hellip;" in it is
			// wrong in a way somebody can report, where a silently deleted
			// one is wrong in a way nobody sees.
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// collapseSpace reduces every run of whitespace to one space and trims the
// ends.
//
// Markup is written with newlines and indentation that meant nothing on a web
// page and would be drawn as a wide gap in the middle of the credit, since
// the panel lays the string out as a single line.
func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
