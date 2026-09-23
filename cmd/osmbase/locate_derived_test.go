package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/boundary/osm"
	"github.com/wisborg/osmbase/osmbasetest"
)

// derivedStore builds a store holding one derived boundary file and nothing
// else -- what "osmbase boundaries --osm" leaves behind when no map has been
// fetched.
func derivedStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	e := osmbasetest.NewExtract().
		Node(1, -33.70, 151.05).Node(2, -33.70, 151.15).
		Node(3, -33.60, 151.15).Node(4, -33.60, 151.05).
		Node(5, -33.68, 151.07).Node(6, -33.68, 151.09).
		Node(7, -33.66, 151.09).Node(8, -33.66, 151.07).
		Way(10, []int64{1, 2, 3}).Way(11, []int64{3, 4, 1}).
		Way(20, []int64{5, 6, 7}).Way(21, []int64{7, 8, 5})
	// A council area with a suburb inside it, which is the shape the ranking
	// rule exists for.
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"}, {Type: "way", ID: 11, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "6", "name", "Hornsby Shire")
	e.Relation(101, []osmbasetest.ExtractMember{
		{Type: "way", ID: 20, Role: "outer"}, {Type: "way", ID: 21, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "9", "name", "Hornsby")

	src := filepath.Join(dir, "sydney.osm.pbf")
	if err := os.WriteFile(src, e.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "store")
	if _, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--yes"); err != nil {
		t.Fatalf("building the boundaries: %v\n%s", err, stderr)
	}
	return store
}

func locateSaying(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = runLocate(args, &out, &errb)
	return out.String(), err
}

// The end of the feature: a coordinate in, an administrative name out, by
// containment rather than by nearest label.
func TestLocateAnswersFromADerivedFile(t *testing.T) {
	store := derivedStore(t)

	// Inside the suburb, which is inside the council area.
	out, err := locateSaying(t, "--store", store, "--lat", "-33.67", "--lon", "151.08",
		"--levels", "locality,macrohood,neighbourhood")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(out, "Hornsby Shire") || !strings.Contains(out, "Hornsby") {
		t.Errorf("neither name came back:\n%s", out)
	}
	// Contained, not near: that is the whole point of the file.
	if strings.Contains(out, "near") {
		t.Errorf("an answer from a boundary file said near:\n%s", out)
	}

	// And the credit is OpenStreetMap's, not the constant this used to
	// print. A contained answer used to mean Natural Earth, which is public
	// domain and owes nothing.
	if !strings.Contains(out, "OpenStreetMap") {
		t.Errorf("the OpenStreetMap credit is missing from an answer taken from its data:\n%s", out)
	}
	if strings.Contains(out, "Natural Earth") {
		t.Errorf("Natural Earth is credited for an answer it did not give:\n%s", out)
	}
}

