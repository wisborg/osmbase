package render

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/mvt"
)

// This file writes a line feature's name ALONG the line: rotated to the
// road's direction and centred on it, so that on a road drawn as a strip
// the name sits on the road it names, as on every street map, rather than
// across the houses beside it.
//
// # Why not horizontal any more
//
// Horizontal text beside a line was the first version, for two reasons the
// PlaceLine comment gave: horizontal text is read faster, and rotating
// glyphs meant resampling a bitmap, which at map sizes looks worse than
// leaving them straight. The first stopped holding once roads became strips
// -- a horizontal name across a diagonal street is written over two blocks
// and the street between them, and belongs to none of them. The second is
// answered by drawing the name at several times its size and averaging it
// down as it is rotated, which is supersampling rather than resampling: the
// result is as sharp as a horizontal label, and the font's own hinting and
// fallback and right-to-left shaping are used unchanged, as the text is
// drawn by the same font.Drawer as every other label.

// alongSupersample is how many times its size a rotated name is drawn at
// before it is averaged down onto the map. Four: below it the averaging
// still shows the glyphs' pixel steps on a slant, above it nothing more is
// visible and the buffers grow by the square.
const alongSupersample = 4

// maxBend is how far, as a share of a name's height, the line under it may
// stray from the straight stretch the name is written along: a road that
// bends more than that under its own name is not where the name goes.
const maxBend = 0.35

// repeatEvery is the least distance between two copies of one name along a
// road, in output pixels, and repeatNames how many of the name's own lengths
// it is at least: a long street is named every so often along it, so that
// wherever on the map it is met its name is near, but never so often that
// the map turns into one name.
const (
	repeatEvery = 600.0
	repeatNames = 4.0
)

// repeatSpacing is how far apart copies of a name w pixels long are kept.
func repeatSpacing(w float64) float64 { return math.Max(repeatEvery, repeatNames*w) }

// alongNudges are how far, as a share of the spacing, a copy may move from
// where it was meant to go to find a straight stretch: the middle first.
var alongNudges = []float64{0, -0.12, 0.12, -0.24, 0.24, -0.36, 0.36}

// middleNudges are how far, as a share of the line, the copy at its middle
// may move to find a straight stretch: as far as the line goes.
var middleNudges = []float64{0, -0.12, 0.12, -0.24, 0.24, -0.36, 0.36, -0.48, 0.48}

// spot is where a name goes along a line: its centre and its direction.
type spot struct{ x, y, angle float64 }

// placeAlong finds where along the longest part of lines -- in tile
// coordinates, placed on the surface by tr -- a name w pixels long and h
// high can be written straight: the middle of a stretch of the line at
// least that long that bends no more than maxBend of h under it, for the
// middle of the line and then every repeatSpacing either side of it, each
// nudged to the nearest straight stretch. The angle is the stretch's
// direction, turned if need be so the text reads left to right, never
// upside down, for a viewer facing the compass bearing facing, in radians:
// 0 for a flat map, read north up. Seen facing h, a direction on the map
// points right on the viewer's screen when it is within a right angle of
// (cos h, sin h) in surface pixels -- east turned clockwise by h, y running
// south -- so a name is turned when its direction is not.
//
// There are none when the line is shorter on the map than its own name, or
// bends everywhere: a name longer than its street, or wrapped round a
// corner, labels nothing and is clutter, so it is not drawn. The first spot
// is the one nearest the middle.
func placeAlong(lines [][]mvt.Point, tr tileTransform, w, h, facing float64) []spot {
	parts := make([][]pt, 0, len(lines))
	for _, line := range lines {
		ps := make([]pt, len(line))
		for i, p := range line {
			ps[i] = tr.apply(p.X, p.Y)
		}
		parts = append(parts, ps)
	}
	return placeAlongPixels(parts, w, h, facing)
}

