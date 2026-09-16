package render

import "testing"

// TestPlainCredit_HTMLAttributionBecomesTheTextAViewerWouldHaveRead is the
// legal obligation rather than a formatting nicety, which is why the wanted
// strings are written out in full: what this function returns is burnt into
// every frame of a rendered video as the ODbL credit for the data it was
// drawn from, and a credit nobody can read discharges nothing.
//
// The first case is the string Protomaps' own archives carry, spelled out
// here because it is the input this exists for -- it is metadata from a
// public archive, not anybody's recording.
func TestPlainCredit_HTMLAttributionBecomesTheTextAViewerWouldHaveRead(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
	}{
		{
			name: "an anchor around the whole credit",
			in:   `<a href="https://www.openstreetmap.org/copyright" target="_blank">&copy; OpenStreetMap</a>`,
			want: "© OpenStreetMap",
		},
		{
			name: "text outside the anchor as well as inside it",
			in:   `Map data from <a href="https://openstreetmap.org">OpenStreetMap</a>, ODbL`,
			want: "Map data from OpenStreetMap, ODbL",
		},
		{
			name: "two anchors keep both names and the word between them",
			in:   `<a href="https://protomaps.com">Protomaps</a> &amp; <a href="https://openstreetmap.org">OpenStreetMap</a>`,
			want: "Protomaps & OpenStreetMap",
		},
		{
			name: "markup laid out over several lines is one line of text",
			in:   "<a\n  href=\"https://openstreetmap.org\"\n  target=\"_blank\">\n  &copy; OpenStreetMap contributors\n</a>",
			want: "© OpenStreetMap contributors",
		},
		{
			name: "the entities that occur are decoded",
			in:   `&copy; A &amp; B &lt;x&gt; &quot;q&quot; &#39;s&#39;`,
			want: `© A & B <x> "q" 's'`,
		},
		{
			// An escaped entity must be decoded ONCE. Replacing &amp; before
			// the rest of the table would turn this into "©", which is a
			// different credit from the one the archive wrote.
			name: "an escaped entity is decoded once, not twice",
			in:   `&amp;copy; Someone`,
			want: "&copy; Someone",
		},
		{
			name: "plain text is returned unchanged",
			in:   "© OpenStreetMap contributors",
			want: "© OpenStreetMap contributors",
		},
		{
			// Nothing about the panel that draws this can follow a link, so
			// the URL would be read as part of the credit; the anchor's text
			// is what a viewer of the web map would have seen.
			name: "a bare tag with no text of its own leaves no residue",
			in:   `Tiles <a href="https://example.test/x?a=1&amp;b=2">here</a><br/>`,
			want: "Tiles here",
		},
		{
			// A break separates words where an anchor joins them: what a
			// browser would have shown is the standard, so the comma above
			// keeps its place and these two lines do not run together.
			name: "a line break is a word boundary",
			in:   "Map data<br>OpenStreetMap",
			want: "Map data OpenStreetMap",
		},
		{
			// A '<' that opens nothing is content, and swallowing the rest of
			// the string would lose the part that names the source.
			name: "an unterminated angle bracket keeps the text after it",
			in:   "Contains data < 2024 from Somebody",
			want: "Contains data < 2024 from Somebody",
		},
		{
			// The decision this function had to make: stripping left nothing,
			// and an empty credit for data that requires attribution is worse
			// than an ugly one. See plainCredit.
			name: "markup with no text at all falls back to the raw string",
			in:   `<img src="https://example.test/logo.png">`,
			want: `<img src="https://example.test/logo.png">`,
		},
		{
			name: "an entity this does not know stays visible rather than vanishing",
			in:   "A &hellip; B",
			want: "A &hellip; B",
		},
		{
			name: "nothing in, nothing out",
			in:   "   \n\t ",
			want: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := PlainCredit(c.in); got != c.want {
				t.Errorf("PlainCredit(%q)\n = %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}
