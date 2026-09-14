package render

import (
	"math"
	"testing"
)

// The clip is the renderer's half of two contracts it cannot delegate.
//
// raster fills what it is given, at a cost proportional to how far outside the
// surface the geometry starts rather than to anything visible, and it uses the
// order of a ring's points as the whole mechanism for holes. So whatever comes
// out of here has to be bounded by the box AND wound the way it went in. A clip
// that got the first right and the second wrong would fill every lake solid,
// which still looks like a map.

// area2 is twice the signed area of a ring by the surveyor's formula. Only its
// sign is used, which is the only part that is exact.
func area2(ring []pt) float64 {
	var s float64
	for i := range ring {
		a, b := ring[i], ring[(i+1)%len(ring)]
		s += a.X*b.Y - b.X*a.Y
	}
	return s
}

// rectRing returns an axis-aligned ring wound positively by the surveyor's
// formula with y running down, which is the direction mvt gives an exterior.
func rectRing(minX, minY, maxX, maxY float64) []pt {
	return []pt{{minX, minY}, {maxX, minY}, {maxX, maxY}, {minX, maxY}}
}

func reversed(ring []pt) []pt {
	out := make([]pt, len(ring))
	for i, p := range ring {
		out[len(ring)-1-i] = p
	}
	return out
}

// TestClipRing_HoleKeepsItsWindingSoItStaysAHole is the trap T13 regression.
//
// The fill rule is absolute accumulated winding, so a hole cuts a hole only
// while it is wound against its exterior. Both rings here straddle the box and
// are really clipped -- the exterior loses its right half and the hole loses
// its right end -- and the expected signs are a derivation and not a
// recording: Sutherland-Hodgman emits input vertices and points between
// consecutive input vertices, in input order, so an exterior of positive area
// stays positive and a hole of negative area stays negative whatever the box
// takes off them.
func TestClipRing_HoleKeepsItsWindingSoItStaysAHole(t *testing.T) {
	var c clipper
	b := box{MinX: 0, MinY: 0, MaxX: 50, MaxY: 100}

	exterior := rectRing(-10, 10, 90, 90)
	hole := reversed(rectRing(20, 30, 70, 70))

	if got := area2(exterior); got <= 0 {
		t.Fatalf("the fixture's exterior has signed area %g, want positive", got)
	}
	if got := area2(hole); got >= 0 {
		t.Fatalf("the fixture's hole has signed area %g, want negative", got)
	}

	gotExt := append([]pt(nil), c.ring(exterior, b)...)
	gotHole := append([]pt(nil), c.ring(hole, b)...)

	if len(gotExt) < 3 || len(gotHole) < 3 {
		t.Fatalf("clipping dropped a ring that straddles the box: exterior %d points, hole %d points", len(gotExt), len(gotHole))
	}
	if a := area2(gotExt); a <= 0 {
		t.Errorf("the clipped exterior has signed area %g, want positive; a reversed exterior and its hole cancel to nothing", a)
	}
	if a := area2(gotHole); a >= 0 {
		t.Errorf("the clipped hole has signed area %g, want negative; a hole wound with its exterior fills solid and the island vanishes from the lake", a)
	}
}

// TestClipRing_EverythingReturnedIsInsideTheBox is the cost contract.
//
// The rasterizer walks a segment scanline by scanline from wherever it starts
// down to the surface, so one vertex left a long way out is a render that takes
// seconds rather than milliseconds. A vertex a million units away is exactly
// what a vector tile's buffer and an overzoomed ancestor both produce.
func TestClipRing_EverythingReturnedIsInsideTheBox(t *testing.T) {
	var c clipper
	b := box{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}
	in := []pt{{-1e6, -1e6}, {1e6, -1e6}, {1e6, 1e6}, {-1e6, 1e6}}

	const eps = 1e-9
	for _, p := range c.ring(in, b) {
		if p.X < b.MinX-eps || p.X > b.MaxX+eps || p.Y < b.MinY-eps || p.Y > b.MaxY+eps {
			t.Fatalf("clipped ring has the point %v, outside the box %v", p, b)
		}
	}
}

