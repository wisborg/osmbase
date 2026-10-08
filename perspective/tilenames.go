package perspective

import (
	"cmp"
	"image"
	"image/draw"
	"math"
	"slices"
	"sync"

	"golang.org/x/image/font"

	"github.com/wisborg/osmbase/render"
)

// DrawNames stands the names a pyramid's tiles carry on the picture: every
// tile the picture was drawn from hands back the names placed on it, and
// each is drawn where its place or its line appears -- a place's name
// upright, a street's or a contour's along the line as the camera sees it,
// turned to read left to right -- in the palette the tiles were drawn in.
//
// # Nothing pops
//
// The pictures of a flight are frames of a video, and a name that appears
// or vanishes from one frame to the next is a flicker. So how strongly each
// name is shown is a continuous function of the camera: nothing about a
// name is decided by a threshold that a small move of the camera can cross.
// Names are drawn at fractions of a pixel, so they glide rather than step a
// pixel at a time; and every reason a name is not shown in full is a
// fraction of it, not a yes or a no:
//
//   - its zoom: a name belongs to the zoom its tile was drawn at -- a town's
//     to a coarse tile, a lane's to a fine one -- and is shown as strongly as
//     that zoom is in the ground's colour around it, so as the camera nears a
//     place, the coarse zoom's names fade out over the stretch the fine
//     zoom's fade in;
//   - the haze, with the ground under it;
//   - a hill: the share of the ground around it that is seen, so a name
//     fades as a crest rises over it;
//   - the edge of the picture, which it fades across rather than leaving the
//     moment it touches;
//   - the names already shown: the share of it they cover, as strongly as
//     they are shown -- and the same name nearby counts as covering it, so a
//     street's name from one zoom gives way to the same name from the next
//     rather than both showing.
//
// Names are taken in one order in every frame -- places, then names along
// lines, then contour heights; coarser zooms before finer -- so the same
// name gives way to the same other name in every frame.
//
// Names are drawn one picture at a time, whatever goroutine draws each: a
// font face is not safe for concurrent use -- it draws each glyph into a
// buffer of its own, which the next glyph overwrites -- and the pictures of
// one flight share the names, and so the faces, of the tiles they share.
func (p *Picture) DrawNames(pal render.Palette) {
	if p.used == nil {
		return
	}
	namesMu.Lock()
	defer namesMu.Unlock()
	for _, s := range p.shownNames(1) {
		p.drawName(pal, s.label, s.x, s.y, s.angle, s.alpha)
	}
}

// nameID is a name of a pyramid: the tile it belongs to, what kind of name
// it is, and which of the tile's names of that kind. It sorts names in the
// order they are taken in.
type nameID struct {
	kind int // 0 a place, 1 along a line, 2 a contour's height
	key  tileKey
	i    int
}

func (a nameID) compare(b nameID) int {
	return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.key.z, b.key.z),
		cmp.Compare(a.key.y, b.key.y), cmp.Compare(a.key.x, b.key.x), cmp.Compare(a.i, b.i))
}

// name is one name of a pyramid, a place's as a name at no angle.
type name struct {
	id    nameID
	label render.LineLabel
	place bool
}

// names are the names of the tiles the picture was drawn from, in the
// order they are taken in.
func (p *Picture) names() []name {
	var out []name
	for k, t := range p.used {
		for i, l := range t.Places {
			out = append(out, name{id: nameID{kind: 0, key: k, i: i}, place: true,
				label: render.LineLabel{Text: l.Text, At: l.At, Face: l.Face, Minor: l.Minor}})
		}
		for i, l := range t.Lines {
			kind := 1
			if l.Contour {
				kind = 2
			}
			out = append(out, name{id: nameID{kind: kind, key: k, i: i}, label: l})
		}
	}
	slices.SortFunc(out, func(a, b name) int { return a.id.compare(b.id) })
	return out
}

// shownName is a name as shown: where, at what angle, how strongly, and
// the box it takes.
type shownName struct {
	name
	box   image.Rectangle
	x, y  float64
	angle float64
	alpha float64
}

