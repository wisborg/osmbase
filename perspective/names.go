package perspective

import (
	"image"
	"image/color"
	"slices"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/render"
)

// DrawPlaceNames stands the names of places on the picture -- the labels a
// render lifted off the map with Options.LiftPointLabels -- in the palette
// the map was drawn in.
//
// Each is centred on where its place appears, upright, with a halo in the
// colour of the land so it reads on any ground, and only where the place is
// seen: a name on the far side of a hill is not drawn floating over the hill.
// Lying on the map instead, a name on a slope is stretched across it and one
// in the distance is squashed flat. They come most important first, as the
// map placed them, and one that would overlap a name already drawn, or run
// off the picture, is left out.
func (p *Picture) DrawPlaceNames(labels []render.PointLabel, pal render.Palette) {
	img := p.Image
	halo := pal.Land
	if pal.Omits(render.RoleLand) {
		halo = pal.Background
	}
	var drawn []image.Rectangle
	for _, l := range labels {
		x, y, seen := p.Locate(l.At)
		if !seen || l.Face == nil {
			continue
		}
		box := placeBox(l, x, y)
		if !box.In(img.Bounds()) || slices.ContainsFunc(drawn, box.Overlaps) {
			continue
		}
		drawn = append(drawn, box)
		drawPlaceName(img, l, x, y, pal, halo)
	}
}

// placeBox is the space a place's name centred on (x, y) takes, with room
// for its halo and some air.
func placeBox(l render.PointLabel, x, y float64) image.Rectangle {
	w := font.MeasureString(l.Face, l.Text).Ceil()
	m := l.Face.Metrics()
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	return image.Rect(int(x)-w/2-4, int(y)-(asc+desc)/2-4, int(x)+w/2+4, int(y)+(asc+desc)/2+4)
}

// drawPlaceName writes a place's name upright, centred on (x, y), in the
// palette's ink over a halo.
func drawPlaceName(img *image.RGBA, l render.PointLabel, x, y float64, pal render.Palette, halo color.RGBA) {
	w := font.MeasureString(l.Face, l.Text).Ceil()
	m := l.Face.Metrics()
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	ink := pal.Label
	if l.Minor && pal.LabelMinor != (color.RGBA{}) {
		ink = pal.LabelMinor
	}
	base := fixed.P(int(x)-w/2, int(y)-(asc+desc)/2+asc)
	d := font.Drawer{Dst: img, Face: l.Face, Src: image.NewUniform(halo)}
	for _, o := range [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}, {-2, 0}, {2, 0}, {0, -2}, {0, 2}} {
		d.Dot = base.Add(fixed.P(o[0], o[1]))
		d.DrawString(l.Text)
	}
	d.Src, d.Dot = image.NewUniform(ink), base
	d.DrawString(l.Text)
}

// DrawMarkers draws markers -- a route's start and finish, its distance
// markers -- standing upright where their places are seen, as
// render.DrawMarkers draws them, and leaves out those a hill hides or the
// picture does not take in. Draped with the map instead, they lie on the
// ground, and a label in the distance or on a slope is squashed past reading.
func (p *Picture) DrawMarkers(markers []render.Marker, face font.Face) {
	render.DrawMarkers(p.Image, markers, p.Locate, face)
}
