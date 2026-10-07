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
// # Which names, and how strongly
//
// A name belongs to the zoom its tile was drawn at: a town's name to a
// coarse tile, a lane's to a fine one. It is shown where the ground is drawn
// from that zoom, and as strongly as that zoom is in the colour there, so
// as the camera nears a place and its ground blends from one zoom to the
// next, the coarse zoom's names fade out over the same stretch the fine
// zoom's fade in. A name is faded into the haze with the ground under it,
// and left out where a hill hides it.
//
// # Which names win
//
// Names are drawn in one order in every frame -- places, then names along
// lines, then contour heights; coarser zooms before finer -- and one that
// would overlap a name already drawn, or run off the picture, is left out.
// The order does not depend on the frame, so from one frame to the next the
// same name wins the same contest; a name changes only as its strength
// does. Only a name at least half shown takes room from the ones after it:
// one fading out does not keep the one replacing it from fading in.
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
	halo := pal.Land
	if pal.Omits(render.RoleLand) {
		halo = pal.Background
	}

	type name struct {
		kind  int // 0 a place, 1 along a line, 2 a contour's height
		key   tileKey
		i     int
		place *render.PointLabel
		line  *render.LineLabel
	}
	var names []name
	for k, t := range p.used {
		for i := range t.Places {
			names = append(names, name{kind: 0, key: k, i: i, place: &t.Places[i]})
		}
		for i := range t.Lines {
			kind := 1
			if t.Lines[i].Contour {
				kind = 2
			}
			names = append(names, name{kind: kind, key: k, i: i, line: &t.Lines[i]})
		}
	}
	slices.SortFunc(names, func(a, b name) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.key.z, b.key.z),
			cmp.Compare(a.key.y, b.key.y), cmp.Compare(a.key.x, b.key.x), cmp.Compare(a.i, b.i))
	})

	type shown struct {
		box   image.Rectangle
		alpha float64
		draw  func(dst *image.RGBA)
	}
	var strong, faint []shown
	for _, n := range names {
		at := render.Coord{}
		var face font.Face
		if n.place != nil {
			at, face = n.place.At, n.place.Face
		} else {
			at, face = n.line.At, n.line.Face
		}
		if face == nil {
			continue
		}
		alpha, x, y, ok := p.nameStrength(at, n.key.z)
		if !ok {
			continue
		}
		var s shown
		if n.place != nil {
			l := *n.place
			s = shown{box: placeBox(l, x, y), alpha: alpha, draw: func(dst *image.RGBA) { drawPlaceName(dst, l, x, y, pal, halo) }}
		} else {
			l := *n.line
			angle, ok := p.screenAngle(at, l.Angle)
			if !ok {
				continue
			}
			s = shown{box: alongBox(l, x, y, angle), alpha: alpha, draw: func(dst *image.RGBA) { render.DrawLineLabel(dst, l, x, y, angle, pal, halo) }}
		}
		if !s.box.In(p.Image.Bounds()) {
			continue
		}
		if s.alpha >= 0.5 {
			strong = append(strong, s)
		} else {
			faint = append(faint, s)
		}
	}

	var taken []image.Rectangle
	overlaps := func(b image.Rectangle) bool { return slices.ContainsFunc(taken, b.Overlaps) }
	var draws []shown
	for _, s := range strong {
		if !overlaps(s.box) {
			taken = append(taken, s.box)
			draws = append(draws, s)
		}
	}
	for _, s := range faint {
		if !overlaps(s.box) {
			draws = append(draws, s)
		}
	}
	for _, s := range draws {
		drawFaded(p.Image, s.box, s.alpha, s.draw)
	}
}

// namesMu is held while names are drawn; see DrawNames.
var namesMu sync.Mutex

// nameStrength is how strongly a name of zoom z centred at c is shown, and
// where in the image: as strongly as zoom z is in the ground's colour there,
// eased so the change from one zoom's names to the next is brief, and faded
// with the ground into the haze. Not ok where c is not seen.
func (p *Picture) nameStrength(c render.Coord, z uint8) (alpha, x, y float64, ok bool) {
	x, y, seen, in := p.place(c)
	if !in || !seen {
		return 0, 0, 0, false
	}
	ix, iy := int(x*supersample), int(y*supersample)
	if ix < 0 || iy < 0 || ix >= p.w || iy >= p.h {
		return 0, 0, 0, false
	}
	i := iy*p.w + ix
	lod := float64(p.lod[i])
	if math.IsNaN(lod) {
		return 0, 0, 0, false
	}
	// The zoom's weight in the colour there, from the blend of the two
	// zooms either side; eased over the middle of the blend.
	w := 1 - math.Abs(lod-float64(z))
	alpha = smoothstep((w - 0.35) / 0.3)
	u, v, err := p.view.Pixel(c)
	if err != nil {
		return 0, 0, 0, false
	}
	alpha *= 1 - math.Max(p.tex.edgeFade(u, v), hazeAt(p.depth[i], p.hazeM))
	if alpha < 1.0/64 {
		return 0, 0, 0, false
	}
	return alpha, x, y, true
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