// placeAlongPixels is placeAlong for lines already in surface pixels: a
// contour traced from the view's own heights has no tile to come from.
func placeAlongPixels(parts [][]pt, w, h, facing float64) []spot {
	var best []pt
	var bestLen float64
	for _, ps := range parts {
		if len(ps) < 2 {
			continue
		}
		var n float64
		for i := 1; i < len(ps); i++ {
			n += math.Hypot(ps[i].X-ps[i-1].X, ps[i].Y-ps[i-1].Y)
		}
		if n > bestLen {
			best, bestLen = ps, n
		}
	}
	if best == nil {
		return nil
	}
	at := func(s float64) (pt, int) {
		var run float64
		for i := 1; i < len(best); i++ {
			seg := math.Hypot(best[i].X-best[i-1].X, best[i].Y-best[i-1].Y)
			if run+seg >= s {
				t := 0.0
				if seg > 0 {
					t = (s - run) / seg
				}
				return pt{X: best[i-1].X + t*(best[i].X-best[i-1].X), Y: best[i-1].Y + t*(best[i].Y-best[i-1].Y)}, i
			}
			run += seg
		}
		return best[len(best)-1], len(best) - 1
	}
	// try is the spot for a name centred s along the line, if the stretch
	// under it is straight enough.
	try := func(s float64) (spot, bool) {
		s0, s1 := s-w/2, s+w/2
		if s0 < 0 || s1 > bestLen {
			return spot{}, false
		}
		a, i0 := at(s0)
		b, i1 := at(s1)
		dx, dy := b.X-a.X, b.Y-a.Y
		chord := math.Hypot(dx, dy)
		if chord == 0 {
			return spot{}, false
		}
		for i := i0; i < i1; i++ {
			p := best[i]
			if math.Abs((p.X-a.X)*dy-(p.Y-a.Y)*dx)/chord > maxBend*h {
				return spot{}, false
			}
		}
		mid, _ := at(s)
		angle := math.Atan2(dy, dx)
		if math.Cos(angle-facing) < 0 {
			angle += math.Pi
			if angle > math.Pi {
				angle -= 2 * math.Pi
			}
		}
		return spot{mid.X, mid.Y, angle}, true
	}
	spacing := repeatSpacing(w)
	var out []spot
	for k := 0; ; k++ {
		any := false
		for _, side := range []float64{1, -1} {
			if k == 0 && side < 0 {
				continue
			}
			target := bestLen/2 + side*float64(k)*spacing
			if target < w/2-spacing/2 || target > bestLen-w/2+spacing/2 {
				continue
			}
			any = true
			// The middle copy may move anywhere along the line to find a
			// straight stretch, as the one name always could -- its nudges
			// are shares of the whole line; the others only so far that
			// they stay most of a spacing apart.
			nudges, by := alongNudges, spacing
			if k == 0 {
				nudges, by = middleNudges, bestLen
			}
			for _, n := range nudges {
				if sp, ok := try(target + n*by); ok {
					out = append(out, sp)
					break
				}
			}
		}
		if !any {
			return out
		}
	}
}

// quad is a label's space on the map: the four corners of a rectangle,
// rotated with a name written along a road, square for any other.
type quad [4]pt

// alongQuad is the space a name w by h, centred on (x, y) at angle, takes,
// padded by pad on every side.
func alongQuad(x, y, angle, w, h float64, pad int) quad {
	hw, hh := w/2+float64(pad), h/2+float64(pad)
	c, s := math.Cos(angle), math.Sin(angle)
	var q quad
	for i, d := range [4][2]float64{{-hw, -hh}, {hw, -hh}, {hw, hh}, {-hw, hh}} {
		q[i] = pt{X: x + d[0]*c - d[1]*s, Y: y + d[0]*s + d[1]*c}
	}
	return q
}

// rectQuad is r as a quad.
func rectQuad(r image.Rectangle) quad {
	return quad{
		{X: float64(r.Min.X), Y: float64(r.Min.Y)}, {X: float64(r.Max.X), Y: float64(r.Min.Y)},
		{X: float64(r.Max.X), Y: float64(r.Max.Y)}, {X: float64(r.Min.X), Y: float64(r.Max.Y)},
	}
}

// overlaps reports whether two quads share any area, by the separating axis
// test: two convex shapes are apart exactly when, along the normal of one
// of their edges, their shadows do not meet.
func (q quad) overlaps(o quad) bool {
	for _, shape := range [2]quad{q, o} {
		for i := 0; i < 4; i++ {
			a, b := shape[i], shape[(i+1)%4]
			nx, ny := -(b.Y - a.Y), b.X-a.X
			qmin, qmax := q.shadow(nx, ny)
			omin, omax := o.shadow(nx, ny)
			if qmax <= omin || omax <= qmin {
				return false
			}
		}
	}
	return true
}

