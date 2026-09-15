// Package render draws a geographic rectangle as a picture: it gathers the
// vector tiles covering a view, decodes them, and fills, strokes and dashes
// their features onto one image according to a style.
//
// It is the package that joins the other four. pmtiles finds the bytes, mvt
// turns them into geometry, mercator says where a coordinate is, raster puts
// ink on pixels -- and none of them knows what a map is. This one does, and it
// is the only place in the library where the words "layer", "zoom" and "pixel"
// all mean something at once.
//
// It knows nothing about where the tiles came from. TileSource is one method
// wide and is declared here rather than by any implementation, so this package
// imports neither the archive reader nor the only package that opens a socket.
// A render cannot reach the network because there is nothing in its vocabulary
// to reach it with.
//
// # What a caller gets back
//
// A Result, carrying the image and an honest account of what was drawn: the
// zoom the view resolved to, how much of it tiles were found for, how much of
// that came from a shallower zoom than asked for, and the rectangles no tile
// covered at all. None of those numbers is measured from the image. Coverage is
// a question about which tiles exist, because an ocean tile draws almost
// nothing and is not missing. See docs/architecture.md, trap T9.
//
// # The three things that make this correct rather than merely working
//
//  1. ONE rasterizer pass per rule, fed by every tile. Not one per tile. The
//     fill rule sums coverage within a pass and composites between passes, so
//     two halves of a polygon that meet on a tile edge give full coverage in
//     one pass and a permanent 24% hairline in two. It is also the only way a
//     road in one tile draws over a neighbouring tile's landuse. See trap T1.
//
//  2. Cull and clip before filling. The rasterizer draws off-surface geometry
//     correctly and at a cost proportional to how far off it is -- a segment
//     starting a billion pixels away costs 4.5 seconds -- and a tile's buffer,
//     a deep zoom and an overzoomed ancestor all produce exactly that. See
//     clip.go.
//
//  3. Deterministic order everywhere. Tiles are walked row by row, rules in the
//     order the style lists them, features in the order the tile holds them,
//     and attributes are looked up by key rather than ranged over. The same
//     activity and the same options have to produce the same frames, and a
//     coverage sum in float32 is sensitive enough to append order that a map
//     iteration would show up in the pixels.
package render

import (
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/wisborg/osmbase/raster"
)

// ErrNoCoverage is returned when no tile covers any part of the view, at any
// zoom.
//
// There is no image with it. A view with no data is not a blank map or a
// hatched one -- both of those are pictures, and a consumer laying out a frame
// needs to decide for itself whether to leave a space, close the layout up, or
// say something in words. The library measures; the caller chooses. Partial
// coverage is a different case and does return an image, with the gaps hatched.
var ErrNoCoverage = errors.New("no tile covers this view at any zoom")

// Options are what a Renderer draws with.
type Options struct {
	// Style is the ordered rule set. The zero Style has no rules and draws
	// nothing but the background, which New refuses.
	Style Style

	// Palette is the colour for each of the style's roles. It comes from the
	// caller so that a consumer with a theme can supply one without knowing
	// what a landcover class is.
	Palette Palette

	// Attribution is the credit the tiles' source requires, passed through to
	// every Result.
	//
	// It is data and not a constant, and it is supplied here rather than read
	// from the tile source, for the same reason the source interface is one
	// method wide: the obligation belongs to the data, the string lives in
	// whatever manifest recorded where the data came from, and a renderer that
	// spelled a credit into its own source code would go on printing the wrong
	// one after somebody changed archives. An empty string is allowed and means
	// the caller has not been told one; it does not mean there is nothing to
	// credit.
	Attribution string
}

// Renderer draws views from one tile source with one style.
//
// It holds no per-render state, so one may be kept for the life of a program
// and used for every view. It is not safe for concurrent use unless the
// TileSource is, which is the source's own contract to state.
type Renderer struct {
	src     TileSource
	style   Style
	palette Palette
	credit  string
}

// New returns a Renderer, refusing options it could only draw a blank from.
//
// The style is validated here rather than at the first render because the
// failure it is guarding against is a rule that draws at no zoom, whose only
// symptom is a missing layer in a map that otherwise looks fine. Reporting it
// when the renderer is built puts the error next to the style that caused it.
func New(src TileSource, o Options) (*Renderer, error) {
	if src == nil {
		return nil, fmt.Errorf("render: no tile source, so there is nothing to draw from")
	}
	if len(o.Style.Rules) == 0 {
		return nil, fmt.Errorf("render: style %q has no rules, so every view would come back as flat background", o.Style.Name)
	}
	if err := o.Style.Validate(); err != nil {
		return nil, err
	}
	return &Renderer{src: src, style: o.Style, palette: o.Palette, credit: o.Attribution}, nil
}

