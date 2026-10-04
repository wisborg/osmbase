package render

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/wisborg/osmbase/mvt"
)

// identity places tile coordinates on the surface as they are.
var identity = tileTransform{ax: 1, ay: 1}

func tileLine(ps ...[2]int32) [][]mvt.Point {
	var l []mvt.Point
	for _, p := range ps {
		l = append(l, mvt.Point{X: p[0], Y: p[1]})
	}
	return [][]mvt.Point{l}
}

// A name goes in the middle of its line, along it; turned to read left to
// right whichever way the line was drawn; on a straight stretch rather than
// across a bend; and not at all on a line shorter than itself.
func TestPlaceAlong(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	x, y, a, ok := placeAlong(tileLine([2]int32{0, 100}, [2]int32{400, 100}), identity, 100, 12)
	if !ok || !near(x, 200) || !near(y, 100) || !near(a, 0) {
		t.Errorf("along a level line: %v,%v at %v, %v; want 200,100 at 0", x, y, a, ok)
	}
	// Down and to the right at 45°: the angle is +45°, clockwise on the image.
	_, _, a, ok = placeAlong(tileLine([2]int32{0, 0}, [2]int32{300, 300}), identity, 100, 12)
	if !ok || !near(a, math.Pi/4) {
		t.Errorf("down a diagonal: %v°", a*180/math.Pi)
	}
	// The same line drawn from its far end reads the same way, not upside
	// down.
	_, _, b, _ := placeAlong(tileLine([2]int32{300, 300}, [2]int32{0, 0}), identity, 100, 12)
	if !near(a, b) {
		t.Errorf("the line drawn backwards turns the name to %v°, want %v°", b*180/math.Pi, a*180/math.Pi)
	}
	// Straight up: -90° either way, never +90°, which would be upside down
	// for a reader turning their head the usual way.
	_, _, a, _ = placeAlong(tileLine([2]int32{50, 400}, [2]int32{50, 0}), identity, 100, 12)
	if !near(a, -math.Pi/2) && !near(a, math.Pi/2) {
		t.Errorf("up a vertical line: %v°", a*180/math.Pi)
	}
	if _, _, _, ok := placeAlong(tileLine([2]int32{0, 0}, [2]int32{80, 0}), identity, 100, 12); ok {
		t.Error("a name longer than its line was placed")
	}
	// A right angle in the middle of a line 400 long: the name goes on
	// one of the straight arms, not round the corner.
	x, y, a, ok = placeAlong(tileLine([2]int32{0, 0}, [2]int32{200, 0}, [2]int32{200, 200}), identity, 100, 12)
	// It may overrun the corner by the little the bend allows, turning
	// it a degree or two.
	level, upright := math.Abs(a) < 3*math.Pi/180, math.Abs(math.Abs(a)-math.Pi/2) < 3*math.Pi/180
	if !ok || !(level && x <= 155 || upright && y >= 45) {
		t.Errorf("round a corner: %v,%v at %v°, %v; want on one straight arm", x, y, a*180/math.Pi, ok)
	}
	// A zigzag tighter than the name everywhere: nowhere to put it.
	var zig [][2]int32
	for i := int32(0); i <= 40; i++ {
		zig = append(zig, [2]int32{i * 10, (i % 2) * 30})
	}
	if _, _, _, ok := placeAlong(tileLine(zig...), identity, 100, 12); ok {
		t.Error("a name was written along a zigzag")
	}
}

