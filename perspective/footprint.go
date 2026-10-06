package perspective

import (
	"math"

	"github.com/wisborg/osmbase/render"
)

// Footprint is the rectangle of ground the camera sees in an image of the
// given aspect (width over height), out to maxRange metres: the area a map
// draped for this camera has to cover.
//
// It traces the image's corners and the middles of its edges onto the
// ground, taken as level at the height of the point looked at; a ray that
// passes above the horizon, or meets the ground beyond maxRange, is taken
// out to maxRange instead, where the haze has all but hidden the ground. A
// map of a fixed size looked at through a wide lens showed its own edges
// with sky beside them; a map of the footprint fills the lens at any field
// of view. Ground higher than the point looked at can reach a little past
// it, which the fade at the map's edge covers.
func (c Camera) Footprint(aspect, maxRange float64) render.Bounds {
	fov := c.FOV
	if fov == 0 {
		fov = DefaultFOV
	}
	v := newView(c, 0, fov, 2, 2) // only its axes are used here
	tv := math.Tan(fov * math.Pi / 360)
	th := tv * aspect

	minE, maxE, minN, maxN := 0.0, 0.0, 0.0, 0.0
	add := func(e, n float64) {
		minE, maxE = math.Min(minE, e), math.Max(maxE, e)
		minN, maxN = math.Min(minN, n), math.Max(maxN, n)
	}
	add(v.eye[0], v.eye[1]) // under the camera
	for _, sx := range []float64{-1, 0, 1} {
		for _, sy := range []float64{-1, 0, 1} {
			// The ray through this point of the image, in east, north, up.
			d := [3]float64{
				v.forward[0] + sx*th*v.right[0] + sy*tv*v.up[0],
				v.forward[1] + sx*th*v.right[1] + sy*tv*v.up[1],
				v.forward[2] + sx*th*v.right[2] + sy*tv*v.up[2],
			}
			flat := math.Hypot(d[0], d[1])
			if flat == 0 {
				continue // straight down, the point under the camera
			}
			t := math.Inf(1)
			if d[2] < 0 {
				t = -v.eye[2] / d[2]
			}
			// No further than maxRange across the ground.
			t = math.Min(t, maxRange/flat)
			add(v.eye[0]+t*d[0], v.eye[1]+t*d[1])
		}
	}
	lat := c.Target.Lat
	cos := math.Cos(lat * math.Pi / 180)
	const perDegree = 111_319.5 // metres per degree along the equator and the meridian
	return render.Bounds{
		West: c.Target.Lon + minE/(perDegree*cos), East: c.Target.Lon + maxE/(perDegree*cos),
		South: lat + minN/perDegree, North: lat + maxN/perDegree,
	}
}

// MapBounds is the area a map draped for this camera should cover: its
// Footprint out to VisibleRange, grown on every side by the width the map's
// edge fades over, so the fade falls outside what the lens sees. Draped
// over the footprint alone, the map's near edge lay just behind the bottom
// of the picture and its fade washed the foreground into the haze.
func (c Camera) MapBounds(aspect float64) render.Bounds {
	b := c.Footprint(aspect, c.VisibleRange())
	// The fade takes edgeFraction of the map's shorter side from each edge,
	// so the area inside it is 1-2*edgeFraction of the whole.
	m := edgeFraction / (1 - 2*edgeFraction)
	dx, dy := (b.East-b.West)*m, (b.North-b.South)*m
	d := math.Max(dx, dy)
	return render.Bounds{West: b.West - d, East: b.East + d, South: b.South - d, North: b.North + d}
}
