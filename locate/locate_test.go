package locate_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/locate"
	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// place is one named point to put in a fixture tile.
type place struct {
	lat, lon float64
	kind     string
	name     string
	detail   string
	names    map[string]string
}

// tiles is a TileSource built from whatever the test asks for, so a test can
// say "there is a town here" without owning a store.
type tiles map[[3]uint32][]byte

func (t tiles) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	b, ok := t[[3]uint32{uint32(z), x, y}]
	return b, ok, nil
}

// withPlaces builds a source holding one tile per zoom that the given places
// need, with each place at its true coordinate inside it.
func withPlaces(t *testing.T, zoom uint8, ps ...place) tiles {
	t.Helper()
	byTile := map[[3]uint32][]osmbasetest.FeatureSpec{}
	for _, p := range ps {
		x, y, err := mercator.TileAt(zoom, p.lon, p.lat)
		if err != nil {
			t.Fatalf("TileAt(%v, %v): %v", p.lat, p.lon, err)
		}
		tr, err := mercator.NewTileTransform(zoom, x, y, mvt.DefaultExtent)
		if err != nil {
			t.Fatalf("NewTileTransform: %v", err)
		}
		tx, ty := tileXY(t, tr, p.lon, p.lat)
		tags := []osmbasetest.Tag{
			{Key: "name", Value: mvt.StringValue(p.name)},
			{Key: "kind", Value: mvt.StringValue(p.kind)},
		}
		if p.detail != "" {
			tags = append(tags, osmbasetest.Tag{Key: "kind_detail", Value: mvt.StringValue(p.detail)})
		}
		for lang, n := range p.names {
			tags = append(tags, osmbasetest.Tag{Key: "name:" + lang, Value: mvt.StringValue(n)})
		}
		key := [3]uint32{uint32(zoom), x, y}
		byTile[key] = append(byTile[key], osmbasetest.FeatureSpec{
			Type: mvt.GeomPoint, Tags: tags,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: tx, Y: ty}}},
		})
	}

	out := tiles{}
	for key, feats := range byTile {
		data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{
			Layers: []osmbasetest.LayerSpec{{Name: "places", Features: feats}},
		})
		if err != nil {
			t.Fatalf("BuildTile: %v", err)
		}
		out[key] = data
	}
	return out
}

// tileXY inverts the tile transform by bisection, so a fixture can place a
// feature at a real coordinate without this test reimplementing the
// projection it is checking against.
func tileXY(t *testing.T, tr mercator.TileTransform, lon, lat float64) (int32, int32) {
	t.Helper()
	find := func(get func(int32) float64, want float64, rising bool) int32 {
		lo, hi := int32(-mvt.DefaultExtent*8), int32(mvt.DefaultExtent*8)
		for range 40 {
			mid := lo + (hi-lo)/2
			if (get(mid) < want) == rising {
				lo = mid
			} else {
				hi = mid
			}
			if hi-lo <= 1 {
				break
			}
		}
		return lo
	}
	x := find(func(v int32) float64 { l, _ := tr.LonLat(v, 0); return l }, lon, true)
	y := find(func(v int32) float64 { _, l := tr.LonLat(x, v); return l }, lat, false)
	return x, y
}

// TestAt_NamesTheNearestPlaceAndSaysHowFar is the whole claim this package
// makes, and the distance is half of it.
//
// These tiles carry no named areas, so the answer is the nearest named POINT
// and not the one containing the coordinate. A result without its distance
// would be indistinguishable from containment, which is the failure the design
// exists to avoid: "Horsens" and "near Horsens, 40 km" are different
// statements and only one of them is what the data supports.
func TestAt_NamesTheNearestPlaceAndSaysHowFar(t *testing.T) {
	const lat, lon = 55.8623, 9.8451
	src := withPlaces(t, 10,
		place{lat: lat + 0.002, lon: lon, kind: "locality", name: "Horsens", detail: "city"},
		place{lat: lat + 0.5, lon: lon + 0.5, kind: "locality", name: "Vejle", detail: "town"},
	)

	got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{})
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	m, ok := got.Match(locate.Locality)
	if !ok {
		t.Fatal("no locality was found for a point beside one")
	}
	if m.Name != "Horsens" {
		t.Errorf("Name = %q, want the nearer Horsens rather than Vejle", m.Name)
	}
	if m.Source != locate.Near {
		t.Errorf("Source = %v, want Near: nothing in these tiles can establish containment", m.Source)
	}
	if m.DistanceM <= 0 || m.DistanceM > 400 {
		t.Errorf("DistanceM = %v, want roughly 220 m: the fixture put the town two thousandths of a degree north", m.DistanceM)
	}
	if m.Kind != "city" {
		t.Errorf("Kind = %q, want the schema's own kind_detail", m.Kind)
	}
}

