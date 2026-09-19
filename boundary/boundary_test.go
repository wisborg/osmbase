package boundary_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/locate"
)

// squareArea is a GeoJSON document with one square, optionally holed.
func squareArea(name string, west, south, east, north float64, hole bool) string {
	ring := func(w, s, e, n float64) string {
		return `[[` +
			coord(w, s) + `,` + coord(e, s) + `,` + coord(e, n) + `,` + coord(w, n) + `,` + coord(w, s) +
			`]]`
	}
	rings := ring(west, south, east, north)
	if hole {
		// A hole covering the middle ninth of the square.
		dx, dy := (east-west)/3, (north-south)/3
		rings = rings[:len(rings)-1] + `,` +
			strings.TrimPrefix(ring(west+dx, south+dy, east-dx, north-dy), `[`)
	}
	return `{"type":"FeatureCollection","features":[{"properties":{"NAME":"` + name +
		`"},"geometry":{"type":"Polygon","coordinates":` + rings + `}}]}`
}

func coord(lon, lat float64) string {
	return `[` + strconv.FormatFloat(lon, 'f', -1, 64) + `,` + strconv.FormatFloat(lat, 'f', -1, 64) + `]`
}

// TestSet_ContainsAPointInsideAndRejectsOneOutside is the whole claim: this
// answers containment, which the tiles cannot.
func TestSet_ContainsAPointInsideAndRejectsOneOutside(t *testing.T) {
	set, err := boundary.Read(strings.NewReader(squareArea("Denmark", 8, 54, 13, 58, false)), "country")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("Len = %d, want 1", set.Len())
	}

	if a, ok := set.At(56, 10); !ok || a.Name != "Denmark" {
		t.Errorf("a point inside gave (%q, %v), want Denmark, true", a.Name, ok)
	}
	// Outside the box entirely: the cheap rejection, and the one that runs
	// for almost every area on almost every lookup.
	if _, ok := set.At(56, 30); ok {
		t.Error("a point well outside the square was reported inside")
	}
	// Inside the bounding box on one axis only, which is the case a box test
	// alone gets wrong and only the ring test catches.
	if _, ok := set.At(60, 10); ok {
		t.Error("a point north of the square was reported inside")
	}
}

// TestSet_APointInAHoleIsOutside covers the ring order GeoJSON uses.
//
// The first ring is the outline and the rest are holes, so a point inside the
// outline AND inside a hole is outside the area. An implementation that tested
// only the first ring would answer every enclave as though it were the country
// around it -- which is precisely the kind of wrong answer that looks right.
func TestSet_APointInAHoleIsOutside(t *testing.T) {
	set, err := boundary.Read(strings.NewReader(squareArea("Italy", 0, 0, 9, 9, true)), "country")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, ok := set.At(4.5, 4.5); ok {
		t.Error("a point in the hole was reported inside the area")
	}
	if _, ok := set.At(1, 1); !ok {
		t.Error("a point inside the outline but outside the hole was reported outside")
	}
}

// TestRead_RefusesAFileWithNoNamedAreas stops a wrong file reading as an
// empty world.
//
// An empty set answers "no area contains this" for every coordinate on earth,
// which is indistinguishable from a point in the ocean and would be reported
// as a missing level rather than as a broken download.
func TestRead_RefusesAFileWithNoNamedAreas(t *testing.T) {
	_, err := boundary.Read(strings.NewReader(`{"type":"FeatureCollection","features":[]}`), "country")
	if err == nil {
		t.Fatal("a file with no areas was accepted")
	}
	if !strings.Contains(err.Error(), "country") {
		t.Errorf("the error does not say which file was wrong: %v", err)
	}
}

// TestSource_CoversOnlyWhatNaturalEarthHas keeps this from answering a
// question the data cannot.
//
// Natural Earth publishes no suburb outlines. Reporting a locality as
// contained by the state around it would be a true statement answering the
// wrong question, and it would carry the Contained source -- which a consumer
// is told means a statement of fact about THAT level.
func TestSource_CoversOnlyWhatNaturalEarthHas(t *testing.T) {
	s := boundary.Open(t.TempDir(), "")
	for _, c := range []struct {
		level locate.Level
		want  bool
	}{
		{locate.Country, true},
		{locate.Region, true},
		{locate.Locality, false},
		{locate.Macrohood, false},
		{locate.Neighbourhood, false},
		{locate.Street, false},
	} {
		if got := s.Covers(c.level); got != c.want {
			t.Errorf("Covers(%s) = %v, want %v", c.level, got, c.want)
		}
	}
}

// TestSource_AMissingFileIsNoAnswerRatherThanAFailure covers the ordinary
// case: boundaries are an optional download.
//
// A store without them must still answer -- every level falls back to the
// tiles, and the output says "near" instead of "in", which is the difference
// the user can see.
func TestSource_AMissingFileIsNoAnswerRatherThanAFailure(t *testing.T) {
	dir := t.TempDir()
	if boundary.Available(dir, "") {
		t.Fatal("an empty directory reports boundaries available")
	}
	s := boundary.Open(dir, "")
	if _, _, ok := s.Contains(locate.Country, 56, 10); ok {
		t.Error("a store with no boundary files answered a containment question")
	}
}
