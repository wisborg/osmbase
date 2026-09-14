package render

// Culling and clipping, which the rasterizer deliberately does not do.
//
// raster.Surface.Fill draws geometry outside the surface correctly and at a
// cost proportional to how far outside it is rather than to anything visible: a
// segment beginning a million pixels above the surface is walked scanline by
// scanline down to the top edge, which was measured at 12 ms, and one beginning
// a billion costs 4.5 seconds. That is not an exotic input here. A vector tile
// carries a buffer of geometry from beyond its own edge, a view at a deep zoom
// puts most of every tile off the surface, and an ancestor tile drawn in place
// of a missing one spans 2^k times the surface. Clipping is therefore not an
// optimisation in this package; it is what makes it terminate.
//
// It lives here rather than in raster because clipping CHANGES geometry, and
// only something that knows what the geometry means can say how. A ring must be
// clipped as a ring, so that the result still encloses the same area and still
// winds the same way; a polyline must be clipped into runs, so that a road
// leaving and re-entering the view is two strokes and not one straight line
// across the middle of it. A rasterizer handed both would have to guess.

// pt is a point in surface pixels, in float64.
//
// raster.Point is float32, and the conversion happens on the way INTO the path
// rather than here: a tile-local integer at a deep zoom multiplied into surface
// pixels can exceed float32's exact integer range, and clipping in float32
// would round the clip boundary itself. The numbers that survive clipping are
// within a pixel or two of the surface, where float32 is exact to a small
// fraction of a pixel.
type pt struct{ X, Y float64 }

// box is an axis-aligned rectangle in surface pixels.
type box struct {
	MinX, MinY, MaxX, MaxY float64
}

func (b box) empty() bool { return !(b.MaxX > b.MinX) || !(b.MaxY > b.MinY) }

func (b box) width() float64  { return b.MaxX - b.MinX }
func (b box) height() float64 { return b.MaxY - b.MinY }

// area is the rectangle's area, and 0 for an empty one.
//
// It is what coverage is measured in. Coverage is deliberately a question about
// rectangles and never about ink: an ocean tile draws almost nothing and is
// fully covered, and measuring "did any geometry land here" would report every
// bay as missing data. See docs/architecture.md, trap T9.
func (b box) area() float64 {
	if b.empty() {
		return 0
	}
	return b.width() * b.height()
}

func (b box) intersect(o box) box {
	return box{
		MinX: max(b.MinX, o.MinX),
		MinY: max(b.MinY, o.MinY),
		MaxX: min(b.MaxX, o.MaxX),
		MaxY: min(b.MaxY, o.MaxY),
	}
}

// inflate grows the box by d on every side.
func (b box) inflate(d float64) box {
	return box{MinX: b.MinX - d, MinY: b.MinY - d, MaxX: b.MaxX + d, MaxY: b.MaxY + d}
}

// clamp snaps a point onto the box.
//
// Liang-Barsky computes the surviving endpoints from a parameter along the
// segment, so an endpoint meant to land exactly on an edge can come out an ulp
// outside it -- {70, 100.00000000000003} for a box ending at 100. That is
// invisible in the picture and it is not invisible in the contract this clip
// exists to keep: the next tile's piece of the same road is clipped to the SAME
// edge value, and the two round caps close the join only if both are centred on
// the same point. Four comparisons make the shared edge exact.
func (b box) clamp(p pt) pt {
	return pt{
		X: min(max(p.X, b.MinX), b.MaxX),
		Y: min(max(p.Y, b.MinY), b.MaxY),
	}
}

// clipper holds the scratch buffers clipping works in, so that a render that
// clips a few hundred thousand rings allocates a few times rather than a few
// hundred thousand.
//
// Every method's result is valid only until the next call on the same clipper,
// which is why nothing here returns anything a caller might keep: the results
// go straight into a raster.Path, which copies the points it is given.
type clipper struct {
	a, b []pt
	run  []pt
}

// ring clips a closed ring to the box by Sutherland-Hodgman, one edge of the
// box at a time.
//
// # Why this algorithm and not a general polygon clip
//
// Sutherland-Hodgman against a convex window has one well-known artifact: a
// concave polygon clipped into several pieces comes back as one ring with
// zero-width bridges running along the window edge between the pieces. Under
// most fill rules those bridges are visible seams. Under this rasterizer's --
// absolute accumulated winding -- each bridge is traversed once in each
// direction and its contributions cancel exactly, so the picture is the union
// of the pieces with nothing drawn between them. The fill rule that forces one
// rasterizer per layer is the same one that makes the cheap clip correct here.
//
// # Winding is preserved, and has to be
//
// Every vertex the algorithm emits is either an input vertex or a point on the
// segment between two consecutive input vertices, in input order. So a ring
// comes out wound the way it went in, and an exterior and its holes keep their
// opposite directions and go on cutting holes. Reversing one would fill the
// hole solid; see docs/architecture.md, trap T13.
//
// A result of fewer than three points encloses no area and is returned as nil,
// because raster.Path.Ring would drop it anyway and a caller checking for
// "clipped away" should not have to know that.
func (c *clipper) ring(in []pt, b box) []pt {
	if len(in) < 3 || b.empty() {
		return nil
	}
	// Two buffers, swapped after every edge, so that each pass reads one and
	// writes the other and neither is ever both. Reusing one would overwrite
	// the input under the loop.
	src := in
	for side := 0; side < 4; side++ {
		c.a = clipRingSide(c.a, src, b, side)
		if len(c.a) < 3 {
			// Wholly outside this edge of the box, so wholly outside the box.
			// Returning early matters for cost as well as correctness: this is
			// the common case for a tile's buffer geometry and for every
			// feature of an ancestor tile outside the missing child's square.
			return nil
		}
		c.a, c.b = c.b, c.a
		src = c.b
	}
	return src
}