// TestAt_WithholdsAnAnswerBeyondTheCap is the constraint that stops this
// shipping a lie.
//
// Without it, a point in the outback is "near Alice Springs" from two hundred
// kilometres away: true, useless, and indistinguishable in the output from a
// match forty metres off. Past the cap there is no answer, which is a thing a
// caller can act on.
func TestAt_WithholdsAnAnswerBeyondTheCap(t *testing.T) {
	const lat, lon = 55.0, 9.0
	// About 5.5 km north: inside the 25 km default and outside the 1 km below.
	src := withPlaces(t, 10, place{lat: lat + 0.05, lon: lon, kind: "locality", name: "Far Away"})

	at := locate.Coord{Lat: lat, Lon: lon}
	generous, err := locate.At(context.Background(), src, at, locate.Options{})
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	if _, ok := generous.Match(locate.Locality); !ok {
		t.Fatal("precondition: the default cap did not reach the fixture, so the tightened cap below proves nothing")
	}

	tight, err := locate.At(context.Background(), src, at, locate.Options{
		MaxDistanceM: map[locate.Level]float64{locate.Locality: 1000},
	})
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	if m, ok := tight.Match(locate.Locality); ok {
		t.Errorf("a locality %v m away was reported under a 1000 m cap: %+v", m.DistanceM, m)
	}
}

// TestAt_PrefersTheAskedForLanguageAndFallsBack covers the names the schema
// carries beside the local one.
//
// Falling back rather than declining is the point of the second half: a
// consumer asking for Danish would rather be told "Sydney" than nothing, and
// the local spelling is a correct answer to "what is this place called".
func TestAt_PrefersTheAskedForLanguageAndFallsBack(t *testing.T) {
	const lat, lon = 48.85, 2.35
	src := withPlaces(t, 10,
		place{
			lat: lat, lon: lon, kind: "locality", name: "Paris",
			names: map[string]string{"ja": "パリ"},
		},
	)

	for _, c := range []struct {
		lang, want string
	}{
		{"ja", "パリ"},
		{"da", "Paris"}, // no name:da in the fixture -- falls back
		{"", "Paris"},
	} {
		got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon},
			locate.Options{Language: c.lang})
		if err != nil {
			t.Fatalf("At(%q): %v", c.lang, err)
		}
		m, ok := got.Match(locate.Locality)
		if !ok {
			t.Fatalf("At(%q) found no locality", c.lang)
		}
		if m.Name != c.want {
			t.Errorf("At(%q) = %q, want %q", c.lang, m.Name, c.want)
		}
	}
}

// TestAtEach_AnswersEveryPointIncludingTheOnesWithNothingNear pins the shape
// of the batch result.
//
// Parallel to the input, including for coordinates nothing was found for. A
// caller walking a route relies on the indices lining up with its own points;
// returning only the answers would silently shift every subsequent reading
// onto the wrong coordinate.
func TestAtEach_AnswersEveryPointIncludingTheOnesWithNothingNear(t *testing.T) {
	src := withPlaces(t, 10, place{lat: 55.0, lon: 9.0, kind: "locality", name: "Somewhere"})

	pts := []locate.Coord{
		{Lat: 55.0, Lon: 9.0},    // on the town
		{Lat: -33.9, Lon: 151.2}, // a hemisphere away, no tile at all
		{Lat: 55.001, Lon: 9.001},
	}
	got, err := locate.AtEach(context.Background(), src, pts, locate.Options{})
	if err != nil {
		t.Fatalf("AtEach: %v", err)
	}
	if len(got) != len(pts) {
		t.Fatalf("got %d places for %d points", len(got), len(pts))
	}
	for i, p := range got {
		if p.Lat != pts[i].Lat || p.Lon != pts[i].Lon {
			t.Errorf("place %d is for %v,%v, want %v,%v", i, p.Lat, p.Lon, pts[i].Lat, pts[i].Lon)
		}
	}
	if _, ok := got[1].Match(locate.Locality); ok {
		t.Error("a point with no tile beneath it was given a locality")
	}
	if len(got[1].Matches) != 0 {
		t.Errorf("a point with no data has %d matches, want none", len(got[1].Matches))
	}
}

