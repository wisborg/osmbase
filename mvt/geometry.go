package mvt

import "fmt"

// Geometry commands. A geometry is a flat list of uint32: a command integer
// carrying an id in its low three bits and a repeat count in the rest,
// followed by that many repetitions of the command's parameters.
const (
	cmdMoveTo    = 1
	cmdLineTo    = 2
	cmdClosePath = 7
)

// coordinateHeadroom is how many tile widths outside its own tile a
// coordinate may lie, and maxCoordinate is the ceiling that bound is clamped
// to whatever the extent is.
//
// A tile carries a buffer of geometry from beyond its own edge, so
// coordinates outside [0, Extent) are normal -- but a real buffer is a few
// hundred units at an extent of 4096, so 256 tile widths is four orders of
// magnitude of headroom and still refuses anything a damaged stream produces.
//
// The ceiling is what makes Ring.Winding provably exact rather than probably
// exact. With every coordinate inside 2^20 of the origin, the translated cross
// products in the surveyor's formula stay below 2^43 and a ring of up to a
// million points accumulates without overflowing an int64. Raise this and that
// guarantee goes with it, quietly: an overflowed area flips the sign, and the
// sign is what decides whether a ring is a hole.
const (
	coordinateHeadroom = 256
	maxCoordinate      = 1 << 20
)

// coordinateBound returns how far from the origin a coordinate of a layer with
// this extent may lie.
func coordinateBound(extent uint32) int64 {
	bound := int64(extent) * coordinateHeadroom
	if bound > maxCoordinate {
		return maxCoordinate
	}
	return bound
}

// Point is a tile-local integer coordinate. The origin is the TOP LEFT of the
// tile, x runs right and y runs DOWN, and the tile is Layer.Extent units
// square -- so these are already in the direction screen pixels run and
// nothing downstream needs a flip.
//
// Coordinates are not clamped to [0, Extent). A tile carries a buffer of
// geometry from beyond its own edge so that a line crossing the edge can be
// stroked without a notch, and clamping that away would put a false vertex on
// every road that leaves the tile.
type Point struct {
	X, Y int32
}

// Ring is one closed ring of a polygon. The closing point is IMPLICIT: a
// triangle is three points, not four. The wire format says so with a ClosePath
// command rather than by repeating the first point, and keeping it implicit
// means a ring cannot be half-closed.
type Ring []Point

// Winding reports the direction a ring is wound: ExteriorWinding, HoleWinding,
// or 0 for a ring that encloses no area.
//
// It is computed from twice the signed area by the surveyor's formula, in
// integers, with the ring translated so its first point is the origin. Twice,
// because the halving is the only part that is not exact and the sign is all
// this needs; in integers, because in floating point the sign of a nearly
// degenerate ring would depend on rounding, and that sign is what decides
// whether the ring is a hole.
//
// The translation is not an optimisation. It is what keeps the cross products
// small: the formula is translation-invariant, so the sign is unchanged, and
// subtracting the first point leaves differences rather than absolute
// coordinates. With every coordinate within a million units of the first --
// which is 256 tile widths at the usual extent of 4096, and far beyond any
// buffer a producer writes -- each term is below 2^43 and a ring of up to a
// million points accumulates exactly in an int64.
//
// That bound is not a hope about real data: decodeGeometry refuses a
// coordinate outside it, so every ring this package produces satisfies it. The
// earlier version of this comment reasoned that a ring at the extremes of
// int32 "cannot come from a tile anybody wrote, only from damaged bytes", which
// is exactly the input a decoder exists to survive -- a square at (2^30, 2^30)
// overflows the sum and reports an exterior as a hole. The check moved to the
// decode, where it costs one comparison per vertex, instead of 128-bit
// arithmetic in the hottest loop here.
//
// A Ring a CALLER assembled by hand is outside that guarantee, and the same
// bound applies to it.
func (r Ring) Winding() int {
	if len(r) < 3 {
		return 0
	}
	ox, oy := int64(r[0].X), int64(r[0].Y)
	var area2 int64
	for i := range r {
		a := r[i]
		b := r[(i+1)%len(r)]
		ax, ay := int64(a.X)-ox, int64(a.Y)-oy
		bx, by := int64(b.X)-ox, int64(b.Y)-oy
		area2 += ax*by - bx*ay
	}
	switch {
	case area2 > 0:
		return ExteriorWinding
	case area2 < 0:
		return HoleWinding
	}
	return 0
}