// shownNames are the names shown on the picture and how strongly; see
// DrawNames. Positions and boxes are in the pixels of a frame scale times
// the picture's size each way -- 1 for the picture itself, more for a
// picture drawn small to plan a full-size frame's names, whose boxes have
// to be measured as they will be drawn.
func (p *Picture) shownNames(scale float64) []shownName {
	b := p.Image.Bounds()
	bounds := image.Rect(0, 0, int(math.Round(float64(b.Dx())*scale)), int(math.Round(float64(b.Dy())*scale)))
	var shown []shownName
	for _, n := range p.names() {
		l := n.label
		if l.Face == nil {
			continue
		}
		m := l.Face.Metrics()
		height := float64(m.Ascent.Ceil() + m.Descent.Ceil())
		alpha, x, y, ok := p.nameStrength(l.At, n.id.key.z, height/2/scale)
		if !ok {
			continue
		}
		x, y = x*scale, y*scale
		angle := 0.0
		if !n.place {
			if angle, ok = p.screenAngle(l.At, l.Angle); !ok {
				continue
			}
		}
		s := shownName{name: n, box: alongBox(l, x, y, angle), x: x, y: y, angle: angle}
		alpha *= edgeFadeBox(s.box, bounds, 2*height)
		for _, o := range shown {
			alpha *= 1 - o.alpha*cover(s.box, o.box, l.Text == o.label.Text)
		}
		if alpha < 1.0/64 {
			continue
		}
		s.alpha = alpha
		shown = append(shown, s)
	}
	return shown
}

// drawName draws one name at alpha strength.
func (p *Picture) drawName(pal render.Palette, l render.LineLabel, x, y, angle, alpha float64) {
	halo := pal.Land
	if pal.Omits(render.RoleLand) {
		halo = pal.Background
	}
	drawFaded(p.Image, alongBox(l, x, y, angle), alpha, func(dst *image.RGBA) {
		render.DrawLineLabel(dst, l, x, y, angle, pal, halo)
	})
}

// namesMu is held while names are drawn; see DrawNames.
var namesMu sync.Mutex

// cover is how much of one name's box another covers, as a share of the
// smaller and tripled -- a third of a name hidden is all of it lost --
// at most 1. Two boxes of the same name count as covering each other more
// the nearer they are, up to twice the longer one's width apart: two copies
// of one street's name a little apart are one name too many.
func cover(a, b image.Rectangle, same bool) float64 {
	c := 0.0
	if in := a.Intersect(b); !in.Empty() {
		small := math.Min(float64(a.Dx()*a.Dy()), float64(b.Dx()*b.Dy()))
		if small > 0 {
			c = math.Min(1, 3*float64(in.Dx()*in.Dy())/small)
		}
	}
	if same {
		ax, ay := float64(a.Min.X+a.Max.X)/2, float64(a.Min.Y+a.Max.Y)/2
		bx, by := float64(b.Min.X+b.Max.X)/2, float64(b.Min.Y+b.Max.Y)/2
		reach := 2 * float64(max(a.Dx(), b.Dx()))
		if reach > 0 {
			c = math.Max(c, math.Max(0, 1-math.Hypot(ax-bx, ay-by)/reach))
		}
	}
	return c
}

// edgeFadeBox is how strongly a name in box is shown at the picture's edge:
// in full inside it, fading out as the box crosses the edge, gone once it
// is ramp pixels past it.
func edgeFadeBox(box, bounds image.Rectangle, ramp float64) float64 {
	d := float64(min(box.Min.X-bounds.Min.X, box.Min.Y-bounds.Min.Y, bounds.Max.X-box.Max.X, bounds.Max.Y-box.Max.Y))
	if d >= 0 {
		return 1
	}
	return smoothstep((d + ramp) / ramp)
}

// nameSamples are where around a name's anchor its zoom and whether it is
// seen are read, as fractions of the radius: the anchor and a ring round
// it, so both change smoothly as a crest or the edge of a zoom crosses the
// name rather than flipping as it crosses one pixel.
var nameSamples = [][2]float64{{0, 0}, {1, 0}, {-1, 0}, {0, 1}, {0, -1}, {0.7, 0.7}, {-0.7, 0.7}, {0.7, -0.7}, {-0.7, -0.7}}