// The ranking: the outermost administrative area containing a point is its
// locality and the innermost is its neighbourhood.
func TestTheOuterAreaIsTheLocalityAndTheInnerTheNeighbourhood(t *testing.T) {
	store := derivedStore(t)
	out, err := locateSaying(t, "--store", store, "--lat", "-33.67", "--lon", "151.08",
		"--levels", "locality,neighbourhood", "--format", "json")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}

	var report struct {
		ContainedCredit string `json:"contained_credit"`
		Places          []struct {
			Matches []struct {
				Level, Name, Kind, Source string
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("parsing the answer: %v\n%s", err, out)
	}
	if len(report.Places) != 1 {
		t.Fatalf("got %d places", len(report.Places))
	}

	got := map[string]string{}
	kinds := map[string]string{}
	for _, m := range report.Places[0].Matches {
		got[m.Level] = m.Name
		kinds[m.Level] = m.Kind
		if m.Source != "contained" {
			t.Errorf("%s came back as %q, want contained", m.Level, m.Source)
		}
	}
	if got["locality"] != "Hornsby Shire" {
		t.Errorf("locality = %q, want the council area around the suburb", got["locality"])
	}
	if got["neighbourhood"] != "Hornsby" {
		t.Errorf("neighbourhood = %q, want the suburb", got["neighbourhood"])
	}
	// The admin_level travels with the answer, which is how a reader tells
	// what the number meant in this country.
	if kinds["neighbourhood"] != "9" || kinds["locality"] != "6" {
		t.Errorf("kinds = %v, want the admin levels the file holds", kinds)
	}
	if !strings.Contains(report.ContainedCredit, "OpenStreetMap") {
		t.Errorf("contained_credit = %q, want the OpenStreetMap credit", report.ContainedCredit)
	}
}

// A store holding boundaries and no tiles is a legitimate configuration for
// the levels those boundaries cover, and used to be unusable: the command
// opened the tile store before anything else.
func TestAStoreWithNoTilesStillAnswersWhatItCan(t *testing.T) {
	store := derivedStore(t)
	if _, err := os.Stat(filepath.Join(store, "store.json")); err == nil {
		t.Fatal("the fixture has a tile store, so this proves nothing")
	}

	if _, err := locateSaying(t, "--store", store, "--lat", "-33.67", "--lon", "151.08",
		"--levels", "locality"); err != nil {
		t.Fatalf("a boundary-only store could not answer a level it covers: %v", err)
	}

	// And a level it cannot cover says so rather than pretending.
	_, err := locateSaying(t, "--store", store, "--lat", "-33.67", "--lon", "151.08", "--levels", "street")
	if err == nil {
		t.Fatal("a level needing tiles was answered by a store with none")
	}
	if !strings.Contains(err.Error(), "tiles") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

// A point outside every area in the file gets no answer, not the nearest one.
func TestOutsideEveryAreaThereIsNoAnswer(t *testing.T) {
	store := derivedStore(t)
	out, err := locateSaying(t, "--store", store, "--lat", "0", "--lon", "0", "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if strings.Contains(out, "Hornsby") {
		t.Errorf("a point in the Atlantic was placed in Sydney:\n%s", out)
	}
	// Nothing was contained, so nothing is owed.
	if strings.Contains(out, "Contained answers are from") {
		t.Errorf("a credit was claimed for an answer that was not given:\n%s", out)
	}
}

// The file's own provenance is what the credit comes from, so a file built
// from a source requiring none says none.
func TestTheCreditComesFromTheFileNotAConstant(t *testing.T) {
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, _ := boundary.DerivedFile("nowhere")
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	area := boundary.NewArea("Anywhere", "9", []boundary.Polygon{{
		Outer: boundary.Ring{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}, {Lat: 1, Lon: 1}, {Lat: 1, Lon: 0}},
	}})
	// No attribution: a hypothetical source that requires none.
	if err := boundary.WriteDerived(f, boundary.NewSet(boundary.Provenance{Source: "x"}, []boundary.Area{area})); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := locateSaying(t, "--store", root, "--lat", "0.5", "--lon", "0.5", "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(out, "Anywhere") {
		t.Fatalf("the area was not found:\n%s", out)
	}
	if strings.Contains(out, osm.Attribution) {
		t.Errorf("a credit was attached to data that does not ask for one:\n%s", out)
	}
}

// A name or a credit comes out of a file, and a derived file is meant to be
// passed between people. One holding an ANSI escape colours somebody's
// terminal; one holding a newline forges a line that looks like another
// answer. Neither is a licence problem and both are the program lying on
// behalf of a file it read.
func TestAPlantedNameCannotForgeOutput(t *testing.T) {
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, _ := boundary.DerivedFile("planted")
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	area := boundary.NewArea(
		"\x1b[1;31mRED\x1b[0m\n  country        contained Atlantis (0)", "9",
		[]boundary.Polygon{{Outer: boundary.Ring{
			{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}, {Lat: 1, Lon: 1}, {Lat: 1, Lon: 0}}}})
	prov := boundary.Provenance{Source: "x", Attribution: "\x1b[5mCREDIT\x1b[0m"}
	if err := boundary.WriteDerived(f, boundary.NewSet(prov, []boundary.Area{area})); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := locateSaying(t, "--store", root, "--lat", "0.5", "--lon", "0.5", "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	// The newline is gone, so the planted text stays inside the name it
	// belongs to instead of becoming a line of its own. That is the whole
	// point: garbage in a name is honest, and a second answer at a level
	// nobody asked for is not.
	var answers int
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  locality ") || strings.HasPrefix(line, "  country ") {
			answers++
		}
	}
	if answers != 1 {
		t.Errorf("the output holds %d answer lines, want 1:\n%q", answers, out)
	}
	if strings.Contains(out, "\n  country") {
		t.Errorf("a name forged a line of output:\n%q", out)
	}
}

// JSON needs none of that -- encoding/json escapes control characters -- so
// the filter must not be doing the escaping twice or mangling real names.
func TestAnOrdinaryNameSurvivesIntact(t *testing.T) {
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, _ := boundary.DerivedFile("nordic")
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	// Non-ASCII letters, an apostrophe and a hyphen are ordinary in place
	// names and must come through untouched.
	const want = "Nørre Snede-Øst (L'Île)"
	area := boundary.NewArea(want, "9", []boundary.Polygon{{Outer: boundary.Ring{
		{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}, {Lat: 1, Lon: 1}, {Lat: 1, Lon: 0}}}})
	if err := boundary.WriteDerived(f, boundary.NewSet(boundary.Provenance{Source: "x"}, []boundary.Area{area})); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := locateSaying(t, "--store", root, "--lat", "0.5", "--lon", "0.5", "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(out, want) {
		t.Errorf("the name did not survive:\n%s", out)
	}
}
