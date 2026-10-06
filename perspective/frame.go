package perspective

import (
	"errors"
	"math"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// FrameMargin is how much of the image, from each edge, Frame keeps clear of
// the points it frames: room for a route's line, its markers and their
// labels, and for hills the level ground Frame reckons with does not have.
const FrameMargin = 0.1

// Frame is the camera looking from heading and pitch through a lens of fov
// (0 for DefaultFOV) that takes in every one of points, in an image of the
// given aspect (width over height), with FrameMargin clear at each edge --
// the view of a whole route.
//
// It looks at the middle of the points and steps back until they all fit,
// reckoning them on level ground at the height of the point looked at. A
// route over a hill rises toward the camera, which the margin is there for;
// a camera fitted to the heights as well would have to render them first.
// The points in front are drawn larger than those behind, so the middle of
// the points is not quite the middle of the picture; the target is moved
// along the heading until the nearest and furthest sit equally far from the
// top and bottom edges.
func (c Camera) Frame(points []render.Coord, aspect float64) (Camera, error) {
	if len(points) == 0 {
		return c, errors.New("perspective: there are no points to frame")
	}
	if !(aspect > 0) {
		return c, errors.New("perspective: an image with no width or height frames nothing")
	}
	fov := c.FOV
	if fov == 0 {
		fov = DefaultFOV
	}
	if !(fov > 0 && fov < 170) {
		return c, errors.New("perspective: that field of view is not a lens")
	}
	if c.Pitch <= 0 || c.Pitch > 90 {
		return c, errors.New("perspective: a camera level with the ground, or looking up, frames nothing on it")
	}

	// The middle of the points' extent, on Web Mercator, where the search
	// starts; the longest way across, which no distance fits less than a
	// fraction of.
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range points {
		x, y := mercator.Project(p.Lon, p.Lat)
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	cam := c
	cam.FOV = fov
	cam.Target.Lon, cam.Target.Lat = mercator.Unproject((minX+maxX)/2, (minY+maxY)/2)
	span := math.Max(1, math.Hypot(maxX-minX, maxY-minY)*metresPerUnit(cam.Target.Lat))

	tv := math.Tan(fov*math.Pi/360) * (1 - 2*FrameMargin)
	th := tv * aspect
	hd := c.Heading * math.Pi / 180
	ahead := [2]float64{math.Sin(hd), math.Cos(hd)}   // along the heading, east and north
	across := [2]float64{math.Cos(hd), -math.Sin(hd)} // to the right of it

	// The points in metres east and north of the target, as the mesh has
	// them; then where they fall in the image, as fractions of the framed
	// half-width and half-height, with the target moved fwd metres ahead
	// and side metres to the right and the camera d away. ok is false when
	// one is behind the camera.
	var ground [][2]float64
	around := func() {
		ground = ground[:0]
		scale := metresPerUnit(cam.Target.Lat)
		tx, ty := mercator.Project(cam.Target.Lon, cam.Target.Lat)
		for _, p := range points {
			x, y := mercator.Project(p.Lon, p.Lat)
			ground = append(ground, [2]float64{(x - tx) * scale, -(y - ty) * scale})
		}
	}
	extent := func(fwd, side, d float64) (left, right, bottom, top float64, ok bool) {
		k := cam
		k.Distance = d
		v := newView(k, 0, fov, 2, 2)
		sx, sy := fwd*ahead[0]+side*across[0], fwd*ahead[1]+side*across[1]
		left, right, bottom, top = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, g := range ground {
			p := v.toCamera(g[0]-sx, g[1]-sy, 0)
			if p[2] < nearMetres {
				return 0, 0, 0, 0, false
			}
			x, y := p[0]/p[2]/th, p[1]/p[2]/tv
			left, right = math.Min(left, x), math.Max(right, x)
			bottom, top = math.Min(bottom, y), math.Max(top, y)
		}
		return left, right, bottom, top, true
	}
	// Stepping back only ever brings the points toward the middle of the
	// image, so the nearest distance that fits is found by halving.
	nearest := func(fwd, side float64) (float64, error) {
		fits := func(d float64) bool {
			l, r, b, t, ok := extent(fwd, side, d)
			return ok && l >= -1 && r <= 1 && b >= -1 && t <= 1
		}
		lo, hi := span/1000, span
		for !fits(hi) {
			if hi *= 2; hi > 1e9 {
				return 0, errors.New("perspective: no distance frames these points")
			}
		}
		for range 60 {
			if mid := (lo + hi) / 2; fits(mid) {
				hi = mid
			} else {
				lo = mid
			}
		}
		return hi, nil
	}
	// The move that centres the points one way, found by halving too:
	// moving the target ahead drops them lower in the image, and moving it
	// right shifts them left. centre is how far off centre they sit, which
	// falls as the move grows.
	balance := func(centre func(m float64) (float64, bool)) float64 {
		lo, hi := -span, span
		for range 60 {
			mid := (lo + hi) / 2
			if off, ok := centre(mid); !ok || off < 0 {
				hi = mid
			} else {
				lo = mid
			}
		}
		return (lo + hi) / 2
	}

	// Perspective draws the near points larger than the far ones, so the
	// middle of their extent is not the middle of the picture: the target
	// is moved ahead and aside until they sit evenly, a few times over, as
	// each move and the distance change what the others have to be.
	around()
	// Until neither move changes by a thousandth of the way across, or
	// for fifty rounds: low over a long route they settle slowly.
	fwd, side := 0.0, 0.0
	for range 50 {
		d, err := nearest(fwd, side)
		if err != nil {
			return c, err
		}
		f := balance(func(m float64) (float64, bool) {
			_, _, b, t, ok := extent(m, side, d)
			return b + t, ok
		})
		sd := balance(func(m float64) (float64, bool) {
			l, r, _, _, ok := extent(f, m, d)
			return l + r, ok
		})
		moved := math.Hypot(f-fwd, sd-side)
		fwd, side = f, sd
		if moved < span/1000 {
			break
		}
	}
	// The target moved, and the points measured again around it -- the
	// metres to a degree are the target's, as they are when it is drawn --
	// for the distance itself.
	tx, ty := mercator.Project(cam.Target.Lon, cam.Target.Lat)
	scale := metresPerUnit(cam.Target.Lat)
	sx, sy := fwd*ahead[0]+side*across[0], fwd*ahead[1]+side*across[1]
	cam.Target.Lon, cam.Target.Lat = mercator.Unproject(tx+sx/scale, ty-sy/scale)
	around()
	d, err := nearest(0, 0)
	if err != nil {
		return c, err
	}
	cam.Distance = d
	cam.FOV = c.FOV
	return cam, nil
}

// metresPerUnit is how many metres one unit of Web Mercator's world square
// spans at latitude lat: the circumference, shrunk as Mercator stretches it.
func metresPerUnit(lat float64) float64 {
	const circumference = 40075016.686
	return circumference * math.Cos(lat*math.Pi/180)
}

// MaxMapSide is the longest side of a map MapView draws to drape, in
// pixels: 8192 is a quarter of a gigabyte of RGBA, and past it a picture
// gains nothing a viewer can see.
const MaxMapSide = 8192

// MapView is the view of b a map to drape for this camera is drawn at: its
// longer side side pixels, or when side is 0, about one map pixel to one
// image pixel at the point looked at -- the ground nearest the camera is
// then a little soft and the distance more than sharp enough -- no less
// than 512 and no more than MaxMapSide. b is usually MapBounds.
func (c Camera) MapView(b render.Bounds, imageHeight, side int) render.View {
	wKM, hKM := BoundsKM(b)
	long := math.Max(wKM, hKM) * 1000
	if side <= 0 {
		fov := c.FOV
		if fov == 0 {
			fov = DefaultFOV
		}
		// Metres one image pixel spans at the point looked at.
		perPixel := 2 * c.Distance * math.Tan(fov*math.Pi/360) / float64(max(1, imageHeight))
		side = int(math.Min(MaxMapSide, math.Max(512, long/perPixel)))
	}
	w, h := side, side
	if wKM > hKM {
		h = max(1, int(float64(side)*hKM/wKM))
	} else {
		w = max(1, int(float64(side)*wKM/hKM))
	}
	return render.View{Bounds: b, Width: w, Height: h}
}

// StepFor is the Scene.Step for a map drawn at v: DefaultStep, or more for a
// large map, so the mesh stays near 512 cells a side however large the map
// over it -- past that, more triangles cost time and show nothing.
func StepFor(v render.View) int {
	return max(DefaultStep, max(v.Width, v.Height)/512)
}

// BoundsKM is the width and height of b, in kilometres, measured across its
// middle.
func BoundsKM(b render.Bounds) (w, h float64) {
	mid := (b.North + b.South) / 2
	return (b.East - b.West) * 111.32 * math.Cos(mid*math.Pi/180), (b.North - b.South) * 111.32
}

// Level is level ground at sea level, as a height source: what a program
// drapes its map over when it holds no heights for the area, so the
// picture is the map tilted rather than nothing at all. A source that holds
// no tile leaves the mesh without vertices there, and a mesh without
// vertices draws only sky.
var Level render.HeightSource = level{}

type level struct{}

// levelSize is the side of the grid level serves: small, since every
// sample is the same.
const levelSize = 16

var levelGrid = make([]float32, levelSize*levelSize)

func (level) Heights(z uint8, x, y uint32) ([]float32, int, bool, error) {
	return levelGrid, levelSize, true, nil
}

// HeadingStep is how finely BestHeading tries bearings, in degrees: finer
// shows no difference anybody would see, and each costs a framing.
const HeadingStep = 15.0

// BestHeading is Frame from whichever bearing, of every HeadingStep from
// north, shows the points largest -- the camera that can stand nearest, at
// c's pitch and lens. For a long straight route in a wide picture that lays
// it corner to corner, the longest way across, and never looks along it.
//
// The bearing is chosen on a few hundred of the points, which frame as all
// of them do to well within the margin and a hundred times faster, and the
// camera then framed on all of them. Of two bearings that tie -- those
// opposite each other often do -- the first from north is taken, so the
// same route is always seen the same way.
func (c Camera) BestHeading(points []render.Coord, aspect float64) (Camera, error) {
	sample := points
	if n := len(points); n > 400 {
		sample = make([]render.Coord, 0, 402)
		for i := 0; i < n; i += n / 400 {
			sample = append(sample, points[i])
		}
		sample = append(sample, points[n-1])
	}
	best, heading := math.Inf(1), 0.0
	for h := 0.0; h < 360; h += HeadingStep {
		c.Heading = h
		cam, err := c.Frame(sample, aspect)
		if err != nil {
			return cam, err
		}
		// A thousandth nearer to count, so rounding does not choose
		// between bearings that see the points alike.
		if cam.Distance < best*0.999 {
			best, heading = cam.Distance, h
		}
	}
	c.Heading = heading
	return c.Frame(points, aspect)
}