// nameStrength is how strongly a name of zoom z centred at c is shown, and
// where in the image: as strongly as zoom z is in the ground's colour around
// it, eased so the change from one zoom's names to the next is brief; as
// much as the ground within r image pixels of it is seen; and faded with
// the ground into the haze. Not ok where it would not be shown at all.
func (p *Picture) nameStrength(c render.Coord, z uint8, r float64) (alpha, x, y float64, ok bool) {
	sx, sy, depth, in := p.onFrame(c)
	if !in {
		return 0, 0, 0, false
	}
	var seen, lodSum, lodN float64
	for _, d := range nameSamples {
		ix, iy := int(sx+d[0]*r*supersample), int(sy+d[1]*r*supersample)
		if p.seenAt(ix, iy, depth) {
			seen++
		}
		if ix >= 0 && iy >= 0 && ix < p.w && iy < p.h {
			if l := p.lod[iy*p.w+ix]; !math.IsNaN(float64(l)) {
				lodSum, lodN = lodSum+float64(l), lodN+1
			}
		}
	}
	if seen == 0 || lodN == 0 {
		return 0, 0, 0, false
	}
	// The zoom's weight in the colour there, from the blend of the two
	// zooms either side; eased over the middle of the blend.
	w := 1 - math.Abs(lodSum/lodN-float64(z))
	alpha = smoothstep((w-0.35)/0.3) * seen / float64(len(nameSamples))
	u, v, err := p.view.Pixel(c)
	if err != nil {
		return 0, 0, 0, false
	}
	alpha *= 1 - math.Max(p.tex.edgeFade(u, v), hazeAt(depth, p.hazeM))
	if alpha < 1.0/64 {
		return 0, 0, 0, false
	}
	return alpha, sx / supersample, sy / supersample, true
}

// screenAngle is the direction, on the picture, of a line through c running
// angle radians clockwise from east on the map: from c to a point a little
// way along it, through the camera, turned if need be to read left to
// right. Not ok where the line runs so nearly toward the camera that the
// direction cannot be told.
func (p *Picture) screenAngle(c render.Coord, angle float64) (float64, bool) {
	const step = 20.0 // metres
	east, south := step*math.Cos(angle), step*math.Sin(angle)
	d := render.Coord{
		Lat: c.Lat - south/110_574,
		Lon: c.Lon + east/(111_320*math.Cos(c.Lat*math.Pi/180)),
	}
	x0, y0, ok0 := p.Project(c)
	x1, y1, ok1 := p.Project(d)
	if !ok0 || !ok1 || math.Hypot(x1-x0, y1-y0) < 1 {
		return 0, false
	}
	a := math.Atan2(y1-y0, x1-x0)
	if math.Cos(a) < 0 {
		a += math.Pi
	}
	return a, true
}

// alongBox is the space a name along a line takes, centred on (x, y) and
// turned by angle: the box round the turned rectangle, with room for its
// halo and some air.
func alongBox(l render.LineLabel, x, y, angle float64) image.Rectangle {
	w := float64(font.MeasureString(l.Face, l.Text).Ceil()) + 8
	m := l.Face.Metrics()
	h := float64(m.Ascent.Ceil()+m.Descent.Ceil()) + 8
	c, s := math.Abs(math.Cos(angle)), math.Abs(math.Sin(angle))
	hw, hh := (w*c+h*s)/2, (w*s+h*c)/2
	return image.Rect(int(math.Floor(x-hw)), int(math.Floor(y-hh)), int(math.Ceil(x+hw)), int(math.Ceil(y+hh)))
}

// drawFaded has paint draw onto a copy of the box, and mixes the copy back
// into img alpha of the way: a name drawn at part strength by code that
// only draws at full.
func drawFaded(img *image.RGBA, box image.Rectangle, alpha float64, paint func(dst *image.RGBA)) {
	r := box.Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	if alpha >= 1 {
		paint(img)
		return
	}
	scratch := image.NewRGBA(r)
	draw.Draw(scratch, r, img, r.Min, draw.Src)
	paint(scratch)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a, b := img.PixOffset(x, y), scratch.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				img.Pix[a+c] = uint8(float64(img.Pix[a+c]) + (float64(scratch.Pix[b+c])-float64(img.Pix[a+c]))*alpha + 0.5)
			}
		}
	}
}

// smoothstep eases 0 to 1 as t goes 0 to 1, flat at both ends.
func smoothstep(t float64) float64 {
	t = math.Max(0, math.Min(1, t))
	return t * t * (3 - 2*t)
}