// TestPlace_MarshalsLevelsAndSourcesAsNames keeps the JSON readable and stable.
//
// The constants are ordered widest-first so a caller can compare them, which
// means inserting a level -- a district between region and locality, say --
// renumbers every value below it. A file written today would then say
// something different when read tomorrow. Names do not move.
func TestPlace_MarshalsLevelsAndSourcesAsNames(t *testing.T) {
	b, err := json.Marshal(locate.Place{
		Lat: 1, Lon: 2,
		Matches: []locate.Match{{
			Level: locate.Neighbourhood, Name: "Bækkelund",
			Source: locate.Near, DistanceM: 1300,
		}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(b)
	for _, want := range []string{`"level":"neighbourhood"`, `"source":"near"`, `"name":"Bækkelund"`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON is missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"level":4`) || strings.Contains(out, `"source":0`) {
		t.Errorf("JSON carries a bare enum number, which renumbers the day a level is inserted:\n%s", out)
	}
}

// TestMatch_DistanceIsRoundedToWhatTheMeasurementSupports stops the output
// claiming a precision nothing here has.
//
// The distance is to a LABEL ANCHOR a cartographer placed somewhere near the
// middle of a town. Eleven decimal places on that is noise dressed as data,
// and it is the kind of thing a reader takes for rigour.
func TestMatch_DistanceIsRoundedToWhatTheMeasurementSupports(t *testing.T) {
	const lat, lon = 55.8623, 9.8451
	src := withPlaces(t, 10, place{lat: lat + 0.01234567, lon: lon + 0.00765432, kind: "locality", name: "Somewhere"})

	got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{})
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	m, ok := got.Match(locate.Locality)
	if !ok {
		t.Fatal("no locality found")
	}
	if got := m.DistanceM * 10; got != math.Trunc(got) {
		t.Errorf("DistanceM = %v, which is finer than a tenth of a metre", m.DistanceM)
	}
}

// road is a named line to put in a fixture tile, given as real coordinates.
type road struct {
	name string
	kind string
	// from and to are the ends, in degrees.
	fromLat, fromLon, toLat, toLon float64
}

// withRoad builds a source holding the tile the road's MIDPOINT falls in, and
// only that tile.
//
// Only that one, deliberately. A test for the neighbour-reading path has to be
// able to put a feature somewhere the query point's own tile is not, and a
// helper that scattered the geometry across every tile it touched would make
// that impossible to express.
func withRoad(t *testing.T, zoom uint8, r road) (tiles, uint32, uint32) {
	t.Helper()
	midLat, midLon := (r.fromLat+r.toLat)/2, (r.fromLon+r.toLon)/2
	x, y, err := mercator.TileAt(zoom, midLon, midLat)
	if err != nil {
		t.Fatalf("TileAt: %v", err)
	}
	tr, err := mercator.NewTileTransform(zoom, x, y, mvt.DefaultExtent)
	if err != nil {
		t.Fatalf("NewTileTransform: %v", err)
	}
	ax, ay := tileXY(t, tr, r.fromLon, r.fromLat)
	bx, by := tileXY(t, tr, r.toLon, r.toLat)

	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "roads",
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomLineString,
			Tags: []osmbasetest.Tag{
				{Key: "name", Value: mvt.StringValue(r.name)},
				{Key: "kind", Value: mvt.StringValue(r.kind)},
			},
			Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{X: ax, Y: ay}, {X: bx, Y: by}}}},
		}},
	}}})
	if err != nil {
		t.Fatalf("BuildTile: %v", err)
	}
	return tiles{{uint32(zoom), x, y}: data}, x, y
}

// TestAt_FindsAStreetInTheNeighbouringTile is the one the rest of the suite
// could not see, and the distances in it are chosen rather than convenient.
//
// Reading only the tile containing the point is the obvious implementation and
// it is wrong at the edges: at zoom 14 a tile is well under two kilometres
// across at this latitude, so a wide band of every tile is within the 250 m
// street cap of a boundary. Vector tiles carry a buffer of their neighbours'
// features, which hides this in production most of the time and therefore
// makes it worse -- the failure shows up only for features outside the buffer,
// which is exactly the ones nothing else will find.
//
// # Why both axes, and why 180 m
//
// The threshold is a fraction of a tile, so a query point a few metres from an
// edge is inside it however badly the tile's ground size is computed. The bug
// this test was written after got the NORTH-SOUTH size wrong by a factor of
// about 1.8 at Danish latitudes -- Web Mercator is conformal, so a tile's
// ground height shrinks with the cosine of the latitude exactly as its width
// does, which the code did not do. A point 180 m outside the edge falls
// between the correct threshold and the broken one: found with the right
// arithmetic, missed with the wrong. Twenty-five metres would pass either way
// and prove only that neighbours are read at all.
//
// Both axes, because the bug was on one of them and a future reader splitting
// them apart again should hear about it.
func TestAt_FindsAStreetInTheNeighbouringTile(t *testing.T) {
	const zoom = 14
	const mPerLat = 111_320
	mPerLon := mPerLat * math.Cos(55.8623*math.Pi/180)

	x, y, err := mercator.TileAt(zoom, 9.8451, 55.8623)
	if err != nil {
		t.Fatalf("TileAt: %v", err)
	}
	west, south, east, north, err := mercator.TileBounds(zoom, x, y)
	if err != nil {
		t.Fatalf("TileBounds: %v", err)
	}
	midLat, midLon := (south+north)/2, (west+east)/2

	// 180 m outside the edge for the query and 30 m inside it for the road:
	// 210 m apart, within the 250 m street cap, and outside the threshold a
	// wrongly sized tile computes.
	const outM, inM = 180.0, 30.0

	for _, c := range []struct {
		name       string
		axis       string
		qLat, qLon float64
		rLat, rLon float64
	}{
		{
			name: "across the western edge", axis: "x",
			qLat: midLat, qLon: west - outM/mPerLon,
			rLat: midLat, rLon: west + inM/mPerLon,
		},
		{
			name: "across the southern edge", axis: "y",
			qLat: south - outM/mPerLat, qLon: midLon,
			rLat: south + inM/mPerLat, rLon: midLon,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, rx, ry := withRoad(t, zoom, road{
				name: "Hestedamsgade", kind: "minor_road",
				fromLat: c.rLat - 0.0008, fromLon: c.rLon - 0.0008,
				toLat: c.rLat + 0.0008, toLon: c.rLon + 0.0008,
			})
			if rx != x || ry != y {
				t.Fatalf("precondition: the road landed in tile %d/%d, not the %d/%d this test is about", rx, ry, x, y)
			}

			at := locate.Coord{Lat: c.qLat, Lon: c.qLon}
			qx, qy, err := mercator.TileAt(zoom, at.Lon, at.Lat)
			if err != nil {
				t.Fatalf("TileAt: %v", err)
			}
			if qx == x && qy == y {
				t.Fatal("precondition: the query point is in the road's own tile, so this proves nothing about neighbours")
			}

			got, err := locate.At(context.Background(), src, at, locate.Options{})
			if err != nil {
				t.Fatalf("At: %v", err)
			}
			m, ok := got.Match(locate.Street)
			if !ok {
				t.Fatalf("the street one tile away was not found: the neighbour on the %s axis is not being read, so a road just across a boundary is invisible", c.axis)
			}
			if m.Name != "Hestedamsgade" {
				t.Errorf("Name = %q, want Hestedamsgade", m.Name)
			}
			if m.DistanceM > 250 {
				t.Errorf("DistanceM = %v, past the street cap: the fixture is not measuring what it means to", m.DistanceM)
			}
		})
	}
}

// TestAt_KeepsTheNEARESTCandidateWhicheverOrderTheDataArrivesIn is the
// assertion the original suite could not make.
//
// Every earlier fixture put its candidates in different, far-apart tiles, so
// nearest() was only ever handed a one-feature list -- and a list of one cannot
// tell "closest" from "first" from "farthest". Mutation testing confirmed it:
// keeping the first match, and keeping the farthest, both passed the whole
// suite.
//
// Two candidates in ONE tile, at different distances, in both fixture orders.
// The order matters as much as the distance: with only one order, "keep the
// first" passes by luck half the time, which is how the original Horsens and
// Vejle test passed while proving nothing.
func TestAt_KeepsTheNearestCandidateWhicheverOrderTheDataArrivesIn(t *testing.T) {
	const lat, lon = 55.8623, 9.8451
	near := place{lat: lat + 0.002, lon: lon, kind: "locality", name: "Near", detail: "town"}
	far := place{lat: lat + 0.05, lon: lon, kind: "locality", name: "Far", detail: "town"}

	for _, c := range []struct {
		name string
		ps   []place
	}{
		{"nearest listed first", []place{near, far}},
		{"nearest listed last", []place{far, near}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := withPlaces(t, 10, c.ps...)
			got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{})
			if err != nil {
				t.Fatalf("At: %v", err)
			}
			m, ok := got.Match(locate.Locality)
			if !ok {
				t.Fatal("no locality found")
			}
			if m.Name != "Near" {
				t.Errorf("Name = %q, want Near: the closer of two candidates in the same tile", m.Name)
			}
		})
	}
}

// TestAt_ALevelAnswersOnlyFromItsOwnKind stops one level's features answering
// another's question.
//
// kindMatches is what keeps a region label out of a locality lookup, and
// mutation testing found it deletable -- replacing it with "true" passed every
// test, because no fixture had ever put two kinds in one tile. The wrong-kind
// feature here is deliberately the CLOSER of the two, so a version that
// ignores kind picks it and fails.
func TestAt_ALevelAnswersOnlyFromItsOwnKind(t *testing.T) {
	const lat, lon = 55.8623, 9.8451
	src := withPlaces(t, 10,
		place{lat: lat + 0.001, lon: lon, kind: "region", name: "Midtjylland", detail: "state"},
		place{lat: lat + 0.02, lon: lon, kind: "locality", name: "Horsens", detail: "city"},
	)

	got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon},
		locate.Options{Levels: []locate.Level{locate.Locality}})
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	m, ok := got.Match(locate.Locality)
	if !ok {
		t.Fatal("no locality found; the region should have been skipped, not the locality")
	}
	if m.Name != "Horsens" {
		t.Errorf("a locality lookup answered %q, which is a region: kind is not being filtered", m.Name)
	}
}

// TestAt_SkipsAFeatureWithNoUsableName covers the two guards that decide
// whether a feature has a name at all.
//
// Both were deletable with the suite green. They matter for the same reason:
// a feature that wins a nearest-match contest and then has nothing to say
// produces a place called "" or called "42", and either is worse than the real
// name a little further away.
//
// The assertion is paired -- the farther, properly named feature must WIN, not
// merely "no match". A test that only checked for absence would also pass if
// the whole layer were being dropped.
func TestAt_SkipsAFeatureWithNoUsableName(t *testing.T) {
	const lat, lon = 55.8623, 9.8451

	t.Run("an empty name", func(t *testing.T) {
		src := withPlaces(t, 10,
			place{lat: lat + 0.001, lon: lon, kind: "locality", name: ""},
			place{lat: lat + 0.02, lon: lon, kind: "locality", name: "Horsens", detail: "city"},
		)
		got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{})
		if err != nil {
			t.Fatalf("At: %v", err)
		}
		m, ok := got.Match(locate.Locality)
		if !ok {
			t.Fatal("no locality found; the named one further away should have answered")
		}
		if m.Name != "Horsens" {
			t.Errorf("Name = %q, want Horsens: a feature named \"\" won on distance", m.Name)
		}
	})

	t.Run("a name that is not a string", func(t *testing.T) {
		numbered := osmbasetest.FeatureSpec{
			Type: mvt.GeomPoint,
			Tags: []osmbasetest.Tag{
				{Key: "name", Value: mvt.SintValue(42)},
				{Key: "kind", Value: mvt.StringValue("locality")},
			},
		}
		src := placesWithRaw(t, 10, lat, lon, numbered,
			place{lat: lat + 0.02, lon: lon, kind: "locality", name: "Horsens", detail: "city"})

		got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{})
		if err != nil {
			t.Fatalf("At: %v", err)
		}
		m, ok := got.Match(locate.Locality)
		if !ok {
			t.Fatal("no locality found")
		}
		if m.Name != "Horsens" {
			t.Errorf("Name = %q, want Horsens: a numeric name was stringified into a label", m.Name)
		}
	})
}

// TestDefaultMaxDistanceM_HoldsTheDocumentedNumbers pins the caps themselves.
//
// The cap MECHANISM was tested through an explicit override, which left the
// shipped defaults decorative: mutation testing collapsed five of the six to
// one metre with the suite still green. The numbers are the product decision --
// how far "near" may stretch at each level -- and they widen with the level
// because the features do.
func TestDefaultMaxDistanceM_HoldsTheDocumentedNumbers(t *testing.T) {
	want := map[locate.Level]float64{
		locate.Country:       1_000_000,
		locate.Region:        500_000,
		locate.Locality:      25_000,
		locate.Macrohood:     5_000,
		locate.Neighbourhood: 3_000,
		locate.Street:        250,
	}
	if len(locate.DefaultMaxDistanceM) != len(want) {
		t.Errorf("there are %d default caps and %d levels: a level with no cap falls back to zero and can never answer",
			len(locate.DefaultMaxDistanceM), len(want))
	}
	for level, w := range want {
		if got := locate.DefaultMaxDistanceM[level]; got != w {
			t.Errorf("%s cap is %v, want %v", level, got, w)
		}
	}
	// And the ordering is the property behind the numbers: a country label may
	// be far away and a street may not.
	for _, pair := range [][2]locate.Level{
		{locate.Country, locate.Region},
		{locate.Region, locate.Locality},
		{locate.Locality, locate.Macrohood},
		{locate.Macrohood, locate.Neighbourhood},
		{locate.Neighbourhood, locate.Street},
	} {
		if locate.DefaultMaxDistanceM[pair[0]] <= locate.DefaultMaxDistanceM[pair[1]] {
			t.Errorf("the %s cap is not wider than the %s cap", pair[0], pair[1])
		}
	}
}

// placesWithRaw builds a source holding one tile with ordinary places plus a
// feature spelled out by hand, for the cases the place struct cannot express --
// a tag of the wrong TYPE, which is a thing the schema should never contain
// and which this package has to survive anyway.
//
// The raw feature is placed at the query coordinate, so it is the nearest
// candidate and a version that accepted it would demonstrably win.
func placesWithRaw(t *testing.T, zoom uint8, lat, lon float64, raw osmbasetest.FeatureSpec, ps ...place) tiles {
	t.Helper()
	x, y, err := mercator.TileAt(zoom, lon, lat)
	if err != nil {
		t.Fatalf("TileAt: %v", err)
	}
	tr, err := mercator.NewTileTransform(zoom, x, y, mvt.DefaultExtent)
	if err != nil {
		t.Fatalf("NewTileTransform: %v", err)
	}
	rx, ry := tileXY(t, tr, lon, lat)
	raw.Geometry = mvt.Geometry{Points: []mvt.Point{{X: rx, Y: ry}}}

	feats := []osmbasetest.FeatureSpec{raw}
	for _, p := range ps {
		px, py := tileXY(t, tr, p.lon, p.lat)
		tags := []osmbasetest.Tag{
			{Key: "name", Value: mvt.StringValue(p.name)},
			{Key: "kind", Value: mvt.StringValue(p.kind)},
		}
		if p.detail != "" {
			tags = append(tags, osmbasetest.Tag{Key: "kind_detail", Value: mvt.StringValue(p.detail)})
		}
		feats = append(feats, osmbasetest.FeatureSpec{
			Type: mvt.GeomPoint, Tags: tags,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: px, Y: py}}},
		})
	}
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{
		Layers: []osmbasetest.LayerSpec{{Name: "places", Features: feats}},
	})
	if err != nil {
		t.Fatalf("BuildTile: %v", err)
	}
	return tiles{{uint32(zoom), x, y}: data}
}

// fakeBoundaries answers containment for the levels it is told to, so the
// precedence rule can be tested without a boundary file.
type fakeBoundaries struct {
	covers map[locate.Level]bool
	name   string
	inside bool
	credit string
}

func (f fakeBoundaries) Covers(l locate.Level) bool { return f.covers[l] }

func (f fakeBoundaries) Contains(l locate.Level, lat, lon float64) (string, string, string, bool) {
	if !f.covers[l] || !f.inside {
		return "", "", "", false
	}
	return f.name, "country", f.credit, true
}

// TestAt_ContainmentAnswersALevelInsteadOfTheTilesAndNotAsWell is the
// precedence rule, and the second half is the one that matters.
//
// A source that covers a level answers it EXCLUSIVELY: the tiles are not
// consulted, and a level the source covers but finds nothing for reports
// nothing. Falling back to the nearest label would turn a correct answer into
// a guess -- a point in the North Sea is outside every country, and "near
// Denmark, 40 km" would be the program inventing a country for it.
func TestAt_ContainmentAnswersALevelInsteadOfTheTilesAndNotAsWell(t *testing.T) {
	const lat, lon = 55.8623, 9.8451
	// A country point in the tiles, so there IS a nearest answer to fall back
	// to if anything wrongly did.
	src := withPlaces(t, 4, place{lat: lat + 0.3, lon: lon, kind: "country", name: "Tiles Say Denmark"})

	t.Run("containment wins where it answers", func(t *testing.T) {
		got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{
			Boundaries: fakeBoundaries{
				covers: map[locate.Level]bool{locate.Country: true},
				name:   "Denmark", inside: true,
			},
		})
		if err != nil {
			t.Fatalf("At: %v", err)
		}
		m, ok := got.Match(locate.Country)
		if !ok {
			t.Fatal("no country found")
		}
		if m.Name != "Denmark" {
			t.Errorf("Name = %q, want the contained Denmark rather than the tiles' nearest label", m.Name)
		}
		if m.Source != locate.Contained {
			t.Errorf("Source = %v, want Contained", m.Source)
		}
		if m.DistanceM != 0 {
			t.Errorf("DistanceM = %v, want 0: a contained match is not at a distance", m.DistanceM)
		}
	})

	t.Run("no fallback when containment finds nothing", func(t *testing.T) {
		got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{
			Boundaries: fakeBoundaries{
				covers: map[locate.Level]bool{locate.Country: true},
				inside: false,
			},
		})
		if err != nil {
			t.Fatalf("At: %v", err)
		}
		if m, ok := got.Match(locate.Country); ok {
			t.Errorf("a point outside every country was given %q from the tiles; containment answering nothing must mean nothing, not a nearest guess", m.Name)
		}
	})

	t.Run("a level the source does not cover is left to the tiles", func(t *testing.T) {
		got, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, locate.Options{
			Boundaries: fakeBoundaries{covers: map[locate.Level]bool{locate.Region: true}},
		})
		if err != nil {
			t.Fatalf("At: %v", err)
		}
		m, ok := got.Match(locate.Country)
		if !ok {
			t.Fatal("a level the source declined was not answered from the tiles either")
		}
		if m.Source != locate.Near {
			t.Errorf("Source = %v, want Near for a tile answer", m.Source)
		}
	})
}
