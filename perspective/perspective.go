// Package perspective draws a map in three dimensions: the ground shaped by
// its heights, the map draped over it, seen from a camera in the sky.
//
// It is the still image a flyover is made of. What is drawn is a map the
// caller already has -- the ordinary 2D render of the area, shading and
// contours and all -- laid over a mesh of the ground's heights and seen in
// perspective. So everything a map shows, and everything a caller draws on
// one, such as a route, follows the ground for free: it is in the texture.
//
// # How
//
// A grid of vertices over the map's area, each at the height of the ground
// there in metres, in a flat frame of metres around the point looked at.
// Each grid cell is two triangles. Each triangle is moved into the camera's
// frame, cut where it crosses the plane just in front of the camera, and
// filled pixel by pixel with a depth buffer, so nearer ground hides what is
// behind it. Its colour comes from the map, interpolated in perspective --
// linearly in the image it would be skewed across any triangle seen at an
// angle -- and is faded toward a haze colour with distance, over a sky. The
// image is drawn at twice its size and averaged down, so the edge of a hill
// against the sky is smooth.
//
// A full mesh rather than the simpler "voxel space" column renderer of old
// flight games: that draws the ground a column at a time and is much less
// code, but it breaks down looking steeply down, and this has to look right
// from any pitch.
//
// The frame is flat: the curve of the earth over the few tens of kilometres
// a map shows is metres, and the map itself is flat Web Mercator.
package perspective

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// Camera is where the ground is seen from: looking at a point on it, from a
// distance, in a direction, at an angle below the horizontal.
type Camera struct {
	// Target is the point on the ground the camera looks at, which is the
	// centre of the image.
	Target render.Coord
	// Distance is from the camera to the target, in metres along the line
	// of sight.
	Distance float64
	// Heading is the compass bearing the camera looks along, in degrees:
	// 0 looks north, 90 east.
	Heading float64
	// Pitch is how far below the horizontal the camera looks, in degrees:
	// 90 straight down, a map seen from above; a few degrees, the horizon.
	Pitch float64
	// FOV is the vertical field of view in degrees; 0 is DefaultFOV.
	FOV float64
}

// HazeMetres is how far from this camera ground is about two-thirds haze by
// default: three times its distance from the point looked at, which is
// then a tenth haze, and the horizon faint.
func (c Camera) HazeMetres() float64 { return 3 * c.Distance }

// VisibleRange is how far from this camera ground can still be told from
// the haze: 1.75 times HazeMetres, where it is 95% haze. A map draped for
// the camera need reach no further.
func (c Camera) VisibleRange() float64 { return 1.75 * c.HazeMetres() }

// DefaultFOV is a camera's vertical field of view when it says none: about
// a 50 mm lens on a full-frame camera, which looks like a view rather than a
// fisheye or a telescope.
const DefaultFOV = 40.0

// Scene is the ground to draw: the map, the view it was rendered for, and
// the heights under it.
type Scene struct {
	// Map is the 2D map of the area, rendered for View.
	Map  image.Image
	View render.View
	// Tiles, in place of Map, is a pyramid the ground's colour is drawn
	// from, each part of the picture at the zoom its pixels need. View is
	// then only the area the ground is laid over and the grid of its mesh;
	// nothing is drawn at its size.
	Tiles *Tiles
	// Heights is the height of the ground: a terrain store's.
	Heights render.HeightSource
	// Exaggeration multiplies the heights; 0 is 1, the ground as measured.
	Exaggeration float64
	// Step is how many map pixels one mesh cell spans; 0 is DefaultStep.
	Step int
}

// DefaultStep is a mesh cell's size in map pixels when the scene says none.
// Four keeps a 1024-pixel map to about 65,000 triangles while the mesh is
// still finer than the elevation it is read from at street zooms.
const DefaultStep = 4

// Options are how the image is drawn.
type Options struct {
	Width, Height int
	// Sky is the colour at the top of the image and Horizon at the bottom
	// of the sky; zero values are DefaultSky and DefaultHorizon.
	Sky, Horizon color.RGBA
	// Haze is what distant ground fades toward; zero is the horizon colour.
	Haze color.RGBA
	// HazeMetres is how far away ground is about two-thirds haze;
	// 0 is the camera's HazeMetres, so the target is clear and the horizon
	// faint.
	HazeMetres float64
}

