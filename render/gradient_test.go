package render

import (
	"image"
	"image/color"
	"math"
	"testing"
)

var (
	low  = color.RGBA{B: 0xff, A: 0xff}
	high = color.RGBA{R: 0xff, A: 0xff}
	grey = color.RGBA{R: 0x77, G: 0x77, B: 0x77, A: 0xff}
	edge = color.RGBA{R: 0x33, G: 0x33, B: 0x33, A: 0xff}
)

// Values at and beyond either end take that end's colour, the stops are
// spread evenly between, and a reversed scale runs the colours the other way.
func TestScaleColour(t *testing.T) {
	three := Scale{Min: 10, Max: 20, Colours: []color.RGBA{low, {G: 0xff, A: 0xff}, high}}
	for _, c := range []struct {
		s    Scale
		v    float64
		want color.RGBA
	}{
		{three, 10, low},
		{three, 5, low},
		{three, 20, high},
		{three, 99, high},
		{three, 15, color.RGBA{G: 0xff, A: 0xff}},
		{three, 12.5, color.RGBA{G: 0x80, B: 0x80, A: 0xff}},
		{Scale{Min: 20, Max: 10, Colours: three.Colours}, 20, low},
		{Scale{Min: 20, Max: 10, Colours: three.Colours}, 11, color.RGBA{R: 0xcc, G: 0x33, A: 0xff}},
		{Scale{Min: 0, Max: 1}, 0, DefaultColours[0]},
		{Scale{Min: 0, Max: 1}, 1, DefaultColours[len(DefaultColours)-1]},
		{Scale{Min: 0, Max: 1}, 0.5, DefaultColours[2]},
		{Scale{Min: 3, Max: 3}, 100, DefaultColours[2]},
	} {
		if got := c.s.Colour(c.v); got != c.want {
			t.Errorf("%+v.Colour(%v) = %v, want %v", c.s, c.v, got, c.want)
		}
	}
	for _, s := range []Scale{{Min: 0, Max: 1}, {Min: 2, Max: 2}} {
		if got := s.At(math.NaN()); !math.IsNaN(got) {
			t.Errorf("%+v.At(NaN) = %v, want NaN: an unknown value has no place on the scale", s, got)
		}
	}
	for v, want := range map[float64]float64{-5: 0, 0.25: 0.25, 7: 1} {
		if got := (Scale{Min: 0, Max: 1}).At(v); got != want {
			t.Errorf("At(%v) = %v, want %v", v, got, want)
		}
	}
}

// line is n+1 points one unit apart along the equator, for pieces with a span
// that counts degrees of longitude.
func line(n int) []Coord {
	p := make([]Coord, n+1)
	for i := range p {
		p[i] = Coord{Lon: float64(i)}
	}
	return p
}

func lonSpan(a, b Coord) float64 { return math.Abs(b.Lon - a.Lon) }

// piecesOf is each piece as its first and last longitude and its value.
func piecesOf(g Gradient, minPiece float64) [][3]float64 {
	var out [][3]float64
	for _, p := range g.pieces(lonSpan, minPiece) {
		out = append(out, [3]float64{p.points[0].Lon, p.points[len(p.points)-1].Lon, p.value})
	}
	return out
}

func samePieces(got, want [][3]float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		for j := range 3 {
			a, b := got[i][j], want[i][j]
			if !(a == b || math.IsNaN(a) && math.IsNaN(b) || math.Abs(a-b) < 1e-9) {
				return false
			}
		}
	}
	return true
}

// A piece runs until it is minPiece long, is valued at its segments averaged
// by length, and meets the next at a shared point; what is left at the end
// joins the piece before it.
func TestGradientPiecesCoalesceToTheMinimumLength(t *testing.T) {
	g := Gradient{Points: line(10), Values: []float64{0, 2, 0, 2, 0, 2, 4, 4, 4, 4, 8}}
	got := piecesOf(g, 4)
	want := [][3]float64{{0, 4, 1}, {4, 10, (1 + 3 + 4 + 4 + 4 + 6) / 6.0}}
	if !samePieces(got, want) {
		t.Errorf("pieces = %v, want %v", got, want)
	}
}

// A long segment counts for the ground it covers, not as one sample among
// many: a piece of one 3-unit segment at 10 and three short ones at 0 is
// mostly 10.
func TestGradientPiecesAverageByLength(t *testing.T) {
	g := Gradient{
		Points: []Coord{{Lon: 0}, {Lon: 3}, {Lon: 3.1}, {Lon: 3.2}, {Lon: 3.3}},
		Values: []float64{10, 10, 0, 0, 0},
	}
	got := piecesOf(g, 100)
	want := [][3]float64{{0, 3.3, (3*10 + 0.1*5) / 3.3}}
	if !samePieces(got, want) {
		t.Errorf("pieces = %v, want %v", got, want)
	}
}

