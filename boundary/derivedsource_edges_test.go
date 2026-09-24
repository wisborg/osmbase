package boundary

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// A derived file covers one extract, and outside it the file says nothing.
//
// Covers is asked once per level for a whole route, so a store holding a
// Sydney file covers locality everywhere on Earth. Before Contains could say
// NoData, a point in Horsens was answered "outside every area" -- and since a
// covered level is answered by containment alone, the tiles' "near Horsens"
// was replaced with nothing. The same happened to every point in a Sydney
// suburb the extract's bounding box had cut open.
func TestAPlaceNoDerivedFileKnowsIsNoData(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "sydney", osmProv(),
		NewArea("Hornsby Shire", "6", []Polygon{{Outer: square(-34, 151, 1)}}),
		NewArea("Hornsby", "9", []Polygon{{Outer: square(-33.8, 151.2, 0.2)}}))
	src := Open(root, DefaultDetail)

	for _, tc := range []struct {
		name     string
		lat, lon float64
		level    locate.Level
		want     locate.Containment
	}{
		// Horsens: no area in the file holds it, so the file knows nothing.
		{"a point in another country", 55.86, 9.85, locate.Locality, locate.NoData},
		{"a point in another country", 55.86, 9.85, locate.Neighbourhood, locate.NoData},
		// Inside both areas: the file knows the place, and the level the
		// ranking leaves empty is a statement, not a gap for the tiles to
		// fill with a nearest label.
		{"a point two areas deep", -33.7, 151.3, locate.Locality, locate.Inside},
		{"a point two areas deep", -33.7, 151.3, locate.Macrohood, locate.Outside},
		{"a point two areas deep", -33.7, 151.3, locate.Neighbourhood, locate.Inside},
		// Inside the council area only: one area is a locality and nothing
		// else, and the rest is Outside for the same reason.
		{"a point one area deep", -33.1, 151.9, locate.Locality, locate.Inside},
		{"a point one area deep", -33.1, 151.9, locate.Neighbourhood, locate.Outside},
	} {
		if _, _, _, got := src.Contains(tc.level, tc.lat, tc.lon); got != tc.want {
			t.Errorf("%s: %s = %v, want %v", tc.name, tc.level, got, tc.want)
		}
	}
}

// Natural Earth covers the world, so a point in no country is at sea -- an
// answer, which the tiles must not second-guess with "near Denmark, 40 km".
// Paired with the test above: a source that said NoData for every miss would
// pass that one alone.
func TestAPointInNoCountryIsOutsideAndNotNoData(t *testing.T) {
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail, neSquare("Atlantis", 0, 0, 4), "", "")
	src := Open(root, DefaultDetail)

	if _, _, _, got := src.Contains(locate.Country, 20, 20); got != locate.Outside {
		t.Errorf("a point in no country is %v, want Outside", got)
	}
	if _, _, _, got := src.Contains(locate.Country, 1, 1); got != locate.Inside {
		t.Errorf("a point in Atlantis is %v, want Inside", got)
	}
}

// Two extracts that overlap -- a country and a province cut from it -- hold
// the same boundaries. Counting one twice spent two of the three levels on
// it: the kommune came back as both macrohood and neighbourhood.
func TestTheSameAreaInTwoFilesIsCountedOnce(t *testing.T) {
	root := t.TempDir()
	areas := []Area{
		NewArea("Region", "4", []Polygon{{Outer: square(0, 0, 8)}}),
		NewArea("Kommune", "7", []Polygon{{Outer: square(1, 1, 4)}}),
	}
	writeDerived(t, root, "denmark", osmProv(), areas...)
	writeDerived(t, root, "jutland", osmProv(), areas...)
	src := Open(root, DefaultDetail)

	for _, tc := range []struct {
		level locate.Level
		want  string
		c     locate.Containment
	}{
		{locate.Locality, "Region", locate.Inside},
		{locate.Macrohood, "", locate.Outside},
		{locate.Neighbourhood, "Kommune", locate.Inside},
	} {
		name, _, _, c := src.Contains(tc.level, 2, 2)
		if c != tc.c || name != tc.want {
			t.Errorf("%s = (%q, %v), want (%q, %v)", tc.level, name, c, tc.want, tc.c)
		}
	}
}