// The default sky: a clear blue overhead, paling toward the horizon, which
// is also what distant ground fades toward -- the blue of distance, which is
// most of what makes a far ridge read as far. The first sky was so pale,
// grey-white at the horizon, that the ground's own pale colours ran into it
// and the horizon had no edge.
var (
	DefaultSky     = color.RGBA{R: 0x4a, G: 0x82, B: 0xc8, A: 0xff}
	DefaultHorizon = color.RGBA{R: 0xc6, G: 0xd9, B: 0xee, A: 0xff}
)

// supersample is how many times the image's size it is drawn at, each way,
// before being averaged down.
const supersample = 2

// nearMetres is how close in front of the camera ground is still drawn.
// Ground nearer than this is cut away: a triangle crossing the camera's own
// plane would project to infinity.
const nearMetres = 1.0

// Picture is a scene drawn: the image, and what is needed to say where in
// it a place on the ground appears.
type Picture struct {
	Image *image.RGBA

	mesh  *mesh
	cam   view
	depth []float64 // the supersampled frame's
	w, h  int       // the supersampled frame's size
}

// Project is where in the image the ground at c falls, in its pixels,
// whether or not nearer ground hides it there; ok is false when it is not in
// the picture at all -- off the map, behind the camera or outside the image.
// It is for drawing something the viewer should know is there even behind a
// hill, such as where a rider is, faintly; Locate says whether it is seen.
func (p *Picture) Project(c render.Coord) (x, y float64, ok bool) {
	x, y, _, ok = p.place(c)
	return x, y, ok
}

// Locate is where in the image the ground at c appears, in its pixels, and
// whether it is seen at all: in front of the camera, inside the image, and
// not behind nearer ground. It is for drawing on the picture things that
// should stand upright rather than lie on the ground -- the names of places
// -- and for leaving out those a hill hides.
func (p *Picture) Locate(c render.Coord) (x, y float64, visible bool) {
	x, y, seen, ok := p.place(c)
	return x, y, ok && seen
}

// place is Project and Locate together: where c falls in the image, whether
// it is in the picture at all, and whether nearer ground hides it there.
func (p *Picture) place(c render.Coord) (x, y float64, seen, ok bool) {
	m := p.mesh
	gx, gy, err := m.grid.Pixel(c)
	if err != nil {
		return 0, 0, false, false
	}
	z, have := m.heightAt(gx-0.5, gy-0.5)
	if !have {
		return 0, 0, false, false
	}
	wx, wy := mercator.Project(c.Lon, c.Lat)
	cp := p.cam.toCamera((wx-m.tx)*m.scale, -(wy-m.ty)*m.scale, z)
	if cp[2] < nearMetres {
		return 0, 0, false, false
	}
	sp := project(p.cam, point{cam: cp})
	ix, iy := int(sp.x), int(sp.y)
	if ix < 0 || iy < 0 || ix >= p.w || iy >= p.h {
		return 0, 0, false, false
	}
	// Seen if nothing nearer was drawn there: within a few metres, or a
	// twentieth of the distance, of the ground drawn at that pixel, so the
	// ground the place is on does not hide the place. A twentieth, not a
	// hundredth, because a pixel seen at a grazing angle spans a long run
	// of ground -- 2 degrees above the ground and 6 km away it is over 200
	// m deep -- and the ground drawn at its centre can be half that nearer
	// than the place. A hill in the way hides by far more.
	tolerance := math.Max(5, cp[2]/20)
	seen = cp[2] <= p.depth[iy*p.w+ix]+tolerance
	return sp.x / supersample, sp.y / supersample, seen, true
}

// Render draws the scene from the camera.
func Render(s Scene, c Camera, o Options) (*Picture, error) {
	return RenderContext(context.Background(), s, c, o)
}

