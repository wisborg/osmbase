package main

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/slice"
)

// The fitted zoom is the deepest that holds the whole rectangle, which is two
// facts and both are asserted: it fits at z, and it does not fit at z+1. A
// zoom one too shallow passes the first alone and draws a country as a speck.
func TestFitZoomIsTheDeepestThatHoldsTheRectangle(t *testing.T) {
	for _, tc := range []struct {
		name          string
		b             slice.Bounds
		width, height int
	}{
		{"Denmark, landscape", slice.Bounds{West: 8.0, South: 54.5, East: 15.2, North: 57.8}, 1024, 768},
		{"Denmark, portrait", slice.Bounds{West: 8.0, South: 54.5, East: 15.2, North: 57.8}, 768, 1024},
		{"a suburb", slice.Bounds{West: 151.08, South: -33.72, East: 151.12, North: -33.69}, 1920, 1080},
	} {
		t.Run(tc.name, func(t *testing.T) {
			z, lat, lon, cropped, err := fitZoom(tc.b, tc.width, tc.height)
			if err != nil {
				t.Fatal(err)
			}
			if cropped {
				t.Fatal("cropped a rectangle that fits")
			}
			x0, y0 := mercator.Project(tc.b.West, tc.b.North)
			x1, y1 := mercator.Project(tc.b.East, tc.b.South)
			px := func(z int, d float64) float64 { return d * 256 * math.Exp2(float64(z)) }
			if px(z, x1-x0) > float64(tc.width) || px(z, y1-y0) > float64(tc.height) {
				t.Errorf("zoom %d does not hold the rectangle", z)
			}
			if px(z+1, x1-x0) <= float64(tc.width) && px(z+1, y1-y0) <= float64(tc.height) {
				t.Errorf("zoom %d is not the deepest: %d holds it too", z, z+1)
			}
			// Centred in the projection, not on the degree average.
			cx, cy := mercator.Project(lon, lat)
			if math.Abs(cx-(x0+x1)/2) > 1e-9 || math.Abs(cy-(y0+y1)/2) > 1e-9 {
				t.Errorf("centre %v,%v is not the middle of the projected rectangle", lat, lon)
			}
		})
	}
}

// A rectangle small enough to want more than the public builds hold is drawn
// at their deepest zoom with ground around it, not overzoomed into a smear.
func TestFitZoomStopsAtTheDeepestZoomTheBuildsHold(t *testing.T) {
	z, _, _, _, err := fitZoom(slice.Bounds{West: 10, South: 55, East: 10.0001, North: 55.0001}, 1024, 768)
	if err != nil || z != maxFitZoom {
		t.Errorf("got zoom %d (%v), want %d", z, err, maxFitZoom)
	}
}

// The whole world at 1024 by 768 fits at zoom 1, where the world is 512
// pixels wide and the image cannot be drawn. The zoom goes as deep as the
// image needs, and the report says the rectangle was cropped.
func TestFitZoomCropsWhatIsWiderThanTheWorld(t *testing.T) {
	z, lat, lon, cropped, err := fitZoom(slice.Bounds{West: -180, South: -85, East: 180, North: 85}, 1024, 768)
	if err != nil {
		t.Fatal(err)
	}
	if !cropped || z != 2 {
		t.Errorf("got zoom %d, cropped %v; want zoom 2, cropped", z, cropped)
	}
	if _, err := viewAround(uint8(z), lon, lat, 1024, 768); err != nil {
		t.Errorf("the fitted view cannot be drawn: %v", err)
	}
}

func TestFitZoomRefusesWhatIsNotARectangle(t *testing.T) {
	for _, b := range []slice.Bounds{
		{West: 15, South: 54, East: 8, North: 57},    // across the antimeridian, or reversed
		{West: 8, South: 57, East: 15, North: 54},    // upside down
		{West: -190, South: 54, East: 15, North: 57}, // off the earth
		{West: math.NaN(), South: 54, East: 15, North: 57},
	} {
		if _, _, _, _, err := fitZoom(b, 1024, 768); err == nil || !isUsageError(err) {
			t.Errorf("%+v: got %v, want a usage error", b, err)
		}
	}
}

// End to end from a local archive: the flag draws, says the zoom it chose,
// and refuses to be combined with --lat/--lon.
func TestRenderBBoxDrawsAndSaysWhatItChose(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	out := filepath.Join(t.TempDir(), "box.png")
	r := runCLI(t, "render", archive, "--bbox", "-40,-30,40,30", "--width", "512", "--height", "512", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if line, ok := lineContaining(r.stdout, "fitted"); !ok || !strings.Contains(line, "--bbox") || !strings.Contains(line, "at zoom 3") {
		t.Errorf("the report does not say the zoom was fitted:\n%s", r.stdout)
	}

	r = runCLI(t, "render", archive, "--bbox", "-40,-30,40,30", "--lat", "1", "--lon", "1", "--out", out)
	if r.code != 2 {
		t.Errorf("--bbox with --lat/--lon: exit %d, want 2\n%s", r.code, r.stderr)
	}
}