// Result is a rendered view and the account of what went into it.
type Result struct {
	// Image is the rendered picture, opaque, with its Min at the origin.
	Image *image.RGBA

	// Zoom is the integer tile zoom the view resolved to and asked the source
	// for, and ContinuousZoom is the unrounded zoom the view's scale
	// corresponds to. The two differ by less than half a level; the second is
	// what a caller reporting the scale of a map should show.
	Zoom           uint8
	ContinuousZoom float64

	// TilesRequested is how many squares the view was divided into and
	// TilesDrawn how many of them a tile was found for, at any zoom.
	TilesRequested, TilesDrawn int

	// Covered is the fraction of the view a tile was found for and Overzoomed
	// the fraction that came from a shallower zoom than Zoom. Both are between
	// 0 and 1, and a fully covered view that was entirely overzoomed has
	// Covered 1 and Overzoomed 1.
	//
	// Overzoomed is not a defect to be hidden. Vector data drawn from a
	// shallower tile is sharp, so it LOOKS complete, and a caller that wants to
	// say "less detail here" has no other way to know. See trap T4.
	Covered    float64
	Overzoomed float64

	// Gaps are the rectangles of the image no tile covered at any zoom, in
	// image pixels. They have been painted with the style's no-data hatch, so
	// they are already visible; they are reported as well because the decision
	// about what a half-covered map means belongs to the caller.
	Gaps []image.Rectangle

	// Attribution is the credit string the options carried, passed through
	// unchanged. A rendered map is a Produced Work under the ODbL and the
	// credit has to appear wherever it is shown.
	Attribution string
}

// Render draws the view.
//
// The whole render is one pass over the tiles per style rule, which is not the
// cheapest arrangement and is the only correct one; see the package comment.
// It is called a handful of times per video rather than once per frame -- a
// basemap is a render-wide invariant, and re-rasterizing it at every frame's
// viewport is tens of thousands of rasterizations for a difference nobody asked
// for. See docs/architecture.md, trap T8.
func (r *Renderer) Render(ctx context.Context, v View) (*Result, error) {
	p, err := resolve(v)
	if err != nil {
		return nil, err
	}

	tiles, cov, err := r.gather(ctx, p)
	if err != nil {
		return nil, err
	}
	if cov.resolved == 0 {
		return nil, fmt.Errorf("render: %d by %d view of %g,%g to %g,%g at zoom %d, %d tiles asked for: %w",
			v.Width, v.Height, v.Bounds.West, v.Bounds.South, v.Bounds.East, v.Bounds.North,
			p.tileZoom, cov.requested, ErrNoCoverage)
	}

	surface := raster.NewSurface(p.width, p.height)
	surface.Background(r.palette.Background)

	d := drawer{p: p, palette: r.palette}
	for i := range r.style.Rules {
		rule := &r.style.Rules[i]
		if !rule.appliesAt(p.tileZoom) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("render: drawing rule %d of %d, layer %q: %w", i, len(r.style.Rules), rule.Layer, err)
		}
		// One call per rule, and the tile loop lives inside it. See drawRule:
		// the nesting is trap T1 and it is enforced by where the loop is
		// rather than by a comment asking for it.
		d.drawRule(surface, rule, tiles, r.palette.colour(rule.Paint.Role))
	}

	// The hatch goes on last so that it is over everything, including any ink a
	// neighbouring tile's buffer put inside the gap. A gap is a statement that
	// nothing is known here, and a road ending halfway into it would contradict
	// that statement in the one place the picture is meant to be honest.
	d.hatch(surface, cov.gaps)

	return &Result{
		Image:          surface.RGBA(),
		Zoom:           p.tileZoom,
		ContinuousZoom: p.zoom,
		TilesRequested: cov.requested,
		TilesDrawn:     cov.resolved,
		Covered:        cov.fraction(cov.covered),
		Overzoomed:     cov.fraction(cov.over),
		Gaps:           cov.gaps,
		Attribution:    r.credit,
	}, nil
}
