package render

import (
	"fmt"
	"math"

	"github.com/wisborg/osmbase/mercator"
)

// tileSize is the nominal width of a vector tile in TILE PIXELS: the unit a
// style's widths are written in, and the unit that makes zoom mean what it
// means everywhere else in a slippy map.
//
// It is not a property of the data. An mvt layer's own coordinate range is its
// Extent, usually 4096, and nothing in a tile says how many screen pixels it is
// meant to occupy. 256 is the convention every zoom level in every tile scheme
// is defined against -- zoom z is 256*2^z pixels across the world -- so a
// continuous zoom computed from it is the same number a web map would show.
const tileSize = 256

// Bounds is a geographic rectangle in degrees.
//
// It is the whole of what a caller says about WHERE, and the pixel size on View
// is the whole of what it says about HOW BIG. Nothing else -- no centre, no
// zoom, no scale -- appears in the API, because every one of those is derivable
// from these two and a second way to say the same thing is a second way for
// them to disagree.
type Bounds struct {
	West, South, East, North float64
}

// validate refuses a rectangle that cannot be drawn, naming which part of it is
// the problem.
//
// A rectangle crossing the antimeridian -- west 179, east -179 -- is a real
// place and is REFUSED rather than handled, because handling it means splitting
// every tile range, every clip and every transform in two, and the alternative
// to refusing is silently drawing the other 358 degrees of the world. A caller
// that needs it can render the two halves and join them.
func (b Bounds) validate() error {
	for _, c := range []struct {
		name  string
		v     float64
		limit float64
	}{
		{"west", b.West, 180}, {"east", b.East, 180},
		{"south", b.South, 90}, {"north", b.North, 90},
	} {
		if math.IsNaN(c.v) || math.IsInf(c.v, 0) {
			return fmt.Errorf("render: the view's %s edge is %v, which is not a coordinate", c.name, c.v)
		}
		if c.v < -c.limit || c.v > c.limit {
			return fmt.Errorf("render: the view's %s edge is %g, outside -%g to %g", c.name, c.v, c.limit, c.limit)
		}
	}
	if b.East <= b.West {
		return fmt.Errorf("render: the view's east edge (%g) is not east of its west edge (%g); a rectangle crossing the antimeridian has to be rendered as two views", b.East, b.West)
	}
	if b.North <= b.South {
		return fmt.Errorf("render: the view's north edge (%g) is not north of its south edge (%g)", b.North, b.South)
	}
	return nil
}

// View is what to draw: a geographic rectangle and the pixel size to draw it
// at.
//
// The image SHOWS ALL of the rectangle rather than filling itself with part of
// it. Degrees and pixels rarely have the same aspect ratio, and the three ways
// to reconcile that are to stretch, to show less than was asked for, or to show
// more. Web
// Mercator is conformal -- a small circle on the ground is a circle on the
// screen -- and stretching it to an aspect ratio destroys exactly that, turning
// every roundabout into an ellipse. Showing less silently answers a different
// question from the one asked. So the shorter axis is extended, symmetrically
// about the centre, and everything the caller asked for is on the image.
type View struct {
	Bounds Bounds
	// Width and Height are the output image's size in pixels.
	Width, Height int
}

// Zoom is the tile zoom a view will be drawn from and the continuous zoom its
// scale corresponds to -- what Render reports as Result.Zoom and
// ContinuousZoom, available before rendering.
//
// For a caller that has to ask something about the tiles first, such as
// whether a store holds them. Working the zoom out again from the bounds
// would be a second statement of the rounding rule, and the first time the
// two disagreed the question would be asked about the wrong zoom.
func (v View) Zoom() (tile uint8, continuous float64, err error) {
	p, err := resolve(v)
	if err != nil {
		return 0, 0, err
	}
	return p.tileZoom, p.zoom, nil
}

