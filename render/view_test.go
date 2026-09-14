package render

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mercator"
)

// tileBounds is the geographic rectangle of one tile, for a test that wants a
// view whose scale it can predict exactly.
func tileBounds(t *testing.T, z uint8, x, y uint32) Bounds {
	t.Helper()
	w, s, e, n, err := mercator.TileBounds(z, x, y)
	if err != nil {
		t.Fatalf("mercator.TileBounds(%d, %d, %d): %v", z, x, y, err)
	}
	return Bounds{West: w, South: s, East: e, North: n}
}

// TestResolve_OneTileShownAtTileSizeIsExactlyThatZoom is the anchor for every
// scale in this package.
//
// One tile at zoom z covers 1/2^z of the world on each axis, and the whole
// world at zoom z is 256*2^z pixels across. So showing exactly one tile on
// exactly 256 pixels is a scale of 256*2^z pixels per world unit, a continuous
// zoom of log2(scale/256) = z on the nose, and one surface pixel per tile
// pixel. Every stroke width in every style is multiplied by that last number,
// which is why it is worth pinning at more than one zoom rather than trusting
// one.
func TestResolve_OneTileShownAtTileSizeIsExactlyThatZoom(t *testing.T) {
	for _, z := range []uint8{0, 1, 5, 12, 15} {
		t.Run(fmt.Sprintf("zoom%d", z), func(t *testing.T) {
			n := uint32(1) << z
			p, err := resolve(View{Bounds: tileBounds(t, z, n/2, n/2), Width: tileSize, Height: tileSize})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if p.tileZoom != z {
				t.Errorf("tileZoom = %d, want %d", p.tileZoom, z)
			}
			if math.Abs(p.zoom-float64(z)) > 1e-9 {
				t.Errorf("continuous zoom = %.12g, want %d", p.zoom, z)
			}
			if math.Abs(p.tileScale-1) > 1e-9 {
				t.Errorf("tileScale = %.12g, want 1; every style width is multiplied by this", p.tileScale)
			}
		})
	}
}

// TestResolve_TheImageCoversTheBoundsOnOneScale pins the aspect-ratio decision.
//
// Degrees and pixels rarely agree, and the three answers are to stretch, to
// show less than was asked for, or to show more. Stretching destroys the one
// property Web Mercator is chosen for -- a circle on the ground is a circle on
// the screen -- so the two scales have to be equal, and showing less would
// answer a question nobody asked, so the requested rectangle has to be inside
// what came back.
//
// The view here is one zoom-4 tile asked for on a 400 by 100 image. The tile is
// 1/16 of the world on each axis, so the scales that would fit it are 6400 and
// 1600 pixels per world unit; covering takes the larger, and at 6400 the image
// is 400/6400 = 1/16 of the world wide -- the tile exactly -- and 100/6400 =
// 1/64 tall, a quarter of the tile, centred.
func TestResolve_TheImageCoversTheBoundsOnOneScale(t *testing.T) {
	b := tileBounds(t, 4, 8, 8)
	p, err := resolve(View{Bounds: b, Width: 400, Height: 100})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if want := 400 * 16.0; math.Abs(p.scale-want) > 1e-6 {
		t.Errorf("scale = %g, want %g", p.scale, want)
	}

	// The requested rectangle has to be on the image. West and east land on the
	// edges; north and south fall outside it, which is the "more, not less"
	// half of the rule.
	x0, y0 := mercator.Project(b.West, b.North)
	x1, y1 := mercator.Project(b.East, b.South)
	const eps = 1e-9
	if got := p.pixelX(x0); math.Abs(got) > eps {
		t.Errorf("the west edge is at pixel %g, want 0", got)
	}
	if got := p.pixelX(x1); math.Abs(got-400) > eps {
		t.Errorf("the east edge is at pixel %g, want 400", got)
	}
	if got := p.pixelY(y0); got > 0 {
		t.Errorf("the north edge is at pixel %g, want 0 or above the image; the image must not show less than was asked for", got)
	}
	if got := p.pixelY(y1); got < 100 {
		t.Errorf("the south edge is at pixel %g, want 100 or below the image", got)
	}
}

