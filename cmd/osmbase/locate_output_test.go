package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	osmlocate "github.com/wisborg/osmbase/locate"
)

// plantedStore is a store holding one derived file with one area, named and
// credited as the caller says, over the square (0,0)-(1,1).
func plantedStore(t *testing.T, name, kind, credit string) string {
	t.Helper()
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, _ := boundary.DerivedFile("planted")
	f, err := os.Create(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	area := boundary.NewArea(name, kind, []boundary.Polygon{{Outer: boundary.Ring{
		{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}, {Lat: 1, Lon: 1}, {Lat: 1, Lon: 0}}}})
	if err := boundary.WriteDerived(f, boundary.NewSet(boundary.Provenance{Source: "x", Attribution: credit}, []boundary.Area{area})); err != nil {
		t.Fatal(err)
	}
	return root
}

// The kind is printed beside the name and comes out of the same file, so it
// is filtered for the same reason. TestAPlantedNameCannotForgeOutput plants
// only a name, and the kind went to the terminal as it was written.
func TestAPlantedKindCannotForgeOutput(t *testing.T) {
	root := plantedStore(t, "Honest", "9\x1b[2J\n  country        contained Atlantis (0)", "")
	out, err := locateSaying(t, "--store", root, "--lat", "0.5", "--lon", "0.5", "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence in a kind reached the terminal:\n%q", out)
	}
	if strings.Contains(out, "\n  country") {
		t.Errorf("a kind forged a line of output:\n%q", out)
	}
}

// Markup in a credit is cleaned in the JSON's per-answer field as well as in
// its summary, so the one document does not say the same thing two ways.
func TestAnAnswersCreditIsPlainInTheJSON(t *testing.T) {
	root := plantedStore(t, "A", "9", `<a href="https://example.org">© Somebody</a>`)
	out, err := locateSaying(t, "--store", root, "--lat", "0.5", "--lon", "0.5",
		"--levels", "locality", "--format", "json")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	var report struct {
		ContainedCredit string `json:"contained_credit"`
		Places          []struct {
			Matches []struct {
				Attribution string `json:"attribution"`
			} `json:"matches"`
		} `json:"places"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("parsing: %v\n%s", err, out)
	}
	if len(report.Places) != 1 || len(report.Places[0].Matches) != 1 {
		t.Fatalf("got %s", out)
	}
	if got := report.Places[0].Matches[0].Attribution; got != "© Somebody" {
		t.Errorf("the answer's attribution = %q, want the plain credit", got)
	}
	if report.ContainedCredit != "© Somebody" {
		t.Errorf("contained_credit = %q, want the plain credit", report.ContainedCredit)
	}
}

// containedCredits is the line that discharges an obligation, so each of its
// three rules is pinned on its own: only contained answers are counted, each
// credit is named once, and the order does not depend on which point came
// first.
func TestContainedCreditsNamesEachContainedCreditOnceInOrder(t *testing.T) {
	m := func(src osmlocate.Source, credit string) osmlocate.Match {
		return osmlocate.Match{Level: osmlocate.Locality, Name: "x", Source: src, Attribution: credit}
	}
	places := []osmlocate.Place{
		{Matches: []osmlocate.Match{m(osmlocate.Contained, "Zeta"), m(osmlocate.Near, "Tiles")}},
		{Matches: []osmlocate.Match{m(osmlocate.Contained, "Alpha"), m(osmlocate.Contained, "Zeta")}},
		{Matches: []osmlocate.Match{m(osmlocate.Contained, "")}},
	}
	got := containedCredits(places)
	if want := []string{"Alpha", "Zeta"}; !slices.Equal(got, want) {
		t.Errorf("containedCredits = %q, want %q", got, want)
	}
}

// What safeForTerminal removes and what it keeps. The line and paragraph
// separators are not control characters to unicode.IsControl and end a line
// in some terminals and most editors a pasted answer lands in; a tab is
// turned into a space rather than dropped, so two words it separated stay
// two words.
func TestSafeForTerminal(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Nørre Snede-Øst", "Nørre Snede-Øst"},
		{"a\x1b[31mb", "a[31mb"},
		{"a\nb\rc", "abc"},
		{"a b c", "abc"},
		{"a\tb", "a b"},
	} {
		if got := safeForTerminal(tc.in); got != tc.want {
			t.Errorf("safeForTerminal(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The tile archive's credit is owed for names taken from the tiles, and the
// archive is now opened whenever the store has one -- as a fallback for
// points the boundaries hold no data for. A lookup the boundaries answered
// entirely took nothing from it.
func TestAnyNearIsTrueOnlyForAnAnswerFromTheTiles(t *testing.T) {
	contained := osmlocate.Place{Matches: []osmlocate.Match{{Source: osmlocate.Contained}}}
	near := osmlocate.Place{Matches: []osmlocate.Match{{Source: osmlocate.Near}}}
	if anyNear([]osmlocate.Place{contained, {}}) {
		t.Error("contained answers and an empty place were counted as a tile answer")
	}
	if !anyNear([]osmlocate.Place{contained, near}) {
		t.Error("a tile answer was not counted")
	}
}