// clipRingSide clips a ring against one half-plane of the box. side numbers the
// four edges: 0 west, 1 east, 2 north, 3 south.
func clipRingSide(out, in []pt, b box, side int) []pt {
	out = out[:0]
	prev := in[len(in)-1]
	prevIn := insideSide(prev, b, side)
	for _, cur := range in {
		curIn := insideSide(cur, b, side)
		if curIn != prevIn {
			out = append(out, crossSide(prev, cur, b, side))
		}
		if curIn {
			out = append(out, cur)
		}
		prev, prevIn = cur, curIn
	}
	return out
}

func insideSide(p pt, b box, side int) bool {
	switch side {
	case 0:
		return p.X >= b.MinX
	case 1:
		return p.X <= b.MaxX
	case 2:
		return p.Y >= b.MinY
	default:
		return p.Y <= b.MaxY
	}
}

// crossSide returns the point where the segment a-b meets one edge of the box.
//
// It is only ever called when a and b are on opposite sides of that edge, so
// the denominator cannot be zero: two points with the same x cannot straddle a
// vertical edge.
func crossSide(a, b pt, bx box, side int) pt {
	switch side {
	case 0:
		return pt{X: bx.MinX, Y: lerp(a.Y, b.Y, (bx.MinX-a.X)/(b.X-a.X))}
	case 1:
		return pt{X: bx.MaxX, Y: lerp(a.Y, b.Y, (bx.MaxX-a.X)/(b.X-a.X))}
	case 2:
		return pt{X: lerp(a.X, b.X, (bx.MinY-a.Y)/(b.Y-a.Y)), Y: bx.MinY}
	default:
		return pt{X: lerp(a.X, b.X, (bx.MaxY-a.Y)/(b.Y-a.Y)), Y: bx.MaxY}
	}
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// line clips a polyline to the box and calls emit for each run that survives.
//
// A polyline that leaves the box and comes back is several runs, not one. That
// is the whole reason this is not a point filter: joining the surviving points
// would draw a straight road across the view between the place one left it and
// the place another rejoined, which is a road that does not exist and looks
// exactly like one that does.
//
// The slice handed to emit is REUSED between calls, as raster's own dash walk
// does and for the same reason: the caller strokes it immediately, and a fresh
// slice per run is one allocation per road per tile.
func (c *clipper) line(in []pt, b box, emit func([]pt)) {
	if len(in) < 2 || b.empty() {
		return
	}
	run := c.run[:0]
	flush := func() {
		if len(run) >= 2 {
			emit(run)
		}
		run = run[:0]
	}
	for i := 1; i < len(in); i++ {
		a, z, ok := clipSegment(in[i-1], in[i], b)
		if !ok {
			flush()
			continue
		}
		// A run continues only where the clipped segment starts exactly where
		// the last one ended. Anywhere else the polyline has been outside in
		// between, and the two pieces are separate strokes.
		if len(run) > 0 && run[len(run)-1] != a {
			flush()
		}
		if len(run) == 0 {
			run = append(run, a)
		}
		run = append(run, z)
	}
	flush()
	c.run = run
}

// clipSegment clips one segment to the box by Liang-Barsky, returning the
// surviving piece.
//
// Liang-Barsky rather than Cohen-Sutherland because it computes the two
// parameters directly instead of iterating, and because the parameters are what
// is wanted: the endpoints of the surviving piece, exactly on the box edge, so
// that the piece from the neighbouring tile starts at the same point and the
// two strokes meet with no gap.
func clipSegment(a, b pt, bx box) (pt, pt, bool) {
	dx, dy := b.X-a.X, b.Y-a.Y
	t0, t1 := 0.0, 1.0
	if !clipT(-dx, a.X-bx.MinX, &t0, &t1) ||
		!clipT(dx, bx.MaxX-a.X, &t0, &t1) ||
		!clipT(-dy, a.Y-bx.MinY, &t0, &t1) ||
		!clipT(dy, bx.MaxY-a.Y, &t0, &t1) {
		return pt{}, pt{}, false
	}
	return bx.clamp(pt{X: a.X + t0*dx, Y: a.Y + t0*dy}), bx.clamp(pt{X: a.X + t1*dx, Y: a.Y + t1*dy}), true
}

// clipT narrows the surviving parameter interval for one boundary, and reports
// false when nothing survives.
//
// p is the segment's rate of approach to the boundary and q its signed distance
// from it at t=0. p == 0 is a segment parallel to that boundary: it is either
// entirely inside it or entirely outside, which q alone decides.
func clipT(p, q float64, t0, t1 *float64) bool {
	if p == 0 {
		return q >= 0
	}
	r := q / p
	if p < 0 {
		if r > *t1 {
			return false
		}
		if r > *t0 {
			*t0 = r
		}
		return true
	}
	if r < *t0 {
		return false
	}
	if r < *t1 {
		*t1 = r
	}
	return true
}