func (q quad) shadow(nx, ny float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, p := range q {
		v := p.X*nx + p.Y*ny
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

// in reports whether every corner of q is inside r.
func (q quad) in(r image.Rectangle) bool {
	for _, p := range q {
		if p.X < float64(r.Min.X) || p.Y < float64(r.Min.Y) || p.X > float64(r.Max.X) || p.Y > float64(r.Max.Y) {
			return false
		}
	}
	return true
}

// haloWidth is how far a road name's halo reaches round its glyphs, in
// output pixels: enough that a road's edge crossing under a narrow street's
// name stops at the letters rather than running through them.
const haloWidth = 1.5

// drawAlong writes l, a name placed along a line, onto dst in ink: drawn
// horizontally at alongSupersample times its size in big -- or in its own
// face, if big is no bigger -- and then, for every pixel the rotated name
// covers, averaged from alongSupersample by alongSupersample samples of
// that drawing. A halo, if not the zero colour, goes under the glyphs first,
// haloWidth round them.
func drawAlong(dst *image.RGBA, l placed, ink, halo color.RGBA, big font.Face) {
	mask, haloMask, k := alongMasks(l.text, l.face, big, halo != (color.RGBA{}))
	paintAlong(dst, l, mask, haloMask, k, ink, halo)
}

// alongMasks is the text drawn in face, or in big when big draws it half as
// large again or more, with a margin for its halo: the text's coverage, the
// halo's when withHalo, and how many times the size of face it was drawn.
// It is the only part of drawing a name along a line that uses a face.
func alongMasks(text string, face, big font.Face, withHalo bool) (mask, haloMask *image.Alpha, k float64) {
	adv := font.MeasureString(face, text)
	k = 1.0
	if big != nil {
		if bigAdv := font.MeasureString(big, text); adv > 0 && float64(bigAdv)/float64(adv) >= 1.5 {
			face, k = big, float64(bigAdv)/float64(adv)
		}
	}
	m := face.Metrics()
	margin := int(math.Ceil(haloWidth*k)) + 2
	w := font.MeasureString(face, text).Ceil()
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	mask = image.NewAlpha(image.Rect(0, 0, w+2*margin, asc+desc+2*margin))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.P(margin, margin+asc)}
	d.DrawString(text)
	if withHalo {
		haloMask = dilate(mask, int(math.Round(haloWidth*k)))
	}
	return mask, haloMask, k
}

// paintAlong draws the masks at l's place and angle: the halo, when there
// is a mask for it and a colour, and the text over it.
func paintAlong(dst *image.RGBA, l placed, mask, haloMask *image.Alpha, k float64, ink, halo color.RGBA) {
	if halo == (color.RGBA{}) {
		haloMask = nil
	}
	cx, cy := float64(mask.Rect.Dx())/2, float64(mask.Rect.Dy())/2
	c, s := math.Cos(l.angle), math.Sin(l.angle)
	bounds := image.Rectangle{}
	for _, p := range l.quad {
		bounds = bounds.Union(image.Rect(int(math.Floor(p.X)), int(math.Floor(p.Y)), int(math.Ceil(p.X))+1, int(math.Ceil(p.Y))+1))
	}
	bounds = bounds.Intersect(dst.Rect)
	const n = alongSupersample
	for py := bounds.Min.Y; py < bounds.Max.Y; py++ {
		for px := bounds.Min.X; px < bounds.Max.X; px++ {
			var sum, haloSum float64
			for j := 0; j < n; j++ {
				for i := 0; i < n; i++ {
					x := float64(px) + (float64(i)+0.5)/n - l.x
					y := float64(py) + (float64(j)+0.5)/n - l.y
					u, v := x*c+y*s, -x*s+y*c
					sum += maskAt(mask, cx+u*k, cy+v*k)
					if haloMask != nil {
						haloSum += maskAt(haloMask, cx+u*k, cy+v*k)
					}
				}
			}
			if a := haloSum / (n * n); a > 0 {
				blend(dst, px, py, halo, a)
			}
			if a := sum / (n * n); a > 0 {
				blend(dst, px, py, ink, a)
			}
		}
	}
}

// DrawLineLabel writes l on dst centred at (x, y), its text running at
// angle radians clockwise from the image's x axis, as a render would have
// drawn it there: in the palette's label ink, or its quieter one for a minor
// name, turned smoothly from l's large face. A name on its road has a halo
// in the road's colour; any other, one in halo -- the zero colour for none.
// For a caller drawing lifted names over a picture of its own; see
// Options.LiftLabels.
func DrawLineLabel(dst *image.RGBA, l LineLabel, x, y, angle float64, p Palette, halo color.RGBA) {
	PrepareLineLabel(l).Draw(dst, x, y, angle, p, halo)
}

// PreparedLabel is a LineLabel made ready to be drawn many times: its text
// drawn once, with its halo, at the size it is drawn at, so that drawing it
// again -- somewhere else, at another angle, in other inks -- uses no font
// face. A face is not safe for two goroutines to draw with at once, and
// drawing a name's text was most of what drawing it cost; a picture standing
// the same names on frame after frame, several frames at once, prepares each
// name once and draws it from any goroutine. Its drawing is DrawLineLabel's
// to the last pixel; DrawLineLabel is exactly preparing and drawing.
type PreparedLabel struct {
	l              LineLabel
	w, h           float64
	mask, haloMask *image.Alpha
	k              float64
}

// PrepareLineLabel prepares l to be drawn; see PreparedLabel. It uses l's
// faces, and so must not run while anything else draws with them. nil for
// a label with no face or no text, which draws nothing.
func PrepareLineLabel(l LineLabel) *PreparedLabel {
	if l.Face == nil || l.Text == "" {
		return nil
	}
	m := l.Face.Metrics()
	pl := &PreparedLabel{
		l: l,
		w: float64(font.MeasureString(l.Face, l.Text).Ceil()),
		h: float64(m.Ascent.Ceil() + m.Descent.Ceil()),
	}
	pl.mask, pl.haloMask, pl.k = alongMasks(l.Text, l.Face, l.big, true)
	return pl
}

// Size is the label's text's width and height in pixels at its face:
// the box it is drawn in before its halo and turning.
func (pl *PreparedLabel) Size() (w, h float64) { return pl.w, pl.h }

// Draw writes the prepared label on dst as DrawLineLabel would: centred at
// (x, y), at angle, in p's label ink, its halo in halo -- the road's colour
// for a name on its road, the zero colour for none. Safe for concurrent use.
func (pl *PreparedLabel) Draw(dst *image.RGBA, x, y, angle float64, p Palette, halo color.RGBA) {
	if pl == nil {
		return
	}
	if pl.l.OnRoad {
		halo = p.nameHalo()
	}
	at := placed{text: pl.l.Text, face: pl.l.Face, along: true, x: x, y: y, angle: angle, quad: alongQuad(x, y, angle, pl.w, pl.h, int(math.Ceil(haloWidth))+1)}
	paintAlong(dst, at, pl.mask, pl.haloMask, pl.k, labelInk(p, pl.l.Minor), halo)
}

// dilate is mask grown by r pixels every way: each pixel the most covered
// within r of it, along each axis in turn, which grows a square rather than
// a disc -- at a halo's width the corners are a fraction of a pixel and not
// worth a pass per angle.
func dilate(mask *image.Alpha, r int) *image.Alpha {
	if r <= 0 {
		return mask
	}
	b := mask.Rect
	tmp, out := image.NewAlpha(b), image.NewAlpha(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			var m uint8
			for d := max(b.Min.X, x-r); d <= min(b.Max.X-1, x+r); d++ {
				m = max(m, mask.AlphaAt(d, y).A)
			}
			tmp.SetAlpha(x, y, color.Alpha{A: m})
		}
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			var m uint8
			for d := max(b.Min.Y, y-r); d <= min(b.Max.Y-1, y+r); d++ {
				m = max(m, tmp.AlphaAt(x, d).A)
			}
			out.SetAlpha(x, y, color.Alpha{A: m})
		}
	}
	return out
}

// maskAt is mask's coverage at (x, y), between its pixels' centres
// bilinearly, nothing outside it.
func maskAt(mask *image.Alpha, x, y float64) float64 {
	x, y = x-0.5, y-0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(xi, yi int) float64 {
		if !(image.Point{X: xi, Y: yi}.In(mask.Rect)) {
			return 0
		}
		return float64(mask.AlphaAt(xi, yi).A) / 255
	}
	return (1-fy)*((1-fx)*at(x0, y0)+fx*at(x0+1, y0)) + fy*((1-fx)*at(x0, y0+1)+fx*at(x0+1, y0+1))
}

// blend puts ink over dst's pixel at (x, y) at coverage a, premultiplied as
// image.RGBA is.
func blend(dst *image.RGBA, x, y int, ink color.RGBA, a float64) {
	i := dst.PixOffset(x, y)
	p := dst.Pix[i : i+4 : i+4]
	ia := a * float64(ink.A) / 255
	mix := func(under, over uint8) uint8 {
		return uint8(math.Round(float64(over)*a + float64(under)*(1-ia)))
	}
	p[0], p[1], p[2], p[3] = mix(p[0], ink.R), mix(p[1], ink.G), mix(p[2], ink.B), mix(p[3], ink.A)
}
