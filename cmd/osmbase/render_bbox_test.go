package main

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// projected is a rectangle in world units.
func projected(b render.Bounds) (x0, y0, x1, y1 float64) {
	x0, y0 = mercator.Project(b.West, b.North)
	x1, y1 = mercator.Project(b.East, b.South)
	return
}

// A fitted view holds the rectangle with the margin, fills the image along
// one axis -- at a continuous zoom, so there is no whole-zoom slack left over
// -- and is centred on the rectangle in the projection.
func TestFitViewHoldsTheRectangleClosely(t *testing.T) {
	for _, tc := range []struct {
		name          string
		b             slice.Bounds
		width, height int
	}{
		{"Denmark, landscape", slice.Bounds{West: 8.1, South: 54.6, East: 15.2, North: 57.8}, 1024, 768},
		{"Denmark, portrait", slice.Bounds{West: 8.1, South: 54.6, East: 15.2, North: 57.8}, 768, 1024},
		{"New South Wales", slice.Bounds{West: 141.0, South: -37.5, East: 153.6, North: -28.1}, 1024, 768},
		{"Australia", slice.Bounds{West: 112.9, South: -43.6, East: 153.6, North: -9.2}, 1024, 768},
		// Shallower than zoom 15, so the cap does not apply; a suburb would
		// hit it, and TestFitViewStopsAtTheDeepestZoomTheBuildsHold has that.
		{"a city", slice.Bounds{West: 150.9, South: -34.0, East: 151.4, North: -33.6}, 1920, 1080},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, cropped := fitView(tc.b, tc.width, tc.height)
			if cropped {
				t.Fatal("cropped a rectangle that fits")
			}
			bx0, by0, bx1, by1 := projected(render.Bounds{West: tc.b.West, South: tc.b.South, East: tc.b.East, North: tc.b.North})
			vx0, vy0, vx1, vy1 := projected(v.Bounds)
			const eps = 1e-9
			if vx0 > bx0+eps || vy0 > by0+eps || vx1 < bx1-eps || vy1 < by1-eps {
				t.Fatalf("the view %+v does not hold the rectangle", v.Bounds)
			}
			// Tight along one axis: the rectangle and its margin fill it.
			fx := (bx1 - bx0) * (1 + 2*fitMargin) / (vx1 - vx0)
			fy := (by1 - by0) * (1 + 2*fitMargin) / (vy1 - vy0)
			if math.Abs(math.Max(fx, fy)-1) > 1e-6 {
				t.Errorf("the rectangle fills %.3f by %.3f of the view; one of them should be 1", fx, fy)
			}
			// The image's own aspect ratio, so the renderer extends nothing.
			if a := (vx1 - vx0) / (vy1 - vy0); math.Abs(a-float64(tc.width)/float64(tc.height)) > 1e-6 {
				t.Errorf("the view's aspect is %.4f, the image's %.4f", a, float64(tc.width)/float64(tc.height))
			}
			if math.Abs((vx0+vx1)/2-(bx0+bx1)/2) > eps || math.Abs((vy0+vy1)/2-(by0+by1)/2) > eps {
				t.Error("the view is not centred on the rectangle in the projection")
			}
		})
	}
}

// A rectangle small enough to want more than the public builds hold is drawn
// at their deepest zoom with ground around it, not overzoomed into a smear.
func TestFitViewStopsAtTheDeepestZoomTheBuildsHold(t *testing.T) {
	v, _ := fitView(slice.Bounds{West: 10, South: 55, East: 10.0001, North: 55.0001}, 1024, 768)
	if _, cont, err := v.Zoom(); err != nil || math.Abs(cont-maxFitZoom) > 1e-9 {
		t.Errorf("continuous zoom %v (%v), want %d", cont, err, maxFitZoom)
	}
}

// The whole world at 1024 by 768 fits at zoom 1.3, where the world is
// narrower than the image. The view goes as deep as the image needs, says it
// cropped, and can be drawn.
func TestFitViewCropsWhatIsWiderThanTheWorld(t *testing.T) {
	v, cropped := fitView(slice.Bounds{West: -180, South: -85, East: 180, North: 85}, 1024, 768)
	if !cropped {
		t.Error("the whole world was not reported cropped")
	}
	if v.Bounds.West < -180 || v.Bounds.East > 180 {
		t.Errorf("the view %+v runs off the world", v.Bounds)
	}
	if _, _, err := v.Zoom(); err != nil {
		t.Errorf("the view cannot be resolved: %v", err)
	}
}

// Near the antimeridian the view is slid back inside the world rather than
// run over the seam, which cannot be drawn. It still holds the rectangle.
func TestFitViewStaysInsideTheWorldAtTheSeam(t *testing.T) {
	fiji := slice.Bounds{West: 177.1, South: -19.2, East: 180, North: -16.1}
	v, _ := fitView(fiji, 1024, 768)
	if v.Bounds.East > 180 {
		t.Fatalf("the view runs over the antimeridian: east %v", v.Bounds.East)
	}
	if v.Bounds.West > fiji.West || v.Bounds.East < fiji.East {
		t.Errorf("sliding it back lost the rectangle: %+v", v.Bounds)
	}
}

// --lat/--lon at a zoom whose view runs over the seam without being wider
// than the world gets the true reason. It was told the image was wider than
// the world, at a zoom where the world is four times wider than the image.
func TestViewAroundSaysTheAntimeridianWhenThatIsTheReason(t *testing.T) {
	_, err := viewAround(4, 158, -27, 1024, 768)
	if err == nil || !strings.Contains(err.Error(), "longitude 180") || strings.Contains(err.Error(), "wider than the whole world") {
		t.Errorf("got %v, want the antimeridian named", err)
	}
	_, err = viewAround(1, 0, 0, 1024, 768)
	if err == nil || !strings.Contains(err.Error(), "wider than the whole world") {
		t.Errorf("got %v, want wider than the whole world", err)
	}
}

func TestCheckRectangleRefusesWhatIsNotARectangle(t *testing.T) {
	for _, b := range []slice.Bounds{
		{West: 15, South: 54, East: 8, North: 57},
		{West: 8, South: 57, East: 15, North: 54},
		{West: -190, South: 54, East: 15, North: 57},
		{West: math.NaN(), South: 54, East: 15, North: 57},
	} {
		if err := checkRectangle(b); err == nil || !isUsageError(err) {
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
	if line, ok := lineContaining(r.stdout, "fitted"); !ok || !strings.Contains(line, "--bbox") || !strings.Contains(line, "to fit the image") {
		t.Errorf("the report does not say the zoom was fitted:\n%s", r.stdout)
	}

	r = runCLI(t, "render", archive, "--bbox", "-40,-30,40,30", "--lat", "1", "--lon", "1", "--out", out)
	if r.code != 2 {
		t.Errorf("--bbox with --lat/--lon: exit %d, want 2\n%s", r.code, r.stderr)
	}
}