// reverse returns the ring wound the other way.
func (r Ring) reverse() Ring {
	out := make(Ring, len(r))
	for i, p := range r {
		out[len(r)-1-i] = p
	}
	return out
}

// ExteriorWinding and HoleWinding are the directions this decoder guarantees
// for every ring it returns.
//
// The values follow the vector tile specification, where an exterior ring has
// positive area by the surveyor's formula -- which, with y running down the
// screen, is a ring drawn clockwise. What the decoder adds is that the
// guarantee holds whatever the producer wrote: see normalisePolygons.
const (
	ExteriorWinding = 1
	HoleWinding     = -1
)

// Polygon is one filled area: an outer boundary and the rings cut out of it.
//
// The exterior and its holes are separate fields rather than a list whose
// first element is special, because the two are not interchangeable and the
// consumer of this package must put all of them into ONE rasterizer path. The
// rasterizer takes the absolute accumulated winding, so an exterior wound
// clockwise and a hole wound anticlockwise cancel to zero inside the hole; a
// hole rasterized on its own, or wound the same way as its exterior, fills
// solid instead. See docs/architecture.md, trap T13.
type Polygon struct {
	Exterior Ring
	Holes    []Ring
}

// Geometry holds a feature's decoded geometry. Exactly one of the three fields
// is populated, chosen by the feature's Type; the others are nil. A feature
// whose type is GeomUnknown has none of them, which is a feature this decoder
// could read but cannot interpret rather than an error.
type Geometry struct {
	// Points is every point of a POINT feature. A multipoint is several
	// points in this one slice.
	Points []Point
	// Lines is one slice of points per part of a LINESTRING feature.
	Lines [][]Point
	// Polygons is one entry per filled area of a POLYGON feature.
	Polygons []Polygon
}

// Empty reports whether the geometry has nothing in it.
func (g Geometry) Empty() bool {
	return len(g.Points) == 0 && len(g.Lines) == 0 && len(g.Polygons) == 0
}

