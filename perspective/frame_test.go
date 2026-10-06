package perspective

import (
	"math"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// where is the point c in the picture the camera takes on level ground, as
// fractions of the half-width and half-height inside the margin: inside the
// framed area when both are within [-1, 1].
func where(cam Camera, aspect float64, c render.Coord) (x, y float64, ok bool) {
	const circumference = 40075016.686
	scale := circumference * math.Cos(cam.Target.Lat*math.Pi/180)
	tx, ty := mercator.Project(cam.Target.Lon, cam.Target.Lat)
	px, py := mercator.Project(c.Lon, c.Lat)
	fov := cam.FOV
	if fov == 0 {
		fov = DefaultFOV
	}
	p := newView(cam, 0, fov, 2, 2).toCamera((px-tx)*scale, -(py-ty)*scale, 0)
	tv := math.Tan(fov*math.Pi/360) * (1 - 2*FrameMargin)
	return p[0] / p[2] / (tv * aspect), p[1] / p[2] / tv, p[2] > 0
}

// An L of points -- long to the north, short to the east, so no heading sees
// it symmetrically -- framed from every side and pitch: every point is in
// the picture, the camera is no further back than it has to be, and the
// points sit as far from the top as from the bottom.
func TestFrameTakesInEveryPoint(t *testing.T) {
	var pts []render.Coord
	for i := 0; i <= 20; i++ {
		pts = append(pts, render.Coord{Lat: -33.70 + 0.002*float64(i), Lon: 151.10})
	}
	for i := 1; i <= 8; i++ {
		pts = append(pts, render.Coord{Lat: -33.70, Lon: 151.10 + 0.002*float64(i)})
	}
	for _, aspect := range []float64{16.0 / 9, 1, 9.0 / 16} {
		for _, heading := range []float64{0, 45, 120, 180, 270} {
			for _, pitch := range []float64{25, 35, 60, 90} {
				cam, err := Camera{Heading: heading, Pitch: pitch}.Frame(pts, aspect)
				if err != nil {
					t.Fatal(err)
				}
				left, right, bottom, top := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
				for _, p := range pts {
					x, y, ok := where(cam, aspect, p)
					if !ok {
						t.Fatalf("aspect %.2f heading %g pitch %g: a point is behind the camera", aspect, heading, pitch)
					}
					left, right = math.Min(left, x), math.Max(right, x)
					bottom, top = math.Min(bottom, y), math.Max(top, y)
				}
				const slack = 1e-6
				if left < -1-slack || right > 1+slack || bottom < -1-slack || top > 1+slack {
					t.Errorf("aspect %.2f heading %g pitch %g: points reach x %.3f..%.3f y %.3f..%.3f, outside the frame",
						aspect, heading, pitch, left, right, bottom, top)
				}
				// Tight: one pair of edges is touched.
				if math.Max(math.Max(-left, right), math.Max(-bottom, top)) < 0.98 {
					t.Errorf("aspect %.2f heading %g pitch %g: points reach only x %.3f..%.3f y %.3f..%.3f; the camera stands further back than it needs to",
						aspect, heading, pitch, left, right, bottom, top)
				}
				if math.Abs(top+bottom) > 0.02 {
					t.Errorf("aspect %.2f heading %g pitch %g: points sit from %.3f to %.3f, not centred top to bottom",
						aspect, heading, pitch, bottom, top)
				}
			}
		}
	}
}

// A single point is framed too -- the camera need not stand anywhere in
// particular, only in front of it -- and nothing, or a camera that cannot
// see the ground, is refused.
func TestFrameEdges(t *testing.T) {
	one := []render.Coord{{Lat: 55.67, Lon: 12.57}}
	cam, err := Camera{Pitch: 35}.Frame(one, 1.5)
	if err != nil || !(cam.Distance > 0) {
		t.Errorf("one point: %+v, %v", cam, err)
	}
	if _, err := (Camera{Pitch: 35}).Frame(nil, 1.5); err == nil {
		t.Error("no points framed")
	}
	if _, err := (Camera{Pitch: 0}).Frame(one, 1.5); err == nil {
		t.Error("a level camera framed points on the ground")
	}
}
