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
		p.drawName(pal, prepareName(s.label), s.x, s.y, s.angle, s.alpha)
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
				label: render.LineLabel{Text: l.Text, At: l.At, Face: l.Face, Minor: l.Minor, Political: l.Political}})
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

	// fineAt is where the finest zoom that names a place puts it, and
	// fineZ that zoom; see samePlace.
	fineAt render.Coord
	fineZ  uint8
}

// shownNames are the names shown on the picture and how strongly; see
// DrawNames. Positions and boxes are in the pixels of a frame scale times
// the picture's size each way -- 1 for the picture itself, more for a
// picture drawn small to plan a full-size frame's names, whose boxes have
// to be measured as they will be drawn.
//
// A place named by the tiles of two zooms is one name, as strong as the two
// zooms together: see samePlace.
func (p *Picture) shownNames(scale float64) []shownName {
	b := p.Image.Bounds()
	bounds := image.Rect(0, 0, int(math.Round(float64(b.Dx())*scale)), int(math.Round(float64(b.Dy())*scale)))
	var found []shownName
	places := map[string][]int{} // a place's name to where it is in found
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
		if n.place {
			if j, ok := samePlace(found, places[l.Text], l.At, n.id.key.z); ok {
				f := &found[j]
				f.alpha = math.Min(1, f.alpha+alpha)
				if n.id.key.z > f.fineZ {
					f.fineAt, f.fineZ = l.At, n.id.key.z
				}
				continue
			}
			places[l.Text] = append(places[l.Text], len(found))
		}
		x, y = x*scale, y*scale
		angle := 0.0
		if !n.place {
			if angle, ok = p.screenAngle(l.At, l.Angle); !ok {
				continue
			}
		}
		box := labelBox(l, x, y, angle)
		found = append(found, shownName{name: n, box: box, x: x, y: y, angle: angle,
			alpha: alpha * edgeFadeBox(box, bounds, 2*height), fineAt: l.At, fineZ: n.id.key.z})
	}
	var shown []shownName
	for _, s := range found {
		for _, o := range shown {
			s.alpha *= 1 - o.alpha*cover(s.box, o.box, s.label.Text == o.label.Text)
		}
		if s.alpha < 1.0/64 {
			continue
		}
		shown = append(shown, s)
	}
	return shown
}

// nearEnough says whether two places of one name, at a and b, are the same
// place, one of them named by a tile of zoom z, the coarser: the same point
// of the data, which each zoom's tile puts a few of its own pixels off --
// rounded to a 256th of the tile, and lifted with the name's box, centred a
// little above its point -- so Poland's name was 5 km from zoom 6's at zoom
// 7, half that again at zoom 8, and on, as the point shown each time was
// the same few pixels off it on a map twice the scale. Six 256ths of the
// coarser zoom's tile takes that in -- 30 km at zoom 5, 1 km at zoom 10 --
// and is short of two places of one name that a map of that zoom would
// both show. Two 256ths, tried first, missed it: each zoom's copy was
// smoothed as a name of its own, and dipped as each gave way to the next.
func nearEnough(a, b render.Coord, z uint8) bool {
	d := 6 * 360 / math.Exp2(float64(z)) / 256
	return math.Abs(a.Lat-b.Lat) < d && math.Abs(a.Lon-b.Lon) < d
}

// samePlace is which of the places found at the given places of found, all
// of one name, is the place a tile of zoom z puts at c, if one is.
//
// A town is named by the tile of every zoom it is a town at, and as the
// camera nears it the coarser zoom's tile gives way to the finer one's over
// a stretch of the ground. Taken as two names, each was shown as strongly
// as its zoom is in the ground's colour there, so across the stretch the
// one fading out and the one fading in were each at a third or so, the
// second hidden further by the first -- and the town's name sank to half
// strength and came back each time a zoom gave way to the next: in motion,
// every town on the way pulsed. Taken as one name, the two zooms' strengths
// add up to the whole of it.
func samePlace(found []shownName, at []int, c render.Coord, z uint8) (int, bool) {
	for _, j := range at {
		f := found[j]
		if nearEnough(f.label.At, c, min(f.id.key.z, z)) {
			return j, true
		}
	}
	return 0, false
}

