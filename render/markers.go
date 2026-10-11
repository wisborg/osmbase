package render

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/raster"
)

// MarkerShape is what a Marker is drawn as. The zero value is the dot every
// marker was before there were shapes, so a caller that sets none draws what
// it always drew.
type MarkerShape int

const (
	// ShapeDot is a disc of Ink, Radius across, with a halo of HaloInk.
	ShapeDot MarkerShape = iota

	// ShapeStartPin is a green balloon pin with a white "play" triangle in
	// its head, its tip on the marker's place; Radius is the head's radius.
	ShapeStartPin
	// ShapeFinishPin is a red balloon pin whose head is a checkered disc
	// inside a red rim.
	ShapeFinishPin
	// ShapeStartFinishPin is one pin for a start and a finish in one place:
	// green on the left with the play triangle, red on the right with the
	// checkered half disc.
	ShapeStartFinishPin

	// ShapeNumberPin is a smaller pin in Ink with Text in white in its head.
	ShapeNumberPin
	// ShapeNumberDisc is a disc of Ink centred on the place, with Text in
	// white inside it, widening into a pill for a longer number.
	ShapeNumberDisc
	// ShapeNumberRing is ShapeNumberDisc in white, ringed and lettered in
	// Ink: lighter on a map, and clear over a line coloured by a value,
	// where a filled disc in one colour can clash with it.
	ShapeNumberRing

	// ShapeArrow is a navigation arrow in Ink, pointing at Heading;
	// Radius is half its length.
	ShapeArrow
	// ShapePlane is an aircraft seen from above in Ink, its nose at
	// Heading; Radius is half its length.
	ShapePlane
)

// The pins' own inks. A start and a finish are green and red on every map,
// as on every other app's: the colour says which end it is before the
// symbol does, and the symbol says it to somebody who cannot tell the two
// colours apart. The white outline is what keeps a pin clear of whatever
// it stands on, a light map, a dark one or a picture in perspective.
var (
	startGreen = color.RGBA{R: 0x1E, G: 0x9E, B: 0x4A, A: 0xff}
	finishRed  = color.RGBA{R: 0xD6, G: 0x28, B: 0x3A, A: 0xff}
	white      = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	checkBlack = color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}
	shadowInk  = color.RGBA{A: 0x55}
)

// pinHeight is how far a pin's tip is below its head's centre, in head
// radii: the classic balloon's proportion.
const pinHeight = 2.1

// checkerFraction is the checkered disc's radius in a finish pin's head, in
// head radii: small enough to leave a red rim round it that says "pin" and
// "finish" together.
const checkerFraction = 0.6

// isPin reports whether a shape stands on its place by a tip, its head above.
func (s MarkerShape) isPin() bool {
	switch s {
	case ShapeStartPin, ShapeFinishPin, ShapeStartFinishPin, ShapeNumberPin:
		return true
	}
	return false
}

// outlineWidth is the white outline round a shape of head or body radius r,
// at least the marker's own halo.
func outlineWidth(m Marker, r float64) float64 {
	return math.Max(m.Halo, math.Max(1, r*0.14))
}

// markerGeometry is a marker's size once its text is measured: the head or
// body radius, and for a pill the half length of its straight middle.
type markerGeometry struct {
	r, half float64
}

// measure sizes m for its text in face: a number's marker grows to hold its
// number with room round it, and never shrinks below Radius.
func measure(m Marker, face font.Face) markerGeometry {
	g := markerGeometry{r: m.Radius}
	if m.Text == "" || face == nil {
		return g
	}
	w := float64(font.MeasureString(face, m.Text).Ceil())
	met := face.Metrics()
	h := float64(met.Ascent.Ceil())
	pad := math.Max(2, h*0.35)
	switch m.Shape {
	case ShapeNumberDisc:
		g.r = math.Max(g.r, h/2+pad)
		g.half = math.Max(0, w/2+pad-g.r)
	case ShapeNumberRing:
		// Room for the ring inside the edge as well as round the number.
		pad *= 1.4
		g.r = math.Max(g.r, h/2+pad)
		g.half = math.Max(0, w/2+pad-g.r)
	case ShapeNumberPin:
		g.r = math.Max(g.r, math.Max(w, h)/2+pad)
	}
	return g
}

