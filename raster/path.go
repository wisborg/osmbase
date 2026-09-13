package raster

import "golang.org/x/image/vector"

// verb is one pen operation. The points it consumes live in Path.pts.
type verb uint8

const (
	verbMove verb = iota // 1 point
	verbLine             // 1 point
	verbCube             // 3 points: two controls and an end
)

// Path is an append-only collection of subpaths, all of which will be filled
// in one colour.
//
// It is a Path rather than a Shape because it is meant to hold everything of
// one ink in one view: every building in the frame, or every residential road
// from every tile, stroked. Filling that as one path is what makes tile seams
// disappear (see Surface.Fill), and holding it as two flat slices rather than
// a slice of subpaths is what makes appending a few hundred thousand points to
// it cheap.
//
// The zero Path is ready to use. Reset keeps the allocated capacity, so a
// renderer drawing several layers should reuse one Path rather than make a new
// one per layer.
type Path struct {
	verbs []verb
	pts   []Point
}

// Reset empties the path but keeps its capacity for the next layer.
func (p *Path) Reset() {
	p.verbs = p.verbs[:0]
	p.pts = p.pts[:0]
}

// Empty reports whether the path would draw nothing.
func (p *Path) Empty() bool { return len(p.verbs) == 0 }

// MoveTo starts a new subpath at pt.
//
// There is deliberately no ClosePath: every subpath is closed when it is
// filled, and a subpath that a caller could leave open would be a loaded gun.
// The rasterizer accumulates coverage across the whole pixel buffer in one
// linear sweep, rows end to end, so an unbalanced subpath does not leak a
// little coverage to its right -- it leaks into every row below it as well,
// and the picture is a wash rather than a wrong edge.
func (p *Path) MoveTo(pt Point) {
	p.verbs = append(p.verbs, verbMove)
	p.pts = append(p.pts, pt)
}

// LineTo adds a straight segment from the pen to pt.
//
// With no preceding MoveTo it starts a subpath at pt instead, which is not a
// meaningful drawing operation but is the behaviour that cannot panic. Every
// entry point here treats malformed input as "draw nothing surprising",
// because the input is generated from somebody's tile data and a library that
// panics on a degenerate polyline takes the whole render down for one broken
// way.
func (p *Path) LineTo(pt Point) {
	if len(p.verbs) == 0 {
		p.MoveTo(pt)
		return
	}
	p.verbs = append(p.verbs, verbLine)
	p.pts = append(p.pts, pt)
}

// CubeTo adds a cubic Bezier from the pen via the control points b and c to d.
//
// The curve is flattened by the rasterizer, at a tolerance it chooses from the
// curve's own deviation, so this stays resolution independent: the same call
// subdivides further on a 4K frame than on a 1080p one. Flattening it here to
// a fixed number of segments would put 1080p's segment count on the 4K frame.
func (p *Path) CubeTo(b, c, d Point) {
	if len(p.verbs) == 0 {
		p.MoveTo(d)
		return
	}
	p.verbs = append(p.verbs, verbCube)
	p.pts = append(p.pts, b, c, d)
}

// Ring adds a closed subpath through pts. The closing segment is implicit, so
// a triangle is three points and not four -- the convention a decoded vector
// tile ring already uses, which means a ring can be handed straight over.
//
// The order of pts is preserved, and that is the whole mechanism for holes.
// Coverage is absolute accumulated winding, so a ring wound against the rest
// of the path subtracts and cuts a hole in it, and a ring wound with the rest
// of the path adds and fills solid. An island in a lake and a hole in a lake
// differ by nothing except this order, so normalising it here would quietly
// fill every hole in the map. Put every ring of one polygon into one Path:
// split across two, the hole has nothing to subtract from and fills.
//
// Fewer than three points encloses no area and is dropped.
func (p *Path) Ring(pts []Point) {
	if len(pts) < 3 {
		return
	}
	p.MoveTo(pts[0])
	for _, q := range pts[1:] {
		p.LineTo(q)
	}
}

// Rect adds an axis-aligned rectangle, wound the same way as everything else
// this package generates so that it adds to a path rather than cutting into
// one.
func (p *Path) Rect(min, max Point) {
	p.Ring([]Point{
		{min.X, min.Y},
		{max.X, min.Y},
		{max.X, max.Y},
		{min.X, max.Y},
	})
}

// kappa is the control-point distance, as a fraction of the radius, that makes
// a cubic Bezier approximate a quarter circle. It is 4/3 * (sqrt(2)-1), and
// the approximation is within about 0.02% of the true arc -- two thousandths
// of a pixel on a ten pixel radius, which is far below one step of an 8-bit
// coverage value.
const kappa = 0.5522847498307936

// Circle adds a circle of radius r about c, as four cubic arcs.
//
// It is wound positively, matching Rect, Ring's exteriors and the stroke
// quads. This matters more than it looks: a stroke puts a circle at every
// joint ON TOP of the segment quads it joins, and a circle wound the other way
// would subtract from them and punch a hole at every vertex of every road --
// in the shape of a disc, which reads as a dotted line rather than as a
// winding bug.
//
// A non-positive radius adds nothing.
func (p *Path) Circle(c Point, r float32) {
	if !(r > 0) { // also rejects NaN
		return
	}
	k := r * kappa
	// Angle increasing, with y down the screen, is positive by the surveyor's
	// formula. Right, down, left, up.
	p.MoveTo(Point{c.X + r, c.Y})
	p.CubeTo(Point{c.X + r, c.Y + k}, Point{c.X + k, c.Y + r}, Point{c.X, c.Y + r})
	p.CubeTo(Point{c.X - k, c.Y + r}, Point{c.X - r, c.Y + k}, Point{c.X - r, c.Y})
	p.CubeTo(Point{c.X - r, c.Y - k}, Point{c.X - k, c.Y - r}, Point{c.X, c.Y - r})
	p.CubeTo(Point{c.X + k, c.Y - r}, Point{c.X + r, c.Y - k}, Point{c.X + r, c.Y})
}

// replay walks the path into a rasterizer, closing every subpath.
//
// The closing is here rather than at construction so that a subpath cannot be
// recorded half-finished: whatever a caller appended, what reaches the
// rasterizer is a set of closed contours whose coverage deltas sum to zero on
// every scanline. See MoveTo for what an unbalanced one does.
func (p *Path) replay(z *vector.Rasterizer) {
	i := 0
	open := false
	for _, v := range p.verbs {
		switch v {
		case verbMove:
			if open {
				z.ClosePath()
			}
			z.MoveTo(p.pts[i].X, p.pts[i].Y)
			i++
			open = true
		case verbLine:
			z.LineTo(p.pts[i].X, p.pts[i].Y)
			i++
		case verbCube:
			z.CubeTo(
				p.pts[i].X, p.pts[i].Y,
				p.pts[i+1].X, p.pts[i+1].Y,
				p.pts[i+2].X, p.pts[i+2].Y,
			)
			i += 3
		}
	}
	if open {
		z.ClosePath()
	}
}
