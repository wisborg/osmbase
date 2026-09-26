package render

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mercator"
)

var (
	bg   = color.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xff}
	red  = color.RGBA{R: 0xff, A: 0xff}
	blue = color.RGBA{B: 0xff, A: 0xff}
	halo = color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
)

// overlayView is 200 by 200 pixels of the ground about 0,0, square in the
// projection, so a pixel's position is a straight proportion of the bounds.
func overlayView() View {
	const d = 0.01
	w, n := mercator.Unproject(0.5-d/2, 0.5-d/2)
	e, s := mercator.Unproject(0.5+d/2, 0.5+d/2)
	return View{Bounds: Bounds{West: w, South: s, East: e, North: n}, Width: 200, Height: 200}
}

// at is the coordinate at pixel x, y of overlayView, worked out from the
// bounds directly rather than through the projection Draw uses.
func at(v View, x, y float64) Coord {
	x0, y0 := mercator.Project(v.Bounds.West, v.Bounds.North)
	x1, y1 := mercator.Project(v.Bounds.East, v.Bounds.South)
	lon, lat := mercator.Unproject(x0+(x1-x0)*x/float64(v.Width), y0+(y1-y0)*y/float64(v.Height))
	return Coord{Lat: lat, Lon: lon}
}

func blank(v View) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, v.Width, v.Height))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	return img
}

func same(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// A marker lands on the pixel its coordinate is at in the view -- the same
// pixel the map put that ground on -- and nowhere else.
func TestDrawPutsAMarkerWhereItsCoordinateIs(t *testing.T) {
	v := overlayView()
	img := blank(v)
	err := Draw(img, v, Drawing{Markers: []Marker{{At: at(v, 60.5, 140.5), Ink: red, Radius: 4}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !same(img.At(60, 140), red) {
		t.Errorf("the marker's own pixel is %v, want its ink", img.At(60, 140))
	}
	for _, p := range []image.Point{{60, 130}, {70, 140}, {140, 60}} {
		if !same(img.At(p.X, p.Y), bg) {
			t.Errorf("pixel %v is %v, outside the marker", p, img.At(p.X, p.Y))
		}
	}
}

// A line's halo is a band either side of it in the halo ink, and nothing
// beyond the band is touched.
func TestDrawStrokesALineWithItsHalo(t *testing.T) {
	v := overlayView()
	img := blank(v)
	l := Line{Points: []Coord{at(v, 10, 100.5), at(v, 190, 100.5)}, Ink: red, Width: 4, Halo: 3, HaloInk: halo}
	if err := Draw(img, v, Drawing{Lines: []Line{l}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		y    int
		want color.Color
	}{{100, red}, {104, halo}, {96, halo}, {110, bg}, {90, bg}} {
		if !same(img.At(100, c.y), c.want) {
			t.Errorf("pixel 100,%d is %v, want %v", c.y, img.At(100, c.y), c.want)
		}
	}
}

// Every halo goes down before any ink. Drawing each line's halo and then its
// ink, line by line, lets the second line's halo cut a gap in the first
// wherever they cross -- a route that crosses itself would look broken at
// every crossing.
func TestDrawPutsNoHaloOverAnyInk(t *testing.T) {
	v := overlayView()
	img := blank(v)
	across := Line{Points: []Coord{at(v, 10, 100.5), at(v, 190, 100.5)}, Ink: red, Width: 4, Halo: 6, HaloInk: halo}
	down := Line{Points: []Coord{at(v, 100.5, 10), at(v, 100.5, 190)}, Ink: blue, Width: 4, Halo: 6, HaloInk: halo}
	if err := Draw(img, v, Drawing{Lines: []Line{across, down}}, nil); err != nil {
		t.Fatal(err)
	}
	// On the red line, inside the blue line's halo but clear of its ink.
	if got := img.At(106, 100); !same(got, red) {
		t.Errorf("the first line is %v where the second one's halo reaches it; its ink was cut", got)
	}
}

// A line reaching far outside the view is clipped before it is stroked. The
// rasterizer walks an unclipped segment scanline by scanline for its whole
// VERTICAL length, visible or not -- measured at 12 ms a million pixels -- so
// the case that costs is a steep line at a deep zoom: here, a view about a
// metre across, and a line that leaves it for the poles, some two hundred
// million pixels each way. A horizontal line would be free either way, and
// was the first version of this test, which passed with the clipping removed.
func TestDrawClipsALineThatReachesFarOutsideTheView(t *testing.T) {
	const d = 1e-8
	w, n := mercator.Unproject(0.5-d/2, 0.5-d/2)
	e, s := mercator.Unproject(0.5+d/2, 0.5+d/2)
	v := View{Bounds: Bounds{West: w, South: s, East: e, North: n}, Width: 200, Height: 200}
	img := blank(v)
	mid := at(v, 100.5, 100)
	l := Line{Points: []Coord{{Lat: 80, Lon: mid.Lon}, {Lat: -80, Lon: mid.Lon}}, Ink: red, Width: 3}

	start := time.Now()
	if err := Draw(img, v, Drawing{Lines: []Line{l}}, nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("drawing took %v; the line was rasterised along its whole length", took)
	}
	if !same(img.At(100, 5), red) || !same(img.At(100, 195), red) {
		t.Error("the clipped line does not cross the view")
	}
}

// A label goes to the right of its marker, and to its left where the right
// would run off the image.
func TestDrawWritesALabelBesideItsMarker(t *testing.T) {
	v := overlayView()
	inkIn := func(img *image.RGBA, r image.Rectangle) bool {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if same(img.At(x, y), blue) {
					return true
				}
			}
		}
		return false
	}
	for _, tc := range []struct {
		name        string
		x           float64
		side, other image.Rectangle
	}{
		{"room on the right", 40, image.Rect(46, 90, 110, 110), image.Rect(0, 90, 34, 110)},
		{"against the right edge", 180, image.Rect(100, 90, 174, 110), image.Rect(186, 90, 200, 110)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := blank(v)
			// No dot, so every blue pixel is a letter.
			m := Marker{At: at(v, tc.x, 100), Ink: blue, Label: "Start 10km", Halo: 1, HaloInk: halo}
			if err := Draw(img, v, Drawing{Markers: []Marker{m}}, basicfont.Face7x13); err != nil {
				t.Fatal(err)
			}
			if !inkIn(img, tc.side) {
				t.Errorf("no letters in %v", tc.side)
			}
			if inkIn(img, tc.other) {
				t.Errorf("letters on the wrong side, in %v", tc.other)
			}
		})
	}
}

// Nothing to draw leaves the picture as it was, and a picture of the wrong
// size for the view is refused rather than drawn off by a scale.
func TestDrawRefusesAPictureOfAnotherSize(t *testing.T) {
	v := overlayView()
	img := blank(v)
	before := append([]uint8(nil), img.Pix...)
	if err := Draw(img, v, Drawing{}, nil); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(img.Pix) {
		t.Error("an empty drawing changed the picture")
	}
	if err := Draw(image.NewRGBA(image.Rect(0, 0, 100, 100)), v, Drawing{}, nil); err == nil {
		t.Error("a 100x100 picture was accepted for a 200x200 view")
	}
	if err := Draw(nil, v, Drawing{}, nil); err == nil {
		t.Error("no picture at all was accepted")
	}
}
