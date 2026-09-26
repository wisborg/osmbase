package render

import (
	"math"

	"github.com/wisborg/osmbase/mercator"
)

// MaxFitZoom is the deepest zoom osmbase's own render fits a view at. The
// public builds stop at 15, and a rectangle small enough to want more is drawn
// at 15 with ground around it. A caller drawing something of its own over
// the map -- a short course round a park -- may want deeper, and passes its
// own; vector tiles drawn past their deepest zoom are sharp, only emptier.
const MaxFitZoom = 15

// FitMargin is the ground left around a fitted rectangle, as a fraction of
// its size on each side, so a coastline does not run along the image's edge.
const FitMargin = 0.04

// Fit is the view that holds a rectangle as closely as the image allows.
//
// At a CONTINUOUS zoom, not the deepest whole one. The renderer draws any
// scale -- it picks the nearest tile zoom and stretches -- so rounding the fit
// down to a whole zoom only threw ground away: up to twice the rectangle's
// size in each direction, which is why New South Wales came with half of
// Victoria and South Australia around it.
//
// The view has the image's own aspect ratio, so there is nothing for the
// renderer to extend or crop; the rectangle fills the image along one axis
// and is centred along the other. Centred in the PROJECTION, not on the
// average of its degrees: Mercator stretches the north more, and centring
// Denmark on 56.15 degrees leaves more margin below it than above.
//
// Three limits, each reported rather than silent. No deeper than maxZoom --
// MaxFitZoom for a map of its own. No shallower than the image allows -- the
// whole world at 1024 by 768 would fit at zoom 1.3, where the world is
// narrower than the image -- which crops, and cropped says so. And never over
// the antimeridian or past the Mercator cut: the view is slid back inside the
// world, since the renderer cannot draw across the seam. A rectangle near the
// seam -- Fiji, or Australia at zoom 4, which was refused as "wider than the
// whole world" -- then has its ground on one side rather than centred.
func Fit(b Bounds, width, height int, maxZoom float64) (View, bool) {
	x0, y0 := mercator.Project(b.West, b.North)
	x1, y1 := mercator.Project(b.East, b.South)
	cx, cy := (x0+x1)/2, (y0+y1)/2
	dx, dy := (x1-x0)*(1+2*FitMargin), (y1-y0)*(1+2*FitMargin)

	// Pixels per world unit: the world is scale pixels across.
	scale := 256 * math.Exp2(maxZoom)
	if dx > 0 {
		scale = math.Min(scale, float64(width)/dx)
	}
	if dy > 0 {
		scale = math.Min(scale, float64(height)/dy)
	}
	cropped := false
	if floor := float64(max(width, height)); scale < floor {
		scale, cropped = floor, true
	}

	w, h := float64(width)/scale, float64(height)/scale
	cx = min(max(cx, w/2), 1-w/2)
	cy = min(max(cy, h/2), 1-h/2)
	west, north := mercator.Unproject(cx-w/2, cy-h/2)
	east, south := mercator.Unproject(cx+w/2, cy+h/2)
	return View{
		Bounds: Bounds{West: west, South: south, East: east, North: north},
		Width:  width, Height: height,
	}, cropped
}