// decodeGeometry turns a feature's command stream into geometry.
//
// The cursor starts at (0, 0) and PERSISTS across commands within the feature,
// so every parameter is a delta from wherever the last one left it. ClosePath
// does not move the cursor: the next ring's MoveTo is a delta from the last
// point of the previous ring, not from that ring's first point. Getting that
// wrong displaces every ring after the first by the width of the one before
// it, which on real data looks like a plausible map that does not match the
// road underneath.
func decodeGeometry(t GeomType, g []uint32, extent uint32) (Geometry, error) {
	if t == GeomUnknown {
		return Geometry{}, nil
	}
	var (
		out    Geometry
		cx, cy int32
		rings  []Ring  // POLYGON: every ring in the order it was written
		cur    []Point // the part being built
		i      int
	)
	bound := coordinateBound(extent)

	// step advances the cursor by count deltas and appends each new position
	// to the part being built. MoveTo and LineTo differ in what they mean and
	// not in what they do to the cursor, so they share this: two copies ten
	// lines apart is two places a bounds check has to be added and one place
	// it gets forgotten.
	step := func(count int) error {
		for n := 0; n < count; n++ {
			// Accumulated in int64 so that the check happens BEFORE the value
			// is narrowed. Adding in int32 and testing afterwards tests the
			// wrapped number, which is back inside the bound and looks like a
			// perfectly ordinary vertex.
			nx := int64(cx) + int64(unzigzag32(g[i]))
			ny := int64(cy) + int64(unzigzag32(g[i+1]))
			if nx < -bound || nx > bound || ny < -bound || ny > bound {
				return fmt.Errorf("a vertex at (%d, %d) is more than %d units from the origin, and a tile of extent %d cannot reach there", nx, ny, bound, extent)
			}
			i += 2
			cx, cy = int32(nx), int32(ny)
			cur = append(cur, Point{X: cx, Y: cy})
		}
		return nil
	}

	// flush ends the part currently being built.
	flush := func() error {
		if cur == nil {
			return nil
		}
		switch t {
		case GeomPoint:
			out.Points = append(out.Points, cur...)
		case GeomLineString:
			if len(cur) < 2 {
				return fmt.Errorf("a linestring part has %d points", len(cur))
			}
			out.Lines = append(out.Lines, cur)
		case GeomPolygon:
			rings = append(rings, Ring(cur))
		}
		cur = nil
		return nil
	}

	for i < len(g) {
		cmd := g[i] & 0x7
		count := int(g[i] >> 3)
		i++
		switch cmd {
		case cmdMoveTo, cmdLineTo:
			if count == 0 {
				return Geometry{}, fmt.Errorf("a %s command repeats zero times", commandName(cmd))
			}
			if len(g)-i < 2*count {
				return Geometry{}, fmt.Errorf("a %s command claims %d points but only %d parameters follow", commandName(cmd), count, len(g)-i)
			}
		case cmdClosePath:
			if count != 1 {
				return Geometry{}, fmt.Errorf("a ClosePath command repeats %d times, and it must repeat exactly once", count)
			}
		default:
			return Geometry{}, fmt.Errorf("command id %d is not one of MoveTo, LineTo or ClosePath", cmd)
		}

		switch cmd {
		case cmdMoveTo:
			// A MoveTo always begins a new part. For a point geometry the
			// whole command is one part; for a line or a ring it is the first
			// vertex and the count must be one.
			if t != GeomPoint && count != 1 {
				return Geometry{}, fmt.Errorf("a MoveTo starting a %s moves %d times, and it must move exactly once", t, count)
			}
			// A ring ends at its ClosePath and nowhere else. Letting a new
			// MoveTo close the previous ring would accept mid-stream what the
			// end-of-stream check below already refuses, which is the same
			// omission treated two ways depending on where in the feature it
			// happened.
			if t == GeomPolygon && cur != nil {
				return Geometry{}, fmt.Errorf("a polygon ring is not closed: a new ring starts after %d points with no ClosePath", len(cur))
			}
			if err := flush(); err != nil {
				return Geometry{}, err
			}
			if err := step(count); err != nil {
				return Geometry{}, err
			}
		case cmdLineTo:
			if t == GeomPoint {
				return Geometry{}, fmt.Errorf("a point feature has a LineTo command")
			}
			if cur == nil {
				return Geometry{}, fmt.Errorf("a LineTo command arrives before any MoveTo")
			}
			if err := step(count); err != nil {
				return Geometry{}, err
			}
		case cmdClosePath:
			if t != GeomPolygon {
				return Geometry{}, fmt.Errorf("a %s feature has a ClosePath command", t)
			}
			if cur == nil {
				return Geometry{}, fmt.Errorf("a ClosePath command arrives before any MoveTo")
			}
			// The ring closes back to its first point, and the cursor stays
			// where the last LineTo left it.
			if err := flush(); err != nil {
				return Geometry{}, err
			}
		}
	}

	if t == GeomPolygon && cur != nil {
		return Geometry{}, fmt.Errorf("a polygon ring is not closed: the geometry ends after %d points with no ClosePath", len(cur))
	}
	if err := flush(); err != nil {
		return Geometry{}, err
	}
	if t == GeomPolygon {
		out.Polygons = normalisePolygons(rings)
	}
	return out, nil
}

