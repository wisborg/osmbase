package render

import (
	"fmt"
	"image"
	"image/color"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/raster"
)

// Coord is a position in degrees, latitude first as everywhere in this module.
type Coord struct{ Lat, Lon float64 }

// Drawing is what a caller asks to have drawn over a map: lines and markers,
// in coordinates.
//
// This package draws them and decides nothing about them. Which points make
// a route, where its start and its kilometre markers go, and in what colours,
// are the caller's; what is here is the geometry the map already has -- the
// projection, the clipping, the strokes -- so a line drawn over a map lands
// on the same pixels the map put the ground at.
type Drawing struct {
	Lines   []Line
	Markers []Marker
}

// Line is a polyline over the map.
type Line struct {
	Points []Coord
	// Ink is the line's colour, and Width its full width in image pixels.
	Ink   color.RGBA
	Width float64
	// Halo is how far a band of HaloInk reaches either side of the line, in
	// image pixels; zero draws none. A halo is what makes a line read over
	// the texture of a map -- roads, labels, fills -- rather than merge into
	// it, and the usual HaloInk is the palette's own Background.
	Halo    float64
	HaloInk color.RGBA
	// Dash is on and off lengths in image pixels, as raster.Stroke takes;
	// empty is solid.
	Dash []float32
}

// Marker is a dot over the map with an optional label beside it.
type Marker struct {
	At Coord
	// Ink is the dot's colour, and Radius its radius in image pixels.
	Ink    color.RGBA
	Radius float64
	// Halo and HaloInk are as a Line's, round the dot and round the label's
	// letters.
	Halo    float64
	HaloInk color.RGBA
	// Label is written to the right of the dot, or to its left when there
	// is no room on the right. Empty writes nothing.
	Label string
}

// Draw draws d over img, which is a picture of v -- usually the Image of the
// Result that rendering v returned.
//
// It is a function of the view and not of a Renderer because the picture may
// be older than any renderer: a caller that rendered a basemap once and
// keeps it draws a different route over it every frame. The projection is the
// one Render used for v, so the two agree to the pixel.
//
// All halos are drawn before any ink, so a line that crosses itself, or
// crosses another, is not cut by its own halo. Then every line in order, then
// every marker, then every label, so a label is never under a line.
//
// face is the font for labels, and may be nil when no marker has one.
func Draw(img *image.RGBA, v View, d Drawing, face font.Face) error {
	p, err := resolve(v)
	if err != nil {
		return err
	}
	if img == nil || img.Bounds() != image.Rect(0, 0, v.Width, v.Height) {
		return fmt.Errorf("render: drawing over a %v image of a %d by %d view; the two must be the same size", boundsOf(img), v.Width, v.Height)
	}
	s := raster.NewSurfaceOn(img)
	o := overlayDrawer{p: p}

	for _, l := range d.Lines {
		if l.Halo > 0 && l.Width > 0 {
			o.stroke(s, l.Points, raster.Stroke{Width: float32(l.Width + 2*l.Halo)}, l.HaloInk)
		}
	}
	for _, m := range d.Markers {
		if m.Halo > 0 && m.Radius > 0 {
			o.dot(s, m.At, m.Radius+m.Halo, m.HaloInk)
		}
	}
	for _, l := range d.Lines {
		if l.Width > 0 {
			o.stroke(s, l.Points, raster.Stroke{Width: float32(l.Width), Dash: l.Dash}, l.Ink)
		}
	}
	for _, m := range d.Markers {
		if m.Radius > 0 {
			o.dot(s, m.At, m.Radius, m.Ink)
		}
	}
	if face != nil {
		for _, m := range d.Markers {
			if m.Label != "" {
				o.label(img, m, face)
			}
		}
	}
	return nil
}

func boundsOf(img *image.RGBA) image.Rectangle {
	if img == nil {
		return image.Rectangle{}
	}
	return img.Bounds()
}

type overlayDrawer struct {
	p    projection
	clip clipper
	src  []pt
	dst  []raster.Point
	path raster.Path
}

// pixel is where a coordinate lands in the image.
func (o *overlayDrawer) pixel(c Coord) pt {
	x, y := mercator.Project(c.Lon, c.Lat)
	return pt{X: o.p.pixelX(x), Y: o.p.pixelY(y)}
}

// stroke draws one polyline in one fill.
//
// Clipped to the image, inflated by the stroke's width, BEFORE it reaches the
// rasterizer. A route runs off the view as often as not, and the rasterizer
// walks a segment's whole length scanline by scanline even where none of it
// is visible -- a segment starting a million pixels away costs milliseconds
// and draws nothing. Every clipped run goes into one path and one fill, for
// the reason the map's own strokes do: two fills along one seam leave a
// hairline where they meet.
func (o *overlayDrawer) stroke(s *raster.Surface, points []Coord, st raster.Stroke, ink color.RGBA) {
	if len(points) < 2 {
		return
	}
	o.src = o.src[:0]
	for _, c := range points {
		o.src = append(o.src, o.pixel(c))
	}
	o.path.Reset()
	clip := o.p.surface().inflate(float64(st.Width))
	o.clip.line(o.src, clip, func(run []pt) {
		o.dst = o.dst[:0]
		for _, q := range run {
			o.dst = append(o.dst, raster.Point{X: float32(q.X), Y: float32(q.Y)})
		}
		st.DashPhase = o.path.Stroke(o.dst, st)
	})
	s.Fill(&o.path, ink)
}

// dot draws a filled circle, if any of it is in the image.
func (o *overlayDrawer) dot(s *raster.Surface, at Coord, r float64, ink color.RGBA) {
	c := o.pixel(at)
	if b := o.p.surface().inflate(r); c.X < b.MinX || c.X > b.MaxX || c.Y < b.MinY || c.Y > b.MaxY {
		return
	}
	o.path.Reset()
	o.path.Circle(raster.Point{X: float32(c.X), Y: float32(c.Y)}, float32(r))
	s.Fill(&o.path, ink)
}

// label writes a marker's label beside it, with a halo round the letters.
//
// On the right of the dot, and on the left when the right would run off the
// image; vertically centred on it. The halo is the text drawn in HaloInk at
// every offset within the halo's reach, then the text in Ink over it --
// crude, and at the sizes a map label is drawn indistinguishable from a
// stroked outline, with no dependency on glyph outlines this package does
// not otherwise need.
func (o *overlayDrawer) label(dst *image.RGBA, m Marker, face font.Face) {
	c := o.pixel(m.At)
	adv := font.MeasureString(face, m.Label).Ceil()
	gap := int(m.Radius+m.Halo) + 3
	x := int(c.X) + gap
	if x+adv > dst.Bounds().Max.X {
		x = int(c.X) - gap - adv
	}
	met := face.Metrics()
	y := int(c.Y) + (met.Ascent.Ceil()-met.Descent.Ceil())/2

	if h := int(m.Halo + 0.5); h > 0 {
		d := font.Drawer{Dst: dst, Src: image.NewUniform(m.HaloInk), Face: face}
		for dy := -h; dy <= h; dy++ {
			for dx := -h; dx <= h; dx++ {
				if dx*dx+dy*dy > h*h {
					continue
				}
				d.Dot = fixed.P(x+dx, y+dy)
				d.DrawString(m.Label)
			}
		}
	}
	d := font.Drawer{Dst: dst, Src: image.NewUniform(m.Ink), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(m.Label)
}