// A stretch with no values is its own piece, never averaged into a colour,
// and the pieces either side of it stop where it starts.
func TestGradientPiecesKeepUnknownApart(t *testing.T) {
	nan := math.NaN()
	g := Gradient{Points: line(8), Values: []float64{1, 1, 1, nan, nan, nan, 5, 5, 5}}
	got := piecesOf(g, 100)
	// A segment with one known end takes that end's value, so 2 to 3 and 5
	// to 6 are coloured; 3 to 5, with neither end known, is not.
	want := [][3]float64{{0, 3, 1}, {3, 5, nan}, {5, 8, 5}}
	if !samePieces(got, want) {
		t.Errorf("pieces = %v, want %v", got, want)
	}

	// A piece that reaches its length just where the values stop is not
	// followed by an empty one before the unknown stretch.
	g = Gradient{Points: line(4), Values: []float64{1, 1, 1, nan, nan}}
	got = piecesOf(g, 1)
	want = [][3]float64{{0, 1, 1}, {1, 2, 1}, {2, 3, 1}, {3, 4, nan}}
	if !samePieces(got, want) {
		t.Errorf("pieces = %v, want %v", got, want)
	}
}

// Across the antimeridian two points are as far apart on the picture as the
// short way between them, not the whole world round.
func TestGradientSpanCrossesTheAntimeridian(t *testing.T) {
	v := overlayView()
	p, err := resolve(v)
	if err != nil {
		t.Fatal(err)
	}
	o := overlayDrawer{p: p}
	across := o.span(Coord{Lon: 179.999}, Coord{Lon: -179.999})
	near := o.span(Coord{Lon: 179.997}, Coord{Lon: 179.999})
	if math.Abs(across-near) > 1e-6*near {
		t.Errorf("0.002° across the antimeridian spans %v pixels, want %v as anywhere else", across, near)
	}
	back := o.span(Coord{Lon: -179.999}, Coord{Lon: 179.999})
	if math.Abs(back-near) > 1e-6*near {
		t.Errorf("0.002° back across the antimeridian spans %v pixels, want %v", back, near)
	}
}

// A short stretch left at the end of a different kind from the piece before
// it stands alone rather than being folded into a colour it does not have.
func TestGradientPiecesLeaveAShortEndOfAnotherKind(t *testing.T) {
	nan := math.NaN()
	g := Gradient{Points: line(6), Values: []float64{1, 1, 1, 1, 1, nan, nan}}
	got := piecesOf(g, 4)
	want := [][3]float64{{0, 4, 1}, {4, 5, 1}, {5, 6, nan}}
	if !samePieces(got, want) {
		t.Errorf("pieces = %v, want %v", got, want)
	}
}

func TestGradientPiecesOfTooLittle(t *testing.T) {
	for _, g := range []Gradient{
		{},
		{Points: line(0), Values: []float64{1}},
		{Points: line(3), Values: []float64{1}},
	} {
		if got := g.pieces(lonSpan, 1); len(got) != 0 {
			t.Errorf("pieces of %v = %v, want none", g, got)
		}
	}
}

