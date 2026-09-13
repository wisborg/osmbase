package raster_test

import (
	"fmt"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// The tests in this file are the reason Surface.Fill takes a whole Path.
//
// A view spans several tiles, and a polygon crossing a tile edge arrives as
// two pieces clipped against it. The pieces abut exactly: along the seam, one
// covers the left fraction of each pixel and the other covers the right
// fraction, and they sum to the whole pixel.
//
// Whether that is what the picture shows depends entirely on whether the two
// pieces went into one rasterizer pass or two, and the difference is a 24%
// hairline along every tile edge in the map -- invisible on a view that sits
// inside one tile, obvious on a view that crosses one. See
// docs/architecture.md, trap T1.

// seamCoverage fills two rectangles abutting at x = 10.5 and returns the
// coverage of the pixel the seam runs through. If oneFill is false they are
// composited as two separate fills, which is the mistake.
func seamCoverage(w, h int, oneFill bool) float64 {
	s := raster.NewSurface(w, h)
	left := func(p *raster.Path) {
		p.Rect(raster.Point{X: 0, Y: 0}, raster.Point{X: 10.5, Y: float32(h)})
	}
	right := func(p *raster.Path) {
		p.Rect(raster.Point{X: 10.5, Y: 0}, raster.Point{X: 20, Y: float32(h)})
	}
	var p raster.Path
	if oneFill {
		left(&p)
		right(&p)
		s.Fill(&p, ink)
	} else {
		left(&p)
		s.Fill(&p, ink)
		p.Reset()
		right(&p)
		s.Fill(&p, ink)
	}
	return coverageAt(s, 10, h/2)
}

// TestFill_AbuttingEdgesInOnePassSumToFullCoverage is the seam regression
// test.
//
// Each rectangle covers exactly half of the pixel spanning [10, 11), so their
// coverages are 0.5 and 0.5 and the pixel is covered once over. Anything less
// than full coverage here is a hairline drawn along the length of every tile
// boundary in the rendered map.
//
// It runs at two surface sizes deliberately. x/image/vector switches from
// fixed point to floating point arithmetic above 512 pixels of width or
// height, and those are two separate implementations of the accumulator in two
// files. The design note that predicted this behaviour was written from
// reading the floating point one; the small case is the only thing that checks
// the fixed point one, and it is the size at which most of these tests run.
func TestFill_AbuttingEdgesInOnePassSumToFullCoverage(t *testing.T) {
	for _, size := range []int{64, 600} {
		t.Run(fmt.Sprintf("%dpx", size), func(t *testing.T) {
			if got := seamCoverage(size, size, true); !nearly(got, 1, 0.004) {
				t.Errorf("the pixel on the seam has coverage %.4f, want 1; two halves of one polygon must not leave a hairline", got)
			}
		})
	}
}

// TestFill_AbuttingEdgesInSeparatePassesLeaveAHairline pins the failure the
// test above exists to catch, so that its expected value is a derivation
// rather than a mystery.
//
// Compositing is not addition. Two fills of 0.5 coverage give
// 0.5 + 0.5*(1-0.5) = 0.75, so a quarter of the ink is missing along the
// seam, in a line one pixel wide, in a colour that is a legitimate shade of
// the thing being drawn. If this test ever starts reporting full coverage,
// the fill rule has changed and the one-path rule can be relaxed; until then
// it is a statement about what the alternative costs.
func TestFill_AbuttingEdgesInSeparatePassesLeaveAHairline(t *testing.T) {
	got := seamCoverage(64, 64, false)
	if !nearly(got, 0.75, 0.01) {
		t.Errorf("two separate fills gave the seam pixel coverage %.4f, want 0.75", got)
	}
}

// TestFill_DuplicateGeometryDoesNotDarkenTheInterior is the property that lets
// a stroke be a heap of overlapping quads and discs, and lets a feature that
// appears in two neighbouring tiles' buffers be drawn twice without being
// noticed.
//
// Winding sums and then clamps to one, so two copies of a shape wound the same
// way fill exactly as one does wherever the shape is more than a pixel thick.
func TestFill_DuplicateGeometryDoesNotDarkenTheInterior(t *testing.T) {
	ring := []raster.Point{{X: 4, Y: 4}, {X: 28, Y: 4}, {X: 28, Y: 28}, {X: 4, Y: 28}}

	once := raster.NewSurface(32, 32)
	var p raster.Path
	p.Ring(ring)
	once.Fill(&p, ink)

	twice := raster.NewSurface(32, 32)
	p.Reset()
	p.Ring(ring)
	p.Ring(ring)
	twice.Fill(&p, ink)

	if a, b := coverageAt(once, 16, 16), coverageAt(twice, 16, 16); a != b || !nearly(a, 1, 0.004) {
		t.Errorf("interior coverage is %.4f drawn once and %.4f drawn twice, want 1 for both", a, b)
	}
}

// TestFill_DuplicateGeometryHardensTheAntialiasedEdge records the part of that
// property that is NOT true, because the obvious summary of it -- "duplicate
// geometry is free" -- is only true away from the edges.
//
// An edge pixel is half covered by each copy, and a half and a half sum to a
// whole before the clamp ever applies. So the duplicate does not darken the
// shape, it hardens its outline: the antialiasing disappears and the edge goes
// from smooth to jagged.
//
// This is the same arithmetic as the seam property above and it cannot be had
// one way without the other. It matters for the renderer because a feature
// that appears in two tiles' buffers is exactly this case, so deduplicating
// features by id across tiles is a cosmetic improvement with a real effect,
// not a pure optimisation.
func TestFill_DuplicateGeometryHardensTheAntialiasedEdge(t *testing.T) {
	half := []raster.Point{{X: 0, Y: 0}, {X: 10.5, Y: 0}, {X: 10.5, Y: 32}, {X: 0, Y: 32}}

	once := raster.NewSurface(32, 32)
	var p raster.Path
	p.Ring(half)
	once.Fill(&p, ink)

	twice := raster.NewSurface(32, 32)
	p.Reset()
	p.Ring(half)
	p.Ring(half)
	twice.Fill(&p, ink)

	if got := coverageAt(once, 10, 16); !nearly(got, 0.5, 0.01) {
		t.Fatalf("one copy gives the edge pixel coverage %.4f, want 0.5", got)
	}
	if got := coverageAt(twice, 10, 16); !nearly(got, 1, 0.004) {
		t.Errorf("two copies give the edge pixel coverage %.4f, want 1: the edge hardens rather than darkening", got)
	}
}