// headCentre is where the middle of m's head or body is drawn, for a marker
// whose place is at c.
func headCentre(m Marker, g markerGeometry, c pt) pt {
	if m.Shape.isPin() {
		return pt{X: c.X, Y: c.Y - pinHeight*g.r}
	}
	return c
}

// drawShape draws a marker that is not a dot at c, in surface pixels: a soft
// shadow, a white outline and the shape itself, with its symbol or its
// number. face writes the number; it may be nil when m has none.
func drawShape(s *raster.Surface, path *raster.Path, m Marker, c pt, face font.Face) {
	g := measure(m, face)
	if g.r <= 0 {
		return
	}
	outline := outlineWidth(m, g.r)
	var body []pt
	switch m.Shape {
	case ShapeStartPin, ShapeFinishPin, ShapeStartFinishPin, ShapeNumberPin:
		body = pinOutline(c, g.r)
	case ShapeNumberDisc, ShapeNumberRing:
		body = pillOutline(c, g.r, g.half)
	case ShapeArrow:
		body = turned(c, g.r, m.Heading, arrowShape)
	case ShapePlane:
		body = turned(c, g.r, m.Heading, planeShape)
	default:
		return
	}

	// The shadow: the outlined shape a little down and darker, which lifts
	// the marker off the map as a drop shadow does, without a blur this
	// package has no use for elsewhere.
	drop := math.Max(1, g.r*0.12)
	shadow := make([]pt, len(body))
	for i, p := range body {
		shadow[i] = pt{X: p.X, Y: p.Y + drop}
	}
	fillOutlined(s, path, shadow, outline, shadowInk, shadowInk)
	ink := shapeInk(m)
	if m.Shape == ShapeNumberRing {
		ink = white
	}
	fillOutlined(s, path, body, outline, white, ink)

	head := headCentre(m, g, c)
	switch m.Shape {
	case ShapeStartPin:
		fillPolygon(s, path, playTriangle(head, g.r), white)
	case ShapeFinishPin:
		checkered(s, head, g.r*checkerFraction, math.Inf(-1), math.Inf(1), g.r*0.08)
	case ShapeStartFinishPin:
		// The right half red, under the checkered half disc; the left
		// stays the green it was filled with.
		fillPolygon(s, path, clipRight(body, head.X), finishRed)
		fillPolygon(s, path, playTriangle(pt{X: head.X - g.r*0.42, Y: head.Y}, g.r*0.62), white)
		checkered(s, pt{X: head.X + g.r*0.02, Y: head.Y}, g.r*checkerFraction*0.85, head.X+g.r*0.12, math.Inf(1), g.r*0.06)
	case ShapeNumberRing:
		width := math.Max(1.5, g.r*0.16)
		ring := pillOutline(c, g.r-width/2, g.half)
		path.Reset()
		path.Stroke(closed(ring), raster.Stroke{Width: float32(width)})
		s.Fill(path, m.Ink)
	}
	if m.Text != "" && face != nil {
		textInk := white
		if m.Shape == ShapeNumberRing {
			textInk = m.Ink
		}
		centredText(s.RGBA(), m.Text, head, face, textInk)
	}
}

// shapeInk is the colour a marker is drawn in, and its label written in: a
// start or finish pin's own, and otherwise Ink.
func shapeInk(m Marker) color.RGBA {
	switch m.Shape {
	case ShapeStartPin, ShapeStartFinishPin:
		return startGreen
	case ShapeFinishPin:
		return finishRed
	}
	return m.Ink
}

// fillOutlined fills pts in ink inside an outline width wide in edge.
func fillOutlined(s *raster.Surface, path *raster.Path, pts []pt, width float64, edge, ink color.RGBA) {
	path.Reset()
	path.Stroke(closed(pts), raster.Stroke{Width: float32(2 * width)})
	s.Fill(path, edge)
	fillPolygon(s, path, pts, ink)
}

