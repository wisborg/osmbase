package raster

import "math"

// Stroke is how a polyline becomes a filled outline.
//
// It is a struct rather than a width argument because a style holds one of
// these per road class and passes it through unchanged, and because dashing is
// part of the same decision: a footpath is "1.2 wide, 4 on 3 off", not a width
// with a separate pattern that some other code remembers to apply.
type Stroke struct {
	// Width is the full width of the line in surface pixels, not the half
	// width. A non-positive width draws nothing.
	//
	// A style's width is in TILE pixels and has to be multiplied by the view's
	// scale before it gets here; see the package comment.
	Width float32

	// Dash is alternating on and off lengths in surface pixels, starting with
	// an on length. An empty pattern, or one whose lengths sum to zero, is a
	// solid line.
	//
	// An odd-length pattern repeats with on and off exchanged, as in SVG and
	// PostScript: [4 2 1] is on 4, off 2, on 1, off 4, on 2, off 1. That is
	// somebody else's convention and it is followed rather than invented,
	// because a style written against a web map's dash arrays should mean here
	// what it means there.
	Dash []float32

	// DashPhase is how far into the pattern the line starts, in surface
	// pixels. It is taken modulo the pattern's total length, negative values
	// included, so a phase can be advanced without bound.
	DashPhase float32
}

// Stroke appends the outline of the polyline pts to the path, with round caps
// at both ends and round joins at every vertex.
//
// # Why round, and why there is no alternative
//
// The outline is per segment a quad of the full width, and at every vertex and
// both ends a disc of half the width, all wound the same way and all in one
// path. The fill rule adds and clamps, so the overlaps resolve themselves and
// the union is the stroke. That is the whole stroker.
//
// Round is not a compromise made because joins are hard. OSM splits a road into
// a new way at every attribute change, so one visual road arrives as dozens of
// polylines meeting end to end; with butt caps every one of those meetings
// notches open on a bend, and the notch is in the middle of a road rather than
// at its end, which is where a reader would look for it. A disc of half the
// width fills it exactly. Miter would additionally spike many times the stroke
// width where a trail switches back at a few degrees, so it is not offered.
//
// # Degenerate input
//
// A single point strokes to a dot of the stroke's width. That is not a special
// case bolted on: a round cap at both ends of a zero-length line is a disc, and
// it is also the honest picture of a way with one node. Repeated points are
// skipped, a non-positive width draws nothing, and an empty polyline draws
// nothing.
//
// A polyline that closes -- last point equal to first, which is how a caller
// strokes a polygon's outline -- gets ONE disc where its ends meet rather than
// two. See strokeSolid.
func (p *Path) Stroke(pts []Point, s Stroke) {
	if !(s.Width > 0) || len(pts) == 0 { // the ! also rejects NaN
		return
	}
	if pat, ok := dashPattern(s.Dash); ok {
		dashWalk(pts, pat, s.DashPhase, func(run []Point) {
			p.strokeSolid(run, s.Width)
		})
		return
	}
	p.strokeSolid(pts, s.Width)
}

// strokeSolid outlines one undashed polyline.
func (p *Path) strokeSolid(pts []Point, width float32) {
	if len(pts) == 0 {
		return
	}
	h := width / 2

	// A polyline whose last point repeats its first is a closed ring, and its
	// two ends are one vertex rather than two. Drawing the disc there twice
	// would be free in the interior -- winding clamps -- but not on the disc's
	// own antialiased rim, where two halves of a pixel sum to a whole and the
	// edge goes hard. One corner of a stroked building outline would then be
	// visibly more jagged than the other three.
	//
	// A vertex that coincides with some OTHER vertex, in a figure eight or a
	// road that touches itself, gets two discs and that rim. Detecting it would
	// cost a search per vertex to remove one pixel of aliasing at a junction
	// that is already several shapes deep, which is not a trade worth making;
	// the closed ring is worth it because it is every polygon outline.
	closed := len(pts) > 2 && pts[len(pts)-1] == pts[0]

	// A disc at every vertex, including both ends. Collinear vertices do not
	// need one and a stroker could test for that, but the test costs a
	// normalisation per vertex to save four cubics that land inside geometry
	// already drawn, and the fill rule makes those cubics free of any visual
	// consequence. Cheap and always right beats clever and conditionally
	// right, in a picture where "conditionally" means "on some roads".
	prev := pts[0]
	p.Circle(prev, h)
	for i, q := range pts[1:] {
		if q == prev {
			// A zero-length segment has no direction, so it has no quad. The
			// disc for this vertex is already there, in the same place.
			continue
		}
		p.quad(prev, q, h)
		if !(closed && i == len(pts)-2) {
			p.Circle(q, h)
		}
		prev = q
	}
}

// quad appends the rectangle covering the segment a to b, offset by h either
// side of it.
//
// The corners are emitted in the order that gives positive area whichever way
// the segment runs: the normal is a fixed quarter turn from the direction, so
// reversing the segment rotates the whole quad by half a turn and leaves its
// winding alone. Everything else this package emits is wound the same way, and
// the package comment says what a stray reversal would cost.
func (p *Path) quad(a, b Point, h float32) {
	dx, dy := b.X-a.X, b.Y-a.Y
	length := float32(math.Hypot(float64(dx), float64(dy)))
	if length == 0 {
		return
	}
	nx, ny := -dy/length*h, dx/length*h
	p.MoveTo(Point{a.X - nx, a.Y - ny})
	p.LineTo(Point{b.X - nx, b.Y - ny})
	p.LineTo(Point{b.X + nx, b.Y + ny})
	p.LineTo(Point{a.X + nx, a.Y + ny})
}
