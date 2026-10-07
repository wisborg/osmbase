package perspective

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// frame is the image being drawn and its depth buffer: for each pixel, how
// far along the line of sight the ground drawn there is, so a triangle
// behind it is not drawn over it.
type frame struct {
	img   *image.RGBA
	depth []float64
	w, h  int
	// horizonRow is where a level line of sight meets the image; the sky
	// is drawn above it, and the horizon colour below it wherever no ground
	// is -- distance, beyond the edge of the map.
	horizonRow float64
	sky, hz    color.RGBA
}

func newFrame(w, h int, sky, horizon color.RGBA) *frame {
	f := &frame{img: image.NewRGBA(image.Rect(0, 0, w, h)), depth: make([]float64, w*h), w: w, h: h, sky: sky, hz: horizon}
	for i := range f.depth {
		f.depth[i] = math.Inf(1)
	}
	return f
}

// paintSky fills the frame behind the ground: a gradient from the sky
// colour at the top to the horizon colour at the horizon, and the horizon
// colour below it.
func (f *frame) paintSky(v view, pitchRad float64) {
	f.horizonRow = v.cy - v.focal*math.Tan(pitchRad)
	for y := 0; y < f.h; y++ {
		t := 1.0
		if f.horizonRow > 0 {
			t = math.Max(0, math.Min(1, (float64(y)+0.5)/f.horizonRow))
		}
		c := mixRGBA(f.sky, f.hz, t)
		row := f.img.Pix[y*f.img.Stride:]
		for x := 0; x < f.w; x++ {
			row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = c.R, c.G, c.B, 0xff
		}
	}
}

// sampler is where the ground's colour comes from: one map (texture), or a
// pyramid of tiles (pyramidSampler). Positions are in the scene view's map
// pixels either way.
type sampler interface {
	// at is the colour at map pixel (u, v), for a pixel of the frame
	// spanning span map pixels, which a pyramid draws from the zoom of
	// that size and one map ignores.
	at(u, v, span float64) color.RGBA
	// edgeFade is how far toward the haze the ground is drawn at (u, v),
	// so that the map ends in the distance rather than against the sky.
	edgeFade(u, v float64) float64
}

// point is a vertex in the camera's frame with its map position.
type point struct {
	cam  [3]float64
	u, v float64
}

// triangle draws one triangle of the mesh: moved into the camera's frame,
// cut at the near plane, projected and filled.
func (f *frame) triangle(cv view, tex sampler, haze color.RGBA, hazeM float64, a, b, c vertex) {
	if !a.ok || !b.ok || !c.ok {
		return
	}
	poly := []point{
		{cv.toCamera(a.x, a.y, a.z), a.u, a.v},
		{cv.toCamera(b.x, b.y, b.z), b.u, b.v},
		{cv.toCamera(c.x, c.y, c.z), c.u, c.v},
	}
	poly = clipNear(poly)
	// A triangle cut by the near plane is a quadrilateral at most; drawn
	// as a fan of triangles from its first corner.
	for k := 1; k+1 < len(poly); k++ {
		f.fill(cv, tex, haze, hazeM, poly[0], poly[k], poly[k+1])
	}
}

// clipNear is the polygon cut to the part in front of the near plane, by
// Sutherland-Hodgman against that one plane.
func clipNear(in []point) []point {
	var out []point
	for i := range in {
		p, q := in[i], in[(i+1)%len(in)]
		pIn, qIn := p.cam[2] >= nearMetres, q.cam[2] >= nearMetres
		if pIn {
			out = append(out, p)
		}
		if pIn != qIn {
			t := (nearMetres - p.cam[2]) / (q.cam[2] - p.cam[2])
			out = append(out, point{
				cam: [3]float64{p.cam[0] + t*(q.cam[0]-p.cam[0]), p.cam[1] + t*(q.cam[1]-p.cam[1]), nearMetres},
				u:   p.u + t*(q.u-p.u), v: p.v + t*(q.v-p.v),
			})
		}
	}
	return out
}

