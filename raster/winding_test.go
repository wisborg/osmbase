package raster_test

import (
	"math"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// square returns the four corners of a square about (16, 16), wound clockwise
// on screen, which is positive by the surveyor's formula with y running down
// -- the direction the vector tile specification gives an exterior ring and
// the direction everything this package generates is wound.
func square(half float32) []raster.Point {
	return []raster.Point{
		{X: 16 - half, Y: 16 - half},
		{X: 16 + half, Y: 16 - half},
		{X: 16 + half, Y: 16 + half},
		{X: 16 - half, Y: 16 + half},
	}
}

func reversed(pts []raster.Point) []raster.Point {
	out := make([]raster.Point, len(pts))
	for i, p := range pts {
		out[len(pts)-1-i] = p
	}
	return out
}

// shoelace returns twice the signed area of a ring. Positive is this package's
// direction.
func shoelace(pts []raster.Point) float64 {
	sum := 0.0
	for i := range pts {
		a, b := pts[i], pts[(i+1)%len(pts)]
		sum += float64(a.X)*float64(b.Y) - float64(b.X)*float64(a.Y)
	}
	return sum
}

// TestRing_AReversedRingCutsAHoleInTheSamePath is the mechanism holes are
// made of, and the reason Ring must not normalise the order it is given.
//
// Coverage is the absolute value of the accumulated winding, so a ring wound
// against its exterior contributes -1 where the exterior contributes +1 and
// the sum is zero. It only works if both rings are in ONE path: the winding
// has to meet in one accumulator, and two fills would composite a solid disc
// over a solid square.
func TestRing_AReversedRingCutsAHoleInTheSamePath(t *testing.T) {
	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Ring(square(12))
	p.Ring(reversed(square(4)))
	s.Fill(&p, ink)

	if got := coverageAt(s, 16, 16); !nearly(got, 0, 0.004) {
		t.Errorf("the middle of the hole has coverage %.4f, want 0", got)
	}
	if got := coverageAt(s, 16, 8); !nearly(got, 1, 0.004) {
		t.Errorf("the ring between the hole and the outside has coverage %.4f, want 1", got)
	}
}

// TestRing_ARingWoundLikeItsNeighbourFillsSolid is the island: a second ring
// inside the first, wound the same way, is land in a lake rather than a hole
// in it, and the only thing that distinguishes the two is the order of the
// points.
//
// A Ring that quietly canonicalised its input -- which looks like defensive
// tidying -- would turn every island into a hole or every hole into an island,
// and both still look like a map.
func TestRing_ARingWoundLikeItsNeighbourFillsSolid(t *testing.T) {
	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Ring(square(12))
	p.Ring(square(4))
	s.Fill(&p, ink)

	if got := coverageAt(s, 16, 16); !nearly(got, 1, 0.004) {
		t.Errorf("the middle of the inner ring has coverage %.4f, want 1; winding 2 must clamp to 1 rather than cancel", got)
	}
}

// TestStroke_OutlineIsWoundLikeARingSoTheTwoDoNotErase checks the thing that
// makes it safe to put a filled polygon and a stroked line of the same colour
// into one path -- a lake and the stream leaving it, say, or a landuse patch
// and its outline.
//
// If the stroker emitted its quads the other way round, the picture would
// still be correct for a path containing only strokes, and correct for a path
// containing only rings, and would erase a line-shaped channel through any
// polygon it overlapped. That is a bug that appears only when two kinds of
// geometry share an ink.
func TestStroke_OutlineIsWoundLikeARingSoTheTwoDoNotErase(t *testing.T) {
	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Ring(square(10))
	p.Stroke([]raster.Point{{X: 2, Y: 16}, {X: 30, Y: 16}}, raster.Stroke{Width: 4})
	s.Fill(&p, ink)

	if got := coverageAt(s, 16, 16); !nearly(got, 1, 0.004) {
		t.Errorf("where the stroke crosses the square the coverage is %.4f, want 1", got)
	}
	if got := coverageAt(s, 4, 16); !nearly(got, 1, 0.004) {
		t.Errorf("where the stroke is alone the coverage is %.4f, want 1", got)
	}

	// Said again as the geometric property, because the coverage test above
	// would also pass if the fill rule were non-zero winding.
	if shoelace(square(10)) <= 0 {
		t.Fatalf("the fixture square is wound the wrong way: 2A = %v", shoelace(square(10)))
	}
}

// TestCircle_IsWoundLikeARingSoACapDoesNotPunchAHole is the same argument for
// the discs a stroke puts at its joints and ends. They sit on top of the
// segment quads by design; wound the other way they would subtract from them,
// and a road would come out as a row of holes with ink between them.
func TestCircle_IsWoundLikeARingSoACapDoesNotPunchAHole(t *testing.T) {
	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Ring(square(12))
	p.Circle(raster.Point{X: 16, Y: 16}, 5)
	s.Fill(&p, ink)

	if got := coverageAt(s, 16, 16); !nearly(got, 1, 0.004) {
		t.Errorf("a circle drawn inside a filled square leaves coverage %.4f there, want 1", got)
	}
}

// TestCircle_IsRoundToWithinAPixel checks that Circle draws a circle and not
// some other blob with the right area: a point well inside the radius is
// covered and a point well outside it is not, in every direction.
//
// The sampling radii are r-1.5 and r+1.5 because coverage is an average over a
// whole pixel, and a pixel whose centre is within 1.5 of the boundary can
// straddle it. At that margin a pixel is entirely on one side, so the expected
// values are exactly 1 and exactly 0 rather than something needing a
// tolerance.
func TestCircle_IsRoundToWithinAPixel(t *testing.T) {
	const r = 10.0
	centre := raster.Point{X: 16, Y: 16}
	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Circle(centre, r)
	s.Fill(&p, ink)

	const directions = 16
	for i := 0; i < directions; i++ {
		angle := 2 * math.Pi * float64(i) / directions
		dx, dy := math.Cos(angle), math.Sin(angle)
		for _, probe := range []struct {
			name   string
			radius float64
			want   float64
		}{
			{"inside", r - 1.5, 1},
			{"outside", r + 1.5, 0},
		} {
			x := int(float64(centre.X) + dx*probe.radius)
			y := int(float64(centre.Y) + dy*probe.radius)
			if got := coverageAt(s, x, y); !nearly(got, probe.want, 0.004) {
				t.Errorf("%s the circle at %.0f degrees, pixel (%d, %d) has coverage %.4f, want %v",
					probe.name, angle*180/math.Pi, x, y, got, probe.want)
			}
		}
	}
}

// TestCircle_AreaIsJustUnderPiRSquared measures the four-arc approximation
// against the area of the circle it claims to be.
//
// The four-cubic approximation is itself within about 0.03% of the true
// radius, but that is not what dominates. The rasterizer flattens each cubic
// into straight chords, by its own published heuristic, and the polygon it
// produces is INSCRIBED -- so the filled area is always slightly less than the
// circle and never more. At r = 10 it chooses five chords per quarter, making
// a regular 20-gon of area 10*r^2*sin(18 degrees) = 309.0, which is 1.6% short
// of 314.2 and corresponds to a radius wrong by an eighth of a pixel at its
// worst. That is invisible in a road cap and it is the reason the bound below
// is one-sided and as loose as it is.
func TestCircle_AreaIsJustUnderPiRSquared(t *testing.T) {
	const r = 10.0
	const exact = math.Pi * r * r

	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Circle(raster.Point{X: 16, Y: 16}, r)
	s.Fill(&p, ink)

	got := coverageTotal(s)
	if got > exact+0.5 {
		t.Errorf("circle of radius %v covers %.4f pixels, which is more than the %.4f it encloses", r, got, exact)
	}
	if got < 0.97*exact {
		t.Errorf("circle of radius %v covers %.4f pixels, want within 3%% of %.4f", r, got, exact)
	}
}
