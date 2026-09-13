package raster_test

import (
	"math/rand/v2"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// roads generates a deterministic set of polylines of the shape a generalised
// tile produces: short, few vertices, spread over the surface.
//
// The seed is fixed because a benchmark whose input varies between runs
// measures two things at once, and because everything in this package has to
// be reproducible anyway.
func roads(n, size int) [][]raster.Point {
	r := rand.New(rand.NewPCG(1, 2))
	out := make([][]raster.Point, n)
	for i := range out {
		x := float32(r.IntN(size))
		y := float32(r.IntN(size))
		line := make([]raster.Point, 6)
		for j := range line {
			line[j] = raster.Point{X: x, Y: y}
			x += float32(r.IntN(41) - 20)
			y += float32(r.IntN(41) - 20)
		}
		out[i] = line
	}
	return out
}

// BenchmarkStrokeAndFill_OneLayer is the shape of the renderer's inner loop:
// every feature of one layer appended to one Path and filled once.
//
// Measured on an Apple M1 Pro at 1920x1080 with 2000 roads of six points:
// 18 ms, of which building the outlines is 0.7 and the rest is flattening,
// accumulating and compositing two million pixels. So the cost is dominated by
// the frame rather than by the features, and caching stroked paths between
// layers would save almost nothing.
//
// That is per LAYER and per render, not per frame -- the basemap is resolved
// once and reused across a video, see docs/architecture.md, trap T8 -- so a
// dozen layers is a fifth of a second at the start of a render. It is recorded
// here because the architecture lists rasterization time as an open question
// and this is the first number for it.
func BenchmarkStrokeAndFill_OneLayer(b *testing.B) {
	const w, h = 1920, 1080
	lines := roads(2000, w)
	s := raster.NewSurface(w, h)
	var p raster.Path

	for b.Loop() {
		p.Reset()
		for _, line := range lines {
			p.Stroke(line, raster.Stroke{Width: 2.4})
		}
		s.Fill(&p, ink)
	}
}

// BenchmarkStrokeAndFill_PerFeature is the same work done the wrong way, one
// fill per feature.
//
// It is here to put a number beside the correctness argument for one pass per
// ink. The seam artefact is the reason to do it the other way; this is what it
// would also cost, because every fill accumulates and composites the whole
// frame however small the shape in it. At 200 roads it runs 9.8 ms per feature
// against 9 microseconds, which is to say a layer would take minutes.
func BenchmarkStrokeAndFill_PerFeature(b *testing.B) {
	const w, h = 1920, 1080
	lines := roads(200, w)
	s := raster.NewSurface(w, h)
	var p raster.Path

	for b.Loop() {
		for _, line := range lines {
			p.Reset()
			p.Stroke(line, raster.Stroke{Width: 2.4})
			s.Fill(&p, ink)
		}
	}
}

// BenchmarkStroke_PathConstructionOnly separates building the outline from
// rasterizing it, because they have different answers to "can this be cached".
func BenchmarkStroke_PathConstructionOnly(b *testing.B) {
	lines := roads(2000, 1920)
	var p raster.Path

	for b.Loop() {
		p.Reset()
		for _, line := range lines {
			p.Stroke(line, raster.Stroke{Width: 2.4})
		}
	}
}
