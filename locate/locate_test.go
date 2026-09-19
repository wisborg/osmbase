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
