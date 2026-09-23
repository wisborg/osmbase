package boundary

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// neSquare is a Natural Earth style GeoJSON document holding one square.
//
// Written here rather than reused from boundary_test.go because that file is
// the external test package and cannot reach the unexported helpers these
// tests share with derivedsource_test.go.
func neSquare(name string, lat, lon, side float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	c := func(la, lo float64) string { return "[" + f(lo) + "," + f(la) + "]" }
	return `{"type":"FeatureCollection","features":[{"properties":{"NAME":"` + name +
		`"},"geometry":{"type":"Polygon","coordinates":[[` +
		c(lat, lon) + `,` + c(lat, lon+side) + `,` + c(lat+side, lon+side) + `,` +
		c(lat+side, lon) + `,` + c(lat, lon) + `]]}}]}`
}

// writeNaturalEarth puts the three Natural Earth files in a store. An empty
// body writes no file, which is how a half-finished download is spelled.
func writeNaturalEarth(t *testing.T, root, detail, countries, regions, waters string) {
	t.Helper()
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		layer Layer
		body  string
	}{{Countries, countries}, {Regions, regions}, {Waters, waters}} {
		if f.body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, File(detail, f.layer)), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTheMergedStackIsRankedBySizeAndNotByFileOrder pins the SECOND sort.
//
// Source.Contains merges the containment stacks of every derived file in the
// store and sorts the result again. Set.Containing has already sorted each
// file's own stack, so with one file -- or with files whose areas do not
// overlap, which is what every other test here builds -- the second sort
// changes nothing and can be deleted with the whole suite green. Measured:
// removing it passes boundary and cmd/osmbase in full.
//
// The discriminating case is two files that DO both contain the point, with
// the small area in the file that is read first. ReadDir returns names in
// order, so "osm_aaa.osmb" is read before "osm_bbb.osmb"; putting the 1x1
// square in aaa and the 6x6 square in bbb means file order and size order
// disagree. Without the merged sort the stack arrives innermost first and the
// ranking inverts: the 1x1 suburb becomes the locality and the 6x6 council
// area becomes the neighbourhood.
func TestTheMergedStackIsRankedBySizeAndNotByFileOrder(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "aaa", osmProv(),
		NewArea("Inner", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	writeDerived(t, root, "bbb", osmProv(),
		NewArea("Outer", "6", []Polygon{{Outer: square(-2, -2, 6)}}))
	src := Open(root, DefaultDetail)

	// (0.5, 0.5) is inside the 1x1 square at (0,0) and inside the 6x6 square
	// at (-2,-2), so both files answer.
	for _, tc := range []struct {
		level locate.Level
		want  string
	}{
		{locate.Locality, "Outer"},
		{locate.Neighbourhood, "Inner"},
	} {
		got, _, _, ok := src.Contains(tc.level, 0.5, 0.5)
		if !ok || got != tc.want {
			t.Errorf("%s = (%q, %v), want %q: the merged stack is in file order, not size order",
				tc.level, got, ok, tc.want)
		}
	}
}

// TestADeepHierarchyReportsTheTwoEndsAndTheStepAboveTheInnermost fixes which
// area the macrohood is.
//
// rankDerived takes areas[len-2] -- the one just OUTSIDE the innermost. At
// three areas that index is also areas[1], the one just inside the outermost,
// so the three-area case in derivedsource_test.go cannot tell the two rules
// apart. Measured: changing the code to areas[1] passes the whole suite.
//
// Four and five areas separate them. The expected values are read off the
// rule rather than off the code: outermost is the locality, innermost the
// neighbourhood, and the macrohood is the one immediately containing the
// neighbourhood -- L3 of four, L4 of five. The areas in between belong to no
// level, and that is asserted too, because a rule that reported everything it
// found would pass the three positive checks alone.
// A deep file reports the THREE INNERMOST areas and nothing wider.
//
// This used to rank the whole stack, so the outermost area became the
// locality -- and a file built the way the command builds one by default
// keeps every admin_level, which in Denmark makes the outermost containing
// area the country. Locality came back as "Danmark", duplicating the answer
// Natural Earth had already given, while the region and the kommune appeared
// at no level at all.
func TestADeepHierarchyReportsTheThreeInnermost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sides []float64
		want  map[locate.Level]string
	}{
		{
			name:  "four deep",
			sides: []float64{10, 8, 6, 4},
			want: map[locate.Level]string{
				locate.Locality: "L2", locate.Macrohood: "L3", locate.Neighbourhood: "L4",
			},
		},
		{
			name:  "five deep",
			sides: []float64{12, 10, 8, 6, 4},
			want: map[locate.Level]string{
				locate.Locality: "L3", locate.Macrohood: "L4", locate.Neighbourhood: "L5",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Concentric squares about (0.5, 0.5): side n is anchored so that
			// every one of them holds the point and each contains the next.
			var areas []Area
			for i, side := range tc.sides {
				areas = append(areas, NewArea("L"+strconv.Itoa(i+1), strconv.Itoa(4+2*i),
					[]Polygon{{Outer: square(0.5-side/2, 0.5-side/2, side)}}))
			}
			root := t.TempDir()
			writeDerived(t, root, "r", osmProv(), areas...)
			src := Open(root, DefaultDetail)

			named := map[string]bool{}
			for _, level := range []locate.Level{locate.Locality, locate.Macrohood, locate.Neighbourhood} {
				got, _, _, ok := src.Contains(level, 0.5, 0.5)
				if !ok {
					t.Errorf("%s was not answered, want %q", level, tc.want[level])
					continue
				}
				named[got] = true
				if got != tc.want[level] {
					t.Errorf("%s = %q, want %q", level, got, tc.want[level])
				}
			}
			// Everything wider than the three innermost is reported at no
			// level at all. Three levels cannot hold five areas, and which
			// are dropped is the decision this pins: the wide ones, because
			// they are the region and the country and something else
			// answers those.
			for i := range tc.sides {
				name := "L" + strconv.Itoa(i+1)
				want := tc.want[locate.Locality] == name ||
					tc.want[locate.Macrohood] == name ||
					tc.want[locate.Neighbourhood] == name
				if named[name] != want {
					t.Errorf("%s reported=%v, want %v", name, named[name], want)
				}
			}
		})
	}
}

