package render

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"slices"
	"strconv"

	"golang.org/x/image/font"

	"github.com/wisborg/osmbase/raster"
)

// Contours: lines of equal height, traced from the same heights the shading
// is drawn from, at an interval for the zoom, every fifth one heavier and
// written with its height along it.

// contourMinZoom is the shallowest zoom contours are drawn at. Shallower, a
// pixel is more than a hundred metres of ground and the lines are either too
// far apart to say anything or too close to be lines; the shading says what
// there is to say about the shape of the land at that scale.
const contourMinZoom = 11

// indexEvery is how many intervals apart the heavier, labelled index lines
// are: every fifth, as on printed topographic maps.
const indexEvery = 5

// contourSteps are the intervals a map may use, in metres. Each is a round
// number, and five of each is a round number too, so the index lines fall
// on heights a reader would pick: 50, 100, 250 is not on this list because
// its index lines would be at 1250 and 2500.
var contourSteps = []float64{5, 10, 20, 50, 100, 200, 500, 1000}

// minContourGap is the least distance between neighbouring lines on the
// steepest ground in view, in output pixels. Closer than that, a
// mountainside's lines merge into a solid band that says "steep" and nothing
// else, and drowns everything drawn over it.
const minContourGap = 4.0

// Contour line widths, in output pixels.
const (
	contourWidth = 0.7
	indexWidth   = 1.4
)

// contourPriority places contour heights below every name a style labels:
// a height is the least of what a map has to say, and is dropped first
// where names compete for room.
const contourPriority = 0

// zoomInterval is the interval a view at zoom z starts from: dense enough
// at each zoom to show a street's rise without a contour on every house.
func zoomInterval(z float64) (float64, bool) {
	switch {
	case z < contourMinZoom:
		return 0, false
	case z < 12:
		return 100, true
	case z < 13:
		return 50, true
	case z < 14:
		return 20, true
	case z < 16:
		return 10, true
	}
	return 5, true
}

// interval is the contour interval for the view: the zoom's, widened to the
// next step where the steepest ground in view would put its lines closer
// than minContourGap. Steepest is the 95th percentile of the slope, so that
// one cliff or quarry face does not thin out every hill in the picture.
func (rl *relief) interval(z float64) (float64, bool) {
	base, ok := zoomInterval(z)
	if !ok {
		return 0, false
	}
	var rises []float64
	// Over the view, not its margin: the steepest ground in view.
	for y := max(1, rl.m); y < rl.fh-max(1, rl.m); y += 2 {
		for x := max(1, rl.m); x < rl.fw-max(1, rl.m); x += 2 {
			dx := float64(rl.field[y*rl.fw+x+1] - rl.field[y*rl.fw+x-1])
			dy := float64(rl.field[(y+1)*rl.fw+x] - rl.field[(y-1)*rl.fw+x])
			if r := math.Hypot(dx, dy) / 2; !math.IsNaN(r) {
				rises = append(rises, r)
			}
		}
	}
	if len(rises) == 0 {
		return 0, false
	}
	slices.Sort(rises)
	need := rises[len(rises)*95/100] * minContourGap
	for _, s := range contourSteps {
		if s >= base && s >= need {
			return s, true
		}
	}
	return contourSteps[len(contourSteps)-1], true
}

// contourLine is one traced line, in surface pixels.
type contourLine struct {
	height float64
	index  bool
	pts    []pt
}

// contours traces the view's contours at the given interval. A cell with an
// unknown corner is skipped, so a line stops where the heights do rather
// than running on through ground nobody measured.
func (rl *relief) contours(interval float64) []contourLine {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range rl.field {
		if !isNaN32(v) {
			lo, hi = math.Min(lo, float64(v)), math.Max(hi, float64(v))
		}
	}
	if math.IsInf(lo, 0) {
		return nil
	}
	var out []contourLine
	for k := math.Ceil(lo / interval); k*interval <= hi; k++ {
		h := k * interval
		// Sea level is the coastline, which the map already draws as the
		// edge of its water -- and the water hides the line while its
		// heights, drawn last, were written down the middle of every
		// estuary. Ground below sea level still has its contours.
		if h == 0 {
			continue
		}
		for _, line := range rl.trace(h) {
			if lineLength(line) < minContourLength {
				continue
			}
			out = append(out, contourLine{height: h, index: math.Mod(k, indexEvery) == 0, pts: line})
		}
	}
	return out
}

