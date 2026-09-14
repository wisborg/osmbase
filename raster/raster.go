// Package raster draws filled paths, stroked polylines and dashed lines onto
// an RGBA image.
//
// It knows about points, paths, widths and colours, and about nothing else:
// there is no tile, no layer, no projection and no OpenStreetMap anywhere in
// its vocabulary. A caller hands it pixel coordinates and a colour. Where
// those coordinates came from is the renderer's problem, and keeping it that
// way is what makes this package testable against pictures rather than
// against map data.
//
// # Coordinates and units
//
// A Point is in pixels on the destination image: x runs right, y runs DOWN
// from the top-left, and the centre of the pixel at integer (x, y) is at
// (x+0.5, y+0.5). Stroke widths are in those same pixels.
//
// That is worth saying plainly because the width a STYLE carries is not in
// these units. A style width is in tile pixels, so that one style renders the
// same map at 1080p and at 4K, and it is multiplied by the scale the view
// resolves to before it reaches this package. See docs/architecture.md,
// "Styling". A width constant written straight into a Stroke here is a width
// in output pixels, which is two different maps at two resolutions.
//
// # The fill rule, which shapes the whole API
//
// The rasterizer underneath is golang.org/x/image/vector, whose fill rule is
// neither non-zero nor even-odd. Reading raster_fixed.go and
// raster_floating.go, both accumulators do this:
//
//	acc += v
//	a := acc
//	if a < 0 { a = -a }
//	if a > 1 { a = 1 }
//
// Absolute accumulated winding, clamped to one. Three things follow, and all
// three are the reason this package looks the way it does.
//
//  1. Geometry wound the same way ADDS and then clamps, so overlapping shapes
//     do not darken and duplicate geometry costs nothing but time. Strokes are
//     built on this: a stroke is a pile of overlapping quads and discs, and
//     under any other fill rule the overlaps would have to be resolved.
//
//  2. Two abutting half-covered edges in ONE path sum to full coverage, and in
//     two separate fills they do not: 0.4 + 0.6*(1-0.4) is 0.76, a permanent
//     24% hairline along the join. So everything of one colour goes into one
//     Path and is filled once. See Surface.Fill.
//
//  3. Geometry wound the OTHER way subtracts. That is how a hole is cut, and
//     it is also how a hairline appears in something that should be solid --
//     mix a ring wound one way with a stroke outline wound the other and they
//     erase each other where they overlap. Everything this package generates
//     is wound the same direction: positively by the surveyor's formula, which
//     with y running down the screen is clockwise, which is the direction the
//     vector tile specification gives an exterior ring. Rings a caller supplies
//     are used in the order given, because reversing one is how that caller
//     says "hole".
package raster

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// Point is a position in pixels on a Surface.
//
// It is float32 rather than float64 because that is what the rasterizer takes,
// and rounding to it at the boundary rather than inside every call keeps one
// representation of a coordinate rather than two.
type Point struct {
	X, Y float32
}

// segment returns the vector from a to b and its length.
//
// It is one function used by both the stroker and the dash walk, which is the
// point of it rather than a saving of three lines: those two agree on where a
// segment ends and how long it is only if they compute it the same way, and
// this project has already been bitten once by two spellings of the same
// arithmetic that agreed until they did not.
func segment(a, b Point) (dx, dy, length float32) {
	dx, dy = b.X-a.X, b.Y-a.Y
	return dx, dy, float32(math.Hypot(float64(dx), float64(dy)))
}

// Surface is a destination image together with the single rasterizer that
// every fill onto it goes through.
//
// The rasterizer is a field rather than something Fill allocates because it
// owns a width*height buffer of float32 or uint32, and a map is a few dozen
// fills of the same size: allocating that per fill would allocate the image
// again dozens of times.
type Surface struct {
	img  *image.RGBA
	rast *vector.Rasterizer
}

// NewSurface returns a Surface of w by h pixels, fully transparent.
//
// A non-positive dimension gives an empty surface rather than an error or a
// panic. Nothing can be drawn on it and nothing tries to: an empty view is a
// caller's arithmetic to get right, and there is no picture this package could
// return that would make it more visible than a zero-sized image already does.
func NewSurface(w, h int) *Surface {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Surface{
		img:  image.NewRGBA(image.Rect(0, 0, w, h)),
		rast: vector.NewRasterizer(w, h),
	}
}

// RGBA returns the image being drawn on. It is the live image, not a copy.
func (s *Surface) RGBA() *image.RGBA { return s.img }

// Bounds returns the surface's rectangle, whose Min is always the origin.
func (s *Surface) Bounds() image.Rectangle { return s.img.Bounds() }

// Background replaces every pixel with c, alpha included. c must not be nil.
//
// It is draw.Src rather than draw.Over, so it is a reset as well as a paint:
// calling it on a surface that has already been drawn on gives the same result
// as calling it on a new one. Compositing instead would be identical for the
// opaque background a map usually has and quietly wrong for a translucent one,
// which is the kind of difference that survives until somebody wants a map
// over something else.
func (s *Surface) Background(c color.Color) {
	draw.Draw(s.img, s.img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
}

// Fill composites every subpath of p onto the surface in the colour c, in one
// rasterizer pass, with antialiased edges. A nil path draws nothing; c must
// not be nil.
//
// # One Fill per colour, not one per feature
//
// The unit here is deliberately a whole path rather than a single shape, and a
// caller is expected to append hundreds of features -- every road of one class,
// from every tile of the view -- into one Path and then call this once.
//
// That is not only an efficiency argument, though it is that too. It is the
// only way to get the right picture. Coverage sums inside a single pass and
// composites between passes, so two clipped halves of one polygon that meet
// along a tile edge produce full coverage in one Fill and a 24% hairline along
// the whole seam in two. The bug is invisible on a view inside one tile and
// obvious on a view that crosses one, which is to say it depends on where the
// user happens to live. See docs/architecture.md, trap T1.
//
// # Cost, which is not proportional to what you can see
//
// Coordinates outside the surface are legal and are drawn correctly: a polygon
// far larger than the view fills it, because coverage accumulates from the
// left edge of each row whether or not the geometry that started it is on
// screen.
//
// They are not free. A segment ABOVE the surface is walked scanline by
// scanline from where it starts down to the top edge before any of it is
// discarded, so the cost is proportional to the distance rather than to
// anything visible. Measured on an M1 Pro at 600x600, one segment beginning a
// million pixels above the surface costs 12 ms and one beginning a billion
// costs 4.5 seconds -- for a line that draws nothing.
//
// Geometry from a tile's buffer sits a few pixels outside and is free.
// Geometry projected at a deep zoom is not: the whole world at zoom 22 is a
// billion pixels across. Culling features against the view, and clipping the
// ones that cross it, is the caller's job, because this package cannot tell a
// coordinate that was meant from one that was not.
func (s *Surface) Fill(p *Path, c color.Color) {
	if p == nil || len(p.verbs) == 0 {
		return
	}
	b := s.img.Bounds()
	if b.Empty() {
		return
	}
	// Reset before rather than after, for two reasons that both bite.
	// Draw is destructive on the fixed-point implementation: it accumulates
	// the delta buffer into itself in place, so a rasterizer that was drawn
	// from and not reset holds coverage where it should hold deltas. And the
	// zero value of a Rasterizer is a zero-sized one, so the first fill needs
	// this call anyway.
	s.rast.Reset(b.Dx(), b.Dy())
	p.replay(s.rast)
	s.rast.Draw(s.img, b, image.NewUniform(c), image.Point{})
}
