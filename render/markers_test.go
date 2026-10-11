package render

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

var markerGround = color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}

// drawOne draws m at x, y over a grey ground and returns the picture.
func drawOne(t *testing.T, m Marker, x, y float64) *image.RGBA {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(img, img.Bounds(), image.NewUniform(markerGround), image.Point{}, draw.Src)
	DrawMarkers(img, []Marker{m}, func(Coord) (float64, float64, bool) { return x, y, true }, testFace())
	return img
}

// near reports whether a pixel is within a few levels of c.
func near(got, c color.RGBA) bool {
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(got.R, c.R) <= 12 && d(got.G, c.G) <= 12 && d(got.B, c.B) <= 12
}

// A pin stands on its place by its tip: its head is above the place, the
// place itself is at the tip, and below it is the ground.
func TestPinsStandOnTheirPlace(t *testing.T) {
	const r = 10
	for _, c := range []struct {
		shape MarkerShape
		ink   color.RGBA
	}{
		{ShapeStartPin, startGreen},
		{ShapeFinishPin, finishRed},
	} {
		img := drawOne(t, Marker{Shape: c.shape, Radius: r}, 100, 100)
		head := 100 - pinHeight*r
		// The left of the head is the pin's own ink, whatever the symbol.
		if got := img.RGBAAt(100-int(0.8*r), int(head)); !near(got, c.ink) {
			t.Errorf("shape %d: the head's left at %v, want %v", c.shape, got, c.ink)
		}
		if got := img.RGBAAt(100, 100-3); !near(got, c.ink) && !near(got, white) {
			t.Errorf("shape %d: just above the place %v, want the pin", c.shape, got)
		}
		if got := img.RGBAAt(100, 100+8); !near(got, markerGround) {
			t.Errorf("shape %d: below the place %v, want the ground", c.shape, got)
		}
		if got := img.RGBAAt(100, int(head-2*r)); !near(got, markerGround) {
			t.Errorf("shape %d: above the head %v, want the ground", c.shape, got)
		}
	}
}

// A start pin's head holds a white triangle; a finish pin's a checkered
// disc, black and white, inside a red rim; one for both is green on the left
// and red on the right.
func TestPinSymbols(t *testing.T) {
	var r = 14.0
	head := 100 - pinHeight*r
	start := drawOne(t, Marker{Shape: ShapeStartPin, Radius: r}, 100, 100)
	if got := start.RGBAAt(100, int(head)); !near(got, white) {
		t.Errorf("the start's play triangle at %v, want white", got)
	}
	finish := drawOne(t, Marker{Shape: ShapeFinishPin, Radius: r}, 100, 100)
	black, whites := 0, 0
	for y := int(head) - 6; y <= int(head)+6; y++ {
		for x := 94; x <= 106; x++ {
			switch c := finish.RGBAAt(x, y); {
			case near(c, checkBlack):
				black++
			case near(c, white):
				whites++
			}
		}
	}
	if black < 20 || whites < 20 {
		t.Errorf("the finish's head has %d black and %d white pixels, want a chequer of both", black, whites)
	}
	if got := finish.RGBAAt(100+int(0.85*r), int(head)); !near(got, finishRed) {
		t.Errorf("the finish's rim at %v, want red round the chequer", got)
	}
	both := drawOne(t, Marker{Shape: ShapeStartFinishPin, Radius: r}, 100, 100)
	if got := both.RGBAAt(100-int(0.85*r), int(head)); !near(got, startGreen) {
		t.Errorf("the left of a start and finish at %v, want green", got)
	}
	if got := both.RGBAAt(100+int(0.75*r), int(head)+int(0.45*r)); !near(got, finishRed) {
		t.Errorf("the right of a start and finish at %v, want red", got)
	}
}