// RenderContext is Render, drawing what tiles of a pyramid it needs under
// ctx.
func RenderContext(ctx context.Context, s Scene, c Camera, o Options) (*Picture, error) {
	if o.Width <= 0 || o.Height <= 0 {
		return nil, fmt.Errorf("perspective: a %d by %d image has no pixels", o.Width, o.Height)
	}
	if (s.Map == nil) == (s.Tiles == nil) || s.Heights == nil {
		return nil, errors.New("perspective: a scene needs a map or a pyramid of tiles, not both, and the heights under it")
	}
	if !(c.Distance > 0) {
		return nil, fmt.Errorf("perspective: a camera %g m from what it looks at is not looking at it", c.Distance)
	}
	if c.Pitch < 0 || c.Pitch > 90 {
		return nil, fmt.Errorf("perspective: pitch %g° is not between 0 (the horizon) and 90 (straight down)", c.Pitch)
	}
	fov := c.FOV
	if fov == 0 {
		fov = DefaultFOV
	}
	if !(fov > 0 && fov < 170) {
		return nil, fmt.Errorf("perspective: a field of view of %g° is not a lens", fov)
	}
	m, err := buildMesh(s, c.Target)
	if err != nil {
		return nil, err
	}

	sky, horizon := orDefault(o.Sky, DefaultSky), orDefault(o.Horizon, DefaultHorizon)
	haze := orDefault(o.Haze, horizon)
	hazeM := o.HazeMetres
	if hazeM <= 0 {
		hazeM = c.HazeMetres()
	}

	w, h := o.Width*supersample, o.Height*supersample
	f := newFrame(w, h, sky, horizon)
	cam := newView(c, m.targetZ, fov, w, h)
	f.paintSky(cam, c.Pitch*math.Pi/180)
	var tex sampler
	var pyr *pyramidSampler
	if s.Tiles != nil {
		if pyr, err = newPyramidSampler(ctx, s.Tiles, s.View); err != nil {
			return nil, err
		}
		tex = pyr
	} else {
		tex = newTexture(s.Map)
	}
	for j := 0; j+1 < m.rows; j++ {
		for i := 0; i+1 < m.cols; i++ {
			a, b, cc, d := m.at(i, j), m.at(i+1, j), m.at(i+1, j+1), m.at(i, j+1)
			f.triangle(cam, tex, haze, hazeM, a, b, cc)
			f.triangle(cam, tex, haze, hazeM, a, cc, d)
		}
	}
	if pyr != nil && pyr.err != nil {
		return nil, pyr.err
	}
	return &Picture{Image: f.downsample(o.Width, o.Height), mesh: m, cam: cam, depth: f.depth, w: w, h: h}, nil
}

func orDefault(c, d color.RGBA) color.RGBA {
	if c == (color.RGBA{}) {
		return d
	}
	return c
}

// vertex is one point of the mesh: where it is, in metres east, north and
// up from the point looked at, and where in the map its colour is.
type vertex struct {
	x, y, z float64
	u, v    float64 // in map pixels
	ok      bool    // false where no height is known
}

type mesh struct {
	cols, rows int
	v          []vertex
	targetZ    float64 // the height of the ground looked at, exaggerated
	// grid is the view the vertices are the pixel centres of, and tx, ty
	// and scale how a place becomes metres around the target.
	grid   render.View
	tx, ty float64
	scale  float64
}

// heightAt is the mesh's height, exaggerated, at (gx, gy) in vertex units
// -- vertex (i, j) at (i, j) -- interpolated between the four around it;
// false where any of them has none or it is off the mesh.
func (m *mesh) heightAt(gx, gy float64) (float64, bool) {
	if gx < 0 || gy < 0 || gx > float64(m.cols-1) || gy > float64(m.rows-1) {
		return 0, false
	}
	i0, j0 := int(gx), int(gy)
	i1, j1 := min(i0+1, m.cols-1), min(j0+1, m.rows-1)
	a, b, c, d := m.at(i0, j0), m.at(i1, j0), m.at(i0, j1), m.at(i1, j1)
	if !a.ok || !b.ok || !c.ok || !d.ok {
		return 0, false
	}
	fx, fy := gx-float64(i0), gy-float64(j0)
	top := a.z + (b.z-a.z)*fx
	bot := c.z + (d.z-c.z)*fx
	return top + (bot-top)*fy, true
}

