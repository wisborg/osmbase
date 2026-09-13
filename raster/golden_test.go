package raster_test

import (
	"bytes"
	"flag"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// update rewrites the golden files instead of comparing against them:
//
//	go test ./raster/ -update
//
// Review the resulting PNGs by eye before keeping them. The measurements in
// the other files in this package say that a stroke is the width it claims and
// that a seam sums to one; what they cannot say is that the picture looks like
// a map, and that is the whole job of these.
var update = flag.Bool("update", false, "rewrite the golden PNGs in testdata")

// The scenes are the three shapes the design calls out as the ones a stroker
// is judged on -- a switchback, a multi-way junction and a dashed path -- plus
// the two cases the fill rule is chosen for: a polygon with a hole and an
// island in it, and geometry clipped at a tile edge and drawn in one pass.
//
// They are deliberately abstract. Nothing here is a place.
var (
	paper  = color.RGBA{R: 0xf5, G: 0xf2, B: 0xe9, A: 0xff}
	casing = color.RGBA{R: 0x9a, G: 0x93, B: 0x86, A: 0xff}
	road   = color.RGBA{R: 0x33, G: 0x36, B: 0x3b, A: 0xff}
	water  = color.RGBA{R: 0x6f, G: 0x93, B: 0xb0, A: 0xff}
)

const goldenSize = 128

var scenes = []struct {
	name string
	draw func(s *raster.Surface)
}{
	{
		// A trail doubling back on itself at a few degrees, which is where a
		// miter join would throw a spike many times the stroke width.
		name: "switchback",
		draw: func(s *raster.Surface) {
			trail := []raster.Point{
				{X: 12, Y: 116}, {X: 100, Y: 104}, {X: 16, Y: 92},
				{X: 104, Y: 76}, {X: 20, Y: 62}, {X: 108, Y: 44},
				{X: 24, Y: 28}, {X: 96, Y: 12},
			}
			var p raster.Path
			p.Stroke(trail, raster.Stroke{Width: 9})
			s.Fill(&p, casing)
			p.Reset()
			p.Stroke(trail, raster.Stroke{Width: 5})
			s.Fill(&p, road)
		},
	},
	{
		// Six ways meeting at a point, at six widths. Every one of them ends
		// at the junction rather than passing through it, which is how OSM
		// stores a junction and why the caps have to close over each other.
		name: "junction",
		draw: func(s *raster.Surface) {
			centre := raster.Point{X: 64, Y: 64}
			ends := []raster.Point{
				{X: 4, Y: 64}, {X: 124, Y: 60}, {X: 64, Y: 4},
				{X: 60, Y: 124}, {X: 14, Y: 14}, {X: 118, Y: 110},
			}
			var p raster.Path
			for i, e := range ends {
				p.Stroke([]raster.Point{centre, e}, raster.Stroke{Width: float32(3 + 2*i)})
			}
			s.Fill(&p, road)
		},
	},
	{
		// A dashed path round two corners, with a phase, at a width where the
		// round caps on each dash are a visible part of the pattern.
		name: "dashed",
		draw: func(s *raster.Surface) {
			path := []raster.Point{
				{X: 8, Y: 24}, {X: 96, Y: 24}, {X: 96, Y: 96}, {X: 24, Y: 96}, {X: 24, Y: 48},
			}
			var p raster.Path
			p.Stroke(path, raster.Stroke{Width: 5, Dash: []float32{11, 7}, DashPhase: 3})
			s.Fill(&p, water)
		},
	},
	{
		// A lake with a hole in it and an island in the hole. The hole is the
		// same ring wound backwards; the island is wound forwards again. All
		// three are in one path, because that is the only place the winding
		// can meet.
		name: "lake",
		draw: func(s *raster.Surface) {
			var p raster.Path
			p.Ring([]raster.Point{{X: 8, Y: 16}, {X: 120, Y: 8}, {X: 112, Y: 118}, {X: 16, Y: 110}})
			p.Ring(reversed([]raster.Point{{X: 32, Y: 36}, {X: 96, Y: 32}, {X: 92, Y: 96}, {X: 36, Y: 92}}))
			p.Ring([]raster.Point{{X: 52, Y: 56}, {X: 76, Y: 54}, {X: 74, Y: 78}, {X: 54, Y: 76}})
			s.Fill(&p, water)
		},
	},
	{
		// What two tiles meeting at x = 64.5 look like when their geometry is
		// clipped to the seam and filled in one pass: a landcover polygon in
		// halves, and a road in halves, with nothing along the join.
		name: "seam",
		draw: func(s *raster.Surface) {
			const seam = 64.5
			var p raster.Path
			p.Rect(raster.Point{X: 8, Y: 8}, raster.Point{X: seam, Y: 120})
			p.Rect(raster.Point{X: seam, Y: 8}, raster.Point{X: 116, Y: 120})
			s.Fill(&p, water)

			p.Reset()
			p.Stroke([]raster.Point{{X: 8, Y: 96}, {X: 40, Y: 40}, {X: seam, Y: 34}}, raster.Stroke{Width: 7})
			p.Stroke([]raster.Point{{X: seam, Y: 34}, {X: 92, Y: 30}, {X: 116, Y: 72}}, raster.Stroke{Width: 7})
			s.Fill(&p, road)
		},
	},
}

func renderScene(i int) *raster.Surface {
	s := raster.NewSurface(goldenSize, goldenSize)
	s.Background(paper)
	scenes[i].draw(s)
	return s
}

// TestGolden_ScenesMatchTheirRecordedPictures is the eyes-on gate. The other
// tests measure one property each; this one notices everything else.
func TestGolden_ScenesMatchTheirRecordedPictures(t *testing.T) {
	for i, scene := range scenes {
		t.Run(scene.name, func(t *testing.T) {
			got := renderScene(i).RGBA()
			path := filepath.Join("testdata", scene.name+".png")

			if *update {
				var buf bytes.Buffer
				if err := png.Encode(&buf, got); err != nil {
					t.Fatalf("encoding %s: %v", path, err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				t.Logf("wrote %s; look at it", path)
				return
			}

			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("opening the golden image: %v (run go test ./raster/ -update to create it)", err)
			}
			defer f.Close()
			want, err := png.Decode(f)
			if err != nil {
				t.Fatalf("decoding %s: %v", path, err)
			}

			// Decoded pixels rather than encoded bytes: a change in the PNG
			// encoder's filtering would rewrite every byte of the file without
			// changing a single pixel, and that is not a rendering regression.
			if got.Bounds() != want.Bounds() {
				t.Fatalf("rendered %v, golden is %v", got.Bounds(), want.Bounds())
			}
			differing, firstX, firstY := 0, -1, -1
			for y := got.Bounds().Min.Y; y < got.Bounds().Max.Y; y++ {
				for x := got.Bounds().Min.X; x < got.Bounds().Max.X; x++ {
					if got.At(x, y) != want.At(x, y) {
						if differing == 0 {
							firstX, firstY = x, y
						}
						differing++
					}
				}
			}
			if differing != 0 {
				t.Errorf("%d pixels differ from %s; the first is (%d, %d), rendered %v, golden %v",
					differing, path, firstX, firstY, got.At(firstX, firstY), want.At(firstX, firstY))
			}
		})
	}
}

// TestSurface_TheSameSceneRendersTheSameTwice is the determinism claim stated
// as a test rather than as an assumption.
//
// A map drawn into a video is drawn once and shown for a thousand frames, so
// nothing here may depend on map iteration order, on the clock, or on a
// rasterizer carrying state between fills. The last of those is the live risk:
// Surface holds one rasterizer across every fill deliberately, and a fill that
// left coverage behind would show up here as the second render differing from
// the first.
func TestSurface_TheSameSceneRendersTheSameTwice(t *testing.T) {
	for i, scene := range scenes {
		t.Run(scene.name, func(t *testing.T) {
			a, b := renderScene(i).RGBA(), renderScene(i).RGBA()
			if !bytes.Equal(a.Pix, b.Pix) {
				t.Errorf("two renders of %s differ", scene.name)
			}
		})
	}
}

// TestSurface_ReusedForASecondSceneDoesNotKeepTheFirst covers the same
// rasterizer state from the other direction: one Surface, drawn on twice, must
// end up holding the second scene and nothing of the first.
func TestSurface_ReusedForASecondSceneDoesNotKeepTheFirst(t *testing.T) {
	fresh := raster.NewSurface(goldenSize, goldenSize)
	fresh.Background(paper)
	scenes[1].draw(fresh)

	reused := raster.NewSurface(goldenSize, goldenSize)
	reused.Background(paper)
	scenes[0].draw(reused)
	reused.Background(paper)
	scenes[1].draw(reused)

	if !bytes.Equal(fresh.RGBA().Pix, reused.RGBA().Pix) {
		t.Error("a surface drawn on twice differs from a fresh one drawn on once")
	}
}
