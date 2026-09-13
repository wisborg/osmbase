package raster_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// TestStroke_WidthIsTheWidthAsked measures a stroke rather than comparing it
// to a picture.
//
// A horizontal line of width w covers, in every column it crosses away from
// its ends, a band w pixels tall. Coverage summed down that column is
// therefore w exactly, whatever the fractional position of the line -- which
// is the point: the measurement does not care how the band falls across pixel
// boundaries, only how much ink is there.
//
// The column sampled is far from both ends, so the round caps do not reach it:
// the widest case here is 8, whose caps have radius 4 and so extend to x = 9
// and x = 29.
func TestStroke_WidthIsTheWidthAsked(t *testing.T) {
	for _, w := range []float32{0.5, 1, 2, 3.5, 8} {
		t.Run(fmt.Sprintf("width%v", w), func(t *testing.T) {
			for _, y := range []float32{20, 20.5, 20.3} {
				s := raster.NewSurface(40, 40)
				var p raster.Path
				p.Stroke([]raster.Point{{X: 5, Y: y}, {X: 25, Y: y}}, raster.Stroke{Width: w})
				s.Fill(&p, ink)

				if got := coverageColumn(s, 17); !nearly(got, float64(w), 0.02) {
					t.Errorf("a stroke of width %v at y=%v covers %.4f pixels down a column, want %v", w, y, got, w)
				}
			}
		})
	}
}

// TestStroke_RoundJoinFillsTheCornerThatButtCapsWouldNotch is the case the
// design says butt caps break: two segments meeting at a bend.
//
// The polyline turns a right angle at (50, 10) with a width of 8, so each
// segment's quad reaches 4 either side of its own centreline and neither of
// them reaches the OUTSIDE of the corner. The test asserts that directly --
// the sampled pixel is proved to lie outside both quads before its coverage is
// checked -- so a pass means the join disc is what covered it and nothing
// else could have.
//
// In a map this notch would not appear at corners a cartographer put there. It
// would appear wherever OSM split one road into two ways, which is at every
// change of speed limit or surface, in the middle of a road.
func TestStroke_RoundJoinFillsTheCornerThatButtCapsWouldNotch(t *testing.T) {
	const w = 8.0
	corner := raster.Point{X: 50, Y: 10}
	line := []raster.Point{{X: 10, Y: 10}, corner, {X: 50, Y: 50}}

	// The pixel spanning [52, 53) x [8, 9). Its farthest corner from the bend
	// is (53, 8), at a distance of sqrt(9+4) = 3.61, inside the join's radius
	// of 4; and it is beyond x = 54 for neither quad but past the end of the
	// first (x > 50 + 0) and above the start of the second (y < 10).
	const px, py = 52, 8
	if dx, dy := float64(px+1-50), float64(py-10); math.Hypot(dx, dy) >= w/2 {
		t.Fatalf("the fixture is wrong: the sampled pixel is not wholly inside the join disc")
	}
	if px < 50 {
		t.Fatalf("the fixture is wrong: the sampled pixel is not past the end of the first segment")
	}
	if py+1 > 10 {
		t.Fatalf("the fixture is wrong: the sampled pixel is not clear of the second segment")
	}

	s := raster.NewSurface(64, 64)
	var p raster.Path
	p.Stroke(line, raster.Stroke{Width: w})
	s.Fill(&p, ink)
	if got := coverageAt(s, px, py); !nearly(got, 1, 0.004) {
		t.Errorf("the outside of the bend has coverage %.4f, want 1: the round join must close it", got)
	}

	// The same two segments as bare quads, which is what butt caps would draw,
	// to show that the corner really is empty without the join.
	butt := raster.NewSurface(64, 64)
	p.Reset()
	p.Ring([]raster.Point{{X: 10, Y: 6}, {X: 50, Y: 6}, {X: 50, Y: 14}, {X: 10, Y: 14}})
	p.Ring([]raster.Point{{X: 46, Y: 10}, {X: 54, Y: 10}, {X: 54, Y: 50}, {X: 46, Y: 50}})
	butt.Fill(&p, ink)
	if got := coverageAt(butt, px, py); !nearly(got, 0, 0.004) {
		t.Fatalf("the fixture is wrong: the same corner has coverage %.4f with butt caps, want 0", got)
	}
}

// TestStroke_ASinglePointIsADot pins the decided policy for a degenerate
// polyline rather than leaving it to whatever falls out.
//
// A round cap at each end of a zero-length line is a disc, so a one-point way
// draws as a dot of the stroke's width. Drawing nothing would also have been
// defensible; it is rejected because a way that survived generalisation down
// to one node is still a thing that is there, and a dot says so.
//
// The dot is measured as an effective radius rather than as an area, because
// the area a flattened circle is short by depends on its size and the radius
// it is short by does not. See effectiveRadius.
func TestStroke_ASinglePointIsADot(t *testing.T) {
	for _, w := range []float32{4, 10, 25} {
		t.Run(fmt.Sprintf("width%v", w), func(t *testing.T) {
			s := raster.NewSurface(40, 40)
			var p raster.Path
			p.Stroke([]raster.Point{{X: 20, Y: 20}}, raster.Stroke{Width: w})
			s.Fill(&p, ink)

			want := float64(w / 2)
			got := effectiveRadius(coverageTotal(s))
			if got > want+0.02 || got < want-0.25 {
				t.Errorf("a one-point stroke of width %v draws a dot of radius %.4f, want %v", w, got, want)
			}
		})
	}
}

