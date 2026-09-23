package osm

import (
	"testing"

	"github.com/wisborg/osmbase/boundary"
)

// rectWay builds a closed way spanning the given latitudes and longitudes.
//
// A rectangle rather than the square squareWay builds, because a square whose
// corner sits on the diagonal is its own transpose: it answers every
// containment question the same whether the pair is read latitude first or
// longitude first, and a transposition between this module's four coordinate
// types is exactly the mistake the type comments were written about.
//
// The node ids must be four distinct ids, which the caller supplies, because
// assembly joins on node id and two boundaries sharing an id would join to
// each other.
func rectWay(id int64, role string, south, north, west, east float64, nodes ...int64) Way {
	corners := []Point{
		{Lat: south, Lon: west},
		{Lat: south, Lon: east},
		{Lat: north, Lon: east},
		{Lat: north, Lon: west},
	}
	w := Way{ID: id, Role: role, Nodes: append(append([]int64{}, nodes...), nodes[0])}
	for i := range w.Nodes {
		w.Points = append(w.Points, corners[i%len(corners)])
	}
	return w
}

// chainWay builds a two-node way that joins to nothing, so the boundary
// holding it has a gap left over after assembly.
func chainWay(id int64, from, to int64, a, b Point) Way {
	return Way{ID: id, Role: "outer", Nodes: []int64{from, to}, Points: []Point{a, b}}
}

// Every number in a Report is a fact about somebody's extract, and the
// command that builds a boundary file prints them for a person to act on: a
// region whose outlines mostly did not close is one whose extract was cut
// through them. A count that is quietly wrong is worse than no count, because
// it is read as a measurement.
//
// The whole struct is compared rather than the one or two fields a case is
// about. A field asserted only where it is expected to be non-zero is a field
// that can be incremented anywhere else for free -- and four of these seven
// were, before this test: Partial, Holes and OrphanHoles were never asserted
// at all, and Outlines was only ever asserted as 1, which does not distinguish
// counting rings from counting boundaries.
//
// Each case's expected Report is derived from the ways listed in it, not from
// what Areas returns: count the closed outer rectangles, the closed inner
// ones, which of those lie inside an outline, and whether anything was left
// over.
func TestTheReportAccountsForEveryBoundaryRingAndHole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bs    []Boundary
		want  Report
		areas int
	}{
		{
			name:  "nothing at all",
			bs:    nil,
			want:  Report{},
			areas: 0,
		},
		{
			// One outline, nothing left over: complete, one ring, no holes.
			name: "an outline on its own",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
			}}},
			want:  Report{Complete: 1, Outlines: 1},
			areas: 1,
		},
		{
			// The hole lies within the outline's latitudes and longitudes,
			// so it is paired: one ring, one hole, no orphan.
			name: "an outline with a hole inside it",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
				rectWay(11, "inner", 0.5, 1.5, 15, 25, 5, 6, 7, 8),
			}}},
			want:  Report{Complete: 1, Outlines: 1, Holes: 1},
			areas: 1,
		},
		{
			// Two outlines and two holes, one in each: the counts are of
			// rings and holes, not of boundaries.
			name: "two outlines with a hole in each",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
				rectWay(11, "outer", 20, 30, 60, 62, 5, 6, 7, 8),
				rectWay(12, "inner", 0.5, 1.5, 15, 25, 9, 10, 11, 12),
				rectWay(13, "inner", 22, 24, 60.5, 61.5, 13, 14, 15, 16),
			}}},
			want:  Report{Complete: 1, Outlines: 2, Holes: 2},
			areas: 1,
		},
		{
			// The hole lies inside no outline, which is what the edge of an
			// extract does to an outline that should have held it. Counted
			// as an orphan and NOT as a hole.
			name: "a hole that lies inside no outline",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
				rectWay(11, "inner", 50, 51, 80, 81, 5, 6, 7, 8),
			}}},
			want:  Report{Complete: 1, Outlines: 1, OrphanHoles: 1},
			areas: 1,
		},
		{
			// An outline that closed and a fragment that did not: rings came
			// out and so did a gap, which is partial rather than complete.
			name: "an outline and a fragment that did not close",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
				chainWay(11, 5, 6, Point{Lat: 0, Lon: 50}, Point{Lat: 1, Lon: 51}),
			}}},
			want:  Report{Partial: 1, Outlines: 1},
			areas: 1,
		},
		{
			// A hole survives a partial outline: the boundary is partial and
			// the hole is still cut out of what did close.
			name: "a partial outline that still holds a hole",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
				rectWay(11, "inner", 0.5, 1.5, 15, 25, 5, 6, 7, 8),
				chainWay(12, 9, 10, Point{Lat: 0, Lon: 50}, Point{Lat: 1, Lon: 51}),
			}}},
			want:  Report{Partial: 1, Outlines: 1, Holes: 1},
			areas: 1,
		},
		{
			// Nothing closed, so there is nothing to test a point against.
			name: "a fragment and no outline",
			bs: []Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
				chainWay(10, 1, 2, Point{Lat: 0, Lon: 50}, Point{Lat: 1, Lon: 51}),
			}}},
			want:  Report{Unclosed: 1},
			areas: 0,
		},
		{
			// One of each, so the three classifications are told apart in a
			// single report rather than one at a time.
			name: "one of each, counted together",
			bs: []Boundary{
				{Name: "whole", AdminLevel: 9, Ways: []Way{
					rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
					rectWay(11, "inner", 0.5, 1.5, 15, 25, 5, 6, 7, 8),
				}},
				{Name: "partial", AdminLevel: 9, Ways: []Way{
					rectWay(20, "outer", 20, 30, 60, 62, 21, 22, 23, 24),
					chainWay(21, 25, 26, Point{Lat: 20, Lon: 70}, Point{Lat: 21, Lon: 71}),
				}},
				{Name: "cut off", AdminLevel: 9, Ways: []Way{
					chainWay(30, 31, 32, Point{Lat: 40, Lon: 80}, Point{Lat: 41, Lon: 81}),
				}},
			},
			want:  Report{Complete: 1, Partial: 1, Unclosed: 1, Outlines: 2, Holes: 1},
			areas: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			areas, rep, err := Areas(tc.bs)
			if err != nil {
				t.Fatalf("Areas: %v", err)
			}
			if rep != tc.want {
				t.Errorf("report  = %+v\nwant      %+v", rep, tc.want)
			}
			if len(areas) != tc.areas {
				t.Errorf("got %d areas, want %d", len(areas), tc.areas)
			}
		})
	}
}

