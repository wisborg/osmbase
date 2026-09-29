package render

import (
	"image"
	"image/color"
	"math"

	"github.com/wisborg/osmbase/raster"
)

// Gradient is a polyline coloured along its length by a value at each of its
// points: pace along a run, height along a ride, time gained on a reference.
//
// What the values are, and the range worth showing, are the caller's; what is
// here is the part that depends on the picture. A value per point is finer
// than any map can show -- a course sampled every few metres is a fraction of
// a pixel per sample at the zoom a whole run is drawn at, and colouring each
// sample would be a line of noise whose average colour nobody can see -- so
// the line is drawn in pieces at least MinPiece pixels long, each in the
// colour of its values averaged by length. How short that is depends on the
// view, which only the drawing knows; a caller that coarsened its values
// itself would have to redo the projection to know how far.
type Gradient struct {
	Points []Coord
	// Values is one value per point, in Points' order. A NaN is a point where
	// the value is not known, and a stretch between unknown points is drawn
	// in Unknown rather than given a colour on the scale it has no place on.
	Values []float64
	Scale  Scale
	// Width is the line's full width in image pixels.
	Width float64
	// Halo and HaloInk are as a Line's. A gradient's colours run from dark to
	// light, so no one halo ink contrasts with all of them against the map; a
	// thin dark edge is what most pace maps use.
	Halo    float64
	HaloInk color.RGBA
	// Unknown is the ink for stretches with no value. Left transparent, such a
	// stretch is drawn as its halo alone: an empty casing where the colour
	// would be, which says "no value here" rather than leaving the line
	// broken off.
	Unknown color.RGBA
	// MinPiece is the shortest a piece of one colour is drawn, in image
	// pixels; zero takes DefaultMinPiece.
	MinPiece float64
}

// DefaultMinPiece is the shortest piece of one colour a Gradient draws unless
// told otherwise: a few pixels, which is about the smallest change of colour
// along a thin line the eye picks out.
const DefaultMinPiece = 4

// Scale is what a Gradient's colours mean: Min is drawn in the first of
// Colours and Max in the last, with the colours between spread evenly and
// blended in between. A value beyond either end takes that end's colour, so a
// scale is a choice of how much of the range is worth telling apart, not a
// promise that nothing falls outside it.
//
// Min may be above Max, which runs the colours the other way.
type Scale struct {
	Min, Max float64
	// Colours are the stops from Min to Max; fewer than two takes
	// DefaultColours.
	Colours []color.RGBA
}

// DefaultColours runs blue, light blue, green, yellow, red: the ramp a pace or
// heat map is usually read in, low blue and high red.
var DefaultColours = []color.RGBA{
	{R: 0x1f, G: 0x5f, B: 0xd6, A: 0xff},
	{R: 0x2e, G: 0xa0, B: 0xd8, A: 0xff},
	{R: 0x3c, G: 0xb8, B: 0x4a, A: 0xff},
	{R: 0xe6, G: 0xc8, B: 0x2a, A: 0xff},
	{R: 0xe0, G: 0x3a, B: 0x2a, A: 0xff},
}

// At is where v falls on the scale, from 0 at Min to 1 at Max, clamped. A
// scale with no range puts everything in its middle, and NaN is NaN.
func (s Scale) At(v float64) float64 {
	if math.IsNaN(v) {
		return v
	}
	if s.Max == s.Min {
		return 0.5
	}
	return math.Max(0, math.Min(1, (v-s.Min)/(s.Max-s.Min)))
}

// Colour is v's colour on the scale.
func (s Scale) Colour(v float64) color.RGBA {
	return s.colourAt(s.At(v))
}

// colourAt is the colour at t, from 0 (the first stop) to 1 (the last).
func (s Scale) colourAt(t float64) color.RGBA {
	stops := s.Colours
	if len(stops) < 2 {
		stops = DefaultColours
	}
	if math.IsNaN(t) {
		t = 0.5
	}
	t = math.Max(0, math.Min(1, t)) * float64(len(stops)-1)
	i := min(int(t), len(stops)-2)
	f := t - float64(i)
	a, b := stops[i], stops[i+1]
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + f*(float64(y)-float64(x)))) }
	return color.RGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: mix(a.A, b.A)}
}