// effectiveRadius returns the radius of the circle with the given area, which
// is how every disc in these tests is checked.
//
// The reason for measuring a radius rather than an area is that the error has
// a natural size in radius and not in area. The rasterizer flattens a cubic
// arc into straight chords and the polygon it produces is INSCRIBED, so a disc
// is always slightly small and never large; the number of chords it chooses
// rises with the radius, so the shortfall stays around a tenth of a pixel of
// radius whatever the size, while as a fraction of area it is 4.5% at radius 2
// and 1.6% at radius 5. A tolerance in area would have to be loose enough for
// the smallest case and would then say nothing about the largest.
func effectiveRadius(area float64) float64 { return math.Sqrt(area / math.Pi) }

// TestStroke_RepeatedPointsDrawTheSameAsCleanInput checks that duplicate
// vertices -- which arrive from a generalised tile whenever two nodes round to
// the same integer -- change nothing at all.
//
// They cannot be allowed to matter: a zero-length segment has no direction, so
// its quad has no orientation, and a stroker that normalised it anyway would
// divide by zero and put a NaN into the path. The comparison is against the
// clean polyline pixel for pixel rather than against a tolerance, because the
// claim is that the duplicates are ignored and not that they are harmless.
func TestStroke_RepeatedPointsDrawTheSameAsCleanInput(t *testing.T) {
	clean := []raster.Point{{X: 6, Y: 6}, {X: 26, Y: 12}, {X: 16, Y: 26}}
	dirty := []raster.Point{{X: 6, Y: 6}, {X: 6, Y: 6}, {X: 26, Y: 12}, {X: 26, Y: 12}, {X: 26, Y: 12}, {X: 16, Y: 26}}

	a := raster.NewSurface(32, 32)
	var p raster.Path
	p.Stroke(clean, raster.Stroke{Width: 5})
	a.Fill(&p, ink)

	b := raster.NewSurface(32, 32)
	p.Reset()
	p.Stroke(dirty, raster.Stroke{Width: 5})
	b.Fill(&p, ink)

	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if got, want := a.RGBA().RGBAAt(x, y), b.RGBA().RGBAAt(x, y); got != want {
				t.Fatalf("pixel (%d, %d) is %+v without the repeated points and %+v with them", x, y, got, want)
			}
		}
	}
}

// TestStroke_AClosedPolylineHasNoNotchWhereItMeetsItself is the ring case: a
// caller that strokes a polygon's outline repeats the first point as the last,
// so the polyline's two ends meet at a vertex like any other.
//
// There is nothing in the stroker for this, which is exactly what wants
// checking. The two ends each put a disc at that corner, so the disc is drawn
// twice -- free under this fill rule -- and the corner must come out identical
// to the three corners that are ordinary joins. The test says that as a
// symmetry: the square is symmetric about both its axes, so if the meeting
// corner differed from the others in any pixel, the image would not be.
func TestStroke_AClosedPolylineHasNoNotchWhereItMeetsItself(t *testing.T) {
	const size = 32
	ring := []raster.Point{{X: 8, Y: 8}, {X: 24, Y: 8}, {X: 24, Y: 24}, {X: 8, Y: 24}, {X: 8, Y: 8}}
	s := raster.NewSurface(size, size)
	var p raster.Path
	p.Stroke(ring, raster.Stroke{Width: 8})
	s.Fill(&p, ink)

	// The pixel spanning [6, 7) x [5, 6) is outside both of the quads meeting
	// at (8, 8) -- they reach x = 4 only below y = 8, and y = 4 only right of
	// x = 8 -- and its farthest corner (6, 5) is sqrt(4+9) = 3.61 from the
	// vertex, inside the join radius of 4. Only the join can cover it.
	if got := coverageAt(s, 6, 5); !nearly(got, 1, 0.004) {
		t.Errorf("the outside of the corner where the two ends meet has coverage %.4f, want 1", got)
	}
	if got := coverageAt(s, 16, 16); !nearly(got, 0, 0.004) {
		t.Errorf("the middle of the stroked ring has coverage %.4f, want 0; a stroke must not fill what it encloses", got)
	}

	// One step of alpha, because the four corners are drawn from segments in
	// different orders and float32 addition is not associative.
	const tol = 1.0 / 255
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			got := coverageAt(s, x, y)
			for _, m := range []struct{ x, y int }{{size - 1 - x, y}, {x, size - 1 - y}, {size - 1 - x, size - 1 - y}} {
				if want := coverageAt(s, m.x, m.y); !nearly(got, want, tol) {
					t.Fatalf("pixel (%d, %d) has coverage %.4f and its mirror (%d, %d) has %.4f: the corner where the ends meet is not like the others",
						x, y, got, m.x, m.y, want)
				}
			}
		}
	}
}