// drawName draws one name, prepared, at alpha strength. It uses no face,
// so pictures draw their names at the same time.
func (p *Picture) drawName(pal render.Palette, l *render.PreparedLabel, x, y, angle, alpha float64) {
	if l == nil {
		return
	}
	halo := pal.Land
	if pal.Omits(render.RoleLand) {
		halo = pal.Background
	}
	p.drawFaded(alongBox(l, x, y, angle), alpha, func(dst *image.RGBA) {
		l.Draw(dst, x, y, angle, pal, halo)
	})
}

// prepareName prepares a name to be stood on a picture: with a halo a fifth
// of its height round each letter. A map's pixel and a half was enough
// among a map's own lines, set to leave room for them; stood over a
// picture of a country's fields, forests and roads, shrunk and shaded,
// and then through a video's encoding, a name with no more than that
// round it was lost in the ground's pattern.
func prepareName(l render.LineLabel) *render.PreparedLabel {
	if l.Face == nil {
		return nil
	}
	m := l.Face.Metrics()
	return render.PrepareLineLabelHalo(l, max(1.5, 0.2*float64(m.Ascent.Ceil()+m.Descent.Ceil())))
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
	alpha = smoothstep((w-0.4)/0.2) * seen / float64(len(nameSamples))
	u, v, err := p.view.Pixel(c)
	if err != nil {
		return 0, 0, 0, false
	}
	// Faded with the ground, but later: a name is the last of the ground to
	// go into the haze. Faded as the ground is, a name half way into it was
	// drawn at half strength, and over a cruise, where the haze lies at the
	// same distance for minutes, every name crossing the far half of the
	// picture crawled through it as a ghost.
	fade := math.Max(p.tex.edgeFade(u, v), hazeAt(depth, p.hazeM))
	alpha *= 1 - fade*fade
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
func alongBox(l *render.PreparedLabel, x, y, angle float64) image.Rectangle {
	w, h := l.Size()
	return turnedBox(w+8, h+8, x, y, angle)
}

// labelBox is alongBox for a name not yet prepared, measured with its face:
// for the planning, which works under namesMu.
func labelBox(l render.LineLabel, x, y, angle float64) image.Rectangle {
	w := float64(font.MeasureString(l.Face, l.Text).Ceil()) + 8
	m := l.Face.Metrics()
	h := float64(m.Ascent.Ceil()+m.Descent.Ceil()) + 8
	return turnedBox(w, h, x, y, angle)
}

// turnedBox is the box round a w by h rectangle centred at (x, y) and turned
// by angle.
func turnedBox(w, h, x, y, angle float64) image.Rectangle {
	c, s := math.Abs(math.Cos(angle)), math.Abs(math.Sin(angle))
	hw, hh := (w*c+h*s)/2, (w*s+h*c)/2
	return image.Rect(int(math.Floor(x-hw)), int(math.Floor(y-hh)), int(math.Ceil(x+hw)), int(math.Ceil(y+hh)))
}

// drawFaded has paint draw onto a copy of the box, and mixes the copy back
// into img alpha of the way: a name drawn at part strength by code that
// only draws at full.
//
// The copy is made in a buffer the picture keeps for it, grown as needed,
// rather than in a new image for every name of every frame.
func (p *Picture) drawFaded(box image.Rectangle, alpha float64, paint func(dst *image.RGBA)) {
	img := p.Image
	r := box.Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	if alpha >= 1 {
		paint(img)
		return
	}
	n := 4 * r.Dx() * r.Dy()
	if cap(p.scratch) < n {
		p.scratch = make([]uint8, n)
	}
	scratch := &image.RGBA{Pix: p.scratch[:n], Stride: 4 * r.Dx(), Rect: r}
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
