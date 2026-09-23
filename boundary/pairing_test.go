package boundary

import "testing"

// rect returns a closed ring spanning the given latitudes and longitudes,
// counterclockwise.
//
// A rectangle rather than a square, and the asymmetry is the point. Every
// fixture square() builds has its latitude range equal to its longitude
// range and its corner on the diagonal, so it is its own transpose: reading
// the pair the wrong way round gives the same shape back and every
// containment answer is unchanged. This module carries four coordinate types
// between which a transposition compiles, so a fixture that cannot see one is
// a fixture that cannot see the bug the type comments are about.
func rect(south, north, west, east float64) Ring {
	return Ring{
		{Lat: south, Lon: west},
		{Lat: south, Lon: east},
		{Lat: north, Lon: east},
		{Lat: north, Lon: west},
	}
}

// Pairing a hole to an outline is three coordinate comparisons -- the
// bounding box, then the ray cast, against a point taken from the hole -- and
// each reads a latitude and a longitude that must line up with each other.
//
// The two cases are one fixture reflected about the diagonal, and they are
// here together because either alone proves little: an implementation that
// paired everything would pass the first, one that paired nothing would pass
// the second, and one that transposes a coordinate anywhere in the pairing
// swaps the two answers over and fails both.
func TestHolePairingTellsLatitudeFromLongitude(t *testing.T) {
	// Two degrees of latitude by thirty of longitude: nothing about this
	// shape survives being read the other way round.
	outer := rect(0, 2, 10, 40)

	for _, tc := range []struct {
		name    string
		hole    Ring
		paired  int
		orphans int
	}{
		{
			name:   "a hole within the outline's latitudes and longitudes",
			hole:   rect(0.5, 1.5, 15, 25),
			paired: 1,
		},
		{
			// The same rectangle reflected: latitudes 15 to 25 and
			// longitudes 0.5 to 1.5 is nowhere near the outline, and is
			// exactly where transposed code looks for the case above.
			name:    "the same hole with its coordinates transposed",
			hole:    rect(15, 25, 0.5, 1.5),
			orphans: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polys, orphans, _ := Polygons([]Ring{outer}, []Ring{tc.hole})
			if len(polys) != 1 {
				t.Fatalf("got %d polygons, want 1", len(polys))
			}
			if len(polys[0].Holes) != tc.paired {
				t.Errorf("the outline took %d holes, want %d", len(polys[0].Holes), tc.paired)
			}
			if len(orphans) != tc.orphans {
				t.Errorf("got %d orphan holes, want %d", len(orphans), tc.orphans)
			}
		})
	}
}

// A hole with no points has no point to test against an outline, which is
// why Polygons looks before it reads one. Malformed input rather than
// ordinary input -- assembly cannot produce an empty ring -- but Polygons is
// exported and takes the rings it is given.
//
// The assertion that matters is that the outline it could not be placed in
// still took the hole that could: an empty ring must not end the pairing or
// take a real hole's place.
func TestAHoleWithNoPointsIsSkippedRatherThanRead(t *testing.T) {
	outer := rect(0, 2, 10, 40)
	polys, orphans, _ := Polygons([]Ring{outer}, []Ring{{}, rect(0.5, 1.5, 15, 25), {}})

	if len(polys) != 1 {
		t.Fatalf("got %d polygons, want 1", len(polys))
	}
	if len(polys[0].Holes) != 1 {
		t.Errorf("the outline took %d holes, want the one that had points", len(polys[0].Holes))
	}
	if len(orphans) != 0 {
		t.Errorf("got %d orphans, want none: an empty ring is not a hole that lies outside every outline",
			len(orphans))
	}
}

// NewSet and NewArea are how a caller that has just derived areas gets a
// searchable set without going through the file, and the command does
// exactly that before writing one. Nothing else in this package's tests
// builds a set that way.
//
// The claim is containment rather than shape, because containment is what a
// Set is for: an Area whose rings were kept but whose holes were appended
// before the outline, or whose coordinates were read in the other order,
// holds the right numbers and answers every question wrongly.
func TestNewSetAnswersContainmentForTheAreasItIsGiven(t *testing.T) {
	set := NewSet(Provenance{}, []Area{
		NewArea("wide", "7", []Polygon{{
			Outer: rect(0, 2, 10, 40),
			Holes: []Ring{rect(0.5, 1.5, 15, 25)},
		}}),
		NewArea("tall", "7", []Polygon{{Outer: rect(20, 50, 60, 62)}}),
	})

	if set.Len() != 2 {
		t.Fatalf("the set holds %d areas, want the 2 it was given", set.Len())
	}

	for _, tc := range []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"inside the wide one", 1, 12, "wide"},
		{"inside the wide one's hole", 1, 20, ""},
		{"inside the tall one", 35, 61, "tall"},
		{"the tall one's coordinates transposed", 61, 35, ""},
		{"the wide one's coordinates transposed", 12, 1, ""},
		{"outside both", 80, 80, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := set.At(tc.lat, tc.lon)
			if got := areaName(a, ok); got != tc.want {
				t.Errorf("At(%v, %v) = %q, want %q", tc.lat, tc.lon, got, tc.want)
			}
		})
	}
}

