package perspective

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// shape is a HeightSource that answers every tile at every zoom from a
// formula of metres east and north of a centre, so a synthetic hill is
// exact wherever and however closely it is read.
type shape struct {
	centre render.Coord
	h      func(east, north float64) float64
}

func (s shape) Heights(z uint8, x, y uint32) ([]float32, int, bool, error) {
	// 512 samples a side, as real elevation tiles have: render.Heights picks
	// its zoom assuming that, and coarser tiles read at that zoom smooth a
	// hill flat.
	const n = 512
	w, south, e, north, err := mercator.TileBounds(z, x, y)
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]float32, n*n)
	cos := math.Cos(s.centre.Lat * math.Pi / 180)
	for j := 0; j < n; j++ {
		lat := north + (south-north)*(float64(j)+0.5)/n
		for i := 0; i < n; i++ {
			lon := w + (e-w)*(float64(i)+0.5)/n
			east := (lon - s.centre.Lon) * 111_320 * cos
			nth := (lat - s.centre.Lat) * 110_574
			out[j*n+i] = float32(s.h(east, nth))
		}
	}
	return out, n, true, nil
}

var (
	centre = render.Coord{Lat: 10, Lon: 20}
	red    = color.RGBA{R: 0xff, A: 0xff}
	green  = color.RGBA{G: 0xff, A: 0xff}
	blue   = color.RGBA{B: 0xff, A: 0xff}
	yellow = color.RGBA{R: 0xff, G: 0xff, A: 0xff}
	white  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// scene is a square area half deg degrees each side of the centre, its map
// coloured by paint at each pixel's position relative to the centre.
func scene(deg float64, h func(e, n float64) float64, paint func(east, north bool) color.RGBA) Scene {
	v := render.View{
		Bounds: render.Bounds{West: centre.Lon - deg, South: centre.Lat - deg, East: centre.Lon + deg, North: centre.Lat + deg},
		Width:  400, Height: 400,
	}
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	cx, cy, _ := v.Pixel(centre)
	for y := 0; y < 400; y++ {
		for x := 0; x < 400; x++ {
			img.SetRGBA(x, y, paint(float64(x)+0.5 > cx, float64(y)+0.5 < cy))
		}
	}
	return Scene{Map: img, View: v, Heights: shape{centre, h}}
}

func flat(e, n float64) float64 { return 0 }

func quadrants(east, north bool) color.RGBA {
	switch {
	case north && !east:
		return red
	case north && east:
		return green
	case !north && !east:
		return blue
	}
	return yellow
}

func near(a, b color.RGBA) bool {
	d := func(x, y uint8) int { return int(x) - int(y) }
	return math.Abs(float64(d(a.R, b.R)))+math.Abs(float64(d(a.G, b.G)))+math.Abs(float64(d(a.B, b.B))) < 40
}

func render1(t *testing.T, s Scene, c Camera, w, h int) *image.RGBA {
	t.Helper()
	img, err := Render(s, c, Options{Width: w, Height: h, HazeMetres: 1e12})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Straight down, the map is the map: north up when looking north, and
// turned to look east, east is up and north is to the left.
func TestStraightDownIsTheMapTheRightWayRound(t *testing.T) {
	s := scene(0.02, flat, quadrants)
	for _, c := range []struct {
		heading float64
		want    [4]color.RGBA // top-left, top-right, bottom-left, bottom-right
	}{
		{0, [4]color.RGBA{red, green, blue, yellow}},
		{90, [4]color.RGBA{green, yellow, red, blue}},
	} {
		img := render1(t, s, Camera{Target: centre, Distance: 2000, Heading: c.heading, Pitch: 90}, 200, 200)
		for k, p := range [][2]int{{50, 50}, {150, 50}, {50, 150}, {150, 150}} {
			if got := img.RGBAAt(p[0], p[1]); !near(got, c.want[k]) {
				t.Errorf("heading %v: pixel %v is %v, want %v", c.heading, p, got, c.want[k])
			}
		}
	}
}

// topOfGround is the first row from the top of column x that is not sky:
// redder or greener than the sky's pale blue.
func topOfGround(img *image.RGBA, x int) int {
	for y := 0; y < img.Bounds().Dy(); y++ {
		c := img.RGBAAt(x, y)
		if int(c.B) < int(c.R)+10 || c.G < 0x60 {
			return y
		}
	}
	return -1
}

// A cone seen from the south, the camera a few degrees above the level and
// looking at flat ground D metres short of the cone's centre: its apex is
// where the camera's geometry, worked out by hand, puts it --
// (D·sin p + H·cos p) / (d + D·cos p - H·sin p) focal lengths above the
// image's centre -- and twice as high a cone, by exaggeration, rises higher.
// The target is off the cone because the camera looks at the GROUND there:
// a target on the apex puts the apex at the centre, whatever its height.
func TestAConesApexIsWhereTheGeometryPutsIt(t *testing.T) {
	const H, R, D, d, pitch = 400.0, 1500.0, 2000.0, 4000.0, 3.0
	cone := func(e, n float64) float64 { return math.Max(0, H*(1-math.Hypot(e, n-D)/R)) }
	s := scene(0.05, cone, func(bool, bool) color.RGBA { return white })
	// A vertex every map pixel, about 28 m: at the default spacing a sharp
	// apex falls between vertices and is drawn tens of metres short, which
	// is the mesh's resolution and not the geometry this checks.
	s.Step = 1
	const w, h = 300, 200
	expect := func(height float64) float64 {
		p := pitch * math.Pi / 180
		focal := float64(h) / 2 / math.Tan(DefaultFOV*math.Pi/360)
		return float64(h)/2 - focal*(D*math.Sin(p)+height*math.Cos(p))/(d+D*math.Cos(p)-height*math.Sin(p))
	}
	for _, ex := range []float64{1, 2} {
		s.Exaggeration = ex
		img := render1(t, s, Camera{Target: centre, Distance: d, Pitch: pitch}, w, h)
		got, want := topOfGround(img, w/2), expect(H*ex)
		if math.Abs(float64(got)-want) > 2 {
			t.Errorf("exaggeration %v: the apex is at row %d, want %.1f", ex, got, want)
		}
	}
}

// Nearer ground hides what is behind it. With the far half of the map red
// and a cone on the boundary, seen low, the top of the cone in the middle of
// the image is its near face, white: neither its own far face nor the red
// ground beyond it shows through. Without the cone, red ground is what
// shows there.
//
// From both sides, because the mesh is drawn from its northern row south:
// seen from the south that is back to front, which hides the far ground by
// drawing order alone and passed with no depth test at all. Seen from the
// north it is front to back, and only the depth buffer keeps the far ground
// from being drawn over the near.
func TestNearerGroundHidesWhatIsBehind(t *testing.T) {
	const H, R = 600.0, 1500.0
	for _, heading := range []float64{0, 180} {
		far := heading == 0 // the red half is north when looking north
		paint := func(_, north bool) color.RGBA {
			if north == far {
				return red
			}
			return white
		}
		cam := Camera{Target: centre, Distance: 4000, Pitch: 2, Heading: heading}
		const w, h = 300, 200
		redBetween := func(s Scene) int {
			img := render1(t, s, cam, w, h)
			top := topOfGround(img, w/2)
			n := 0
			for y := top + 2; y < top+12; y++ {
				if c := img.RGBAAt(w/2, y); c.R > 0xc0 && c.G < 0x60 {
					n++
				}
			}
			return n
		}
		if n := redBetween(scene(0.05, flat, paint)); n == 0 {
			t.Fatalf("heading %v: precondition: with no cone, no red ground shows at the top of the ground", heading)
		}
		cone := func(e, n float64) float64 { return math.Max(0, H*(1-math.Hypot(e, n)/R)) }
		if n := redBetween(scene(0.05, cone, paint)); n != 0 {
			t.Errorf("heading %v: %d red pixels show through the cone", heading, n)
		}
	}
}

// A camera all but on the ground: the triangles under and behind it cross
// its own plane and are cut there, and the bottom of the image is still
// ground from edge to edge, with no gaps.
func TestGroundUnderTheCameraIsCutNotLost(t *testing.T) {
	s := scene(0.02, flat, quadrants)
	img := render1(t, s, Camera{Target: centre, Distance: 30, Pitch: 5, Heading: 45}, 160, 120)
	for x := 0; x < 160; x++ {
		if c := img.RGBAAt(x, 119); !(near(c, red) || near(c, green) || near(c, blue) || near(c, yellow)) {
			t.Fatalf("bottom row, x %d: %v, not ground", x, c)
		}
	}
}

// Above the horizon is sky; below it, at the centre, the ground looked at.
func TestTheHorizonDividesSkyFromGround(t *testing.T) {
	s := scene(0.05, flat, func(bool, bool) color.RGBA { return red })
	const w, h, pitch = 300, 200, 8.0
	img := render1(t, s, Camera{Target: centre, Distance: 300, Pitch: pitch}, w, h)
	focal := float64(h) / 2 / math.Tan(DefaultFOV*math.Pi/360)
	horizon := float64(h)/2 - focal*math.Tan(pitch*math.Pi/180)
	if c := img.RGBAAt(w/2, int(horizon)-8); c.B < c.R {
		t.Errorf("above the horizon: %v, want sky", c)
	}
	if c := img.RGBAAt(w/2, h/2); !near(c, red) {
		t.Errorf("at the centre: %v, want the ground", c)
	}
}

// A camera that cannot be a camera is refused, not drawn as nonsense.
func TestRefusesWhatIsNotACamera(t *testing.T) {
	s := scene(0.02, flat, quadrants)
	for _, c := range []Camera{
		{Target: centre, Distance: 0, Pitch: 45},
		{Target: centre, Distance: 100, Pitch: -10},
		{Target: centre, Distance: 100, Pitch: 100},
		{Target: centre, Distance: 100, Pitch: 45, FOV: 179},
	} {
		if _, err := Render(s, c, Options{Width: 10, Height: 10}); err == nil {
			t.Errorf("drew %+v", c)
		}
	}
}