// DrawScaleBar fills r in img with s, from Min to Max: left to right in a bar
// wider than it is tall, bottom to top in one taller than wide, which is the
// way a reader expects a higher value to lie. What the ends are called, and
// any frame round the bar, are the caller's, as a legend's words are.
func DrawScaleBar(img *image.RGBA, r image.Rectangle, s Scale) {
	// The colours are spread over r as asked, not over the part of it on
	// the image: a bar running off the edge still puts each colour where the
	// caller's words for it are.
	if r.Intersect(img.Bounds()).Empty() {
		return
	}
	across := r.Dx() >= r.Dy()
	n := r.Dy()
	if across {
		n = r.Dx()
	}
	for i := 0; i < n; i++ {
		t := 0.5
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		c := s.colourAt(t)
		if across {
			for y := r.Min.Y; y < r.Max.Y; y++ {
				img.SetRGBA(r.Min.X+i, y, c) // a no-op off the image
			}
		} else {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.SetRGBA(x, r.Max.Y-1-i, c)
			}
		}
	}
}

// gradientPiece is a run of a Gradient's points drawn in one colour.
type gradientPiece struct {
	points []Coord
	value  float64 // NaN: not known
}

// pieces cuts g into runs of one colour, each at least minPiece pixels long
// as length is measured by span.
//
// A segment's value is the mean of its two ends, or the one end that is known;
// a piece's is its segments' values averaged by their length on the picture,
// so a long straight segment counts for what it covers rather than for one
// sample among many. A piece ends where it has reached minPiece, and also
// where the values stop or start being known, because a colour averaged over
// a gap would be made up. What is left at the end shorter than minPiece joins
// the piece before it, if that one is of the same kind, rather than standing
// alone as a speck of colour decided by one sample.
//
// Neighbouring pieces share their end point, so the line is unbroken.
func (g Gradient) pieces(span func(a, b Coord) float64, minPiece float64) []gradientPiece {
	n := min(len(g.Points), len(g.Values))
	if n < 2 {
		return nil
	}
	var out []gradientPiece
	start := 0
	var length, weighted, plain float64
	var count int
	known := !math.IsNaN(segmentValue(g.Values[0], g.Values[1]))
	flush := func(end int) {
		v := math.NaN()
		if known {
			v = plain / float64(count)
			if length > 0 {
				v = weighted / length
			}
		}
		out = append(out, gradientPiece{points: g.Points[start : end+1], value: v})
		start, length, weighted, plain, count = end, 0, 0, 0, 0
	}
	for i := 1; i < n; i++ {
		v := segmentValue(g.Values[i-1], g.Values[i])
		if k := !math.IsNaN(v); k != known {
			if start < i-1 {
				flush(i - 1)
			}
			known = k
		}
		d := span(g.Points[i-1], g.Points[i])
		length += d
		if known {
			weighted += d * v
			plain += v
			count++
		}
		if length >= minPiece {
			flush(i)
		}
	}
	if start < n-1 {
		last := len(out) - 1
		if last >= 0 && math.IsNaN(out[last].value) == !known {
			// Too short to stand alone: fold it into the piece before,
			// re-averaged over both.
			p := out[last]
			l := pathLength(p.points, span)
			v := p.value
			if known && l+length > 0 {
				v = (p.value*l + weighted) / (l + length)
			}
			out[last] = gradientPiece{points: g.Points[start-len(p.points)+1 : n], value: v}
		} else {
			flush(n - 1)
		}
	}
	return out
}

// segmentValue is the value of the segment between two points' values.
func segmentValue(a, b float64) float64 {
	switch {
	case math.IsNaN(a):
		return b
	case math.IsNaN(b):
		return a
	}
	return (a + b) / 2
}

func pathLength(points []Coord, span func(a, b Coord) float64) float64 {
	var l float64
	for i := 1; i < len(points); i++ {
		l += span(points[i-1], points[i])
	}
	return l
}

// span is how far apart two coordinates are on the picture, in pixels,
// taking two points more than 180° of longitude apart as neighbours across
// the antimeridian, as stroke does.
func (o *overlayDrawer) span(a, b Coord) float64 {
	if d := b.Lon - a.Lon; d > 180 {
		b.Lon -= 360
	} else if d < -180 {
		b.Lon += 360
	}
	p, q := o.pixel(a), o.pixel(b)
	return math.Hypot(q.X-p.X, q.Y-p.Y)
}

// gradient draws g's colours, piece by piece in order along it.
func (o *overlayDrawer) gradient(s *raster.Surface, g Gradient) {
	if !(g.Width > 0) {
		return
	}
	minPiece := g.MinPiece
	if !(minPiece > 0) {
		minPiece = DefaultMinPiece
	}
	st := raster.Stroke{Width: float32(g.Width)}
	for _, p := range g.pieces(o.span, minPiece) {
		ink := g.Unknown
		if !math.IsNaN(p.value) {
			ink = g.Scale.Colour(p.value)
		}
		if ink.A > 0 {
			o.stroke(s, p.points, st, ink)
		}
	}
}
