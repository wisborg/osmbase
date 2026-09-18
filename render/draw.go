package render

import (
	"fmt"
	"golang.org/x/image/font"
	"image"
	"image/color"
	"math"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/raster"
)

// kindKey is the attribute a style selects features on.
//
// It is the tile schema's own vocabulary rather than OSM's: "kind" carries
// water, park, minor_road and the rest of a selection somebody else already
// made, which is most of what the tile source is bought for. A style is written
// against a schema version and Style.Schema records which. See
// docs/architecture.md, "What the tile choice costs".
const kindKey = "kind"

// drawer holds the buffers one render reuses.
//
// Every one of them exists because the alternative is an allocation per ring,
// per line or per feature, and a city view at zoom 15 has a few hundred
// thousand of those. They are on a value passed down the call chain rather than
// on the Renderer so that two goroutines rendering two views through one
// Renderer cannot share them.
type drawer struct {
	p       projection
	palette Palette
	path    raster.Path
	clip    clipper

	// src is the current ring or line transformed into surface pixels, and dst
	// the clipped result converted to the rasterizer's float32.
	src  []pt
	dst  []raster.Point
	dash []float32
}

// appendTile adds every feature of one tile that one rule selects to the
// rule's path.
//
// Nothing is filled here. The whole point is that the path outlives this call
// and accumulates the same rule's features from every tile of the view, so that
// one fill covers the lot; see the package comment and trap T1.
// drawRule appends every tile's contribution to one rule and fills once.
//
// It owns the tile loop, and that is the whole point of its existence. The
// invariant is that a rule is ONE fill fed by every tile, because compositing
// tiles separately leaves a permanent 24% hairline along every tile edge --
// trap T1, which the design calls the most likely "works on the tile I tested"
// bug in the project. While the caller held the tile loop, both nestings
// type-checked and the invariant rested on a comment. Here the wrong version
// cannot be written without taking the loop back out of this function, which
// is a visible thing to do rather than an easy mistake.
//
// It returns whether anything was appended, so the caller can skip the fill
// for a rule that matched nothing.
func (d *drawer) drawRule(s *raster.Surface, rule *Rule, tiles []drawTile, ink color.Color) {
	d.path.Reset()
	for _, dt := range tiles {
		d.appendTile(rule, dt)
	}
	// Nothing here tracks the extent of what was appended. raster.Path
	// maintains its own bounding box as points arrive and Surface.Fill returns
	// immediately when that box misses the surface, so a rule whose features
	// all landed outside costs one comparison rather than a second walk over
	// geometry this package has just handed over.
	if !d.path.Empty() {
		s.Fill(&d.path, ink)
	}
}

func (d *drawer) appendTile(rule *Rule, dt drawTile) {
	layer, ok := dt.tile.Layer(rule.Layer)
	if !ok {
		// A tile legitimately lacks most layers: no water inland, no buildings
		// on a trail. That is the correct picture of the place and not missing
		// data, so there is nothing to report. See trap T9.
		return
	}
	extent := layer.Extent
	if extent == 0 {
		// Defensive: the decoder applies the schema's default. A zero here
		// would make the transform divide by zero and put every point of the
		// layer at infinity.
		extent = mvt.DefaultExtent
	}
	tr := d.p.tileTransform(dt.ref.z, dt.ref.x, dt.ref.y, extent)

	// Widths and dash lengths are in tile pixels and are scaled by the view,
	// once per layer rather than once per feature. The scale comes from the
	// projection's target zoom, so an overzoomed tile's roads come out the same
	// width as the roads in the tiles beside it.
	width := rule.Paint.Width * float32(d.p.tileScale)
	dash := d.scaleDash(rule.Paint.Dash)

	for i := range layer.Features {
		f := &layer.Features[i]
		kind, present := featureKind(f)
		if !rule.matches(kind, present) {
			continue
		}
		switch f.Type {
		case mvt.GeomPolygon:
			if rule.Paint.Fill {
				d.appendPolygons(f.Geometry.Polygons, tr, dt.clip)
			}
		case mvt.GeomLineString:
			if width > 0 {
				d.appendLines(f.Geometry.Lines, tr, dt.clip, raster.Stroke{Width: width, Dash: dash})
			}
		}
		// Points draw nothing. This library renders no text, and a place point
		// without its name is a dot nobody can read; the name comes back as
		// data instead. See docs/architecture.md, "What this library is not".
	}
}