// TestResolve_TileScaleTracksTheImageSizeWithinAZoom is the property that stops
// a style width being an output-pixel constant.
//
// Hold the ground fixed and make the image 1.3 times as wide and tall: there
// are 1.3 times as many pixels for the same metres, so every stroke has to come
// out 1.3 times as thick or the map is thinner at the larger size. Both sizes
// here resolve to the same tile zoom -- 1.3 is well inside the factor of two a
// zoom step spans -- so the whole difference lands in tileScale, which is the
// number widths are multiplied by.
func TestResolve_TileScaleTracksTheImageSizeWithinAZoom(t *testing.T) {
	b := tileBounds(t, 12, 2048, 2048)

	small, err := resolve(View{Bounds: b, Width: 256, Height: 256})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	large, err := resolve(View{Bounds: b, Width: 333, Height: 333})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if small.tileZoom != large.tileZoom {
		t.Fatalf("the two sizes resolved to zooms %d and %d; this test is about a change within one zoom", small.tileZoom, large.tileZoom)
	}
	want := 333 / 256.0
	if got := large.tileScale / small.tileScale; math.Abs(got-want) > 1e-9 {
		t.Errorf("tileScale grew by %.6g when the image grew by %.6g; a width multiplied by it would be an output-pixel constant", got, want)
	}
}

// TestResolve_RefusesWhatItCannotDraw checks that each refusal names its own
// cause, because every one of these is something a caller typed.
func TestResolve_RefusesWhatItCannotDraw(t *testing.T) {
	ok := Bounds{West: 10, South: 50, East: 11, North: 51}
	for _, tc := range []struct {
		name string
		view View
		want string
	}{
		{"no pixels", View{Bounds: ok, Width: 0, Height: 100}, "no pixels"},
		{"negative height", View{Bounds: ok, Width: 100, Height: -1}, "no pixels"},
		{"east of west", View{Bounds: Bounds{West: 11, South: 50, East: 10, North: 51}, Width: 10, Height: 10}, "antimeridian"},
		{"north of south", View{Bounds: Bounds{West: 10, South: 51, East: 11, North: 50}, Width: 10, Height: 10}, "north edge"},
		{"longitude out of range", View{Bounds: Bounds{West: 10, South: 50, East: 181, North: 51}, Width: 10, Height: 10}, "east edge is 181"},
		{"not a coordinate", View{Bounds: Bounds{West: math.NaN(), South: 50, East: 11, North: 51}, Width: 10, Height: 10}, "not a coordinate"},
		{"both latitudes past the cut", View{Bounds: Bounds{West: 10, South: 86, East: 11, North: 89}, Width: 10, Height: 10}, "no height"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolve(tc.view)
			if err == nil {
				t.Fatalf("resolve accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error is %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestTileRange_CoversTheSurfaceAndNothingBeyondIt checks the arithmetic that
// decides how many tiles a render asks for.
//
// A view showing exactly four tiles must ask for four. Asking for a fifth costs
// a lookup, and over a remote archive a round trip, for a tile covering no
// pixels; asking for three leaves a quarter of the image hatched as though
// there were no data.
func TestTileRange_CoversTheSurfaceAndNothingBeyondIt(t *testing.T) {
	// Two tiles across and two down at zoom 6, on 512 by 512 pixels.
	topLeft := tileBounds(t, 6, 20, 20)
	bottomRight := tileBounds(t, 6, 21, 21)
	p, err := resolve(View{
		Bounds: Bounds{West: topLeft.West, North: topLeft.North, East: bottomRight.East, South: bottomRight.South},
		Width:  512, Height: 512,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.tileZoom != 6 {
		t.Fatalf("tileZoom = %d, want 6", p.tileZoom)
	}
	x0, y0, x1, y1 := p.tileRange(6)
	if x0 != 20 || x1 != 21 || y0 != 20 || y1 != 21 {
		t.Errorf("tileRange = x %d..%d, y %d..%d, want 20..21 in both", x0, x1, y0, y1)
	}
}

// TestTileRange_ClampsToTheGridRatherThanWrapping covers a view that runs off
// the top of the world, which a window over the Arctic legitimately does. The
// ground beyond the edge has no tile because there is no ground; wrapping would
// draw Antarctica there.
func TestTileRange_ClampsToTheGridRatherThanWrapping(t *testing.T) {
	p, err := resolve(View{Bounds: Bounds{West: -10, South: 84, East: 10, North: 85}, Width: 512, Height: 512})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	x0, y0, x1, y1 := p.tileRange(p.tileZoom)
	last := uint32(1)<<p.tileZoom - 1
	if x1 > last || y1 > last || x0 > x1 || y0 > y1 {
		t.Errorf("tileRange at zoom %d gave x %d..%d, y %d..%d, outside a grid of %d", p.tileZoom, x0, x1, y0, y1, last+1)
	}
}
