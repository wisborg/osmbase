package raster_test

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// Every measurement in these tests reads the ALPHA channel of a surface that
// started fully transparent and was filled with opaque black.
//
// Compositing opaque black over nothing leaves alpha equal to the coverage the
// rasterizer computed, so alpha/255 is the coverage, directly, with no colour
// arithmetic in between. Measuring a colour instead would fold the blend
// formula into every expected number and make a coverage bug and a
// compositing bug look the same.
var ink = color.RGBA{A: 0xff}

// coverageAt returns the coverage at one pixel, in [0, 1].
func coverageAt(s *raster.Surface, x, y int) float64 {
	return float64(s.RGBA().RGBAAt(x, y).A) / 255
}

// coverageColumn returns the total coverage down the column x, which for a
// horizontal stroke is its width in pixels.
func coverageColumn(s *raster.Surface, x int) float64 {
	b := s.Bounds()
	total := 0.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		total += coverageAt(s, x, y)
	}
	return total
}

// coverageTotal returns the total coverage of the surface, which is the area
// in pixels of whatever was drawn on it.
func coverageTotal(s *raster.Surface) float64 {
	b := s.Bounds()
	total := 0.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			total += coverageAt(s, x, y)
		}
	}
	return total
}

// nearly reports whether got is within tol of want. One step of an 8-bit
// alpha is 1/255, so a tolerance below 0.004 is asking a byte to hold more
// than a byte.
func nearly(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// TestFill_HalfCoveredPixelGetsHalfCoverage is the calibration every other
// measurement in this package rests on: that alpha is coverage and that a
// pixel the geometry crosses halfway comes out halfway.
//
// The rectangle's right edge is at x = 10.5, so the pixel spanning [10, 11) is
// covered over exactly half its width and over all of its height. The expected
// 0.5 is that area, not a number read off the implementation.
func TestFill_HalfCoveredPixelGetsHalfCoverage(t *testing.T) {
	s := raster.NewSurface(20, 8)
	var p raster.Path
	p.Rect(raster.Point{X: 0, Y: 0}, raster.Point{X: 10.5, Y: 8})
	s.Fill(&p, ink)

	if got := coverageAt(s, 9, 4); !nearly(got, 1, 0.004) {
		t.Errorf("pixel wholly inside the rectangle has coverage %.4f, want 1", got)
	}
	if got := coverageAt(s, 10, 4); !nearly(got, 0.5, 0.01) {
		t.Errorf("pixel crossed halfway by the edge has coverage %.4f, want 0.5", got)
	}
	if got := coverageAt(s, 11, 4); !nearly(got, 0, 0.004) {
		t.Errorf("pixel wholly outside the rectangle has coverage %.4f, want 0", got)
	}
}

// TestBackground_IsAResetRatherThanAComposite pins that Background uses
// draw.Src.
//
// A background painted with draw.Over would be correct for an opaque colour
// and silently wrong for a translucent one, and it would leave whatever was
// drawn before it showing through. The map's background is the first thing
// drawn and it must not depend on what the surface held.
func TestBackground_IsAResetRatherThanAComposite(t *testing.T) {
	s := raster.NewSurface(4, 4)
	var p raster.Path
	p.Rect(raster.Point{}, raster.Point{X: 4, Y: 4})
	s.Fill(&p, color.RGBA{R: 0xff, A: 0xff})

	translucent := color.RGBA{R: 0x00, G: 0x40, B: 0x00, A: 0x80}
	s.Background(translucent)

	want := color.RGBA{G: 0x40, A: 0x80}
	if got := s.RGBA().RGBAAt(2, 2); got != want {
		t.Errorf("after Background the pixel is %+v, want %+v; red from the earlier fill must not survive", got, want)
	}
}

// TestFill_DegenerateInputDrawsNothingAndDoesNotPanic covers the inputs a
// renderer will hand this package the first time a tile contains something
// unexpected.
//
// None of these is a programming error worth a panic. They are what a
// generalised tile produces at the edge of a view: a way that lost all but one
// of its nodes to simplification, a style whose width scaled to zero at a
// shallow zoom, a layer that turned out to be empty.
func TestFill_DegenerateInputDrawsNothingAndDoesNotPanic(t *testing.T) {
	tests := []struct {
		name  string
		build func(p *raster.Path)
	}{
		{"empty path", func(p *raster.Path) {}},
		{"ring of two points", func(p *raster.Path) {
			p.Ring([]raster.Point{{X: 1, Y: 1}, {X: 9, Y: 9}})
		}},
		{"ring of no points", func(p *raster.Path) { p.Ring(nil) }},
		{"zero width stroke", func(p *raster.Path) {
			p.Stroke([]raster.Point{{X: 1, Y: 1}, {X: 9, Y: 9}}, raster.Stroke{Width: 0})
		}},
		{"negative width stroke", func(p *raster.Path) {
			p.Stroke([]raster.Point{{X: 1, Y: 1}, {X: 9, Y: 9}}, raster.Stroke{Width: -4})
		}},
		{"NaN width stroke", func(p *raster.Path) {
			p.Stroke([]raster.Point{{X: 1, Y: 1}, {X: 9, Y: 9}}, raster.Stroke{Width: float32(math.NaN())})
		}},
		{"stroke of no points", func(p *raster.Path) {
			p.Stroke(nil, raster.Stroke{Width: 3})
		}},
		{"stroke of repeated points", func(p *raster.Path) {
			pt := raster.Point{X: 5, Y: 5}
			p.Stroke([]raster.Point{pt, pt, pt}, raster.Stroke{Width: 0})
		}},
		{"circle of zero radius", func(p *raster.Path) {
			p.Circle(raster.Point{X: 5, Y: 5}, 0)
		}},
		{"circle of NaN radius", func(p *raster.Path) {
			p.Circle(raster.Point{X: 5, Y: 5}, float32(math.NaN()))
		}},
		{"line before any move", func(p *raster.Path) {
			p.LineTo(raster.Point{X: 5, Y: 5})
		}},
		{"curve before any move", func(p *raster.Path) {
			p.CubeTo(raster.Point{X: 1}, raster.Point{X: 2}, raster.Point{X: 3})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := raster.NewSurface(10, 10)
			var p raster.Path
			tc.build(&p)
			s.Fill(&p, ink)
			if got := coverageTotal(s); got != 0 {
				t.Errorf("drew %.4f pixels of coverage, want nothing", got)
			}
		})
	}
}

// TestFill_OnAnEmptySurfaceDoesNothing covers a view whose pixel size came out
// zero or negative, which is arithmetic in the caller rather than a drawing
// problem -- but it must not be a crash in the middle of somebody's render.
func TestFill_OnAnEmptySurfaceDoesNothing(t *testing.T) {
	for _, size := range []image.Point{{X: 0, Y: 0}, {X: 0, Y: 10}, {X: 10, Y: 0}, {X: -5, Y: -5}} {
		s := raster.NewSurface(size.X, size.Y)
		if b := s.Bounds(); !b.Empty() {
			t.Errorf("NewSurface(%d, %d) has bounds %v, want an empty rectangle", size.X, size.Y, b)
		}
		var p raster.Path
		p.Rect(raster.Point{}, raster.Point{X: 5, Y: 5})
		s.Background(ink)
		s.Fill(&p, ink)
	}
}

// TestPath_ResetEmptiesWithoutForgettingItsCapacity pins the reuse the
// renderer is expected to do: one Path, reset between layers, rather than one
// per layer.
func TestPath_ResetEmptiesWithoutForgettingItsCapacity(t *testing.T) {
	var p raster.Path
	if !p.Empty() {
		t.Fatal("the zero Path is not empty")
	}
	p.Rect(raster.Point{}, raster.Point{X: 4, Y: 4})
	if p.Empty() {
		t.Fatal("a path with a rectangle in it reports empty")
	}
	p.Reset()
	if !p.Empty() {
		t.Fatal("a path still reports non-empty after Reset")
	}

	s := raster.NewSurface(10, 10)
	s.Fill(&p, ink)
	if got := coverageTotal(s); got != 0 {
		t.Errorf("a reset path drew %.4f pixels of coverage, want nothing", got)
	}
}

// TestFill_GeometryLargerThanTheSurfaceStillFillsIt is the property a
// renderer relies on for every landcover polygon bigger than the view.
//
// Coverage accumulates along each row from its left edge, so an edge to the
// left of the surface has to turn the winding on for the whole row even though
// nothing about it is visible. The rasterizer does that by clamping the
// column it writes into rather than by discarding the edge, and a path
// starting far off-screen must therefore come out solid rather than empty.
func TestFill_GeometryLargerThanTheSurfaceStillFillsIt(t *testing.T) {
	s := raster.NewSurface(32, 32)
	var p raster.Path
	p.Rect(raster.Point{X: -500, Y: -500}, raster.Point{X: 900, Y: 900})
	s.Fill(&p, ink)

	if got := coverageTotal(s); !nearly(got, 32*32, 0.5) {
		t.Errorf("a rectangle enclosing the whole surface covers %.4f of its %d pixels, want all of them", got, 32*32)
	}
}

// TestFill_NonFiniteCoordinatesDrawSomethingHarmlessRatherThanCrashing is not
// a claim that a NaN produces a sensible picture -- there is no sensible
// picture -- only that it produces one at all.
//
// A projection handed a latitude beyond the Mercator cut, or a width divided
// by a zero-sized view, yields infinities that reach here as coordinates. The
// rasterizer converts them to integers with an implementation-defined result
// and draws whatever that is; what matters is that a render in progress is not
// taken down by one bad feature, and that the loop does not become unbounded.
func TestFill_NonFiniteCoordinatesDrawSomethingHarmlessRatherThanCrashing(t *testing.T) {
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	for _, bad := range []raster.Point{{X: nan, Y: 4}, {X: 4, Y: nan}, {X: inf, Y: 4}, {X: 4, Y: -inf}} {
		s := raster.NewSurface(32, 32)
		var p raster.Path
		p.Stroke([]raster.Point{bad, {X: 16, Y: 16}}, raster.Stroke{Width: 3})
		p.Ring([]raster.Point{bad, {X: 4, Y: 4}, {X: 28, Y: 20}})
		p.Circle(bad, 5)
		s.Fill(&p, ink)
	}
}
