package perspective

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"sync"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// zoomColours paints each zoom's tiles one flat colour of its own, so a
// picture says which zoom every part of it was drawn from.
var zoomColours = map[uint8]color.RGBA{
	13: {R: 0xff, A: 0xff},
	14: {G: 0xff, A: 0xff},
	15: {B: 0xff, A: 0xff},
	16: {R: 0xff, G: 0xff, A: 0xff},
}

func flatTiles(t *testing.T) (*Tiles, *sync.Map) {
	t.Helper()
	var seen sync.Map // tileKey -> number of times drawn
	draw := func(_ context.Context, z uint8, x, y uint32) (*Tile, error) {
		n, _ := seen.LoadOrStore(tileKey{z, x, y}, new(int))
		*n.(*int)++
		img := image.NewRGBA(image.Rect(0, 0, TileSize, TileSize))
		c := zoomColours[z]
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
		}
		return &Tile{Image: img}, nil
	}
	tiles, err := NewTiles(draw, 13, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	return tiles, &seen
}

// pyramidScene is level ground under a camera looking north-east from 800 m,
// 25° down: near ground under the bottom of the picture, far ground under
// the top.
func pyramidScene(tiles *Tiles) (Scene, Camera) {
	cam := Camera{Target: render.Coord{Lat: -33.70, Lon: 151.10}, Distance: 800, Heading: 45, Pitch: 25}
	b := cam.MapBounds(4.0 / 3)
	return Scene{Tiles: tiles, View: cam.MapView(b, 300, 1024), Heights: Level}, cam
}

