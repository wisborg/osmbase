package perspective

import (
	"context"
	"image"
	"image/color"
	"math"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// A name is shown where the ground is drawn from its zoom: the finest
// zoom's under the camera, not in the distance; the coarsest zoom's the
// other way round.
func TestNamesAreShownWhereTheirZoomIs(t *testing.T) {
	tiles, _ := flatTiles(t)
	s, cam := pyramidScene(tiles)
	pic, err := Render(s, cam, Options{Width: 400, Height: 300, HazeMetres: 1e9})
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y float64) render.Coord {
		t.Helper()
		// The ground under picture pixel (x, y): found by walking the map
		// for the point the camera places there.
		best, bestD := render.Coord{}, math.Inf(1)
		for v := 0.0; v < float64(s.View.Height); v += 4 {
			for u := 0.0; u < float64(s.View.Width); u += 4 {
				c, err := s.View.Coord(u, v)
				if err != nil {
					t.Fatal(err)
				}
				px, py, ok := pic.Project(c)
				if ok {
					if d := math.Hypot(px-x, py-y); d < bestD {
						best, bestD = c, d
					}
				}
			}
		}
		return best
	}
	underCamera, distance := at(200, 285), at(200, 40)
	if a, _, _, ok := pic.nameStrength(underCamera, 16, 6); !ok || a < 0.9 {
		t.Errorf("a zoom 16 name under the camera is shown at %.2f, want in full", a)
	}
	if a, _, _, ok := pic.nameStrength(underCamera, 13, 6); ok && a > 0 {
		t.Errorf("a zoom 13 name under the camera is shown at %.2f, want not at all", a)
	}
	if a, _, _, ok := pic.nameStrength(distance, 16, 6); ok && a > 0 {
		t.Errorf("a zoom 16 name in the distance is shown at %.2f, want not at all", a)
	}
}

// A tile keeps the names centred on its own ground and lets go of those
// placed past its edges, which its neighbours place again from their side.
func TestATileKeepsTheNamesOnItsOwnGround(t *testing.T) {
	k := tileKey{z: 14, x: 15069, y: 9831}
	w, s, e, n, err := mercator.TileBounds(k.z, k.x, k.y)
	if err != nil {
		t.Fatal(err)
	}
	inside := render.Coord{Lat: (s + n) / 2, Lon: (w + e) / 2}
	outside := render.Coord{Lat: (s + n) / 2, Lon: e + (e-w)/10}
	got := ownNames(&Tile{
		Places: []render.PointLabel{{Text: "in", At: inside}, {Text: "out", At: outside}},
		Lines:  []render.LineLabel{{Text: "out", At: outside}, {Text: "in", At: inside}},
	}, k)
	if len(got.Places) != 1 || got.Places[0].Text != "in" || len(got.Lines) != 1 || got.Lines[0].Text != "in" {
		t.Errorf("kept %+v and %+v, want only the names inside", got.Places, got.Lines)
	}
}

// A street running east reads left to right whichever way the camera looks
// along it: looking north it runs to the right as it is; looking south it
// runs to the left and is turned round.
func TestANameAlongALineReadsLeftToRight(t *testing.T) {
	for _, heading := range []float64{0, 180} {
		tiles, _ := flatTiles(t)
		cam := Camera{Target: render.Coord{Lat: -33.70, Lon: 151.10}, Distance: 800, Heading: heading, Pitch: 35}
		s := Scene{Tiles: tiles, View: cam.MapView(cam.MapBounds(4.0/3), 300, 1024), Heights: Level}
		pic, err := Render(s, cam, Options{Width: 400, Height: 300})
		if err != nil {
			t.Fatal(err)
		}
		a, ok := pic.screenAngle(cam.Target, 0)
		if !ok {
			t.Fatalf("heading %g: no direction for a street across the view", heading)
		}
		if math.Abs(math.Remainder(a, 2*math.Pi)) > 0.05 {
			t.Errorf("heading %g: an east-running street's name runs at %.1f°, want level and left to right", heading, a*180/math.Pi)
		}
	}
}