// projection is a resolved View: everything the rest of the renderer needs to
// turn a tile-local integer into a pixel, computed once.
//
// It is computed once per render and passed down rather than recomputed per
// tile, because every field here is a property of the VIEW and a per-tile
// recomputation is a per-tile opportunity for two of them to disagree.
type projection struct {
	width, height int

	// scale is surface pixels per world unit, where the world is the unit
	// square Project returns. It is the same on both axes; see View.
	scale float64
	// originX and originY are the world coordinates of the surface's top left
	// pixel corner.
	originX, originY float64

	// zoom is the continuous zoom the view resolves to, and tileZoom the
	// integer tile zoom asked of the source.
	zoom     float64
	tileZoom uint8

	// tileScale is surface pixels per TILE pixel at tileZoom, which is the
	// number every style width is multiplied by. It is 2^(zoom-tileZoom), so
	// with tileZoom the nearest integer to zoom it is always between 0.71 and
	// 1.41.
	//
	// # Why a stroke is not the same pixel width at every image size
	//
	// This holds stroke thickness fixed WITHIN a zoom step and not across one.
	// Hold the ground fixed and grow the image by a third and every stroke
	// grows by a third, which is the property the design asks for. Double the
	// image at the same ground and the target zoom increments instead, so the
	// picture becomes a more detailed map drawn at a similar thickness.
	//
	// That second behaviour is deliberate and it is the right one. The
	// alternative -- a reference resolution on the View, so that widths scale
	// with image size alone -- was considered and rejected, because it fixes
	// the smaller half of the problem and leaves the larger half broken. What
	// actually changes with image size is the DETAIL: a 4K frame of the same
	// ground crosses a zoom boundary and gains buildings, minor roads and
	// footpaths the 1080p frame did not have. Pinning stroke width while the
	// feature count doubles does not give two sizes of one picture; it gives a
	// denser map with strokes too heavy for its density.
	//
	// So a caller wanting two sizes of the SAME picture renders once and
	// scales the image, which is a resampling question rather than a styling
	// one. A caller rendering at two sizes for two purposes wants two maps,
	// and gets them.
	//
	// It is computed from the view's target zoom and NOT from the zoom a tile
	// was actually read at. When a tile is missing and an ancestor is drawn in
	// its place, the ancestor's geometry is placed by its own transform but its
	// roads must come out the same width as the roads beside them; a width
	// derived from the ancestor's zoom would make the overzoomed patch draw its
	// roads four or sixteen times too thin.
	tileScale float64
}

// resolve turns a View into the projection every later step works in.
func resolve(v View) (projection, error) {
	if v.Width <= 0 || v.Height <= 0 {
		return projection{}, fmt.Errorf("render: a %d by %d view has no pixels to draw on", v.Width, v.Height)
	}
	if err := v.Bounds.validate(); err != nil {
		return projection{}, err
	}

	// North is the SMALLER y: world y runs south. See mercator.Project, which
	// explains at length why there is no flip anywhere in this library.
	x0, y0 := mercator.Project(v.Bounds.West, v.Bounds.North)
	x1, y1 := mercator.Project(v.Bounds.East, v.Bounds.South)
	worldW, worldH := x1-x0, y1-y0
	if worldW <= 0 || worldH <= 0 {
		// Reachable from a legal rectangle: both latitudes beyond the same
		// Mercator cut clamp to it and the rectangle loses its height.
		return projection{}, fmt.Errorf("render: latitudes %g to %g project to a rectangle of no height; Web Mercator is cut at %g degrees", v.Bounds.South, v.Bounds.North, mercator.MaxLatitude)
	}

	// The SMALLER of the two scales, so the whole rectangle is on the image:
	// it fills one axis and the other is extended, symmetrically about the
	// centre, as View promises. This took the larger for its first two weeks,
	// which cropped the longer axis instead -- a 400 by 100 image of one
	// square tile showed a quarter of its height -- and a test asserted that,
	// reading edges beyond the image as "more" when they are ground not drawn.
	// Every caller in this module passes a view already at the image's aspect
	// ratio, where the two scales are equal, which is why nothing showed it.
	scale := math.Min(float64(v.Width)/worldW, float64(v.Height)/worldH)

	p := projection{
		width:   v.Width,
		height:  v.Height,
		scale:   scale,
		originX: (x0+x1)/2 - float64(v.Width)/(2*scale),
		originY: (y0+y1)/2 - float64(v.Height)/(2*scale),
		zoom:    math.Log2(scale / tileSize),
	}

	// Nearest rather than floor. Either is defensible and they differ in which
	// direction the tile pixels are stretched: floor never asks for a zoom
	// deeper than the view needs, and nearest keeps the stretch inside a factor
	// of 1.41 either way, so the generalisation the tile was built for is the
	// generalisation being looked at. The cost of nearest is that a view just
	// past the halfway point asks for a zoom the source may not hold, which is
	// the overzoom path -- honest degradation rather than a failure.
	z := math.Round(p.zoom)
	switch {
	case !(z >= 0): // the ! also catches NaN, from a zero or negative scale
		z = 0
	case z > mercator.MaxZoom:
		z = mercator.MaxZoom
	}
	p.tileZoom = uint8(z)
	p.tileScale = scale / (tileSize * math.Exp2(z))
	return p, nil
}

// pixelX and pixelY convert a world coordinate to a surface pixel.
func (p projection) pixelX(worldX float64) float64 { return (worldX - p.originX) * p.scale }
func (p projection) pixelY(worldY float64) float64 { return (worldY - p.originY) * p.scale }