// fillPolygon fills the polygon pts.
func fillPolygon(s *raster.Surface, path *raster.Path, pts []pt, ink color.RGBA) {
	if len(pts) < 3 {
		return
	}
	path.Reset()
	rp := make([]raster.Point, len(pts))
	for i, p := range pts {
		rp[i] = raster.Point{X: float32(p.X), Y: float32(p.Y)}
	}
	path.Ring(rp)
	s.Fill(path, ink)
}

// closed is pts as raster points, the first repeated at the end, for a
// stroke all the way round.
func closed(pts []pt) []raster.Point {
	out := make([]raster.Point, 0, len(pts)+1)
	for _, p := range pts {
		out = append(out, raster.Point{X: float32(p.X), Y: float32(p.Y)})
	}
	if len(pts) > 0 {
		out = append(out, out[0])
	}
	return out
}

// pinOutline is a balloon pin with its tip at tip and a head of radius r: the
// head's circle and the two lines from the tip that touch it.
func pinOutline(tip pt, r float64) []pt {
	c := pt{X: tip.X, Y: tip.Y - pinHeight*r}
	// The tangent points are acos(r/d) either side of straight down from the
	// centre, d the distance to the tip; the head runs round the top from one
	// to the other.
	a := math.Acos(1 / pinHeight)
	from, to := math.Pi/2+a, math.Pi/2-a+2*math.Pi
	n := max(24, int(r*2))
	pts := []pt{tip}
	for i := 0; i <= n; i++ {
		t := from + (to-from)*float64(i)/float64(n)
		pts = append(pts, pt{X: c.X + r*math.Cos(t), Y: c.Y + r*math.Sin(t)})
	}
	return pts
}

// pillOutline is a disc of radius r at c, or, with half above zero, a pill:
// two half discs half either side of c joined by straight sides.
func pillOutline(c pt, r, half float64) []pt {
	n := max(16, int(r*2))
	var pts []pt
	for i := 0; i <= n; i++ { // the right end, top to bottom
		t := -math.Pi/2 + math.Pi*float64(i)/float64(n)
		pts = append(pts, pt{X: c.X + half + r*math.Cos(t), Y: c.Y + r*math.Sin(t)})
	}
	for i := 0; i <= n; i++ { // the left end, bottom to top
		t := math.Pi/2 + math.Pi*float64(i)/float64(n)
		pts = append(pts, pt{X: c.X - half + r*math.Cos(t), Y: c.Y + r*math.Sin(t)})
	}
	return pts
}

// playTriangle is a "play" symbol pointing right, centred on c in a head of
// radius r.
func playTriangle(c pt, r float64) []pt {
	return []pt{
		{X: c.X - 0.32*r, Y: c.Y - 0.46*r},
		{X: c.X + 0.5*r, Y: c.Y},
		{X: c.X - 0.32*r, Y: c.Y + 0.46*r},
	}
}

// clipRight is the part of the polygon pts right of the vertical x.
func clipRight(pts []pt, x float64) []pt {
	var out []pt
	for i := range pts {
		a, b := pts[i], pts[(i+1)%len(pts)]
		ain, bin := a.X >= x, b.X >= x
		if ain {
			out = append(out, a)
		}
		if ain != bin {
			f := (x - a.X) / (b.X - a.X)
			out = append(out, pt{X: x, Y: a.Y + f*(b.Y-a.Y)})
		}
	}
	return out
}

// arrowShape and planeShape are the arrow and the aircraft pointing up, a
// unit long either side of their middle.
var (
	arrowShape = []pt{{0, -1}, {0.8, 0.85}, {0, 0.42}, {-0.8, 0.85}}
	planeShape = func() []pt {
		// An airliner from above, nose up, in sixteenths: fuselage, swept
		// wings, tailplane.
		raw := []pt{
			{0, -16}, {1.6, -15.2}, {2.2, -12}, {2.2, -4.5}, {14.5, 2.5}, {14.5, 5.5}, {2.2, 1.8},
			{2.2, 9.5}, {6, 12.6}, {6, 15}, {0, 13.4}, {-6, 15}, {-6, 12.6}, {-2.2, 9.5},
			{-2.2, 1.8}, {-14.5, 5.5}, {-14.5, 2.5}, {-2.2, -4.5}, {-2.2, -12}, {-1.6, -15.2},
		}
		for i := range raw {
			raw[i].X /= 16
			raw[i].Y /= 16
		}
		return raw
	}()
)