// A numbered disc is centred on its place, holds its number, and widens for
// a longer one rather than letting it spill out.
func TestNumberDiscsHoldTheirNumber(t *testing.T) {
	ink := color.RGBA{R: 0x24, G: 0x42, B: 0x7a, A: 0xff}
	width := func(img *image.RGBA) (lo, hi int) {
		lo, hi = 200, -1
		for x := 0; x < 200; x++ {
			if near(img.RGBAAt(x, 100), ink) || near(img.RGBAAt(x, 100), white) {
				lo, hi = min(lo, x), max(hi, x)
			}
		}
		return lo, hi
	}
	short := drawOne(t, Marker{Shape: ShapeNumberDisc, Ink: ink, Radius: 6, Text: "7"}, 100, 100)
	long := drawOne(t, Marker{Shape: ShapeNumberDisc, Ink: ink, Radius: 6, Text: "21.1"}, 100, 100)
	slo, shi := width(short)
	llo, lhi := width(long)
	if !(lhi-llo > shi-slo+10) {
		t.Errorf("a disc for 21.1 spans %d to %d, one for 7 %d to %d; want the longer number's wider", llo, lhi, slo, shi)
	}
	if mid := (llo + lhi) / 2; mid < 98 || mid > 102 {
		t.Errorf("the disc's middle is at %d, want it on its place at 100", mid)
	}
	whites := 0
	for y := 94; y <= 106; y++ {
		for x := 94; x <= 106; x++ {
			if near(short.RGBAAt(x, y), white) {
				whites++
			}
		}
	}
	if whites < 5 {
		t.Errorf("%d white pixels in the middle of a disc of 7, want its number", whites)
	}
	ring := drawOne(t, Marker{Shape: ShapeNumberRing, Ink: ink, Radius: 6, Text: "7"}, 100, 100)
	lo, _ := width(ring)
	ringed := false
	for x := lo; x < lo+6; x++ {
		ringed = ringed || near(ring.RGBAAt(x, 100), ink)
	}
	if !ringed || !near(ring.RGBAAt(lo+7, 100), white) {
		t.Errorf("a ringed disc: no ring of its ink at its edge, or not white inside it")
	}
}

// An arrow points where it is told: its tip is up at a heading of zero and
// to the right at ninety.
func TestArrowsPointAtTheirHeading(t *testing.T) {
	ink := color.RGBA{R: 0xf0, G: 0x53, B: 0x2d, A: 0xff}
	up := drawOne(t, Marker{Shape: ShapeArrow, Ink: ink, Radius: 12, Heading: 0}, 100, 100)
	right := drawOne(t, Marker{Shape: ShapeArrow, Ink: ink, Radius: 12, Heading: 90}, 100, 100)
	if !near(up.RGBAAt(100, 91), ink) || near(up.RGBAAt(109, 100), ink) {
		t.Errorf("heading 0: above %v, right %v; want the tip above", up.RGBAAt(100, 91), up.RGBAAt(109, 100))
	}
	if !near(right.RGBAAt(109, 100), ink) || near(right.RGBAAt(100, 91), ink) {
		t.Errorf("heading 90: right %v, above %v; want the tip to the right", right.RGBAAt(109, 100), right.RGBAAt(100, 91))
	}
	plane := drawOne(t, Marker{Shape: ShapePlane, Ink: ink, Radius: 16, Heading: 0}, 100, 100)
	if !near(plane.RGBAAt(100, 100), ink) || !near(plane.RGBAAt(100-12, 102), ink) || near(plane.RGBAAt(100-12, 92), ink) {
		t.Errorf("a plane nose up has its wings out level with its middle, and nothing up beside its nose")
	}
}

// A pin's label is written beside its head, where the eye is, not beside
// its tip.
func TestAPinsLabelIsBesideItsHead(t *testing.T) {
	const r = 10
	img := drawOne(t, Marker{Shape: ShapeStartPin, Radius: r, Label: "Start", HaloInk: white, Halo: 1}, 60, 120)
	ink := startGreen // a pin's label is in the pin's own colour
	headY, tipY := 120-int(pinHeight*r), 120
	count := func(y0, y1 int) int {
		n := 0
		for y := y0; y <= y1; y++ {
			for x := 60 + r; x < 160; x++ {
				if near(img.RGBAAt(x, y), ink) {
					n++
				}
			}
		}
		return n
	}
	if beside, low := count(headY-6, headY+6), count(tipY-4, tipY+6); beside == 0 || low != 0 {
		t.Errorf("label pixels: %d beside the head, %d beside the tip; want them all beside the head", beside, low)
	}
}