// TestEveryDerivedFilesCreditIsOwedAndEachIsNamedOnce covers a store built
// from more than one extract.
//
// Every test that reaches CreditFor uses one attribution, or two files
// sharing one, so a source that kept only the first credit it saw passes.
// Measured: it does. Two extracts with different licences is not exotic --
// "osmbase boundaries --osm" is per region, and a user with Denmark and
// Australia has run it twice -- and dropping one of two credits is a licence
// failure rather than a cosmetic one.
//
// Each file's answer carries that file's own credit.
//
// This used to ask the SOURCE what a level owed, which joined every
// attribution in the store -- so an answer from one file was credited to
// another, and a file claiming no attribution had a neighbour's credit
// attached to its answers. The third file below repeats the first's
// attribution, which is what makes the per-answer version discriminating:
// the union version cannot tell those two apart at all.
func TestEachFilesAnswerCarriesThatFilesOwnCredit(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "aaa", Provenance{Source: "a.osm.pbf", Attribution: "Credit A"},
		NewArea("A", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	writeDerived(t, root, "bbb", Provenance{Source: "b.osm.pbf", Attribution: "Credit B"},
		NewArea("B", "9", []Polygon{{Outer: square(10, 10, 1)}}))
	writeDerived(t, root, "ccc", Provenance{Source: "c.osm.pbf", Attribution: ""},
		NewArea("C", "9", []Polygon{{Outer: square(20, 20, 1)}}))

	src := Open(root, DefaultDetail)
	for _, tc := range []struct {
		lat, lon     float64
		name, credit string
	}{
		{0.5, 0.5, "A", "Credit A"},
		{10.5, 10.5, "B", "Credit B"},
		// The file that asks for nothing gets nothing, even standing beside
		// two files that do.
		{20.5, 20.5, "C", ""},
	} {
		name, _, credit, ok := src.Contains(locate.Locality, tc.lat, tc.lon)
		if !ok || name != tc.name {
			t.Errorf("at %v,%v got %q (%v), want %q", tc.lat, tc.lon, name, ok, tc.name)
			continue
		}
		if credit != tc.credit {
			t.Errorf("%q is credited to %q, want %q", name, credit, tc.credit)
		}
	}
}

// TestNaturalEarthAndDerivedFilesEachAnswerTheirOwnLevels covers the complete
// store, which is what a user who has run both boundary commands has.
//
// The tests written with the change use one kind of file or the other, so a
// source that let the derived files answer country -- or that let the
// presence of Natural Earth files suppress the derived ones -- passes them
// all. The names are deliberately different per level so an answer from the
// wrong file is visible rather than coincidental, and the credits are
// asserted per level because that is the whole point of CreditFor: the two
// obligations are different and the command has to be able to tell them
// apart.
func TestNaturalEarthAndDerivedFilesEachAnswerTheirOwnLevels(t *testing.T) {
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail,
		neSquare("Atlantis", 0, 0, 4),
		neSquare("North Province", 0, 0, 3),
		neSquare("Bay of Nowhere", 0, 0, 2))
	writeDerived(t, root, "atlantis", osmProv(),
		NewArea("Council Area", "6", []Polygon{{Outer: square(0, 0, 1)}}),
		NewArea("Suburb", "9", []Polygon{{Outer: square(0, 0, 0.5)}}))

	src := Open(root, DefaultDetail)
	for _, tc := range []struct {
		level  locate.Level
		want   string
		credit string
	}{
		{locate.Country, "Atlantis", NaturalEarthCredit},
		{locate.Region, "North Province", NaturalEarthCredit},
		{locate.Water, "Bay of Nowhere", NaturalEarthCredit},
		{locate.Locality, "Council Area", osmProv().Attribution},
		{locate.Neighbourhood, "Suburb", osmProv().Attribution},
	} {
		if !src.Covers(tc.level) {
			t.Errorf("%s is not covered by a store holding both kinds of file", tc.level)
			continue
		}
		got, _, _, ok := src.Contains(tc.level, 0.25, 0.25)
		if !ok || got != tc.want {
			t.Errorf("%s = (%q, %v), want %q", tc.level, got, ok, tc.want)
		}
		if _, _, c, _ := src.Contains(tc.level, 0.25, 0.25); c != tc.credit {
			t.Errorf("%s is credited to %q, want %q", tc.level, c, tc.credit)
		}
	}
	// Two areas is a locality and a neighbourhood and nothing between them.
	if got, _, _, ok := src.Contains(locate.Macrohood, 0.25, 0.25); ok {
		t.Errorf("macrohood = %q, want nothing: the file holds two nested areas", got)
	}
}