// normalisePolygons groups a feature's rings into polygons and gives every
// ring the winding its role requires.
//
// The wire format marks neither where one polygon ends and the next begins nor
// which ring is a hole. Both are inferred from orientation: a ring wound like
// the feature's first ring starts a new polygon, and a ring wound the other
// way is a hole in the polygon being built.
//
// The anchor is the FIRST ring rather than the specification's absolute
// convention, and that is the point of this function. A producer that writes
// every ring the wrong way round is not rare, and reading orientation
// absolutely would classify every one of its rings as a hole -- leaving a
// polygon with no exterior at all. Reading it relatively survives that,
// because a globally reversed tile still has holes wound opposite to their
// exteriors. The absolute convention is then applied on the way out, so what
// this returns is canonical whichever way the input came.
//
// That last step is not cosmetic. The rasterizer sums winding and takes the
// absolute value, so a hole wound like its exterior fills solid instead of
// cutting a hole, and it does it silently -- a lake with an island becomes a
// lake, which is a believable map. See docs/architecture.md, trap T13.
//
// A ring wound like the exterior starts a new polygon even when it lies inside
// the one before it, and that is correct rather than a gap: an island in a
// lake is encoded exactly that way, as a second polygon wound as an exterior.
// Deciding the role by containment instead would erase every island.
//
// A ring enclosing no area has no orientation to read. Unless it is the first
// ring of the feature -- which is an exterior by definition, there being no
// polygon for it to be a hole of -- it is attached as a hole of the polygon in
// progress, where it draws nothing either way. It is not dropped: dropping it
// would make the decode disagree with the bytes for no benefit.
func normalisePolygons(rings []Ring) []Polygon {
	if len(rings) == 0 {
		return nil
	}
	exteriorWinding := rings[0].Winding()
	if exteriorWinding == 0 {
		// Nothing to read from; fall back to the specification's convention.
		exteriorWinding = ExteriorWinding
	}

	var out []Polygon
	for _, r := range rings {
		w := r.Winding()
		if w == exteriorWinding || len(out) == 0 {
			out = append(out, Polygon{Exterior: orient(r, w, ExteriorWinding)})
			continue
		}
		last := len(out) - 1
		out[last].Holes = append(out[last].Holes, orient(r, w, HoleWinding))
	}
	return out
}

// orient returns the ring wound in the direction want, reversing it if it is
// wound the other way.
//
// The caller passes the winding it already computed rather than letting this
// recompute it. Winding is a pass over every vertex of the ring, and this is
// the only loop in the decoder that touches every vertex twice if it is not
// told -- which is worth avoiding in a function whose own documentation
// declines 128-bit arithmetic on the grounds of how hot it is.
//
// A ring with no area is returned untouched: there is no direction to correct
// and reversing it would change the point order for nothing.
func orient(r Ring, have, want int) Ring {
	if have != 0 && have != want {
		return r.reverse()
	}
	return r
}

// unzigzag32 decodes a geometry parameter integer, and unzigzag64 decodes an
// sint attribute value.
//
// Deltas are commonly small and as often negative as positive, so the format
// interleaves the two signs onto the naturals -- 0, -1, 1, -2 -> 0, 1, 2, 3 --
// and a varint then spends one byte on both directions instead of ten on every
// step west or north. An sint value is the same encoding at 64 bits.
//
// The two are written together, and named for their width, because they are
// one rule with two spellings: the 64-bit one used to sit inline in the value
// decoder where nothing connected it to this, and a shift or a cast at the
// wrong width is invisible except at the extremes of the range.
func unzigzag32(v uint32) int32 {
	return int32(v>>1) ^ -int32(v&1)
}

func unzigzag64(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
}

func commandName(cmd uint32) string {
	switch cmd {
	case cmdMoveTo:
		return "MoveTo"
	case cmdLineTo:
		return "LineTo"
	case cmdClosePath:
		return "ClosePath"
	}
	return fmt.Sprintf("command %d", cmd)
}