// screen is a point projected: its pixel position, and the reciprocal of
// its depth with its map position over its depth -- the quantities that are
// linear across a triangle in the image, which depth and map position are
// not.
type screen struct {
	x, y         float64
	iz, uiz, viz float64
}

func project(cv view, p point) screen {
	iz := 1 / p.cam[2]
	return screen{
		x:   cv.cx + cv.focal*p.cam[0]*iz,
		y:   cv.cy - cv.focal*p.cam[1]*iz,
		iz:  iz,
		uiz: p.u * iz, viz: p.v * iz,
	}
}

// fill rasterizes a triangle already in front of the camera: each pixel
// whose centre is inside, nearer than what is drawn there, coloured from
// the map at its interpolated position and faded by its depth.
func (f *frame) fill(cv view, tex sampler, haze color.RGBA, hazeM float64, pa, pb, pc point) {
	a, b, c := project(cv, pa), project(cv, pb), project(cv, pc)
	area := edge(a, b, c.x, c.y)
	if area == 0 {
		return
	}
	// How the interpolated quantities change from pixel to pixel: the
	// plane through the three corners, for each. What a pixel spans of the
	// map follows from them; see span below.
	det := (b.x-a.x)*(c.y-a.y) - (c.x-a.x)*(b.y-a.y)
	grad := func(qa, qb, qc float64) (dx, dy float64) {
		return ((qb-qa)*(c.y-a.y) - (qc-qa)*(b.y-a.y)) / det, ((qc-qa)*(b.x-a.x) - (qb-qa)*(c.x-a.x)) / det
	}
	izx, izy := grad(a.iz, b.iz, c.iz)
	uzx, uzy := grad(a.uiz, b.uiz, c.uiz)
	vzx, vzy := grad(a.viz, b.viz, c.viz)

	minX := int(math.Max(0, math.Floor(math.Min(a.x, math.Min(b.x, c.x)))))
	maxX := int(math.Min(float64(f.w-1), math.Ceil(math.Max(a.x, math.Max(b.x, c.x)))))
	minY := int(math.Max(0, math.Floor(math.Min(a.y, math.Min(b.y, c.y)))))
	maxY := int(math.Min(float64(f.h-1), math.Ceil(math.Max(a.y, math.Max(b.y, c.y)))))
	for y := minY; y <= maxY; y++ {
		py := float64(y) + 0.5
		for x := minX; x <= maxX; x++ {
			px := float64(x) + 0.5
			// Barycentric weights from edge functions, signed by the
			// triangle's own winding, so either winding is filled: the
			// mesh is seen from above and below the horizon alike.
			w0, w1, w2 := edge(b, c, px, py)/area, edge(c, a, px, py)/area, edge(a, b, px, py)/area
			if w0 < 0 || w1 < 0 || w2 < 0 {
				continue
			}
			iz := w0*a.iz + w1*b.iz + w2*c.iz
			z := 1 / iz
			i := y*f.w + x
			if z >= f.depth[i] {
				continue
			}
			f.depth[i] = z
			u := (w0*a.uiz + w1*b.uiz + w2*c.uiz) * z
			v := (w0*a.viz + w1*b.viz + w2*c.viz) * z
			// How far across the map the pixel reaches, in map pixels: the
			// longer of its steps across and down. u = U/I with U and I
			// linear in the image, so du = (dU - u dI) / I.
			ux, vx := (uzx-u*izx)*z, (vzx-v*izx)*z
			uy, vy := (uzy-u*izy)*z, (vzy-v*izy)*z
			span := math.Sqrt(math.Max(ux*ux+vx*vx, uy*uy+vy*vy))
			col := tex.at(u, v, span)
			// Faded into the haze toward the edge of the map, so the
			// ground ends in distance rather than in a straight line
			// against the sky; and with distance, as the air does.
			fade := math.Max(tex.edgeFade(u, v), hazeAt(z, hazeM))
			col = mixRGBA(col, haze, fade)
			o := i * 4
			f.img.Pix[o], f.img.Pix[o+1], f.img.Pix[o+2], f.img.Pix[o+3] = col.R, col.G, col.B, 0xff
		}
	}
}