// TestADerivedSourceIsSafeToShareBetweenGoroutines covers the lazy load this
// change added.
//
// The existing concurrency test goes through Contains on a Natural Earth
// level, which reaches Source.set. Nothing exercises Source.derived, which is
// a second lazy load with a second done-flag, reached from three exported
// methods -- Covers, Contains and CreditFor -- two of which take the mutex
// twice in one call. Source is documented as safe to share, and locate.AtEach
// asks Covers once per level and Contains once per point.
//
// Only meaningful under -race, which "make race" runs; without it this checks
// that nothing deadlocks and that the answers survive the traffic.
func TestADerivedSourceIsSafeToShareBetweenGoroutines(t *testing.T) {
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail, neSquare("Atlantis", 0, 0, 4), "", "")
	writeDerived(t, root, "atlantis", osmProv(),
		NewArea("Council Area", "6", []Polygon{{Outer: square(0, 0, 1)}}),
		NewArea("Suburb", "9", []Polygon{{Outer: square(0, 0, 0.5)}}))
	src := Open(root, DefaultDetail)

	levels := []locate.Level{locate.Country, locate.Locality, locate.Neighbourhood, locate.Street}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := levels[i%len(levels)]
			for range 20 {
				src.Covers(l)
				src.Contains(l, 0.25, 0.25)
			}
		}(i)
	}
	wg.Wait()

	// And it still answers correctly afterwards, so a lock that serialised
	// the load into nonsense -- or a done-flag set before the work -- shows
	// up here rather than as a flake.
	if got, _, _, ok := src.Contains(locate.Neighbourhood, 0.25, 0.25); !ok || got != "Suburb" {
		t.Errorf("after concurrent use, neighbourhood = (%q, %v), want Suburb", got, ok)
	}
	if _, _, got, _ := src.Contains(locate.Locality, 0.25, 0.25); got != osmProv().Attribution {
		t.Errorf("after concurrent use, the derived credit = %q, want %q", got, osmProv().Attribution)
	}
}

