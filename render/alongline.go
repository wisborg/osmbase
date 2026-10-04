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

// alongFractions are where along its line a name is tried, in order: the
// middle first, as lineAnchor always chose, and then further out either
// side, for a road whose middle is a bend.
var alongFractions = []float64{0.5, 0.38, 0.62, 0.26, 0.74, 0.14, 0.86}

// placeAlong finds where along the longest part of lines -- in tile
// coordinates, placed on the surface by tr -- a name w pixels long and h
// high can be written straight: the middle of a stretch of the line at
// least that long that bends no more than maxBend of h under it. The angle
// is the stretch's direction, turned if need be so the text reads left to
// right, never upside down.
//
// ok is false when the line is shorter on the map than its own name, or
// bends everywhere: a name longer than its street, or wrapped round a
// corner, labels nothing and is clutter, so it is not drawn.
func placeAlong(lines [][]mvt.Point, tr tileTransform, w, h float64) (x, y, angle float64, ok bool) {
	var best []pt
	var bestLen float64
	for _, line := range lines {
		if len(line) < 2 {
			continue
		}
		ps := make([]pt, len(line))
		var n float64
		for i, p := range line {
			ps[i] = tr.apply(p.X, p.Y)
			if i > 0 {
				n += math.Hypot(ps[i].X-ps[i-1].X, ps[i].Y-ps[i-1].Y)
			}
		}
		if n > bestLen {
			best, bestLen = ps, n
		}
	}
	if best == nil {
		return 0, 0, 0, false
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
	for _, f := range alongFractions {
		s0 := f*bestLen - w/2
		s1 := s0 + w
		if s0 < 0 || s1 > bestLen {
			continue
		}
		a, i0 := at(s0)
		b, i1 := at(s1)
		dx, dy := b.X-a.X, b.Y-a.Y
		chord := math.Hypot(dx, dy)
		if chord == 0 {
			continue
		}
		straight := true
		for i := i0; i < i1; i++ {
			p := best[i]
			if math.Abs((p.X-a.X)*dy-(p.Y-a.Y)*dx)/chord > maxBend*h {
				straight = false
				break
			}
		}
		if !straight {
			continue
		}
		mid, _ := at(f * bestLen)
		angle = math.Atan2(dy, dx)
		if math.Cos(angle) < 0 {
			angle += math.Pi
			if angle > math.Pi {
				angle -= 2 * math.Pi
			}
		}
		return mid.X, mid.Y, angle, true
	}
	return 0, 0, 0, false
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
	face := l.face
	adv := font.MeasureString(face, l.text)
	k := 1.0
	if big != nil {
		if bigAdv := font.MeasureString(big, l.text); adv > 0 && float64(bigAdv)/float64(adv) >= 1.5 {
			face, k = big, float64(bigAdv)/float64(adv)
		}
	}
	m := face.Metrics()
	margin := int(math.Ceil(haloWidth*k)) + 2
	w := font.MeasureString(face, l.text).Ceil()
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	mask := image.NewAlpha(image.Rect(0, 0, w+2*margin, asc+desc+2*margin))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.P(margin, margin+asc)}
	d.DrawString(l.text)
	var haloMask *image.Alpha
	if halo != (color.RGBA{}) {
		haloMask = dilate(mask, int(math.Round(haloWidth*k)))
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