// Two DIFFERENT areas that happen to share a name are not merged: the rule
// is one boundary read twice, not one name seen twice. Each case differs from
// the other area in exactly one of the two things besides the name that the
// rule compares, so a rule that dropped either comparison fails one of them.
func TestTwoAreasSharingANameAreBothKept(t *testing.T) {
	for _, tc := range []struct {
		name         string
		outer, inner Area
		at           Coord
	}{
		{
			// A suburb that is the whole of the council area named after
			// it: same name, same outline, different admin_level.
			"a different level",
			NewArea("Hornsby", "6", []Polygon{{Outer: square(0, 0, 1)}}),
			NewArea("Hornsby", "9", []Polygon{{Outer: square(0, 0, 1)}}),
			Coord{Lat: 0.5, Lon: 0.5},
		},
		{
			"a different extent",
			NewArea("Springfield", "9", []Polygon{{Outer: square(0, 0, 4)}}),
			NewArea("Springfield", "9", []Polygon{{Outer: square(1, 1, 1)}}),
			Coord{Lat: 1.5, Lon: 1.5},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeDerived(t, root, "r", osmProv(), tc.outer, tc.inner)
			src := Open(root, DefaultDetail)
			for _, level := range []locate.Level{locate.Locality, locate.Neighbourhood} {
				if _, _, _, c := src.Contains(level, tc.at.Lat, tc.at.Lon); c != locate.Inside {
					t.Errorf("%s = %v, want Inside: two areas were merged into one", level, c)
				}
			}
		})
	}
}

// A suburb that is the whole of its council area has the council area's
// outline, and the file's order used to decide which of the two was the
// locality. admin_level decides it now, lower being wider -- OpenStreetMap's
// convention everywhere -- and both file orders are tried, because either
// one alone passes a comparator that ignores the level.
func TestEqualExtentsAreOrderedByAdminLevelAndNotByTheFile(t *testing.T) {
	council := NewArea("Council", "6", []Polygon{{Outer: square(0, 0, 1)}})
	suburb := NewArea("Suburb", "9", []Polygon{{Outer: square(0, 0, 1)}})
	for _, order := range [][]Area{{council, suburb}, {suburb, council}} {
		root := t.TempDir()
		writeDerived(t, root, "r", osmProv(), order...)
		src := Open(root, DefaultDetail)

		loc, _, _, _ := inside(src, locate.Locality, 0.5, 0.5)
		nb, _, _, _ := inside(src, locate.Neighbourhood, 0.5, 0.5)
		if loc != "Council" || nb != "Suburb" {
			t.Errorf("file order %s, %s: locality %q, neighbourhood %q; want Council, Suburb",
				order[0].Name, order[1].Name, loc, nb)
		}
	}
}

// A non-numeric kind leaves a tie where the file put it, rather than
// comparing strings as though they were levels.
func TestANonNumericKindLeavesTheTieToTheFile(t *testing.T) {
	a := NewArea("First", "state", []Polygon{{Outer: square(0, 0, 1)}})
	b := NewArea("Second", "9", []Polygon{{Outer: square(0, 0, 1)}})
	if c := outermostFirst(a, b); c != 0 {
		t.Errorf("outermostFirst = %d, want 0 for a kind that is not a level", c)
	}
}

// A child in two parts whose rectangles overlap summed to more than the
// rectangle of the council area around it, and came back as the locality with
// its own council as the neighbourhood. The rectangle around the union of the
// parts cannot exceed the parent's, because every part is inside it.
func TestAChildInSeveralPartsDoesNotOutrankItsParent(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "r", osmProv(),
		NewArea("Parent", "6", []Polygon{{Outer: square(0, 0, 1)}}),
		NewArea("Child", "9", []Polygon{
			{Outer: square(0.05, 0.05, 0.8)},
			{Outer: square(0.15, 0.15, 0.8)},
		}))
	src := Open(root, DefaultDetail)

	loc, _, _, _ := inside(src, locate.Locality, 0.5, 0.5)
	nb, _, _, _ := inside(src, locate.Neighbourhood, 0.5, 0.5)
	if loc != "Parent" || nb != "Child" {
		t.Errorf("locality %q, neighbourhood %q; want Parent, Child", loc, nb)
	}
}