// Names a pyramid's tiles carry are drawn: a tile under the camera names
// the ground it is on, and the name's ink is in the picture there.
func TestDrawNamesStandsTheTilesNamesOnThePicture(t *testing.T) {
	cam := Camera{Target: render.Coord{Lat: -33.70, Lon: 151.10}, Distance: 800, Heading: 45, Pitch: 25}
	tiles, err := NewTiles(func(_ context.Context, z uint8, x, y uint32) (*Tile, error) {
		img := image.NewRGBA(image.Rect(0, 0, TileSize, TileSize))
		for i := range img.Pix {
			img.Pix[i] = 0xff
		}
		tile := &Tile{Image: img}
		// Every tile names the point looked at; only the one holding it
		// keeps the name.
		tile.Places = []render.PointLabel{{Text: "Here", At: cam.Target, Face: basicfont.Face7x13}}
		return tile, nil
	}, 13, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := Scene{Tiles: tiles, View: cam.MapView(cam.MapBounds(4.0/3), 300, 1024), Heights: Level}
	pic, err := Render(s, cam, Options{Width: 400, Height: 300, HazeMetres: 1e9})
	if err != nil {
		t.Fatal(err)
	}
	pal := render.Palette{Land: colorWhite, Label: colorBlack}
	pic.DrawNames(pal)
	dark := 0
	for y := 140; y < 160; y++ {
		for x := 180; x < 220; x++ {
			if c := pic.Image.RGBAAt(x, y); c.R < 0x80 {
				dark++
			}
		}
	}
	if dark == 0 {
		t.Error("no name drawn at the point looked at")
	}
}

var (
	colorWhite = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colorBlack = color.RGBA{A: 0xff}
)

// A name fades across the picture's edge: in full inside, less the further
// it runs off, a step at a time and never all at once.
func TestANameFadesAcrossThePicturesEdge(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 300)
	prev := 1.0
	for x := 10; x >= -40; x-- {
		f := edgeFadeBox(image.Rect(x, 100, x+80, 120), bounds, 30)
		if x >= 0 && f != 1 {
			t.Errorf("a name %d pixels inside the edge is shown at %.2f", x, f)
		}
		if f > prev || prev-f > 0.1 {
			t.Fatalf("at %d pixels a name crossing the edge went from %.2f to %.2f", x, prev, f)
		}
		prev = f
	}
	if prev != 0 {
		t.Errorf("a name 40 pixels past the edge of a 30-pixel ramp is shown at %.2f", prev)
	}
}

// How much one name covers another grows with their overlap, a step at a
// time; and two copies of one name cover each other the more the nearer
// they are, without overlapping at all.
func TestNamesCoverEachOtherByDegrees(t *testing.T) {
	a := image.Rect(100, 100, 200, 120)
	prev := 0.0
	for dx := 120; dx >= 0; dx-- {
		c := cover(a, a.Add(image.Pt(dx, 0)), false)
		if c < prev || c-prev > 0.1 {
			t.Fatalf("moving over by a pixel to %d, the cover went from %.2f to %.2f", dx, prev, c)
		}
		prev = c
	}
	if prev != 1 {
		t.Errorf("a name on top of another covers it %.2f", prev)
	}
	apart := a.Add(image.Pt(150, 0))
	if c := cover(a, apart, false); c != 0 {
		t.Errorf("two names apart cover each other %.2f", c)
	}
	if c := cover(a, apart, true); c <= 0 || c >= 1 {
		t.Errorf("two copies of one name a little apart cover each other %.2f, want some but not all", c)
	}
	if c := cover(a, a.Add(image.Pt(500, 0)), true); c != 0 {
		t.Errorf("two copies of one name far apart cover each other %.2f", c)
	}
}

// A name half behind a crest is shown at about half strength, not in full
// or not at all: its strength is the share of the ground round it that is
// seen.
func TestANameBehindACrestIsShownByTheShareSeen(t *testing.T) {
	tiles, _ := flatTiles(t)
	s, cam := pyramidScene(tiles)
	pic, err := Render(s, cam, Options{Width: 400, Height: 300, HazeMetres: 1e9})
	if err != nil {
		t.Fatal(err)
	}
	// A name of the zoom the ground there is drawn from.
	cx, cy, _, ok := pic.onFrame(cam.Target)
	if !ok {
		t.Fatal("the point looked at is not in the picture")
	}
	z := uint8(math.Round(float64(pic.lod[int(cy)*pic.w+int(cx)])))
	full, x, y, ok := pic.nameStrength(cam.Target, z, 6)
	if !ok {
		t.Fatal("the point looked at is not shown")
	}
	// Raise a wall of nearer ground over the top half of the samples.
	sx, sy := int(x*supersample), int(y*supersample)
	for j := sy - 20; j <= sy; j++ {
		for i := sx - 20; i <= sx+20; i++ {
			pic.depth[j*pic.w+i] = 1
		}
	}
	part, _, _, ok := pic.nameStrength(cam.Target, z, 6)
	if !ok || part <= 0 || part >= full {
		t.Errorf("half behind a crest a name is shown at %.2f (ok %v), in the open at %.2f; want part of it", part, ok, full)
	}
}