// dominant is the zoom whose colour most of the pixels of a row are
// nearest.
func dominant(img *image.RGBA, y int) uint8 {
	count := map[uint8]int{}
	for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
		c := img.RGBAAt(x, y)
		best, bestD := uint8(0), 1<<30
		for z, zc := range zoomColours {
			d := abs(int(c.R)-int(zc.R)) + abs(int(c.G)-int(zc.G)) + abs(int(c.B)-int(zc.B))
			if d < bestD {
				best, bestD = z, d
			}
		}
		count[best]++
	}
	best, n := uint8(0), -1
	for z, c := range count {
		if c > n {
			best, n = z, c
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Each part of the picture is drawn from the zoom its pixels need: the
// finest under the camera, coarser with distance. The haze is turned off
// so the tiles' own colours show.
func TestPyramidDrawsNearGroundFineAndFarGroundCoarse(t *testing.T) {
	tiles, _ := flatTiles(t)
	s, cam := pyramidScene(tiles)
	pic, err := Render(s, cam, Options{Width: 400, Height: 300, HazeMetres: 1e9})
	if err != nil {
		t.Fatal(err)
	}
	near, middle := dominant(pic.Image, 295), dominant(pic.Image, 150)
	if near != 16 {
		t.Errorf("the ground under the camera is drawn from zoom %d, want the finest, 16", near)
	}
	if middle >= near {
		t.Errorf("the ground looked at is drawn from zoom %d, want coarser than the %d under the camera", middle, near)
	}
	// Further still, toward the horizon, coarser again or the same.
	var far uint8
	for y := 100; y > 0; y-- {
		if c := pic.Image.RGBAAt(200, y); c != (color.RGBA{}) {
			far = dominant(pic.Image, y)
			break
		}
	}
	if far > middle {
		t.Errorf("ground beyond the point looked at is drawn from zoom %d, finer than the %d at it", far, middle)
	}
}

// A tile is drawn once, however many frames show it and however many are
// drawn at once: a moving camera's frames look at the same ground, and
// drawing it again is the cost a pyramid exists to save -- and a chance
// for it to come out differently.
func TestPyramidDrawsEachTileOnce(t *testing.T) {
	tiles, seen := flatTiles(t)
	s, cam := pyramidScene(tiles)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Render(s, cam, Options{Width: 200, Height: 150}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	first := tiles.Drawn()
	if _, err := Render(s, cam, Options{Width: 200, Height: 150}); err != nil {
		t.Fatal(err)
	}
	if tiles.Drawn() != first {
		t.Errorf("drawing the same frame again drew %d more tiles", tiles.Drawn()-first)
	}
	seen.Range(func(k, n any) bool {
		if *n.(*int) != 1 {
			t.Errorf("tile %v was drawn %d times", k, *n.(*int))
		}
		return true
	})
}

// A tile that cannot be drawn fails the frame, rather than leaving a hole
// in the ground the colour of nothing.
func TestPyramidReportsATileItCouldNotDraw(t *testing.T) {
	broken := errors.New("no map here")
	tiles, err := NewTiles(func(context.Context, uint8, uint32, uint32) (*Tile, error) { return nil, broken }, 13, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, cam := pyramidScene(tiles)
	if _, err := Render(s, cam, Options{Width: 100, Height: 75}); !errors.Is(err, broken) {
		t.Errorf("got %v, want the tile's own error", err)
	}
}

// Every vertex of a lattice's view is on the lattice, wherever the view is:
// two frames a few metres apart read the ground's height at the same points.
func TestLatticeViewsPutVerticesOnTheLattice(t *testing.T) {
	l, err := NewLattice(-33.70, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, shift := range []float64{0, 0.00003, 0.00011, 0.0007} {
		b := render.Bounds{West: 151.10 + shift, East: 151.13 + shift, South: -33.72 + shift/2, North: -33.69 + shift/2}
		v, err := l.View(b, 4)
		if err != nil {
			t.Fatal(err)
		}
		if v.Bounds.West > b.West || v.Bounds.East < b.East || v.Bounds.South > b.South || v.Bounds.North < b.North {
			t.Errorf("shift %g: the view %+v does not hold the ground asked for, %+v", shift, v.Bounds, b)
		}
		grid := render.View{Bounds: v.Bounds, Width: v.Width / 4, Height: v.Height / 4}
		for _, ij := range [][2]int{{0, 0}, {grid.Width - 1, grid.Height - 1}, {grid.Width / 2, grid.Height / 3}} {
			c, err := grid.Coord(float64(ij[0])+0.5, float64(ij[1])+0.5)
			if err != nil {
				t.Fatal(err)
			}
			x, y := mercator.Project(c.Lon, c.Lat)
			for _, f := range []float64{x / l.cell, y / l.cell} {
				if math.Abs(f-math.Round(f)) > 1e-6 {
					t.Errorf("shift %g: vertex %v is %.6f cells along, off the lattice", shift, ij, f)
				}
			}
		}
	}
}

// The point looked at rises with the ground under it smoothly as it moves,
// not in steps of a mesh cell: a camera moving along a slope used to take
// its height from the nearest vertex and jumped up and down a cell's rise
// at a time.
func TestTheTargetRisesWithTheGroundSmoothly(t *testing.T) {
	centre := render.Coord{Lat: -33.70, Lon: 151.10}
	slope := shape{centre: centre, h: func(east, _ float64) float64 { return 100 + 0.2*east }}
	l, err := NewLattice(centre.Lat, 15)
	if err != nil {
		t.Fatal(err)
	}
	prev := math.NaN()
	for i := 0; i <= 60; i++ {
		target := render.Coord{Lat: centre.Lat, Lon: centre.Lon + float64(i)/(111_320*math.Cos(centre.Lat*math.Pi/180))}
		cam := Camera{Target: target, Distance: 800, Heading: 90, Pitch: 35}
		v, err := l.View(cam.MapBounds(16.0/9), 4)
		if err != nil {
			t.Fatal(err)
		}
		m, err := buildMesh(Scene{View: v, Heights: slope, Step: 4}, target)
		if err != nil {
			t.Fatal(err)
		}
		if want := 100 + 0.2*float64(i); math.Abs(m.targetZ-want) > 0.5 {
			t.Errorf("%d m east the target is at %.2f m, the ground at %.2f", i, m.targetZ, want)
		}
		if !math.IsNaN(prev) && math.Abs(m.targetZ-prev) > 0.5 {
			t.Errorf("a metre east the target rose %.2f m, on a slope rising 0.2", m.targetZ-prev)
		}
		prev = m.targetZ
	}
}

// A camera given the height of the point it looks at uses it, whatever the
// ground under it: the height a moving camera's path has smoothed.
func TestACameraGivenItsTargetHeightUsesIt(t *testing.T) {
	centre := render.Coord{Lat: -33.70, Lon: 151.10}
	slope := shape{centre: centre, h: func(east, _ float64) float64 { return 100 + 0.2*east }}
	cam := Camera{Target: centre, Distance: 800, Heading: 90, Pitch: 35, TargetHeight: 250, HasTargetHeight: true}
	v := cam.MapView(cam.MapBounds(16.0/9), 300, 1024)
	tiles, _ := flatTiles(t)
	pic, err := Render(Scene{Tiles: tiles, View: v, Heights: slope, Exaggeration: 2}, cam, Options{Width: 160, Height: 90})
	if err != nil {
		t.Fatal(err)
	}
	// The eye is the given height's distance up the line of sight: the
	// height exaggerated as the ground is.
	if got, want := pic.cam.eye[2], 500+800*math.Sin(35*math.Pi/180); math.Abs(got-want) > 1e-6 {
		t.Errorf("the eye is %.2f m up, want %.2f", got, want)
	}
}