// Rotated boxes collide by their shapes, not by the squares round them: two
// names along parallel diagonals clear of each other both fit, although
// their bounding squares overlap; two crossing at a junction do not.
func TestAlongQuadsOverlapByShape(t *testing.T) {
	a := alongQuad(100, 100, math.Pi/4, 100, 12, 0)
	b := alongQuad(125, 75, math.Pi/4, 100, 12, 0) // 35 px beside a, square to it
	if a.overlaps(b) || b.overlaps(a) {
		t.Error("two names along parallel diagonals 35 px apart overlap")
	}
	box := func(q quad) image.Rectangle {
		r := image.Rectangle{}
		for _, p := range q {
			r = r.Union(image.Rect(int(p.X), int(p.Y), int(p.X)+1, int(p.Y)+1))
		}
		return r
	}
	if !box(a).Overlaps(box(b)) {
		t.Fatal("the test's two names do not even have overlapping squares")
	}
	// Touching is not overlapping, as image.Rectangle.Overlaps has it for
	// the square boxes this replaced.
	if rectQuad(image.Rect(0, 0, 10, 10)).overlaps(rectQuad(image.Rect(10, 0, 20, 10))) {
		t.Error("two boxes sharing an edge overlap")
	}
	cross := alongQuad(100, 100, -math.Pi/4, 100, 12, 0)
	if !a.overlaps(cross) {
		t.Error("two names crossing at one point do not overlap")
	}
	if !rectQuad(image.Rect(0, 0, 10, 10)).in(image.Rect(0, 0, 10, 10)) || alongQuad(5, 5, 0, 20, 4, 0).in(image.Rect(0, 0, 10, 10)) {
		t.Error("in is wrong about a quad's corners")
	}
}

// Placement keeps the first of two crossing road names and both of two
// parallel ones.
func TestPlaceLabelsAlongRoads(t *testing.T) {
	along := func(text string, x, y, angle float64, priority int) candidate {
		c := cand(text, x, y, priority, 1)
		c.along, c.angle = true, angle
		return c
	}
	bounds := image.Rect(0, 0, 400, 400)
	got := placeLabelsT([]candidate{
		along("High Street", 200, 200, math.Pi/4, 10),
		along("Low Road", 200, 200, -math.Pi/4, 5),
		along("Side Lane", 240, 160, math.Pi/4, 3),
	}, testFace(), DefaultLabelPadding, bounds)
	if len(got) != 2 || got[0].text != "High Street" || got[1].text != "Side Lane" {
		var names []string
		for _, p := range got {
			names = append(names, p.text)
		}
		t.Errorf("placed %v; want High Street and the parallel Side Lane", names)
	}
}

// A name along a line is drawn turned to it: ink along the diagonal it was
// placed on and none across it, its halo round the glyphs in the halo's
// colour, and nothing outside its box.
func TestDrawAlongTurnsTheText(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 200, 200))
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for i := range dst.Pix {
		dst.Pix[i] = 0
	}
	ink, halo := color.RGBA{R: 0xff, A: 0xff}, color.RGBA{B: 0xff, A: 0xff}
	c := cand("HHHHHHHHHH", 100, 100, 1, 1)
	c.along, c.angle = true, math.Pi/4
	got := placeLabelsT([]candidate{c}, testFace(), 0, dst.Rect)
	if len(got) != 1 {
		t.Fatal("not placed")
	}
	got[0].onRoad = true
	drawLabel(dst, got[0], Palette{Label: ink, LabelMinor: ink, Background: halo}, 0, nil)
	count := func(x0, y0, x1, y1 int, want color.RGBA) int {
		n := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				p := dst.RGBAAt(x, y)
				if p.R > 0x80 && want.R > 0 || p.B > 0x80 && want.B > 0 {
					n++
				}
			}
		}
		return n
	}
	// Along the diagonal through the centre, down and to the right.
	if count(70, 70, 90, 90, ink) == 0 || count(110, 110, 130, 130, ink) == 0 {
		t.Error("no ink along the diagonal the name was placed on")
	}
	// Across it, off the name's line: nothing.
	if count(55, 135, 75, 155, ink) != 0 || count(125, 45, 145, 65, ink) != 0 {
		t.Error("ink across the diagonal, where a level name would have put it")
	}
	if count(60, 60, 140, 140, halo) == 0 {
		t.Error("no halo round the glyphs")
	}
	_ = white
}
