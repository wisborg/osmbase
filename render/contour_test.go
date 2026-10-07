package render

import (
	"image"
	"image/color"
	"math"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/raster"
)

// field is a relief whose heights are h at each sample of a w by ht field.
func field(w, ht int, h func(x, y int) float64) *relief {
	rl := &relief{fw: w, fh: ht, m: 1, field: make([]float32, w*ht)}
	for y := 0; y < ht; y++ {
		for x := 0; x < w; x++ {
			rl.field[y*w+x] = float32(h(x, y))
		}
	}
	return rl
}

// A cone's contour at any height is one closed ring round its peak, at the
// distance that height is down its side.
func TestTrace_AConeGivesOneClosedRing(t *testing.T) {
	rl := field(41, 41, func(x, y int) float64 { return 100 - math.Hypot(float64(x-20), float64(y-20))*5 })
	lines := rl.trace(50) // ten samples from the peak
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1", len(lines))
	}
	l := lines[0]
	if a, b := l[0], l[len(l)-1]; math.Hypot(a.X-b.X, a.Y-b.Y) > 1e-9 {
		t.Errorf("the ring is not closed: starts %v, ends %v", a, b)
	}
	for _, p := range l {
		// Sample (20, 20) is the centre of surface pixel (19, 19), whose
		// centre is at 19.5.
		if r := math.Hypot(p.X-19.5, p.Y-19.5); math.Abs(r-10) > 0.3 {
			t.Fatalf("a point %v is %.2f from the peak, want 10", p, r)
		}
	}
}

// On a plane rising to the east, a contour is one open line from the top
// edge to the bottom, at the column where the plane passes its height.
func TestTrace_APlaneGivesOneLineAcrossIt(t *testing.T) {
	rl := field(20, 10, func(x, y int) float64 { return float64(x) * 10 })
	lines := rl.trace(55)
	if len(lines) != 1 || len(lines[0]) != 10 {
		t.Fatalf("lines %v, want one of 10 points", lines)
	}
	for _, p := range lines[0] {
		// 55 m is halfway between samples 5 and 6, whose pixels' centres
		// are at 4.5 and 5.5.
		if math.Abs(p.X-5) > 1e-9 {
			t.Fatalf("point %v, want x 5", p)
		}
	}
}

// A line stops where the heights do: unknown ground cuts it in two rather
// than being interpolated across.
func TestTrace_UnknownHeightsCutALine(t *testing.T) {
	rl := field(20, 11, func(x, y int) float64 {
		if y == 5 {
			return math.NaN()
		}
		return float64(x) * 10
	})
	if lines := rl.trace(55); len(lines) != 2 {
		t.Errorf("%d lines, want 2, one either side of the unknown row", len(lines))
	}
}

// The interval is the zoom's, widened where the steepest ground would crowd
// its lines closer than minContourGap; and nothing is drawn below
// contourMinZoom.
func TestInterval_ByZoomAndSteepness(t *testing.T) {
	gentle := field(50, 50, func(x, y int) float64 { return float64(x) * 0.5 }) // half a metre a pixel
	steep := field(50, 50, func(x, y int) float64 { return float64(x) * 6 })    // six metres a pixel
	for _, c := range []struct {
		rl   *relief
		z    float64
		want float64
	}{
		{gentle, 14, 10}, {gentle, 16.5, 5}, {gentle, 12, 50},
		{steep, 14, 50}, // 6 m a pixel, four pixels apart: 24, so the next step is 50
	} {
		got, ok := c.rl.interval(c.z)
		if !ok || got != c.want {
			t.Errorf("zoom %v: %v, %v; want %v", c.z, got, ok, c.want)
		}
	}
	if _, ok := gentle.interval(10.9); ok {
		t.Error("contours at zoom 10.9, shallower than contourMinZoom")
	}
}

// Every fifth line is an index line; sea level is left to the coastline;
// lines shorter than minContourLength are dropped.
func TestContours_IndexLinesSeaLevelAndShortLines(t *testing.T) {
	rl := field(80, 40, func(x, y int) float64 { return float64(x)*2 - 20 }) // -20 to 138
	lines := rl.contours(10)
	seen := map[float64]bool{}
	for _, l := range lines {
		seen[l.height] = true
		if want := math.Mod(l.height, 50) == 0; l.index != want {
			t.Errorf("height %v: index %v", l.height, l.index)
		}
	}
	if seen[0] {
		t.Error("a contour at sea level")
	}
	if !seen[-10] || !seen[50] || !seen[130] {
		t.Errorf("heights %v, want -10, 50 and 130 among them", seen)
	}
	tiny := field(40, 40, func(x, y int) float64 { return math.Max(1, 10-math.Hypot(float64(x-20), float64(y-20))) })
	if got := tiny.contours(5); len(got) != 1 {
		t.Errorf("%d lines round a small hill, want only the ring at 5 m (radius 5, 31 pixels round), not the dot at its top", len(got))
	}
}

// A contour's height is placed as a label, and the lines are cut under it.
func TestDrawContours_CutsTheLineUnderItsHeight(t *testing.T) {
	rl := field(402, 102, func(x, y int) float64 { return float64(y) * 1 }) // rises to the south
	lines := rl.contours(10)
	cands := contourLabels(lines, basicfont.Face7x13, 0)
	if len(cands) == 0 {
		t.Fatal("no heights to place")
	}
	labels := placeLabels(cands, 4, image.Rect(0, 0, 400, 100))
	if len(labels) == 0 {
		t.Fatal("no heights placed")
	}
	d := drawer{p: projection{width: 400, height: 100}, palette: Palette{Contour: color.RGBA{R: 0xff, A: 0xff}}}
	s := raster.NewSurface(400, 100)
	s.Background(color.RGBA{A: 0xff})
	d.drawContours(s, lines, labels)
	l := labels[0]
	if got := s.RGBA().RGBAAt(int(l.x), int(l.y)); got.R != 0 {
		t.Errorf("the line is drawn through its height at %v,%v: %v", l.x, l.y, got)
	}
	// Away from the label the same line is drawn.
	if got := s.RGBA().RGBAAt(int(l.x)+150, int(l.y)); got.R == 0 && int(l.x)+150 < 400 {
		t.Errorf("no line beside the label at %v,%v", int(l.x)+150, int(l.y))
	}
}

// Contour heights are turned for the viewer as street names are: the same
// level line labelled for a flat map's reader and for one looking south
// reads in opposite directions.
func TestContourLabelsReadUprightForTheViewerFacing(t *testing.T) {
	var pts []pt
	for x := 0.0; x <= 400; x += 10 {
		pts = append(pts, pt{X: x, Y: 100})
	}
	lines := []contourLine{{pts: pts, height: 200, index: true}}
	north, south := contourLabels(lines, basicfont.Face7x13, 0), contourLabels(lines, basicfont.Face7x13, math.Pi)
	if len(north) == 0 || len(south) == 0 {
		t.Fatalf("labels: %d facing north, %d facing south", len(north), len(south))
	}
	if math.Abs(north[0].angle) > 1e-6 || math.Abs(math.Abs(south[0].angle)-math.Pi) > 1e-6 {
		t.Errorf("angles %.3f facing north and %.3f facing south; want 0 and ±π", north[0].angle, south[0].angle)
	}
}