// edge is twice the signed area of the triangle a, b, (x, y).
func edge(a, b screen, x, y float64) float64 {
	return (b.x-a.x)*(y-a.y) - (b.y-a.y)*(x-a.x)
}

// downsample averages the supersampled frame down to the image's size.
func (f *frame) downsample(w, h int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	n := supersample * supersample
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b int
			for dy := 0; dy < supersample; dy++ {
				row := f.img.Pix[(y*supersample+dy)*f.img.Stride:]
				for dx := 0; dx < supersample; dx++ {
					o := (x*supersample + dx) * 4
					r, g, b = r+int(row[o]), g+int(row[o+1]), b+int(row[o+2])
				}
			}
			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8((r+n/2)/n), uint8((g+n/2)/n), uint8((b+n/2)/n), 0xff
		}
	}
	return out
}

// texture is the map, as RGBA, sampled between its pixels.
type texture struct {
	img  *image.RGBA
	w, h int
}

func newTexture(m image.Image) *texture {
	rgba, ok := m.(*image.RGBA)
	if !ok || rgba.Rect.Min != (image.Point{}) {
		b := m.Bounds()
		rgba = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Rect, m, b.Min, draw.Src)
	}
	return &texture{img: rgba, w: rgba.Rect.Dx(), h: rgba.Rect.Dy()}
}

// hazeAt is how far toward the haze ground z metres away is drawn: rising
// with the square of the distance, so the ground near the camera is clear
// and the haze closes in beyond -- about a tenth at a third of hazeM, two
// thirds at hazeM, all but all at twice it. It first rose with the distance
// itself, which put a quarter haze on the very point looked at and washed
// the whole picture pale.
func hazeAt(z, hazeM float64) float64 {
	r := z / hazeM
	return 1 - math.Exp(-r*r)
}

// edgeFraction is how much of the map, from each edge, fades into the haze:
// enough that the edge is never seen as a line, little enough that the
// ground around the point looked at is untouched.
const edgeFraction = 0.12

// edgeFade is how far toward the haze the map is drawn at (u, v): 0 inside,
// rising smoothly to 1 at its edge.
func (t *texture) edgeFade(u, v float64) float64 {
	return fadeAt(u, v, float64(t.w), float64(t.h))
}

// fadeAt is edgeFade for an area w by h map pixels.
func fadeAt(u, v, w, h float64) float64 {
	m := edgeFraction * math.Min(w, h)
	d := math.Min(math.Min(u, w-u), math.Min(v, h-v))
	if d >= m {
		return 0
	}
	x := 1 - math.Max(0, d)/m
	return x * x * (3 - 2*x)
}

// at is the map's colour at (u, v) in its pixels, interpolated between the
// four nearest pixel centres and clamped at the edges.
func (t *texture) at(u, v, _ float64) color.RGBA {
	x := math.Max(0, math.Min(float64(t.w-1), u-0.5))
	y := math.Max(0, math.Min(float64(t.h-1), v-0.5))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, t.w-1), min(y0+1, t.h-1)
	fx, fy := x-float64(x0), y-float64(y0)
	px := func(x, y int) (float64, float64, float64) {
		o := t.img.PixOffset(x, y)
		return float64(t.img.Pix[o]), float64(t.img.Pix[o+1]), float64(t.img.Pix[o+2])
	}
	r00, g00, b00 := px(x0, y0)
	r10, g10, b10 := px(x1, y0)
	r01, g01, b01 := px(x0, y1)
	r11, g11, b11 := px(x1, y1)
	lerp := func(a, b, c, d float64) uint8 {
		top := a + (b-a)*fx
		bot := c + (d-c)*fx
		return uint8(top + (bot-top)*fy + 0.5)
	}
	return color.RGBA{R: lerp(r00, r10, r01, r11), G: lerp(g00, g10, g01, g11), B: lerp(b00, b10, b01, b11), A: 0xff}
}

// mixRGBA is a moved t of the way toward b.
func mixRGBA(a, b color.RGBA, t float64) color.RGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.RGBA{R: m(a.R, b.R), G: m(a.G, b.G), B: m(a.B, b.B), A: 0xff}
}