// turned is shape, scaled by r, turned heading degrees clockwise from up and
// centred on c.
func turned(c pt, r, heading float64, shape []pt) []pt {
	sin, cos := math.Sincos(heading * math.Pi / 180)
	out := make([]pt, len(shape))
	for i, p := range shape {
		x, y := p.X*r, p.Y*r
		out[i] = pt{X: c.X + x*cos - y*sin, Y: c.Y + x*sin + y*cos}
	}
	return out
}

// checkered paints a checkered disc of radius r at c, only where x is
// between left and right, ringed in white ring wide: a chequer of squares
// five across, black and white, antialiased at its edge by sampling each
// pixel four by four. The raster package fills one colour a path, and a
// chequer as paths is a hundred squares each clipped to a circle; per pixel
// it is a loop.
func checkered(s *raster.Surface, c pt, r, left, right, ring float64) {
	img := s.RGBA()
	b := img.Bounds()
	outer := r + ring
	x0, x1 := int(math.Floor(c.X-outer)), int(math.Ceil(c.X+outer))
	y0, y1 := int(math.Floor(c.Y-outer)), int(math.Ceil(c.Y+outer))
	sq := 2 * r / 5
	const n = 4
	for y := max(y0, b.Min.Y); y < min(y1, b.Max.Y); y++ {
		for x := max(x0, b.Min.X); x < min(x1, b.Max.X); x++ {
			var rr, gg, bb, cover float64
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					px := float64(x) + (float64(sx)+0.5)/n
					py := float64(y) + (float64(sy)+0.5)/n
					if px < left || px > right {
						continue
					}
					d := math.Hypot(px-c.X, py-c.Y)
					if d > outer {
						continue
					}
					ink := white
					if d <= r {
						i := int(math.Floor((px - c.X + 5*sq) / sq))
						j := int(math.Floor((py - c.Y + 5*sq) / sq))
						if (i+j)%2 == 0 {
							ink = checkBlack
						}
					}
					rr += float64(ink.R)
					gg += float64(ink.G)
					bb += float64(ink.B)
					cover++
				}
			}
			if cover == 0 {
				continue
			}
			a := cover / (n * n)
			under := img.RGBAAt(x, y)
			mix := func(top float64, below uint8) uint8 {
				return uint8(math.Round(top/cover*a + float64(below)*(1-a)))
			}
			img.SetRGBA(x, y, color.RGBA{
				R: mix(rr, under.R), G: mix(gg, under.G), B: mix(bb, under.B),
				A: uint8(math.Round(255*a + float64(under.A)*(1-a))),
			})
		}
	}
}

// centredText writes s centred on c in ink.
func centredText(dst *image.RGBA, s string, c pt, face font.Face, ink color.RGBA) {
	s = Visual(s)
	w := font.MeasureString(face, s)
	met := face.Metrics()
	x := fixed.Int26_6(math.Round(c.X*64)) - w/2
	y := fixed.Int26_6(math.Round(c.Y*64)) + (met.Ascent-met.Descent)/2
	d := font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face, Dot: fixed.Point26_6{X: x, Y: y}}
	d.DrawString(s)
}

// labelAnchor is where a marker's label is measured from, and how far from
// its centre the label must start: beside a pin's head, not its tip, and
// clear of a pill's end.
func labelAnchor(m Marker, c pt, face font.Face) (pt, float64) {
	if m.Shape == ShapeDot {
		return c, m.Radius + m.Halo
	}
	g := measure(m, face)
	return headCentre(m, g, c), g.r + g.half + outlineWidth(m, g.r)
}