// A hole is placed by asking about several of its vertices, not one.
//
// A vertex lying exactly ON the outline is undecidable for ray casting --
// inRing says its on-boundary behaviour is a convention -- and holes that
// touch the outline around them are ordinary in administrative data. Decided
// from one vertex, such a hole becomes an orphan, which Report then
// attributes to the extract's edge; the hole is dropped and the area answers
// "inside" for every point in what should be the lake.
func TestAHoleTouchingItsOutlineIsStillItsHole(t *testing.T) {
	outer := rect(0, 10, 0, 10)

	for _, tc := range []struct {
		name string
		hole Ring
	}{
		// The first vertex OUTSIDE the outline and the body inside, which
		// is what a partial inner ring from a cut extract looks like. From
		// the first vertex alone this is an orphan, and the lake fills in.
		{"first vertex outside", Ring{
			{Lat: -1, Lon: 5}, {Lat: 2, Lon: 6}, {Lat: 1, Lon: 4}}},
		{"first two vertices outside", Ring{
			{Lat: -1, Lon: 5}, {Lat: -1, Lon: 6}, {Lat: 2, Lon: 6}, {Lat: 2, Lon: 5}}},
		{"first vertex on a corner of the outline", Ring{
			{Lat: 0, Lon: 0}, {Lat: 2, Lon: 1}, {Lat: 1, Lon: 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polys, orphans, err := Polygons([]Ring{outer}, []Ring{tc.hole})
			if err != nil {
				t.Fatalf("Polygons: %v", err)
			}
			if len(orphans) != 0 {
				t.Errorf("the hole was reported as lying outside every outline")
			}
			if len(polys) != 1 || len(polys[0].Holes) != 1 {
				t.Errorf("the outline took %d holes, want 1", len(polys[0].Holes))
			}
		})
	}
}

// And a hole that really is outside must still be an orphan, however many
// vertices are asked about.
func TestAHoleGenuinelyOutsideStaysAnOrphan(t *testing.T) {
	_, orphans, err := Polygons([]Ring{rect(0, 10, 0, 10)}, []Ring{rect(50, 60, 50, 60)})
	if err != nil {
		t.Fatalf("Polygons: %v", err)
	}
	if len(orphans) != 1 {
		t.Errorf("got %d orphans, want 1", len(orphans))
	}
}

// The pairing is quadratic and the box prefilter defeats nothing when every
// ring sits at the same place, which a crafted relation can arrange.
func TestPairingIsBounded(t *testing.T) {
	n := 1 + maxPairings/2
	outers := make([]Ring, 2)
	for i := range outers {
		outers[i] = rect(0, 1, 0, 1)
	}
	holes := make([]Ring, n)
	for i := range holes {
		holes[i] = rect(0.2, 0.3, 0.2, 0.3)
	}
	if _, _, err := Polygons(outers, holes); err == nil {
		t.Errorf("pairing %d outlines against %d holes was accepted", len(outers), len(holes))
	}
}

// Wraps belongs beside inRing because inRing is what makes it necessary.
func TestWrapsFindsARingThatCrossesTheSeam(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Ring
		want bool
	}{
		{"an ordinary ring", rect(-34, -33, 151, 152), false},
		{"a ring beside the seam", rect(-17, -16, 179, 179.9), false},
		{"a ring across the seam", Ring{
			{Lat: -17, Lon: 179.5}, {Lat: -17, Lon: -179.5},
			{Lat: -16, Lon: -179.5}, {Lat: -16, Lon: 179.5}}, true},
		// The closing edge counts, because inRing walks it: a Ring does not
		// repeat its first vertex. A ring whose only oversized step is that
		// one has vertices two thirds of the world apart, which no boundary
		// has -- the measured maximum on the real Natural Earth file is half
		// a degree -- so calling it unusable is the right answer for the
		// same reason the threshold is.
		{"a ring whose only oversized step is the closing one", Ring{
			{Lat: 0, Lon: -170}, {Lat: 1, Lon: 0}, {Lat: 2, Lon: 170}}, true},
		{"nothing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Wraps(tc.r); got != tc.want {
				t.Errorf("Wraps = %v, want %v", got, tc.want)
			}
		})
	}
}