// Drawn, a gradient is its colours along the line with its halo either side.
func TestDrawColoursAGradientAlongItsLength(t *testing.T) {
	v := overlayView()
	img := blank(v)
	g := Gradient{
		Points: []Coord{at(v, 10, 100.5), at(v, 60, 100.5), at(v, 140, 100.5), at(v, 190, 100.5)},
		Values: []float64{0, 0, 1, 1},
		Scale:  Scale{Min: 0, Max: 1, Colours: []color.RGBA{low, high}},
		Width:  4, Halo: 2, HaloInk: edge,
	}
	if err := Draw(img, v, Drawing{Gradients: []Gradient{g}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		x, y int
		want color.Color
	}{{30, 100, low}, {170, 100, high}, {100, 100, color.RGBA{R: 0x80, B: 0x80, A: 0xff}}, {30, 103, edge}, {30, 110, bg}} {
		if got := img.At(c.x, c.y); !same(got, c.want) {
			t.Errorf("pixel %d,%d is %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

// Values that change faster than the picture can show are drawn in their
// average: a line alternating between the two ends every tenth of a pixel is
// the middle colour, not a flicker of blue and red.
func TestDrawAveragesValuesFinerThanAPixel(t *testing.T) {
	v := overlayView()
	img := blank(v)
	var g Gradient
	for i := 0; i <= 1000; i++ {
		g.Points = append(g.Points, at(v, 50+float64(i)/10, 100.5))
		// In pairs, so the segments themselves alternate 0, ½, 1, ½: a
		// piece per segment would draw the ends of the scale.
		g.Values = append(g.Values, float64(i/2%2))
	}
	g.Scale = Scale{Min: 0, Max: 1, Colours: []color.RGBA{low, high}}
	g.Width = 4
	if err := Draw(img, v, Drawing{Gradients: []Gradient{g}}, nil); err != nil {
		t.Fatal(err)
	}
	mid := color.RGBA{R: 0x80, B: 0x80, A: 0xff}
	for x := 60; x < 140; x += 7 {
		// Within a few levels: a piece's segments are whole, so its share
		// of each end of the scale is a few segments off an exact half.
		if got := img.RGBAAt(x, 100); absDiff(got.R, mid.R) > 8 || absDiff(got.B, mid.B) > 8 {
			t.Fatalf("pixel %d,100 is %v, want about the average %v", x, got, mid)
		}
	}
}

// A stretch with no values is drawn in Unknown, or, when that is transparent,
// as its halo alone -- never in a colour from the scale.
func TestDrawAGradientsUnknownStretch(t *testing.T) {
	nan := math.NaN()
	v := overlayView()
	g := Gradient{
		Points: []Coord{at(v, 10, 100.5), at(v, 60, 100.5), at(v, 140, 100.5), at(v, 190, 100.5)},
		Values: []float64{0, nan, nan, 0},
		Scale:  Scale{Min: 0, Max: 1, Colours: []color.RGBA{low, high}},
		Width:  4, Halo: 2, HaloInk: edge,
	}
	for _, c := range []struct {
		unknown, want color.RGBA
	}{{grey, grey}, {color.RGBA{}, edge}} {
		img := blank(v)
		g.Unknown = c.unknown
		if err := Draw(img, v, Drawing{Gradients: []Gradient{g}}, nil); err != nil {
			t.Fatal(err)
		}
		if got := img.At(100, 100); !same(got, c.want) {
			t.Errorf("with Unknown %v, the unknown stretch is %v, want %v", c.unknown, got, c.want)
		}
		if got := img.At(30, 100); !same(got, low) {
			t.Errorf("with Unknown %v, the known stretch is %v, want %v", c.unknown, got, low)
		}
	}
}

// A gradient's halo goes down with the lines' halos, before any ink, so it
// does not cut a line it crosses; and its colours go over the lines.
func TestDrawPutsAGradientsHaloUnderEveryInk(t *testing.T) {
	v := overlayView()
	img := blank(v)
	across := Line{Points: []Coord{at(v, 10, 100.5), at(v, 190, 100.5)}, Ink: grey, Width: 4}
	down := Gradient{
		Points: []Coord{at(v, 100.5, 10), at(v, 100.5, 190)},
		Values: []float64{1, 1},
		Scale:  Scale{Min: 0, Max: 1, Colours: []color.RGBA{low, high}},
		Width:  4, Halo: 6, HaloInk: edge,
	}
	if err := Draw(img, v, Drawing{Lines: []Line{across}, Gradients: []Gradient{down}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := img.At(106, 100); !same(got, grey) {
		t.Errorf("the line is %v where the gradient's halo reaches it; its ink was cut", got)
	}
	if got := img.At(100, 100); !same(got, high) {
		t.Errorf("where they cross is %v, want the gradient's colour over the line", got)
	}
}

// A bar wider than tall runs Min to Max left to right; taller than wide,
// bottom to top.
func TestDrawScaleBar(t *testing.T) {
	s := Scale{Min: 0, Max: 1, Colours: []color.RGBA{low, high}}
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	DrawScaleBar(img, image.Rect(10, 10, 31, 15), s)
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{{10, 10, low}, {30, 14, high}, {20, 12, color.RGBA{R: 0x80, B: 0x80, A: 0xff}}, {31, 12, color.RGBA{}}, {20, 15, color.RGBA{}}} {
		if got := img.RGBAAt(c.x, c.y); got != c.want {
			t.Errorf("across: pixel %d,%d is %v, want %v", c.x, c.y, got, c.want)
		}
	}
	img = image.NewRGBA(image.Rect(0, 0, 50, 50))
	DrawScaleBar(img, image.Rect(40, 20, 45, 41), s)
	if got := img.RGBAAt(42, 40); got != low {
		t.Errorf("up: the bottom is %v, want %v", got, low)
	}
	if got := img.RGBAAt(42, 20); got != high {
		t.Errorf("up: the top is %v, want %v", got, high)
	}
	// A bar running off the image keeps its colours where they were asked
	// for, rather than squeezing the whole scale into what is left.
	img = image.NewRGBA(image.Rect(0, 0, 50, 50))
	DrawScaleBar(img, image.Rect(29, 0, 70, 5), s)
	if got := img.RGBAAt(49, 2); got != (color.RGBA{R: 0x80, B: 0x80, A: 0xff}) {
		t.Errorf("the middle of a bar half off the image is %v, want the middle of the scale", got)
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