// featureKind reads the attribute a style selects on, reporting whether the
// feature carried it.
//
// Absence is membership in the tag map, never a zero value, and a kind that is
// present but is not a string is treated as absent rather than stringified: a
// schema that encoded a kind as a number would be a schema this style was not
// written for, and papering over it would draw the wrong features silently.
func featureKind(f *mvt.Feature) (string, bool) {
	v, ok := f.Tag(kindKey)
	if !ok {
		return "", false
	}
	return v.Text()
}

// scaleDash converts a dash pattern from tile pixels to surface pixels, into a
// buffer reused for the whole render.
func (d *drawer) scaleDash(dash []float32) []float32 {
	if len(dash) == 0 {
		return nil
	}
	s := float32(d.p.tileScale)
	d.dash = d.dash[:0]
	for _, v := range dash {
		d.dash = append(d.dash, v*s)
	}
	return d.dash
}

// appendPolygons adds one feature's filled areas.
//
// Every ring of one polygon goes into the SAME path, exterior first and then
// its holes, and the path is the rule's -- shared with every other feature of
// the same colour from every tile. Both halves of that matter. The rasterizer
// takes the absolute accumulated winding, so a hole separated from its exterior
// has nothing to subtract from and fills solid, turning an island in a lake
// into a lake with no island; and two pieces of one polygon clipped at a tile
// edge only sum to full coverage if they are in one pass. See
// docs/architecture.md, traps T1 and T13.
func (d *drawer) appendPolygons(polys []mvt.Polygon, tr tileTransform, clip box) {
	for _, poly := range polys {
		d.appendRing(poly.Exterior, tr, clip)
		for _, hole := range poly.Holes {
			d.appendRing(hole, tr, clip)
		}
	}
}

// appendRing transforms, clips and adds one ring.
//
// The clip preserves the ring's direction, which is the whole hole mechanism:
// mvt normalises exteriors and holes to opposite windings on decode, raster
// uses the order it is given, and Sutherland-Hodgman emits input vertices and
// points between consecutive input vertices in input order. Nothing in that
// chain reverses anything, and anything that did would fill every hole in the
// map.
func (d *drawer) appendRing(ring mvt.Ring, tr tileTransform, clip box) {
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
	d.path.Ring(d.points(clipped))
}

// appendLines strokes one feature's parts, threading the dash phase along the
// way.
//
// The parts are walked in order, and each part's clipped runs in order, and the
// phase each stroke ENDS on is what the next one starts from. Restarting the
// pattern at every piece would put a phase jump wherever the pieces were cut,
// which for a tile clip is every tile boundary in the map -- invisible within
// one tile and obvious across one, the same shape of failure as the coverage
// seam. The arithmetic belongs to raster, which accumulates arc length per
// segment in the float32 the stroker already works in; a start-phase-plus-
// length computed here in float64 is a second implementation that drifts from
// the first over a long way.
//
// raster.Path.Stroke names two conditions the caller owns, and both are met
// here. The pieces are consecutive, because clipping a polyline cuts it into
// runs in order along the line. And they do not OVERLAP, because each tile's
// geometry is clipped to that tile's own square: a vector tile carries a buffer
// of the same way from beyond its edge, and two tiles' copies of one stretch of
// path, dashed at two phases, would be colliding dashes rather than the free
// duplicate a solid stroke gets.
//
// What is still not threaded is one way arriving as separate FEATURES in two
// tiles. Those are two features with two identities and no order between them,
// and stitching them would mean matching geometry across tiles before drawing
// anything. The pattern therefore restarts at a tile edge for a dashed way that
// crosses one. That is a real and visible limit, written down here rather than
// left to be discovered.
func (d *drawer) appendLines(lines [][]mvt.Point, tr tileTransform, clip box, s raster.Stroke) {
	for _, line := range lines {
		if len(line) < 2 {
			continue
		}
		d.src = d.src[:0]
		for _, p := range line {
			d.src = append(d.src, tr.apply(p.X, p.Y))
		}
		d.clip.line(d.src, clip, func(run []pt) {
			s.DashPhase = d.path.Stroke(d.points(run), s)
		})
	}
}

// points converts clipped surface coordinates to the rasterizer's float32.
//
// The conversion happens here, at the last possible moment, because everything
// that reaches this point is within a pixel or two of the surface: clipping in
// float32 would round the clip boundary itself, and a tile-local integer
// multiplied out at a deep zoom leaves float32's exact integer range long
// before it gets near the image.
func (d *drawer) points(src []pt) []raster.Point {
	d.dst = d.dst[:0]
	for _, p := range src {
		d.dst = append(d.dst, raster.Point{X: float32(p.X), Y: float32(p.Y)})
	}
	return d.dst
}

