package render

import (
	"cmp"
	"image/color"
	"math"
	"slices"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/raster"
)

// Where two areas of one layer overlap, the smaller one is drawn on top.
//
// The tile schema says what is on top of what with sort_rank, and where it
// does, that decides. Where it does not -- two polygons of one layer at one
// rank -- the style's rule order used to: every green landuse kind, then
// every built-up one. So a patch of bushland mapped inside a suburb's
// residential polygon, as along a creek in Hornsby, was painted over by the
// suburb and drawn as houses. The data was right and the paint order threw
// it away.
//
// The smaller of two overlapping areas is almost always the more specific
// statement about the ground under it: the wood inside the suburb, the
// playing field inside the school, the park inside the town. Drawing larger
// areas first and smaller ones over them is what a mapper means by mapping
// one inside the other, whichever kinds they are -- a school inside a park
// shows as a school, as a park inside a suburb shows as a park.
//
// # What "smaller" is measured as
//
// The area each polygon covers IN THE VIEW, summed over every tile that
// holds a piece of it. A tile carries no polygon's full area, only its own
// clipped piece, and ranking by the piece would let one polygon's order
// against another change from tile to tile, swapping colours along tile
// edges. Pieces of one polygon share its feature id in every tile, so their
// areas are added up by id first, and the whole view agrees on one order. A
// feature with no id is ranked by its own piece.
//
// That makes one view agree with itself and not with its neighbours. A view
// holding half a wood ranks it by the half, and the view beside it, holding
// all of it, ranks it by the whole: where the wood overlaps something of a
// size between the two, they draw the overlap in different colours. One map
// drawn once never shows it. A picture put together from views of
// neighbouring ground does -- a flyover's fixed tiles, side by side, or one
// view a frame as a camera moves, where the same ground changes colour from
// one frame to the next. So a caller can measure the areas once, over the
// whole region its views will cover (MeasureAreas), and every view ranks by
// that instead (Options.Areas). Areas are compared in Web Mercator's world
// units either way, so a measured one and one only seen in the view rank
// against each other at the same scale.
//
// # Why this does not break the one-pass rule
//
// One rasterizer pass per rule is what keeps two clipped halves of a polygon
// from leaving a hairline at a tile edge (trap T1). Here every piece of a
// polygon still goes into one pass -- the pass its rank falls in -- and
// consecutive polygons of one colour share it, so the number of passes is
// the number of times the colour changes in the order, not the number of
// polygons.

// stackable reports whether rule r can join a stack of areas: a fill-only
// rule, drawn by no width.
func stackable(r *Rule) bool {
	return r.Paint.Fill && r.Paint.Width == 0 && len(r.Paint.Widths) == 0
}

// stackEnd is where the stack of areas starting at rule i ends: the run of
// stackable rules on the same layer after it, stopping before limit. A
// stack of one is no stack, and is drawn as any other rule.
func stackEnd(rules []Rule, i, limit int) int {
	j := i + 1
	for j < len(rules) && j < limit && stackable(&rules[j]) && rules[j].Layer == rules[i].Layer {
		j++
	}
	return j
}

// stackPiece is one tile's piece of one polygon feature, clipped to the
// tile's square and transformed to the surface.
type stackPiece struct {
	rings [][]pt
}

// stackArea is one polygon's pieces from every tile, and what decides where
// it is drawn in the stack.
type stackArea struct {
	key    uint64
	hasID  bool
	rank   float64
	area   float64
	rule   int // index into the stack's rules, which gives the colour
	order  int // first seen, for a tie nothing else breaks
	pieces []stackPiece
	bounds bbox // on the surface, every piece's
}