// onSurface is the lines cut down to the surface the projection draws on,
// with room for the widest stroke: they are traced over the field's margin
// as well, so that each is kept or dropped by its whole length, and what is
// beyond the image is drawn by nobody and must not be where a height is
// written along it.
func (rl *relief) onSurface(p projection, lines []contourLine) []contourLine {
	b := p.surface().inflate(indexWidth + 1)
	var c clipper
	var out []contourLine
	for _, l := range lines {
		c.line(l.pts, b, func(run []pt, _ float64) {
			out = append(out, contourLine{height: l.height, index: l.index, pts: slices.Clone(run)})
		})
	}
	return out
}

// minContourLength is the shortest line drawn, in output pixels. Shorter
// lines are the data's texture rather than the land's shape -- a ring round
// a garden bed, a stub where a line clips the corner of the view -- and at
// street zooms a town is speckled with them.
const minContourLength = 20.0

func lineLength(ps []pt) float64 {
	var n float64
	for i := 1; i < len(ps); i++ {
		n += math.Hypot(ps[i].X-ps[i-1].X, ps[i].Y-ps[i-1].Y)
	}
	return n
}

// trace is marching squares at one height over the field, with the pieces
// joined into lines.
//
// Each cell is the square between four samples, and a contour crosses the
// edges whose two ends are either side of the height, at the point found by
// interpolating between them. Every crossing is on one edge shared by at
// most two cells, so the pieces join end to end through the edges they
// share; that is what turns a cloud of short segments into lines a stroke
// can join smoothly and a label can follow.
//
// A sample exactly at the height counts as above it, so a line never passes
// through a sample and no crossing is ever made twice.
func (rl *relief) trace(h float64) [][]pt {
	w, ht := rl.fw, rl.fh
	f := rl.field
	above := func(x, y int) bool { return float64(f[y*w+x]) >= h }
	// An edge's id: 2*(y*w+x) for the edge from (x, y) to (x+1, y), plus one
	// for the edge from (x, y) to (x, y+1).
	hEdge := func(x, y int) int { return 2 * (y*w + x) }
	vEdge := func(x, y int) int { return 2*(y*w+x) + 1 }
	at := func(e int) pt {
		i := e / 2
		x, y := i%w, i/w
		x2, y2 := x+1, y
		if e%2 == 1 {
			x2, y2 = x, y+1
		}
		a, b := float64(f[y*w+x]), float64(f[y2*w+x2])
		t := (h - a) / (b - a)
		// Field sample (x, y) is the centre of surface pixel (x-m, y-m).
		return pt{X: float64(x-rl.m) + 0.5 + t*float64(x2-x), Y: float64(y-rl.m) + 0.5 + t*float64(y2-y)}
	}

	type seg struct{ a, b int }
	var segs []seg
	for y := 0; y < ht-1; y++ {
		for x := 0; x < w-1; x++ {
			v0, v1, v2, v3 := f[y*w+x], f[y*w+x+1], f[(y+1)*w+x+1], f[(y+1)*w+x]
			if isNaN32(v0) || isNaN32(v1) || isNaN32(v2) || isNaN32(v3) {
				continue
			}
			c := 0
			if above(x, y) {
				c |= 1
			}
			if above(x+1, y) {
				c |= 2
			}
			if above(x+1, y+1) {
				c |= 4
			}
			if above(x, y+1) {
				c |= 8
			}
			top, right, bottom, left := hEdge(x, y), vEdge(x+1, y), hEdge(x, y+1), vEdge(x, y)
			switch c {
			case 0, 15:
			case 1, 14:
				segs = append(segs, seg{left, top})
			case 2, 13:
				segs = append(segs, seg{top, right})
			case 3, 12:
				segs = append(segs, seg{left, right})
			case 4, 11:
				segs = append(segs, seg{right, bottom})
			case 6, 9:
				segs = append(segs, seg{top, bottom})
			case 7, 8:
				segs = append(segs, seg{left, bottom})
			case 5, 10:
				// A saddle: two opposite corners above, two below. The
				// centre, as the mean of the four, says which pair the
				// high ground joins.
				centreAbove := (float64(v0)+float64(v1)+float64(v2)+float64(v3))/4 >= h
				if (c == 5) == centreAbove {
					segs = append(segs, seg{left, bottom}, seg{top, right})
				} else {
					segs = append(segs, seg{left, top}, seg{right, bottom})
				}
			}
		}
	}

	// Join the pieces through shared edges, open lines first -- from an end
	// only one piece reaches -- then the closed loops that are left.
	ends := make(map[int][]int, 2*len(segs))
	for i, s := range segs {
		ends[s.a] = append(ends[s.a], i)
		ends[s.b] = append(ends[s.b], i)
	}
	used := make([]bool, len(segs))
	walk := func(start int, from int) []pt {
		line := []pt{at(from)}
		e, i := from, start
		for {
			used[i] = true
			s := segs[i]
			next := s.a
			if next == e {
				next = s.b
			}
			line = append(line, at(next))
			e = next
			i = -1
			for _, j := range ends[e] {
				if !used[j] {
					i = j
					break
				}
			}
			if i < 0 {
				return line
			}
		}
	}
	var lines [][]pt
	for i, s := range segs {
		if used[i] {
			continue
		}
		for _, e := range []int{s.a, s.b} {
			if len(ends[e]) == 1 {
				lines = append(lines, walk(i, e))
				break
			}
		}
	}
	for i, s := range segs {
		if !used[i] {
			lines = append(lines, walk(i, s.a))
		}
	}
	return lines
}