func (m *mesh) at(i, j int) vertex { return m.v[j*m.cols+i] }

// buildMesh lays the grid over the map's view: a vertex at the centre of
// every Step-th map pixel, at the height of the ground there.
func buildMesh(s Scene, target render.Coord) (*mesh, error) {
	step := s.Step
	if step <= 0 {
		step = DefaultStep
	}
	ex := s.Exaggeration
	if ex == 0 {
		ex = 1
	}
	cols, rows := max(2, s.View.Width/step), max(2, s.View.Height/step)
	grid := render.View{Bounds: s.View.Bounds, Width: cols, Height: rows}
	hs, err := render.Heights(s.Heights, grid)
	if err != nil {
		return nil, err
	}

	// Metres per unit of Web Mercator's world square at the target: the
	// circumference, shrunk by the latitude as Mercator stretches it.
	const circumference = 40075016.686
	tx, ty := mercator.Project(target.Lon, target.Lat)
	scale := circumference * math.Cos(target.Lat*math.Pi/180)

	m := &mesh{cols: cols, rows: rows, v: make([]vertex, cols*rows), grid: grid, tx: tx, ty: ty, scale: scale}
	for j := 0; j < rows; j++ {
		for i := 0; i < cols; i++ {
			c, err := grid.Coord(float64(i)+0.5, float64(j)+0.5)
			if err != nil {
				return nil, err
			}
			u, v, err := s.View.Pixel(c)
			if err != nil {
				return nil, err
			}
			wx, wy := mercator.Project(c.Lon, c.Lat)
			h := float64(hs[j*cols+i])
			m.v[j*cols+i] = vertex{
				x: (wx - tx) * scale,
				y: -(wy - ty) * scale, // world y runs south; north is up here
				z: h * ex,
				u: u, v: v,
				ok: !math.IsNaN(h),
			}
		}
	}
	// The height under the target, so the camera looks at the ground and
	// not at sea level beneath it: the nearest vertex with one.
	gx, gy, err := grid.Pixel(target)
	if err != nil {
		return nil, err
	}
	best := math.Inf(1)
	for j := 0; j < rows; j++ {
		for i := 0; i < cols; i++ {
			if vt := m.at(i, j); vt.ok {
				if d := math.Hypot(float64(i)+0.5-gx, float64(j)+0.5-gy); d < best {
					best, m.targetZ = d, vt.z
				}
			}
		}
	}
	return m, nil
}

// view is a camera resolved: where the eye is, its three axes, and the
// focal length in pixels.
type view struct {
	eye                [3]float64
	right, up, forward [3]float64
	focal, cx, cy      float64
}

func newView(c Camera, targetZ, fov float64, w, h int) view {
	hd, pt := c.Heading*math.Pi/180, c.Pitch*math.Pi/180
	// Looking along the heading, tipped down by the pitch: x east, y north,
	// z up.
	fwd := [3]float64{math.Sin(hd) * math.Cos(pt), math.Cos(hd) * math.Cos(pt), -math.Sin(pt)}
	// Right is level, square to the heading; up completes the frame. At a
	// pitch of 90° the heading still decides which way is up in the image.
	right := [3]float64{math.Cos(hd), -math.Sin(hd), 0}
	up := cross(right, fwd)
	target := [3]float64{0, 0, targetZ}
	eye := [3]float64{target[0] - c.Distance*fwd[0], target[1] - c.Distance*fwd[1], target[2] - c.Distance*fwd[2]}
	return view{
		eye: eye, right: right, up: up, forward: fwd,
		focal: float64(h) / 2 / math.Tan(fov*math.Pi/360),
		cx:    float64(w) / 2, cy: float64(h) / 2,
	}
}

// toCamera is a point in the camera's frame: across, up, and depth along
// the line of sight.
func (v view) toCamera(x, y, z float64) [3]float64 {
	d := [3]float64{x - v.eye[0], y - v.eye[1], z - v.eye[2]}
	return [3]float64{dot(d, v.right), dot(d, v.up), dot(d, v.forward)}
}

func cross(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
