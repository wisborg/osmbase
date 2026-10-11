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
	pic, err := Render(s, c, Options{Width: w, Height: h, HazeMetres: 1e12})
	if err != nil {
		t.Fatal(err)
	}
	return pic.Image
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

// The map ends in haze, not in a line: seen whole from above, its border is
// the haze colour and its middle is the map's own.
func TestTheMapsEdgeFadesIntoTheHaze(t *testing.T) {
	s := scene(0.02, flat, func(bool, bool) color.RGBA { return red })
	haze := color.RGBA{R: 0x20, G: 0x40, B: 0x60, A: 0xff}
	pic, err := Render(s, Camera{Target: centre, Distance: 30000, Pitch: 90}, Options{Width: 200, Height: 200, Haze: haze, HazeMetres: 1e12})
	if err != nil {
		t.Fatal(err)
	}
	img := pic.Image
	// Find the map's extent along the middle row: where the ground starts.
	row := 100
	left := -1
	for x := 0; x < 200; x++ {
		if c := img.RGBAAt(x, row); c.R > 0x10 || c.G > 0x30 {
			if c != DefaultHorizon {
				left = x
				break
			}
		}
	}
	if left < 0 || left > 90 {
		t.Fatalf("no map found along the middle row (left edge %d)", left)
	}
	// The fade rises smoothly over the last part of the map, so a pixel
	// inside the edge is well on its way to the haze rather than exactly
	// it: most of its red gone. Without the fade it is the map's red.
	if c := img.RGBAAt(left+1, row); c.R > 0xa0 {
		t.Errorf("just inside the map's edge: %v, want it faded most of the way from red toward the haze %v", c, haze)
	}
	if c := img.RGBAAt(100, row); !near(c, red) {
		t.Errorf("in the middle: %v, want the map's red", c)
	}
}

// Locate says where a place appears and whether it is seen: the point looked
// at is the centre of the image; ground a cone stands in front of is hidden,
// and seen when the cone is gone; ground behind the camera is not seen.
func TestLocateFindsPlacesAndKnowsWhatIsHidden(t *testing.T) {
	top, err := Render(scene(0.02, flat, quadrants), Camera{Target: centre, Distance: 2000, Pitch: 90}, Options{Width: 200, Height: 100})
	if err != nil {
		t.Fatal(err)
	}
	if x, y, ok := top.Locate(centre); !ok || math.Abs(x-100) > 0.5 || math.Abs(y-50) > 0.5 {
		t.Errorf("the point looked at is at (%.1f, %.1f), seen %v; want the centre, seen", x, y, ok)
	}

	// 1.8 km north: beyond a cone of radius 1.5 km on the target, seen from
	// 4 km south and 2 degrees up.
	behind := render.Coord{Lat: centre.Lat + 1800.0/110_574, Lon: centre.Lon}
	cam := Camera{Target: centre, Distance: 4000, Pitch: 2}
	cone := func(e, n float64) float64 { return math.Max(0, 600*(1-math.Hypot(e, n)/1500)) }
	for _, c := range []struct {
		name   string
		ground func(e, n float64) float64
		seen   bool
	}{{"flat", flat, true}, {"behind a cone", cone, false}} {
		pic, err := Render(scene(0.05, c.ground, quadrants), cam, Options{Width: 300, Height: 200})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok := pic.Locate(behind); ok != c.seen {
			t.Errorf("%s: seen %v, want %v", c.name, ok, c.seen)
		}
		// 1 km south of the target is 3 km in front of the camera; 5 km
		// south is 1 km behind it.
		if _, _, ok := pic.Locate(render.Coord{Lat: centre.Lat - 5000.0/110_574, Lon: centre.Lon}); ok {
			t.Errorf("%s: ground behind the camera is seen", c.name)
		}
	}
}