// drawContours strokes the lines: the ordinary ones in one pass, the index
// lines in another, heavier, so a line's weight does not depend on which
// neighbour it was filled with.
//
// They are drawn into a layer of their own, cut out wherever a contour's
// height is placed -- labels are placed from the tiles before anything is
// drawn, so where they go is known by now -- and the layer laid over the
// map. A height written on top of its own line, as the first version drew
// it, is struck through by the line; printed maps break the line instead.
func (d *drawer) drawContours(s *raster.Surface, lines []contourLine, labels []placed) {
	if len(lines) == 0 {
		return
	}
	layer := raster.NewSurface(d.p.width, d.p.height)
	for _, index := range []bool{false, true} {
		width := float32(contourWidth)
		if index {
			width = indexWidth
		}
		d.path.Reset()
		for _, l := range lines {
			if l.index == index && len(l.pts) >= 2 {
				d.path.Stroke(d.points(l.pts), raster.Stroke{Width: width})
			}
		}
		if !d.path.Empty() {
			layer.Fill(&d.path, d.palette.Contour)
		}
	}
	img := layer.RGBA()
	for _, l := range labels {
		if l.contour {
			l.quad.clear(img)
		}
	}
	draw.Draw(s.RGBA(), img.Bounds(), img, image.Point{}, draw.Over)
}

// clear makes every pixel whose centre is inside the quad transparent.
func (q quad) clear(img *image.RGBA) {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range q {
		x0, x1 = math.Min(x0, p.X), math.Max(x1, p.X)
		y0, y1 = math.Min(y0, p.Y), math.Max(y1, p.Y)
	}
	r := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1))+1, int(math.Ceil(y1))+1).Intersect(img.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if q.contains(float64(x)+0.5, float64(y)+0.5) {
				i := img.PixOffset(x, y)
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0, 0, 0, 0
			}
		}
	}
}

// contains reports whether (x, y) is inside the quad, which is convex: on
// the same side of all four edges.
func (q quad) contains(x, y float64) bool {
	var sign float64
	for i := 0; i < 4; i++ {
		a, b := q[i], q[(i+1)%4]
		c := (b.X-a.X)*(y-a.Y) - (b.Y-a.Y)*(x-a.X)
		if c == 0 {
			continue
		}
		if sign == 0 {
			sign = c
		} else if (c > 0) != (sign > 0) {
			return false
		}
	}
	return true
}

// contourLabels are the index lines' heights, written along them where they
// run straight for long enough, as label candidates below every name.
func contourLabels(lines []contourLine, face font.Face, facing float64) []candidate {
	if face == nil {
		return nil
	}
	m := face.Metrics()
	h := float64(m.Ascent.Ceil() + m.Descent.Ceil())
	var out []candidate
	for li, l := range lines {
		if !l.index {
			continue
		}
		text := strconv.FormatFloat(l.height, 'f', -1, 64)
		w := float64(font.MeasureString(face, text).Ceil())
		for n, sp := range placeAlongPixels([][]pt{l.pts}, w, h, facing) {
			out = append(out, candidate{
				text: text, x: sp.x, y: sp.y, along: true, angle: sp.angle, scale: 1,
				priority: contourPriority,
				nth:      n,
				minor:    true,
				contour:  true,
				face:     face,
				key:      fmt.Sprintf("contour %d:%.0f,%.0f", li, sp.x, sp.y),
			})
		}
	}
	return out
}