// hatch paints the squares no tile was found for.
//
// It is a placeholder and not a blank, and the difference is the whole point.
// Background alone would be indistinguishable from ocean -- which in this
// schema is exactly what background means -- and from a crash, so an area with
// no data would look like an area with water in it. Diagonal lines in a colour
// that belongs to nothing else on the map cannot be read as either.
//
// The gap is repainted in background first. A neighbouring tile's stroke can
// put half a road width inside the square, and a road running a few pixels into
// a region where nothing is known is a claim the hatch is there to withdraw.
func (d *drawer) hatch(s *raster.Surface, gaps []image.Rectangle) {
	if len(gaps) == 0 {
		return
	}

	d.path.Reset()
	for _, g := range gaps {
		d.path.Rect(
			raster.Point{X: float32(g.Min.X), Y: float32(g.Min.Y)},
			raster.Point{X: float32(g.Max.X), Y: float32(g.Max.Y)},
		)
	}
	s.Fill(&d.path, d.palette.Background)

	// The spacing is a fraction of the image rather than a pixel count, so that
	// the same view at 1080p and at 4K is the same picture at two sizes instead
	// of two different textures. The floor keeps it drawable on a thumbnail.
	step := math.Max(8, math.Min(float64(d.p.width), float64(d.p.height))/48)
	width := step / 6

	d.path.Reset()
	for _, g := range gaps {
		b := hatchBox(g, gaps, width)
		if b.empty() {
			continue
		}
		// The family of lines x - y = k*step, indexed from the IMAGE origin and
		// not from the rectangle's own corner. Two gaps that touch then share
		// one set of lines and read as one hole, rather than as two patches
		// with a visible discontinuity along the join.
		lo := int(math.Floor((b.MinX - b.MaxY) / step))
		hi := int(math.Ceil((b.MaxX - b.MinY) / step))
		for k := lo; k <= hi; k++ {
			off := float64(k) * step
			d.src = append(d.src[:0],
				pt{X: off + b.MinY, Y: b.MinY},
				pt{X: off + b.MaxY, Y: b.MaxY},
			)
			d.clip.line(d.src, b, func(run []pt) {
				d.path.Stroke(d.points(run), raster.Stroke{Width: float32(width)})
			})
		}
	}
	s.Fill(&d.path, d.palette.NoData)
}

// hatchBox is the rectangle one gap's hatch lines are clipped to.
//
// It is the gap itself on any side shared with ANOTHER gap, and the gap inset
// by a stroke width on any side that is not. The two cases are different
// questions.
//
// On a shared side the ground beyond is also unknown, so the hatch should run
// straight through: the lines come from one family indexed off the image
// origin, so a line crossing the shared edge is clipped on both sides of it and
// the two pieces abut exactly, with a round cap from each closing the join --
// the same mechanism that rejoins a road at a tile seam. Insetting there
// instead leaves a background-coloured stripe along every internal edge, and
// the hole in the map reads as a grid of separate patches rather than as one
// region nothing is known about.
//
// On an unshared side the ground beyond is either drawn or off the image, and
// neither wants hatch ink on it. The stroker puts a round cap of half the width
// past the end of each line, so half a width of inset is what keeps the ink
// inside the square; the second half keeps it off the image's own last row and
// column, where the rasterizer folds everything beyond the edge onto a single
// accumulator slot and its arithmetic is least exact.
func hatchBox(g image.Rectangle, gaps []image.Rectangle, width float64) box {
	b := box{
		MinX: float64(g.Min.X), MinY: float64(g.Min.Y),
		MaxX: float64(g.Max.X), MaxY: float64(g.Max.Y),
	}
	// A side is shared when another gap covers the ground just beyond it. The
	// test is "reaches past this edge from the other side" rather than "ends
	// exactly on it", because these rectangles are rounded OUTWARD from tile
	// squares whose shared edge falls at a fraction of a pixel: the tile
	// boundary at x = 214.4 becomes Max.X 215 on one side and Min.X 214 on the
	// other, so two abutting gaps overlap by a pixel and never compare equal.
	// An equality test here leaves a stripe of background down every internal
	// edge and the hole reads as a grid of patches.
	//
	// The scan is over a slice in a fixed order and compares integers, so the
	// answer is the same on every run.
	var left, right, above, below bool
	for _, o := range gaps {
		if o == g {
			continue
		}
		if o.Max.Y > g.Min.Y && o.Min.Y < g.Max.Y {
			left = left || (o.Min.X < g.Min.X && o.Max.X >= g.Min.X)
			right = right || (o.Max.X > g.Max.X && o.Min.X <= g.Max.X)
		}
		if o.Max.X > g.Min.X && o.Min.X < g.Max.X {
			above = above || (o.Min.Y < g.Min.Y && o.Max.Y >= g.Min.Y)
			below = below || (o.Max.Y > g.Max.Y && o.Min.Y <= g.Max.Y)
		}
	}
	if !left {
		b.MinX += width
	}
	if !right {
		b.MaxX -= width
	}
	if !above {
		b.MinY += width
	}
	if !below {
		b.MaxY -= width
	}
	return b
}