// seamWay is a closed way whose ring steps across the antimeridian between
// two consecutive vertices.
func seamWay(id int64, role string, south, north float64, nodes ...int64) Way {
	corners := []Point{
		{Lat: south, Lon: 179.5},
		{Lat: south, Lon: -179.5}, // 359 degrees in one step: the seam
		{Lat: north, Lon: -179.5},
		{Lat: north, Lon: 179.5},
	}
	w := Way{ID: id, Role: role, Nodes: append(append([]int64{}, nodes...), nodes[0])}
	for i := range w.Nodes {
		w.Points = append(w.Points, corners[i%len(corners)])
	}
	return w
}

// A HOLE that crosses the seam is filtered by the same rule as an outline
// that does, and for the same reason: boundary.inRing treats longitude as
// linear, so a wrapped ring handed to it answers about the wrong half of the
// planet. A hole is the worse of the two, because a wrong hole subtracts
// ground from an area that is otherwise right, and the area still looks
// plausible everywhere else.
//
// The outline here does not cross the seam, so it survives: an implementation
// that filtered nothing would report no wrapped rings, and one that filtered
// everything near the seam would return no areas.
func TestAHoleThatCrossesTheSeamIsCountedAndLeftOut(t *testing.T) {
	areas, rep, err := Areas([]Boundary{{Name: "A", AdminLevel: 9, Ways: []Way{
		rectWay(10, "outer", -20, -10, 178.0, 179.4, 1, 2, 3, 4),
		seamWay(11, "inner", -16, -15, 5, 6, 7, 8),
	}}})
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	want := Report{Complete: 1, Outlines: 1, WrappedRings: 1}
	if rep != want {
		t.Errorf("report  = %+v\nwant      %+v", rep, want)
	}
	if len(areas) != 1 {
		t.Fatalf("got %d areas, want the one whose outline did not wrap", len(areas))
	}
	// The outline is intact: the hole was dropped, not cut out anyway.
	if a, ok := boundary.NewSet(boundary.Provenance{}, areas).At(-15, 179.0); !ok || a.Name != "A" {
		t.Errorf("a point inside the surviving outline answered %q (%v), want A", a.Name, ok)
	}
}

