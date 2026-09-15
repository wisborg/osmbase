package raster_test

import (
	"math"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// Path.Bounds had no test of any kind before this file, and it is the kind of
// thing that cannot be noticed by eye: nothing in the package draws with it,
// so a box that is wrong in one direction produces no wrong picture here and
// waits for the first caller that culls with it. Its own doc comment names two
// such callers -- Surface.Fill skipping a path that misses the surface, and a
// renderer culling a feature against the view -- and at this commit neither
// exists, in this package or anywhere in the module. So every assertion below
// is about the contract rather than about an observed consequence.
//
// Every expected box is worked out from the geometry that was appended, in the
// doc comment above the test, and never read back from the code.

// box is a hand-computed expectation, compared exactly: the box is the max and
// min of coordinates that went in, so no arithmetic has happened to it that
// could round.
func sameBox(gotMin, gotMax, wantMin, wantMax raster.Point) bool {
	return gotMin == wantMin && gotMax == wantMax
}

// TestBounds_APathWithNoPointsReportsNoBoxRatherThanABoxAtTheOrigin is the
// first half of the pair that makes the second half mean something.
//
// A path with no points has no extent, and the origin is a place: a caller
// culling against a box of {0,0}-{0,0} would decide that an empty path sits in
// the top-left corner of the surface. The third return value is how that is
// said, and it must be false in every way a path can be empty.
func TestBounds_APathWithNoPointsReportsNoBoxRatherThanABoxAtTheOrigin(t *testing.T) {
	tests := []struct {
		name  string
		build func(p *raster.Path)
	}{
		{"the zero path", func(p *raster.Path) {}},
		{"a ring of too few points", func(p *raster.Path) {
			p.Ring([]raster.Point{{X: 100, Y: 100}, {X: 200, Y: 200}})
		}},
		{"a circle of zero radius", func(p *raster.Path) {
			p.Circle(raster.Point{X: 100, Y: 100}, 0)
		}},
		{"a stroke of no points", func(p *raster.Path) {
			p.Stroke(nil, raster.Stroke{Width: 4})
		}},
		{"a stroke of zero width", func(p *raster.Path) {
			p.Stroke([]raster.Point{{X: 100, Y: 100}, {X: 200, Y: 100}}, raster.Stroke{Width: 0})
		}},
		{"a path that was reset", func(p *raster.Path) {
			p.Rect(raster.Point{X: 100, Y: 100}, raster.Point{X: 200, Y: 200})
			p.Reset()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p raster.Path
			tc.build(&p)
			if min, max, ok := p.Bounds(); ok {
				t.Errorf("Bounds reported a box %v-%v for a path with no points, want ok=false", min, max)
			}
		})
	}
}

// TestBounds_IsTheExtremeOfEveryCoordinateAppended is the other half: a path
// that does have points must report the box around them, so that an
// implementation reporting "no box" for everything fails here and one
// reporting a box for everything fails above.
//
// The points are (12, 40), (-6, 7), (3, -2) and (30, 9). The smallest x is -6
// and the smallest y is -2; the largest x is 30 and the largest y is 40. None
// of those extremes comes from the same point as another, so a box that
// tracked whole points rather than coordinates would fail, and none of them is
// the first or the last point, so a box that only remembered one of those
// would fail too.
func TestBounds_IsTheExtremeOfEveryCoordinateAppended(t *testing.T) {
	var p raster.Path
	p.MoveTo(raster.Point{X: 12, Y: 40})
	p.LineTo(raster.Point{X: -6, Y: 7})
	p.LineTo(raster.Point{X: 3, Y: -2})
	p.LineTo(raster.Point{X: 30, Y: 9})

	wantMin := raster.Point{X: -6, Y: -2}
	wantMax := raster.Point{X: 30, Y: 40}
	min, max, ok := p.Bounds()
	if !ok {
		t.Fatal("Bounds reported no box for a path with four points in it")
	}
	if !sameBox(min, max, wantMin, wantMax) {
		t.Errorf("Bounds is %v-%v, want %v-%v", min, max, wantMin, wantMax)
	}
}

// TestBounds_AShapeAwayFromTheOriginDoesNotReachBackToIt is the bug a box
// initialised to the zero value rather than to the first point produces.
//
// Every coordinate here is well to the right of and below the origin, so a box
// that started at {0, 0} and only ever grew would come out as
// {0,0}-{240,180}: four times the area, all of it empty, and every culling
// decision made with it too generous rather than wrong. That is the direction
// that never shows as a missing feature, which is why it survives.
func TestBounds_AShapeAwayFromTheOriginDoesNotReachBackToIt(t *testing.T) {
	var p raster.Path
	p.Rect(raster.Point{X: 120, Y: 90}, raster.Point{X: 240, Y: 180})

	wantMin := raster.Point{X: 120, Y: 90}
	wantMax := raster.Point{X: 240, Y: 180}
	min, max, ok := p.Bounds()
	if !ok {
		t.Fatal("Bounds reported no box for a rectangle")
	}
	if !sameBox(min, max, wantMin, wantMax) {
		t.Errorf("Bounds is %v-%v, want %v-%v; the box must start at the first point, not at the origin", min, max, wantMin, wantMax)
	}
}

// TestBounds_ACircleIsBoundedByItsRadiusInEveryDirection checks the curve case,
// where the box is over control points rather than over the ink.
//
// Circle appends four cardinal points at exactly r from the centre and eight
// control points at (r, k) and (k, r) with k = 0.552*r, so every coordinate is
// within r of the centre and r is attained on all four sides. The box is
// therefore exactly the centre plus and minus the radius -- which is also the
// true extent of the circle, so the documented conservatism costs nothing
// here. A box computed from the four ARC ENDS only would be the same, so the
// case below adds the one that tells them apart.
func TestBounds_ACircleIsBoundedByItsRadiusInEveryDirection(t *testing.T) {
	centre := raster.Point{X: 100, Y: 60}
	const r = 8

	var p raster.Path
	p.Circle(centre, r)

	wantMin := raster.Point{X: centre.X - r, Y: centre.Y - r}
	wantMax := raster.Point{X: centre.X + r, Y: centre.Y + r}
	min, max, ok := p.Bounds()
	if !ok {
		t.Fatal("Bounds reported no box for a circle")
	}
	if !sameBox(min, max, wantMin, wantMax) {
		t.Errorf("a circle of radius %v about %v has box %v-%v, want %v-%v", r, centre, min, max, wantMin, wantMax)
	}
}

// TestBounds_AStrokeIsBoundedByTheInkAndNotByItsCentreline is the property a
// renderer culling a road actually needs, and the one that separates a box
// over the path from a box over the points the CALLER handed in.
//
// The polyline runs from (10, 10) to (30, 10) at width 6, so the ink reaches 3
// either side of the centreline and 3 beyond each end, through the round caps:
// x from 7 to 33 and y from 7 to 13. A box over the centreline would be
// {10,10}-{30,10}, and a feature culled with it would pop into existence three
// pixels late at the edge of the view.
func TestBounds_AStrokeIsBoundedByTheInkAndNotByItsCentreline(t *testing.T) {
	const w = 6
	var p raster.Path
	p.Stroke([]raster.Point{{X: 10, Y: 10}, {X: 30, Y: 10}}, raster.Stroke{Width: w})

	wantMin := raster.Point{X: 7, Y: 7}
	wantMax := raster.Point{X: 33, Y: 13}
	min, max, ok := p.Bounds()
	if !ok {
		t.Fatal("Bounds reported no box for a stroked polyline")
	}
	if !sameBox(min, max, wantMin, wantMax) {
		t.Errorf("a stroke of width %v from (10,10) to (30,10) has box %v-%v, want %v-%v", w, min, max, wantMin, wantMax)
	}
}

// TestBounds_GrowsAcrossSubpathsAndForgetsThemOnReset pins the reuse a
// renderer does between layers, from the direction that matters: a box that
// survived a Reset would describe the PREVIOUS layer's geometry.
func TestBounds_GrowsAcrossSubpathsAndForgetsThemOnReset(t *testing.T) {
	var p raster.Path
	p.Rect(raster.Point{X: 10, Y: 10}, raster.Point{X: 20, Y: 20})
	p.Rect(raster.Point{X: 40, Y: 5}, raster.Point{X: 50, Y: 15})

	if min, max, ok := p.Bounds(); !ok || !sameBox(min, max, raster.Point{X: 10, Y: 5}, raster.Point{X: 50, Y: 20}) {
		t.Errorf("two rectangles give box %v-%v (ok=%v), want {10 5}-{50 20}", min, max, ok)
	}

	p.Reset()
	p.Rect(raster.Point{X: 200, Y: 200}, raster.Point{X: 210, Y: 210})
	if min, max, ok := p.Bounds(); !ok || !sameBox(min, max, raster.Point{X: 200, Y: 200}, raster.Point{X: 210, Y: 210}) {
		t.Errorf("after Reset the box is %v-%v (ok=%v), want {200 200}-{210 210}: the previous layer must be forgotten", min, max, ok)
	}
}

// TestBounds_ANonFiniteCoordinateLeavesABoxThatCullsNothing is about the
// direction an unusable box has to be unusable in.
//
// A projection past the Mercator cut, or a scale divided by a zero-sized view,
// reaches this package as NaN. There is no honest box for that, and the only
// thing that must not happen is a box that causes a feature to be SKIPPED:
// drawing something wrong is visible and skipping something is not.
//
// So the assertion is the culling decision itself rather than the numbers. For
// each of the four comparisons a culler makes, a box containing a NaN must not
// answer "outside".
//
// Note that this passes for two different implementations -- one that ignores
// the NaN and one that lets it poison the box -- and only the first is what
// Path.add's comment claims. See the report: a NaN in the FIRST point does
// poison the box, and the reason it is not a defect today is this property
// rather than that claim.
func TestBounds_ANonFiniteCoordinateLeavesABoxThatCullsNothing(t *testing.T) {
	nan := float32(math.NaN())
	// A surface-sized box to cull against, with the real geometry inside it.
	const sx0, sy0, sx1, sy1 = 0, 0, 64, 64

	for _, bad := range []raster.Point{{X: nan, Y: 20}, {X: 20, Y: nan}, {X: nan, Y: nan}} {
		for _, first := range []bool{true, false} {
			var p raster.Path
			if first {
				p.MoveTo(bad)
				p.LineTo(raster.Point{X: 20, Y: 30})
				p.LineTo(raster.Point{X: 40, Y: 50})
			} else {
				p.MoveTo(raster.Point{X: 20, Y: 30})
				p.LineTo(bad)
				p.LineTo(raster.Point{X: 40, Y: 50})
			}
			min, max, ok := p.Bounds()
			if !ok {
				t.Fatalf("bad=%v first=%v: Bounds reported no box for a path with three points", bad, first)
			}
			if min.X > sx1 || max.X < sx0 || min.Y > sy1 || max.Y < sy0 {
				t.Errorf("bad=%v first=%v: box %v-%v culls a path whose finite points are inside the surface", bad, first, min, max)
			}
		}
	}
}