// TestClipRing_AWholePolygonSwallowingTheBoxBecomesTheBox checks the case that
// makes a filled view possible at all: a landcover polygon far larger than the
// view still has to fill it, not vanish.
func TestClipRing_AWholePolygonSwallowingTheBoxBecomesTheBox(t *testing.T) {
	var c clipper
	b := box{MinX: 10, MinY: 20, MaxX: 60, MaxY: 90}
	got := c.ring(rectRing(-1e6, -1e6, 1e6, 1e6), b)

	want := b.width() * b.height()
	if a := math.Abs(area2(got)) / 2; math.Abs(a-want) > 1e-6 {
		t.Errorf("a polygon containing the box clips to area %g, want the box's own %g", a, want)
	}
}

func TestClipRing_RingWhollyOutsideIsDropped(t *testing.T) {
	var c clipper
	b := box{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}
	if got := c.ring(rectRing(200, 200, 300, 300), b); got != nil {
		t.Errorf("a ring entirely outside the box clipped to %v, want nothing", got)
	}
}

// TestClipLine_LeavingAndReenteringIsTwoRunsNotOne is why clipping a polyline
// is not a point filter.
//
// Keeping only the points inside and joining what is left draws a road from
// where the real one left the view to where it came back, which is a road that
// does not exist and is indistinguishable from one that does.
func TestClipLine_LeavingAndReenteringIsTwoRunsNotOne(t *testing.T) {
	var c clipper
	b := box{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}
	line := []pt{{10, 50}, {40, 50}, {40, 500}, {70, 500}, {70, 50}, {90, 50}}

	var runs [][]pt
	c.line(line, b, func(run []pt) { runs = append(runs, append([]pt(nil), run...)) })

	if len(runs) != 2 {
		t.Fatalf("a line leaving and re-entering the box gave %d runs, want 2: %v", len(runs), runs)
	}
	for i, run := range runs {
		for _, p := range run {
			if p.X < b.MinX || p.X > b.MaxX || p.Y < b.MinY || p.Y > b.MaxY {
				t.Errorf("run %d has the point %v, outside the box", i, p)
			}
		}
	}
}

// TestClipLine_EndsLandExactlyOnTheBoxEdge is what makes two tiles' halves of
// one road meet.
//
// Each tile's geometry is clipped to that tile's own square, so the road ends
// on the shared edge from one side and begins on it from the other. Both get a
// round cap of half the stroke width centred on the same point, and the two
// caps close the join. An endpoint rounded inward by even a fraction of a pixel
// would leave a notch at every tile boundary in the map.
func TestClipLine_EndsLandExactlyOnTheBoxEdge(t *testing.T) {
	var c clipper
	b := box{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}

	var got []pt
	c.line([]pt{{50, 50}, {150, 50}}, b, func(run []pt) { got = append([]pt(nil), run...) })

	if len(got) != 2 {
		t.Fatalf("clipping gave %d points, want 2: %v", len(got), got)
	}
	if got[1] != (pt{X: 100, Y: 50}) {
		t.Errorf("the clipped end is %v, want exactly {100 50}", got[1])
	}
}

// TestClipLine_SegmentParallelToAnEdgeSurvives pins the p == 0 branch of the
// Liang-Barsky narrowing, which decides on q alone. A road running exactly
// along a tile edge is ordinary -- boundaries and coastlines do it -- and a
// division by that zero would drop it or keep it at random.
func TestClipLine_SegmentParallelToAnEdgeSurvives(t *testing.T) {
	var c clipper
	b := box{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}

	for _, tc := range []struct {
		name string
		line []pt
		want int
	}{
		{"along the top edge", []pt{{10, 0}, {90, 0}}, 1},
		{"along the left edge", []pt{{0, 10}, {0, 90}}, 1},
		{"parallel and outside", []pt{{10, -5}, {90, -5}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			c.line(tc.line, b, func([]pt) { n++ })
			if n != tc.want {
				t.Errorf("got %d runs, want %d", n, tc.want)
			}
		})
	}
}