// wraps must ask about the same edges inRing walks, and inRing walks the
// closing one: a Ring does not repeat its first point at the end, so the edge
// from the last vertex back to the first exists only by the wraparound both
// loops do.
//
// Drop the wraparound from wraps and the ring below is handed to a
// containment test that reads its closing edge as a line across the whole
// world -- which is the exact failure the filter was added to prevent, on the
// one edge nothing else covers. The existing seam test cannot see it: its
// ring crosses the seam between two listed vertices as well, so that crossing
// is found first.
func TestARingThatWrapsOnlyOnItsClosingEdgeIsCaught(t *testing.T) {
	// Four vertices spanning the globe. Every listed step is 110 or 120
	// degrees, under the 180 that means a wrap; the step from the last
	// vertex back to the first is 340.
	w := Way{
		ID: 10, Role: "outer",
		Nodes: []int64{1, 2, 3, 4, 1},
		Points: []Point{
			{Lat: -10, Lon: -170},
			{Lat: -10, Lon: -50},
			{Lat: 10, Lon: 60},
			{Lat: 10, Lon: 170},
			{Lat: -10, Lon: -170},
		},
	}
	areas, rep, err := Areas([]Boundary{{Name: "A", AdminLevel: 2, Ways: []Way{w}}})
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	want := Report{Unclosed: 1, WrappedRings: 1}
	if rep != want {
		t.Errorf("report  = %+v\nwant      %+v", rep, want)
	}
	if len(areas) != 0 {
		t.Errorf("got %d areas, want none: the only ring cannot be tested against", len(areas))
	}
}

// Areas is the only thing between assembled rings and the geometry a lookup
// answers from, and nothing asserted that the geometry came out where it went
// in: the tests checked names, kinds and counts. An area with the right name
// and a transposed outline passes all of those and puts a suburb in the
// wrong ocean, which is the mistake the point type's comment was written
// about after it happened once.
//
// The transposed queries are what make the rest discriminating. Answering
// "inside" for the real coordinate proves little on a fixture whose latitude
// range equals its longitude range -- so this one's does not, and the same
// numbers the other way round must answer "outside".
func TestAreasKeepsTheGeometryWhereItWas(t *testing.T) {
	areas, _, err := Areas([]Boundary{{Name: "wide", AdminLevel: 9, Ways: []Way{
		rectWay(10, "outer", 0, 2, 10, 40, 1, 2, 3, 4),
		rectWay(11, "inner", 0.5, 1.5, 15, 25, 5, 6, 7, 8),
	}}})
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	set := boundary.NewSet(boundary.Provenance{}, areas)

	for _, tc := range []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"inside the outline", 1, 12, "wide"},
		{"inside its hole", 1, 20, ""},
		{"outside it in longitude", 1, 45, ""},
		{"outside it in latitude", 5, 12, ""},
		{"the inside point transposed", 12, 1, ""},
		{"the hole's point transposed", 20, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := set.At(tc.lat, tc.lon)
			got := ""
			if ok {
				got = a.Name
			}
			if got != tc.want {
				t.Errorf("At(%v, %v) = %q, want %q", tc.lat, tc.lon, got, tc.want)
			}
		})
	}
}