// surface is the rectangle being drawn on, in its own pixels.
func (p projection) surface() box {
	return box{MaxX: float64(p.width), MaxY: float64(p.height)}
}

// tileBox is the surface rectangle tile z/x/y covers.
//
// For the tiles the view asked for this is the tile's own ground; for an
// ancestor drawn in a missing tile's place it is NOT, and the caller uses the
// missing tile's box instead. See gather.
func (p projection) tileBox(z uint8, x, y uint32) box {
	n := math.Exp2(float64(z))
	return box{
		MinX: p.pixelX(float64(x) / n),
		MinY: p.pixelY(float64(y) / n),
		MaxX: p.pixelX(float64(x+1) / n),
		MaxY: p.pixelY(float64(y+1) / n),
	}
}

// tileTransform is the affine map from one layer's tile-local integers to
// surface pixels: sx = ax*px + bx, sy = ay*py + by.
//
// It is per LAYER and not per tile, because the extent is per layer: a tile
// whose landcover layer is at extent 512 and whose buildings layer is at 4096
// needs two of these, and using one for both draws a layer at an eighth of its
// size in what looks like the right place. mercator.TileTransform says the same
// thing about degrees.
type tileTransform struct {
	ax, bx float64
	ay, by float64
}

func (p projection) tileTransform(z uint8, x, y uint32, extent uint32) tileTransform {
	n := math.Exp2(float64(z))
	a := p.scale / (n * float64(extent))
	return tileTransform{
		ax: a, bx: p.pixelX(float64(x) / n),
		ay: a, by: p.pixelY(float64(y) / n),
	}
}

// apply converts one tile-local integer coordinate to a surface pixel.
//
// Coordinates outside [0, extent) are expected rather than exceptional: a tile
// carries a buffer of geometry from beyond its own edge. They land outside the
// tile's box, which is what the clip is for.
func (t tileTransform) apply(x, y int32) pt {
	return pt{X: t.ax*float64(x) + t.bx, Y: t.ay*float64(y) + t.by}
}

// tileRange is the span of tiles at zoom z that the surface covers, inclusive
// at both ends and clamped to the grid.
//
// The count is bounded by the view rather than by the zoom: the surface is
// width by height pixels and a tile is tileSize*tileScale pixels across, so a
// 1024 by 768 view is about forty tiles whether the zoom is 4 or 19. That is
// what makes starting at the view's own zoom and walking up cheap.
func (p projection) tileRange(z uint8) (x0, y0, x1, y1 uint32) {
	n := math.Exp2(float64(z))
	last := uint32(1)<<z - 1
	span := func(worldMin, worldMax float64) (uint32, uint32) {
		lo := gridIndex(math.Floor(worldMin*n+tileEdgeSlack), last)
		// Ceil minus one, so a view whose edge falls exactly on a tile boundary
		// does not ask for the tile beyond it, which would cover no pixels.
		hi := gridIndex(math.Ceil(worldMax*n-tileEdgeSlack)-1, last)
		if hi < lo {
			hi = lo
		}
		return lo, hi
	}
	x0, x1 = span(p.originX, p.originX+float64(p.width)/p.scale)
	y0, y1 = span(p.originY, p.originY+float64(p.height)/p.scale)
	return x0, y0, x1, y1
}

// tileEdgeSlack is how much of a tile's width an overlap has to exceed before
// the tile is worth asking for.
//
// A view whose edge is meant to sit exactly on a tile boundary rarely does to
// the last bit: the bounds are degrees, and degrees reach this package through
// a projection and often through an inverse projection before that, so a
// boundary comes out as 22 plus or minus a few parts in 10^15. Without slack
// that decides whether an extra column of tiles is requested -- tiles
// overlapping the view by a nanometre, each costing a lookup and, over a remote
// archive, a round trip.
//
// A billionth of a tile is about thirty micrometres of ground at zoom 15 and
// well under a millionth of a pixel. Nothing is lost by ignoring it and the
// tile count stops depending on rounding.
const tileEdgeSlack = 1e-9

// gridIndex clamps a tile index to the grid.
//
// A view may legitimately extend past the top or bottom of the world -- the
// Mercator square is finite and a window is not -- and past the antimeridian
// only through arithmetic, since validate refuses a rectangle that crosses it.
// Clamping rather than wrapping means the ground beyond the edge has no tile,
// which is the truth: there is nothing there to draw.
func gridIndex(v float64, last uint32) uint32 {
	if v < 0 {
		return 0
	}
	if v > float64(last) {
		return last
	}
	return uint32(v)
}