// The footprint is what the lens sees: straight down, a rectangle round the
// point looked at as wide as the lens is; looking north and low, ground
// reaching far ahead and little behind; and a wider lens, more to the sides.
func TestFootprintIsWhatTheLensSees(t *testing.T) {
	down := Camera{Target: centre, Distance: 1000, Pitch: 90}.Footprint(1, 1e6)
	half := 1000 * math.Tan(DefaultFOV*math.Pi/360) // metres from the centre to an edge
	if got := (down.North - centre.Lat) * 111_319.5; math.Abs(got-half) > 1 {
		t.Errorf("straight down, the footprint reaches %.1f m north, want %.1f", got, half)
	}
	low := Camera{Target: centre, Distance: 1000, Pitch: 10}
	fp := low.Footprint(4.0/3, 20000)
	ahead, behind := (fp.North-centre.Lat)*111_319.5, (centre.Lat-fp.South)*111_319.5
	if !(ahead > 15000 && behind < 1500) {
		t.Errorf("low and north: %.0f m ahead and %.0f m behind; want far ahead and little behind", ahead, behind)
	}
	wide := low
	wide.FOV = 80
	if w, n := wide.Footprint(4.0/3, 20000), fp; !(w.East-w.West > 1.5*(n.East-n.West)) {
		t.Errorf("an 80 degree lens sees %.4f degrees across, a 40 degree one %.4f; want the wider lens to see far more", w.East-w.West, n.East-n.West)
	}
}

// The map's bounds hold what the lens sees with room to spare on every side
// for the fade at the map's edge, so the fade is never in the picture.
func TestMapBoundsHoldTheFootprintAndTheFade(t *testing.T) {
	c := Camera{Target: centre, Distance: 1000, Pitch: 25, FOV: 80}
	fp, mb := c.Footprint(4.0/3, c.VisibleRange()), c.MapBounds(4.0/3)
	if !(mb.West < fp.West && mb.East > fp.East && mb.South < fp.South && mb.North > fp.North) {
		t.Fatalf("map bounds %+v do not hold the footprint %+v", mb, fp)
	}
	// The fade takes edgeFraction of the map's shorter side from each edge;
	// what is left must still hold the footprint.
	w, h := mb.East-mb.West, mb.North-mb.South
	inset := edgeFraction * math.Min(w, h)
	if fp.West < mb.West+inset*0.99 || fp.South < mb.South+inset*0.99 {
		t.Errorf("the fade reaches into the footprint")
	}
}

// A camera high over a continent near the antimeridian sees, on a flat
// earth, ground past it; the map's bounds stop at the world's edges, so a
// map can be drawn of them at all.
func TestMapBoundsStopAtTheEdgesOfTheWorld(t *testing.T) {
	for _, c := range []Camera{
		{Target: render.Coord{Lat: 50, Lon: 170}, Distance: 400_000, Pitch: 35, Heading: 90},
		{Target: render.Coord{Lat: 50, Lon: -170}, Distance: 400_000, Pitch: 35, Heading: 270},
		{Target: render.Coord{Lat: 80, Lon: 0}, Distance: 400_000, Pitch: 35, Heading: 0},
	} {
		mb := c.MapBounds(16.0 / 9)
		if mb.West < -180 || mb.East > 180 || mb.South < -mercator.MaxLatitude || mb.North > mercator.MaxLatitude {
			t.Errorf("camera at %+v heading %v: map bounds %+v run off the world", c.Target, c.Heading, mb)
		}
		if _, err := (render.View{Bounds: mb, Width: 160, Height: 90}).Resolve(); err != nil {
			t.Errorf("camera at %+v heading %v: %v", c.Target, c.Heading, err)
		}
	}
	// Away from the edges nothing is clamped.
	c := Camera{Target: centre, Distance: 1000, Pitch: 25, FOV: 80}
	if fp := c.Footprint(4.0/3, c.VisibleRange()); !(c.MapBounds(4.0/3).East > fp.East) {
		t.Error("the map's bounds were clamped far from any edge")
	}
}