// The credit is carried beside each area through the merged sort, so it has
// to be reordered with it. Every other credit test has one area per stack,
// where nothing moves; here the file read first holds the INNER area, so the
// sort reverses them and a credit left in file order lands on the other name.
func TestACreditMovesWithItsAreaThroughTheMergedSort(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "aaa", Provenance{Source: "a", Attribution: "Credit Inner"},
		NewArea("Inner", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	writeDerived(t, root, "bbb", Provenance{Source: "b", Attribution: "Credit Outer"},
		NewArea("Outer", "6", []Polygon{{Outer: square(-2, -2, 6)}}))
	src := Open(root, DefaultDetail)

	for _, tc := range []struct {
		level        locate.Level
		name, credit string
	}{
		{locate.Locality, "Outer", "Credit Outer"},
		{locate.Neighbourhood, "Inner", "Credit Inner"},
	} {
		name, _, credit, ok := inside(src, tc.level, 0.5, 0.5)
		if !ok || name != tc.name || credit != tc.credit {
			t.Errorf("%s = (%q, credit %q), want (%q, credit %q)", tc.level, name, credit, tc.name, tc.credit)
		}
	}
}

// A Natural Earth file holding no features claims its level and then answers
// nothing, which takes the level from the tiles -- the same hazard Covers
// already refuses for a derived file holding no areas.
func TestAnEmptyNaturalEarthFileDoesNotClaimItsLevel(t *testing.T) {
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail, `{"type":"FeatureCollection","features":[]}`, "", "")
	src := Open(root, DefaultDetail)
	if src.Covers(locate.Country) {
		t.Error("country is claimed on the strength of a file holding no countries")
	}
}

// The writer refuses an outline with no points, not only a polygon with no
// rings: both give the empty box, and the reader refusing it later is a file
// already written that nobody can use.
func TestAnOutlineWithNoPointsIsNotWritten(t *testing.T) {
	a := NewArea("n", "9", []Polygon{{Outer: Ring{}}})
	if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, []Area{a})); err == nil {
		t.Error("an area whose outline has no points was written")
	}
}

// The store shares one budget between its files, and this measures what it
// HOLDS rather than counting what it read -- the rule this module keeps
// relearning is that a test asserting a count passes on either side of the
// fix.
//
// The limits are shrunk to two files' worth so the allocation is affordable
// to measure. Eight files are offered; a store that resets its budget per
// file keeps all eight, and one that shares it keeps two. The measure is the
// heap retained against one file read alone, which is what a directory is
// now meant to cost at most, in each of the two currencies a file spends.
func TestAStoreHoldsNoMoreThanItsSharedBudget(t *testing.T) {
	const files = 8

	t.Run("geometry", func(t *testing.T) {
		// One area of many one-point polygons: cheap on disk, eighty bytes
		// each in memory, and exactly the shape the geometry budget exists
		// for. A cap on AREAS sees four areas here and is untroubled.
		const polys = 1 << 14
		mk := func(i int) Area {
			ps := make([]Polygon, polys)
			for j := range ps {
				ps[j] = Polygon{Outer: Ring{{Lat: float64(i), Lon: float64(j) / polys}}}
			}
			return NewArea("A", "9", ps)
		}
		root := t.TempDir()
		for i := range files {
			writeDerived(t, root, regionName(i), Provenance{}, mk(i))
		}
		lim := defaultDerivedLimits()
		lim.geometry = budget{polygons: 2 * polys, rings: 2 * polys}
		assertRetainedAtMost(t, root, lim, 2)
	})

	t.Run("bytes", func(t *testing.T) {
		// One ring of many points per file. Points have no budget of their
		// own -- the file's length is theirs -- so this is the byte limit.
		const points = 1 << 16
		mk := func(i int) Area {
			r := make(Ring, points)
			for j := range r {
				r[j] = Coord{Lat: float64(i) + float64(j%256)/256, Lon: float64(j) / points}
			}
			return NewArea("A", "9", []Polygon{{Outer: r}})
		}
		root := t.TempDir()
		for i := range files {
			writeDerived(t, root, regionName(i), Provenance{}, mk(i))
		}
		fi, err := os.Stat(filepath.Join(Dir(root), "osm_"+regionName(0)+DerivedExt))
		if err != nil {
			t.Fatal(err)
		}
		lim := defaultDerivedLimits()
		lim.bytes = 2*fi.Size() + fi.Size()/2
		assertRetainedAtMost(t, root, lim, 2)
	})
}