// TestAnEmptyDerivedFileLeavesTheLevelsToTheTiles is the third state of a
// derived file, between "no file" and "a file with areas in it".
//
// "osmbase boundaries --osm" writes a file with zero areas and says so:
// "Boundaries were found, but none of them closed into an outline, so the
// file holds nothing to test a point against." That file is then in the
// store, and Covers counts FILES rather than areas -- so it claims locality,
// macrohood and neighbourhood, and locate.AtEach consults containment
// "exclusively" and never asks the tiles. The user loses "near Hornsby,
// 300 m" and gets nothing at all, which is precisely the outcome the comment
// above Covers says the file-present check exists to prevent.
//
// Paired with TestLevelsAreClaimedWhenAFileIsThere, which asserts the
// opposite for a file that does hold an area: a source that claimed nothing
// would pass this test alone.
func TestAnEmptyDerivedFileLeavesTheLevelsToTheTiles(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "empty", osmProv())

	src := Open(root, DefaultDetail)
	for _, level := range []locate.Level{locate.Locality, locate.Macrohood, locate.Neighbourhood} {
		if src.Covers(level) {
			t.Errorf("%s is claimed on the strength of a file holding no areas; the level is taken from the tiles and then answered with nothing", level)
		}
	}
	// And nothing is owed for an answer that cannot be given.
	if _, _, c, ok := src.Contains(locate.Locality, 0.25, 0.25); ok || c != "" {
		t.Errorf("a file holding no areas answered %q with credit %q", locate.Locality, c)
	}
}

// TestALevelIsNotClaimedWhenItsNaturalEarthFileIsAbsent covers the invariant
// this change broke.
//
// Covers returns true for country, region and water unconditionally, which
// was safe while Available required all three Natural Earth files: a source
// only ever existed when those files did. Available now also returns true for
// a store holding a derived file and no Natural Earth outlines, which is what
// "osmbase boundaries --osm" leaves behind -- and a user who has ALSO run
// "osmbase fetch" has tiles that can answer country by nearest label.
//
// For that user the source now claims country, region and water, AtEach
// consults containment exclusively, Contains finds no file and returns false,
// and three levels that used to be answered are silently not. It is the same
// failure the comment above Covers describes for the levels below region,
// arriving from the other side.
//
// Paired with TestNaturalEarthAndDerivedFilesEachAnswerTheirOwnLevels, which
// asserts these three levels ARE covered when the files are present.
func TestALevelIsNotClaimedWhenItsNaturalEarthFileIsAbsent(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "r", osmProv(), NewArea("A", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	if !Available(root, DefaultDetail) {
		t.Fatal("a store holding only a derived file reports no boundaries; the rest of this test assumes it does")
	}

	src := Open(root, DefaultDetail)
	for _, level := range []locate.Level{locate.Country, locate.Region, locate.Water} {
		if src.Covers(level) {
			t.Errorf("%s is claimed although the store holds no %s file; the level is taken from the tiles and then answered with nothing",
				level, File(DefaultDetail, mustLayerFor(level)))
		}
	}
}

func mustLayerFor(l locate.Level) Layer {
	layer, ok := layerFor(l)
	if !ok {
		panic("no layer for " + l.String())
	}
	return layer
}
