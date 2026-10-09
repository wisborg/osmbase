package perspective

import (
	"context"
	"image"
	"math"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/render"
)

// A name shown for one frame only is spread over the window and never
// jumps by more than one frame's share of it.
func TestSmoothedSpreadsAOneFrameBlip(t *testing.T) {
	out := smoothed([]float32{0, 0, 1, 0, 0}, 3)
	if len(out) != 5+6 {
		t.Fatalf("%d values, want the 5 and 3 either side", len(out))
	}
	prev := float32(0)
	for i, a := range out {
		if d := math.Abs(float64(a - prev)); d > 1.0/7+1e-6 {
			t.Errorf("value %d steps %.3f from the one before, want at most a seventh", i, d)
		}
		prev = a
	}
	if totalChange(out) >= totalChange([]float32{0, 0, 1, 0, 0}) {
		t.Error("smoothing did not reduce the change")
	}
}

// A picture drawn small to plan a frame's names takes the zoom the full
// frame takes at each place: each of its pixels spans four of the frame's,
// and its zooms are shifted by the two levels that is.
func TestAPlanPictureTakesTheFramesZooms(t *testing.T) {
	tiles, _ := flatTiles(t)
	s, cam := pyramidScene(tiles)
	full, err := Render(s, cam, Options{Width: 400, Height: 300})
	if err != nil {
		t.Fatal(err)
	}
	small, err := renderScene(context.Background(), s, cam, Options{Width: 100, Height: 75}, &planning{scale: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range [][2]float64{{0.5, 0.9}, {0.5, 0.6}, {0.3, 0.4}} {
		fx, fy := int(at[0]*float64(full.w)), int(at[1]*float64(full.h))
		sx, sy := int(at[0]*float64(small.w)), int(at[1]*float64(small.h))
		f, s := float64(full.lod[fy*full.w+fx]), float64(small.lod[sy*small.w+sx])
		if math.IsNaN(f) || math.IsNaN(s) || math.Abs(f-s) > 0.35 {
			t.Errorf("at %v the frame is drawn from zoom %.2f, its plan from %.2f", at, f, s)
		}
	}
	if len(small.used) == 0 {
		t.Error("the plan knows no tiles, so no names")
	}
}

// Planned over a flight, every name's strength changes by at most one
// frame's share of the window from one frame to the next, the plan says
// the names flicker less than they would have, and a planned frame draws
// them.
func TestPlannedNamesChangeNoFasterThanTheWindow(t *testing.T) {
	tiles, err := NewTiles(func(_ context.Context, z uint8, x, y uint32) (*Tile, error) {
		img := image.NewRGBA(image.Rect(0, 0, TileSize, TileSize))
		for i := range img.Pix {
			img.Pix[i] = 0xff
		}
		tile := &Tile{Image: img}
		// A place in the middle of every tile.
		n := math.Exp2(float64(z))
		lon := (float64(x)+0.5)/n*360 - 180
		lat := math.Atan(math.Sinh(math.Pi*(1-2*(float64(y)+0.5)/n))) * 180 / math.Pi
		tile.Places = []render.PointLabel{{Text: "Here", At: render.Coord{Lat: lat, Lon: lon}, Face: basicfont.Face7x13}}
		return tile, nil
	}, 13, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	const frames, smooth = 40, 4
	cams := make([]Camera, frames)
	for i := range cams {
		cams[i] = Camera{Target: render.Coord{Lat: -33.70 + float64(i)*0.0002, Lon: 151.10}, Distance: 800, Heading: 0, Pitch: 30}
	}
	frame := func(i int) (Scene, Camera, Options, error) {
		c := cams[i]
		return Scene{Tiles: tiles, View: c.MapView(c.MapBounds(4.0/3), 300, 1024), Heights: Level}, c, Options{Width: 400, Height: 300, HazeMetres: 1e9}, nil
	}
	plan, err := PlanNames(context.Background(), frames, frame, smooth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.order) == 0 {
		t.Fatal("no names planned")
	}
	for _, id := range plan.order {
		prev := 0.0
		for i := -smooth - 1; i <= frames+smooth; i++ {
			a := plan.strength(id, i)
			if math.Abs(a-prev) > 1.0/(2*smooth+1)+1e-6 {
				t.Fatalf("name %v steps from %.3f to %.3f at frame %d", id, prev, a, i)
			}
			prev = a
		}
	}
	if before, after := plan.Steadiness(); !(after < before) {
		t.Errorf("flicker %.4f a frame before smoothing, %.4f after", before, after)
	}
	s, c, o, _ := frame(frames / 2)
	pic, err := Render(s, c, o)
	if err != nil {
		t.Fatal(err)
	}
	clean := image.NewRGBA(pic.Image.Rect)
	copy(clean.Pix, pic.Image.Pix)
	pic.DrawNamesPlanned(render.Palette{Land: colorWhite, Label: colorBlack}, plan, frames/2)
	changed := 0
	for i := range clean.Pix {
		if clean.Pix[i] != pic.Image.Pix[i] {
			changed++
		}
	}
	if changed == 0 {
		t.Error("a planned frame drew no names")
	}
}