// assertRetainedAtMost loads a store under limits and checks it holds about
// as much as `want` of its files would -- measured in heap, against one file
// read alone -- and at least one of them.
func assertRetainedAtMost(t *testing.T, root string, lim derivedLimits, want int) {
	t.Helper()
	var one int64
	{
		f, err := os.Open(filepath.Join(Dir(root), "osm_"+regionName(0)+DerivedExt))
		if err != nil {
			t.Fatal(err)
		}
		one = retained(func() any {
			set, err := ReadDerived(f)
			if err != nil {
				t.Fatal(err)
			}
			return set
		})
		f.Close()
	}

	src := Open(root, DefaultDetail)
	src.limits = lim
	var sets []*Set
	all := retained(func() any { sets = src.derived(); return sets })

	if len(sets) == 0 {
		t.Fatal("the store kept nothing at all")
	}
	ratio := float64(all) / float64(one)
	t.Logf("kept %d files, %.1f times one file's heap", len(sets), ratio)
	if ratio > float64(want)+0.5 {
		t.Errorf("the store holds %.1f files' worth of heap, want at most %d: the budget is not shared", ratio, want)
	}
}

// retained is the heap a value built by f still occupies.
func retained(f func() any) int64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	held := f()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(held)
	return int64(after.HeapAlloc) - int64(before.HeapAlloc)
}

// The country is not a candidate for locality, macrohood or neighbourhood.
//
// Ranking only the three innermost areas was meant to keep it out, and does
// when a point sits four or more deep. But a file built at every level in
// Denmark holds country (2), region (4) and kommune (7) for most of the land,
// and there the three innermost ARE those three: measured against the real
// extract, Horsens came back as locality "Danmark". admin_level 2 is the
// national border in every country's tagging, and the country level is
// answered by Natural Earth, whose outline follows the coast where OSM's runs
// out to the territorial-waters limit.
func TestTheCountryIsNotRankedBelowARegion(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "denmark", osmProv(),
		NewArea("Danmark", "2", []Polygon{{Outer: square(0, 0, 8)}}),
		NewArea("Region Midtjylland", "4", []Polygon{{Outer: square(1, 1, 4)}}),
		NewArea("Horsens Kommune", "7", []Polygon{{Outer: square(2, 2, 1)}}))
	src := Open(root, DefaultDetail)

	for _, tc := range []struct {
		level locate.Level
		want  string
		c     locate.Containment
	}{
		{locate.Locality, "Region Midtjylland", locate.Inside},
		{locate.Macrohood, "", locate.Outside},
		{locate.Neighbourhood, "Horsens Kommune", locate.Inside},
	} {
		name, _, _, c := src.Contains(tc.level, 2.5, 2.5)
		if c != tc.c || name != tc.want {
			t.Errorf("%s = (%q, %v), want (%q, %v)", tc.level, name, c, tc.want, tc.c)
		}
	}

	// Inside the country only -- the territorial-waters strip OSM's national
	// border includes -- no file knows anything below the country, so the
	// tiles answer rather than the country being called a locality.
	if _, _, _, c := src.Contains(locate.Locality, 7.5, 7.5); c != locate.NoData {
		t.Errorf("a point inside only the country is %v, want NoData", c)
	}
}