// collectLabels gathers every label the style asks for from the tiles in
// view, without deciding which of them will fit.
//
// Two zoom filters apply and they mean different things. The rule's own
// MinZoom is a statement about a CLASS of label -- road names below the
// deepest zooms are noise whatever the road -- and the feature's min_zoom tag
// is the producer's statement about that one feature, which is what keeps a
// city visible from far out while a hamlet waits until the map is close. Both
// have to pass, and neither can substitute for the other.
//
// The feature's tag is compared against the zoom being DISPLAYED rather than
// the zoom of the tile it came from. Those differ whenever a view is drawn
// from an overzoomed ancestor, and the display zoom is the right one: the
// question is how much room the reader has on screen, not which file the
// feature arrived in.
func (d *drawer) collectLabels(rules []LabelRule, tiles []drawTile, at uint8, faceFor func(float64) font.Face) []candidate {
	var out []candidate
	for i := range rules {
		rule := &rules[i]
		if !rule.appliesAt(at) {
			continue
		}
		face := faceFor(rule.SizeScale)
		if face == nil {
			continue
		}
		for _, dt := range tiles {
			d.appendTileLabels(&out, rule, dt, at, face)
		}
	}
	return out
}

func (d *drawer) appendTileLabels(out *[]candidate, rule *LabelRule, dt drawTile, at uint8, face font.Face) {
	layer, ok := dt.tile.Layer(rule.Layer)
	if !ok {
		return
	}
	extent := layer.Extent
	if extent == 0 {
		extent = mvt.DefaultExtent
	}
	tr := d.p.tileTransform(dt.ref.z, dt.ref.x, dt.ref.y, extent)

	for i := range layer.Features {
		f := &layer.Features[i]
		if !rule.matches(f) || !rule.labels(f.Type) {
			continue
		}
		if v, ok := f.Tags["min_zoom"]; ok {
			if z, isNum := v.Float64(); isNum && float64(at) < z {
				continue
			}
		}
		text, ok := labelText(f, rule.Field)
		if !ok {
			continue
		}
		for _, a := range labelAnchors(rule, f) {
			p := tr.apply(a.X, a.Y)
			*out = append(*out, candidate{
				text: text, x: p.X, y: p.Y,
				priority: rule.Priority,
				rank:     labelRank(f),
				once:     rule.OncePerName,
				minor:    rule.Minor,
				face:     face,
				// The tile and the coordinate, which together are unique and
				// stable. Only ever compared, never shown.
				key: fmt.Sprintf("%d/%d/%d:%d,%d", dt.ref.z, dt.ref.x, dt.ref.y, a.X, a.Y),
			})
		}
	}
}

// labelText reads the field a rule names, rejecting anything that is not a
// non-empty string.
//
// A feature with no name is not an error and not a label: most features in
// most layers have none. A name that is present but empty is treated the same
// way, since an empty label would reserve space and draw nothing.
func labelText(f *mvt.Feature, field string) (string, bool) {
	v, ok := f.Tags[field]
	if !ok {
		return "", false
	}
	s, ok := v.Text()
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// labelAnchors is where on a feature its name could be written.
//
// A point feature offers each of its points; a line offers one, the midpoint
// of its longest part. One rather than several along the way, because
// repeating a name down a road is a decision about how crowded a map should
// look and this pass has no way to judge that -- and because the rule that
// wants line labels at all also wants OncePerName, which would discard the
// repeats anyway.
func labelAnchors(rule *LabelRule, f *mvt.Feature) []mvt.Point {
	switch rule.Placement {
	case PlacePoint:
		return f.Geometry.Points
	case PlaceLine:
		if x, y, ok := lineAnchor(f.Geometry.Lines); ok {
			return []mvt.Point{{X: x, Y: y}}
		}
	}
	return nil
}