// drawStack draws the rules -- consecutive fill rules of one layer, each
// with its colour -- as one stack: every matching polygon from every tile,
// by sort_rank, then largest area in the view first, then the rules' own
// order.
func (d *drawer) drawStack(s *raster.Surface, rules []*Rule, inks []color.Color, tiles []drawTile) {
	byKey := map[uint64]*stackArea{}
	var areas []*stackArea
	seq := uint64(0)
	for _, dt := range tiles {
		layer, ok := dt.tile.Layer(rules[0].Layer)
		if !ok {
			continue
		}
		extent := layer.Extent
		if extent == 0 {
			extent = mvt.DefaultExtent
		}
		tr := d.p.tileTransform(dt.ref.z, dt.ref.x, dt.ref.y, extent)
		for fi := range layer.Features {
			f := &layer.Features[fi]
			if f.Type != mvt.GeomPolygon {
				continue
			}
			ri := slices.IndexFunc(rules, func(r *Rule) bool { return r.matches(f) })
			if ri < 0 {
				continue
			}
			piece, area := d.clipPiece(f.Geometry.Polygons, tr, dt.clip)
			if len(piece.rings) == 0 {
				continue
			}
			// A feature without an id is a polygon of its own; the keys of
			// those are kept apart from every id by the top bit.
			key, hasID := f.ID, f.HasID
			if !hasID {
				key, seq = seq|1<<63, seq+1
			}
			a := byKey[key]
			if a == nil {
				a = &stackArea{key: key, hasID: hasID, rank: sortRank(f), rule: ri, order: len(areas)}
				byKey[key] = a
				areas = append(areas, a)
			}
			a.area += area
			a.pieces = append(a.pieces, piece)
			a.bounds = a.bounds.union(piece.bounds())
		}
	}
	// In world units, so that an area measured over the region and one the
	// region does not hold are compared at one scale.
	perWorld := d.p.scale * d.p.scale
	for _, a := range areas {
		a.area /= perWorld
		if w, ok := d.areas.of(a); ok {
			a.area = w
		}
	}
	slices.SortStableFunc(areas, func(a, b *stackArea) int {
		return cmp.Or(
			cmp.Compare(a.rank, b.rank),
			cmp.Compare(b.area, a.area),
			cmp.Compare(a.rule, b.rule),
			cmp.Compare(a.order, b.order),
		)
	})

	// The order matters only between areas that overlap, so areas share a
	// pass wherever that keeps every overlap in order: each goes into the
	// earliest pass of its colour that comes after every pass holding an
	// area it overlaps. Drawing the sorted list a pass per change of colour
	// was right and three times slower in a city, where the colours
	// alternate hundreds of times; this is the same picture in a handful of
	// passes. Every piece of an area still goes into one pass, which is what
	// keeps trap T1 shut.
	type pass struct {
		rule   int
		areas  []*stackArea
		bounds bbox
	}
	var passes []*pass
	for _, a := range areas {
		after := -1 // the last pass holding an area a overlaps
		for i := len(passes) - 1; i >= 0 && after < 0; i-- {
			if !passes[i].bounds.overlaps(a.bounds) {
				continue
			}
			for _, b := range passes[i].areas {
				if b.bounds.overlaps(a.bounds) {
					after = i
					break
				}
			}
		}
		at := -1
		for i := max(after, 0); i < len(passes); i++ {
			if passes[i].rule == a.rule {
				at = i
				break
			}
		}
		if at < 0 {
			passes = append(passes, &pass{rule: a.rule})
			at = len(passes) - 1
		}
		p := passes[at]
		p.areas = append(p.areas, a)
		p.bounds = p.bounds.union(a.bounds)
	}
	for _, p := range passes {
		d.path.Reset()
		for _, a := range p.areas {
			for _, pc := range a.pieces {
				for _, r := range pc.rings {
					d.path.Ring(d.points(r))
				}
			}
		}
		if !d.path.Empty() {
			s.Fill(&d.path, inks[p.rule])
		}
	}
}

// bounds is the box round every ring of the piece.
func (p stackPiece) bounds() bbox {
	var b bbox
	for _, r := range p.rings {
		for _, q := range r {
			b = b.union(bbox{q.X, q.Y, q.X, q.Y, true})
		}
	}
	return b
}

// bbox is a bounding box that starts out holding nothing and grows: unlike
// box, a single point is something.
type bbox struct {
	minX, minY, maxX, maxY float64
	set                    bool
}

func (b bbox) union(o bbox) bbox {
	switch {
	case !o.set:
		return b
	case !b.set:
		return o
	}
	return bbox{min(b.minX, o.minX), min(b.minY, o.minY), max(b.maxX, o.maxX), max(b.maxY, o.maxY), true}
}

// overlaps reports whether two boxes share any area. Boxes that only touch
// share none, and neither do the areas inside them, so their order does not
// matter.
func (b bbox) overlaps(o bbox) bool {
	return b.set && o.set && b.minX < o.maxX && o.minX < b.maxX && b.minY < o.maxY && o.minY < b.maxY
}

// clipPiece transforms and clips one feature's polygons to the tile's
// square, returning the rings as they will be filled and the area they
// cover on the surface, holes taken out.
func (d *drawer) clipPiece(polys []mvt.Polygon, tr tileTransform, clip box) (stackPiece, float64) {
	var piece stackPiece
	var area float64
	add := func(ring mvt.Ring) {
		if len(ring) < 3 {
			return
		}
		d.src = d.src[:0]
		for _, p := range ring {
			d.src = append(d.src, tr.apply(p.X, p.Y))
		}
		clipped := d.clip.ring(d.src, clip)
		if len(clipped) < 3 {
			return
		}
		// The clipper's buffer is reused, so the ring is copied out.
		piece.rings = append(piece.rings, slices.Clone(clipped))
		area += signedArea(clipped)
	}
	for _, poly := range polys {
		add(poly.Exterior)
		for _, h := range poly.Holes {
			add(h)
		}
	}
	// Exteriors and holes are wound oppositely, so the signed sum is the
	// area with the holes taken out, of one sign or the other.
	return piece, math.Abs(area)
}

// signedArea is the shoelace area of a ring, positive or negative with its
// winding.
func signedArea(r []pt) float64 {
	var a float64
	for i := range r {
		j := (i + 1) % len(r)
		a += r[i].X*r[j].Y - r[j].X*r[i].Y
	}
	return a / 2
}

// sortRank is the feature's sort_rank, the schema's own statement of what
// is drawn over what, lower first; 0 for a feature without one, which is
// where the schema itself starts counting.
func sortRank(f *mvt.Feature) float64 {
	if v, ok := f.Tag("sort_rank"); ok {
		if r, isNum := v.Float64(); isNum {
			return r
		}
	}
	return 0
}
