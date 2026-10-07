package render_test

import (
	"context"
	"image"
	"math"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// hill is a HeightSource with a tile at every zoom, each sampled from one
// smooth hill on the ground: a round hill 600 m across near Hornsby's
// latitude, whose top is in tile (16, x, y) of seamTile.
type hill struct{ size int }

func (s hill) Heights(z uint8, x, y uint32) ([]float32, int, bool, error) {
	n := math.Exp2(float64(z))
	cx, cy := seamCentre()
	out := make([]float32, s.size*s.size)
	for j := 0; j < s.size; j++ {
		for i := 0; i < s.size; i++ {
			wx := (float64(x) + (float64(i)+0.5)/float64(s.size)) / n
			wy := (float64(y) + (float64(j)+0.5)/float64(s.size)) / n
			// World units to metres at this latitude: about 33.4 million.
			d := math.Hypot(wx-cx, wy-cy) * 33.4e6
			out[j*s.size+i] = float32(100 + 60*math.Exp(-d*d/(2*150*150)))
		}
	}
	return out, s.size, true, nil
}

// seamTile is a zoom 16 tile at Hornsby's latitude, where a pixel at zoom
// 16 is about two metres and the shading's blur about two and a half
// pixels: the radius a rounded blur flips at between neighbouring tiles.
func seamTile() (x, y uint32) {
	wx, wy := mercator.Project(151.095, -33.704)
	return uint32(wx * 65536), uint32(wy * 65536)
}

// seamCentre is the hill's top, in world units: on the edge between
// seamTile and the tile east of it, a third of the way down.
func seamCentre() (float64, float64) {
	x, y := seamTile()
	return float64(x+1) / 65536, (float64(y) + 1.0/3) / 65536
}

// Two neighbouring tiles drawn on their own are the one view of both cut
// in two, shading and contours included: what a flyover assembled from
// fixed tiles needs not to show its seams. Each used to read and blur the
// heights only under itself, round its blur radius from its own latitude
// and pick its own contour interval, and the contours along the edge
// between them did not meet.
func TestRelief_NeighbouringTilesAgreeAtTheirEdge(t *testing.T) {
	x, y := seamTile()
	src := newSource()
	for dx := uint32(0); dx < 2; dx++ {
		src.put(t, 16, x+dx, y, wholeTile("earth", ""))
	}
	pal := shadingPalette()
	pal.Contour = testPalette.Water
	r, err := render.New(src, render.Options{Style: testStyle(), Palette: pal, Terrain: hill{size: 256}, ContourInterval: 10})
	if err != nil {
		t.Fatal(err)
	}
	draw := func(v render.View) *image.RGBA {
		t.Helper()
		res, err := r.Render(context.Background(), v)
		if err != nil {
			t.Fatal(err)
		}
		if res.ContourInterval != 10 {
			t.Fatalf("contours every %g m, want the 10 asked for", res.ContourInterval)
		}
		return res.Image
	}
	both := draw(tileView(t, 16, x, y, x+1, y, 256))
	contour := 0
	for dx := 0; dx < 2; dx++ {
		one := draw(tileView(t, 16, x+uint32(dx), y, x+uint32(dx), y, 256))
		for py := 0; py < 256; py++ {
			for px := 0; px < 256; px++ {
				got, want := one.RGBAAt(px, py), both.RGBAAt(px+256*dx, py)
				if !near(got, want, 3) {
					t.Fatalf("tile %d pixel (%d, %d) is %v, the view of both drew %v there", dx, px, py, got, want)
				}
				if near(want, pal.Contour, 40) {
					contour++
				}
			}
		}
	}
	if contour == 0 {
		t.Fatal("no contour was drawn, so their agreement proves nothing")
	}
}
