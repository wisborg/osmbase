package render

import (
	"fmt"
	"image/color"
	"math"
	"slices"

	"github.com/wisborg/osmbase/raster"
)

// HeightSource supplies the height of the ground, a tile at a time, for
// hillshading.
//
// It is the elevation counterpart of TileSource and is declared here for the
// same reason: the renderer draws from whatever the caller hands it and has
// no way to fetch a missing tile. Decoding is the source's business -- this
// package knows pixels, not image formats -- so a tile arrives as heights:
// size by size samples in metres, row by row from the north-west, sample
// (i, j) at the centre of the tile's pixel in column i, row j. dem.Source
// is the implementation this module provides.
//
// ok false is a tile the source does not hold, which is an answer: the
// renderer walks up to a shallower zoom, as it does for the map.
type HeightSource interface {
	Heights(z uint8, x, y uint32) (heights []float32, size int, ok bool, err error)
}

// The light and the shading, and why they are these numbers.
const (
	// The sun is in the north-west, 45° up: the cartographic convention, for
	// the reason it became one. Light from the top of the page reads as
	// hills; light from the bottom turns the same hills into valleys for most
	// viewers, which is a picture of the wrong landscape.
	sunAzimuth  = 315 * math.Pi / 180
	sunAltitude = 45 * math.Pi / 180

	// maxShadow and maxLight are how far toward Palette.Shade and
	// Palette.Highlight the steepest slopes go. The shade is a tint over the
	// map and not a photograph of the ground: at full strength it would turn
	// a mountain range into black and white and every overlay on it with it.
	// CheckContrast holds a palette's surfaces at these extremes to the same
	// rules as the surfaces themselves, so a palette's Shade and Highlight
	// are chosen knowing exactly how far they reach.
	maxShadow = 0.45
	maxLight  = 0.45

	// shadeKnee is where the shade starts to saturate, in units of the
	// difference in illumination from flat ground. Below it the tint is in
	// proportion to the slope; above it every step gains less, so a cliff is
	// darker than a hillside but a mountain range is not burnt out. See
	// shadeOf.
	shadeKnee = 0.35

	// smoothMetres is the ground distance the heights are blurred over before
	// shading, once the view is fine enough to see the data's own texture.
	// Bare-earth city models fill the ground under removed buildings with
	// flat triangles, and at street zooms a hillshade turns them into facets
	// that look like paving; a blur of a couple of metres removes them and
	// keeps every slope anyone would call a hill. Coarser than a few metres a
	// pixel the blur is under a pixel and is skipped.
	smoothMetres = 5.0
	maxSmooth    = 16
)

// exaggeration is how much the view's slopes are steepened at zoom z.
//
// A shallow zoom reads the elevation at a coarse resolution, and averaging
// a valley and its ridge over half a kilometre flattens both: at zoom 8 the
// same Alps are a gentle swell. Steepening by zoom gives the shade back the
// shape a reader expects at that scale. Past zoom 13 the data is near its
// own resolution and the slopes are drawn as measured.
func exaggeration(z float64) float64 {
	return max(1, math.Pow(1.6, (13-z)/2))
}

// shadeIndex is where the shade and the contours go among the style's
// rules: after its leading run of fill-only rules -- the ground and what
// covers it -- and before the first rule that draws a line, water or a
// building. A hill is under the roads, not over them: shading a road
// changes its colour along its length, and the road is the thing the map is
// read by. Water is not ground to contour: its surface's noise drew lines
// across every lake and estuary, and an estuary at sea level was labelled
// "0" along its length. And a building is not ground either: bare-earth
// elevation fills the ground under a removed building with flat triangles,
// and shading a footprint draws those as smudges on its roof.
func shadeIndex(rules []Rule) int {
	for i, r := range rules {
		if !r.Paint.Fill || r.Paint.Width != 0 || len(r.Paint.Widths) != 0 || r.Paint.Role == RoleBuilding || r.Paint.Role == RoleWater {
			return i
		}
	}
	return len(rules)
}

// relief is the ground under one view, worked out before anything is drawn:
// its heights, and the shading and contours drawn from them.
type relief struct {
	// field is the height at every output pixel, with m pixels of margin
	// all round, fw by fh, smoothed at street zooms; NaN where none is
	// known.
	field  []float32
	fw, fh int
	m      int
	// mpp is the ground distance one output pixel spans at the view's
	// centre, in metres.
	mpp float64
	// amount is the tint at each output pixel: negative toward the shade,
	// positive toward the highlight, NaN where no height is known. Nil when
	// the palette draws no shading.
	amount []float32
	// zoom is the deepest elevation zoom any of the view was drawn from.
	zoom uint8
	// shaded is the fraction of the view a height was found for.
	shaded float64
}

// demTile is one elevation tile as the view needs it: the grid that stands
// in for it, which may be an ancestor's, and where the tile lies inside it.
type demTile struct {
	heights []float32
	size    int
	// up is how many zooms shallower the grid is; ox, oy where this tile's
	// north-west corner is in the grid's own pixels.
	up     uint8
	ox, oy float64
	ok     bool
}

// computeRelief works out the ground under the view projected by p from
// src, and its shading when shade is set.
//
// The heights are read at the elevation zoom nearest one sample per output
// pixel -- a tile zoom less one, since an elevation tile is 512 pixels where
// the map's zoom counts 256 -- interpolated to every output pixel, and the
// slope at each pixel taken in metres from its neighbours.
func computeRelief(src HeightSource, p projection, shade bool) (*relief, error) {
	dz := uint8(0)
	if p.tileZoom > 0 {
		dz = p.tileZoom - 1
	}

	// Metres per output pixel at the view's centre: the mercator scale
	// varies across a view, but the blur radius is a coarse number and one
	// value serves the whole picture. The slope below uses each row's own.
	centreLat := latOfWorldY(p.originY + float64(p.height)/2/p.scale)
	mpp := metresPerPixel(p, centreLat)
	radius := blurRadius(smoothMetres / mpp)

	// The heights are read and smoothed well beyond the view, so that every
	// pixel of it is worked out from the same ground whichever view it is
	// in: far enough that the blur, which reaches three times its radius,
	// never meets the field's clamped edge inside the view; and far enough
	// that a contour leaving the view is followed for minContourLength
	// before the field ends, so the shortest lines are dropped by their
	// whole length rather than by how much of them one view holds. A view
	// whose field ended a pixel past its edge shaded and contoured that
	// edge differently from the view beside it, and a picture assembled
	// from neighbouring views -- a flyover's fixed tiles -- showed seams.
	m := max(int(minContourLength)+2, 3*int(math.Ceil(radius))+2)
	g, err := readGrid(src, p, dz, m)
	if err != nil {
		return nil, err
	}
	w, h := p.width+2*m, p.height+2*m
	field, known := g.field(p, m)
	if radius > 0 {
		boxBlur(field, w, h, radius)
	}
	rl := &relief{
		field: field, fw: w, fh: h, m: m, mpp: mpp,
		zoom:   g.deepest,
		shaded: float64(known) / float64(p.width*p.height),
	}
	if !shade {
		return rl, nil
	}

	ex := exaggeration(p.zoom)
	sinAlt, cosAlt := math.Sin(sunAltitude), math.Cos(sunAltitude)
	amount := make([]float32, p.width*p.height)
	for y := 0; y < p.height; y++ {
		lat := latOfWorldY(p.originY + (float64(y)+0.5)/p.scale)
		cell := metresPerPixel(p, lat) / ex
		for x := 0; x < p.width; x++ {
			at := func(dx, dy int) float64 { return float64(field[(y+m+dy)*w+x+m+dx]) }
			// Horn's method: the slope from the eight neighbours, weighted
			// toward the four nearest. y grows southward here, so dzdy is
			// the slope toward the south.
			dzdx := ((at(1, -1) + 2*at(1, 0) + at(1, 1)) - (at(-1, -1) + 2*at(-1, 0) + at(-1, 1))) / (8 * cell)
			dzdy := ((at(-1, 1) + 2*at(0, 1) + at(1, 1)) - (at(-1, -1) + 2*at(0, -1) + at(1, -1))) / (8 * cell)
			if math.IsNaN(dzdx) || math.IsNaN(dzdy) {
				amount[y*p.width+x] = float32(math.NaN())
				continue
			}
			amount[y*p.width+x] = float32(shadeOf(dzdx, dzdy, sinAlt, cosAlt))
		}
	}
	rl.amount = amount
	return rl, nil
}

// blurRadius is the radius, in output pixels, the heights are blurred over
// for a smoothMetres blur of r pixels: r itself, at most maxSmooth, and none
// below a quarter of a pixel, eased in between a quarter and a half.
//
// It is a fraction rather than a whole number of pixels, so that it moves
// continuously with the scale. Rounded, it jumped a whole pixel at some
// latitude for every zoom -- at zoom 16 near Sydney the radius is about
// 2.5 -- and two neighbouring views either side of that latitude shaded the
// same slope at two blurs: a seam where a picture is assembled from them.
// The ease in keeps that true where the blur starts, and keeps it away from
// coarse views, where a blur under a pixel only softens what the data says.
func blurRadius(r float64) float64 {
	r = math.Min(r, maxSmooth)
	switch {
	case r <= 0.25:
		return 0
	case r < 0.5:
		return 2*r - 0.5
	}
	return r
}

// field is the height at the centre of every pixel of the view p projects,
// with margin pixels of it all round -- (width+2*margin) by
// (height+2*margin), row by row from the north-west -- interpolated from
// the grid, NaN where no height is known; and how many of the view's own
// pixels, margin aside, have one.
func (g *demGrid) field(p projection, margin int) ([]float32, int) {
	w, h := p.width+2*margin, p.height+2*margin
	out := make([]float32, w*h)
	known := 0
	for py := 0; py < h; py++ {
		wy := p.originY + (float64(py-margin)+0.5)/p.scale
		gy := wy*g.worldSize - 0.5 - float64(g.y0)
		for px := 0; px < w; px++ {
			wx := p.originX + (float64(px-margin)+0.5)/p.scale
			gx := wx*g.worldSize - 0.5 - float64(g.x0)
			v := g.bilinear(gx, gy)
			out[py*w+px] = v
			if px >= margin && py >= margin && px < w-margin && py < h-margin && !isNaN32(v) {
				known++
			}
		}
	}
	return out, known
}

// Heights is the height of the ground, in metres, at the centre of every
// pixel of the view: Width by Height values, row by row from the north-west,
// NaN where src holds no height at any zoom. It is read as the shading reads
// it -- at the elevation zoom nearest one sample per pixel, walking up to a
// shallower tile where one is missing, interpolated -- and is neither
// smoothed nor exaggerated: what the data says, for a caller drawing the
// ground some other way, such as in perspective.
//
// The view is resolved as a render resolves it, so the heights of a view and
// the map rendered for the same view cover the same ground, pixel for pixel
// in proportion: a grid of a quarter of the map's size in each direction
// lands on every fourth pixel of it.
func Heights(src HeightSource, v View) ([]float32, error) {
	if src == nil {
		return nil, fmt.Errorf("render: no height source to read heights from")
	}
	p, err := resolve(v)
	if err != nil {
		return nil, err
	}
	dz := uint8(0)
	if p.tileZoom > 0 {
		dz = p.tileZoom - 1
	}
	g, err := readGrid(src, p, dz, 0)
	if err != nil {
		return nil, err
	}
	out, _ := g.field(p, 0)
	return out, nil
}

// shadeOf is the tint for a surface rising dzdx to the east and dzdy to the
// south per metre: the change in its illumination from flat ground's, eased
// past shadeKnee and scaled to -1 (full shadow) to +1 (full light).
func shadeOf(dzdx, dzdy, sinAlt, cosAlt float64) float64 {
	slope := math.Atan(math.Hypot(dzdx, dzdy))
	// The direction the surface faces, as a compass bearing: downhill. A
	// surface rising to the east faces west.
	aspect := math.Atan2(-dzdx, dzdy)
	lit := sinAlt*math.Cos(slope) + cosAlt*math.Sin(slope)*math.Cos(sunAzimuth-aspect)
	d := lit - sinAlt
	return math.Tanh(d / shadeKnee)
}

// demGrid is the heights covering a view, gathered from the tiles at one
// elevation zoom into one array so that interpolation never asks which
// tile a sample is in.
type demGrid struct {
	// worldSize is the elevation zoom's width in samples.
	worldSize float64
	// x0, y0 is the north-west sample of the array, in the zoom's samples.
	x0, y0 int
	w, h   int
	v      []float32
	// deepest is the deepest zoom a tile was read at.
	deepest uint8
}

// readGrid reads the elevation tiles under the view at zoom dz, and under
// margin output pixels round it, walking up for each one the source lacks,
// into one array of samples.
func readGrid(src HeightSource, p projection, dz uint8, margin int) (*demGrid, error) {
	tx0, ty0, tx1, ty1 := p.tileRangeOver(dz, p.surface().inflate(float64(margin)+1))
	tiles := map[tileRef]demTile{}
	size := 0
	for ty := ty0; ty <= ty1; ty++ {
		for tx := tx0; tx <= tx1; tx++ {
			t, err := resolveDEM(src, tileRef{z: dz, x: tx, y: ty})
			if err != nil {
				return nil, err
			}
			if t.ok {
				if size == 0 {
					size = t.size
				} else if t.size != size {
					return nil, fmt.Errorf("render: elevation tiles of %d and %d samples in one view; a pyramid's tiles are one size", size, t.size)
				}
			}
			tiles[tileRef{z: dz, x: tx, y: ty}] = t
		}
	}
	g := &demGrid{}
	if size == 0 {
		// No height anywhere under the view. Every sample is unknown and
		// nothing is shaded; the size is a placeholder for the arithmetic.
		size = 1
	}
	n := 1 << dz
	g.worldSize = float64(size) * float64(n)
	// The array is exactly the tiles under the view and its margin, and one
	// pixel more, so the margin's own outermost samples have the grid
	// sample beyond them to interpolate toward. A sample past the array is
	// clamped to the nearest one.
	g.x0, g.y0 = int(tx0)*size, int(ty0)*size
	g.w, g.h = int(tx1-tx0+1)*size, int(ty1-ty0+1)*size
	g.v = make([]float32, g.w*g.h)
	for ref, t := range tiles {
		bx, by := int(ref.x-tx0)*size, int(ref.y-ty0)*size
		for j := 0; j < size; j++ {
			for i := 0; i < size; i++ {
				g.v[(by+j)*g.w+bx+i] = t.sample(i, j)
			}
		}
		if t.ok && dz-t.up > g.deepest {
			g.deepest = dz - t.up
		}
	}
	return g, nil
}

// resolveDEM finds the grid standing in for tile want: its own, or the
// nearest ancestor's.
func resolveDEM(src HeightSource, want tileRef) (demTile, error) {
	for ref, up := want, uint8(0); ; ref, up = ref.parent(), up+1 {
		hs, size, ok, err := src.Heights(ref.z, ref.x, ref.y)
		if err != nil {
			return demTile{}, fmt.Errorf("render: reading elevation tile %s: %w", ref, err)
		}
		if ok {
			if size <= 0 || len(hs) != size*size {
				return demTile{}, fmt.Errorf("render: elevation tile %s has %d heights for a side of %d", ref, len(hs), size)
			}
			k := uint32(1) << up
			return demTile{
				heights: hs, size: size, up: up, ok: true,
				ox: float64(want.x-ref.x*k) * float64(size) / float64(k),
				oy: float64(want.y-ref.y*k) * float64(size) / float64(k),
			}, nil
		}
		if ref.z == 0 {
			return demTile{}, nil
		}
	}
}

// sample is the height at this tile's sample (i, j), interpolated from an
// ancestor's grid where that is what stands in for it.
func (t demTile) sample(i, j int) float32 {
	if !t.ok {
		return float32(math.NaN())
	}
	if t.up == 0 {
		return t.heights[j*t.size+i]
	}
	k := float64(uint32(1) << t.up)
	gx := t.ox + (float64(i)+0.5)/k - 0.5
	gy := t.oy + (float64(j)+0.5)/k - 0.5
	return bilinear(t.heights, t.size, t.size, gx, gy)
}

// bilinear is the height at (gx, gy) in the array's own samples, clamped to
// its edges.
func (g *demGrid) bilinear(gx, gy float64) float32 {
	return bilinear(g.v, g.w, g.h, gx, gy)
}

func bilinear(v []float32, w, h int, gx, gy float64) float32 {
	gx = math.Max(0, math.Min(float64(w-1), gx))
	gy = math.Max(0, math.Min(float64(h-1), gy))
	x0, y0 := int(gx), int(gy)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := float32(gx-float64(x0)), float32(gy-float64(y0))
	a := v[y0*w+x0]*(1-fx) + v[y0*w+x1]*fx
	b := v[y1*w+x0]*(1-fx) + v[y1*w+x1]*fx
	return a*(1-fy) + b*fy
}

// boxBlur blurs the field in place with three passes of a box of radius r,
// which is near enough a gaussian. Unknown samples stay unknown and spread:
// a height averaged with a gap is not a height.
//
// The radius may be a fraction: the box takes the samples within the whole
// part of it fully and the two just beyond in proportion to the rest, so
// the blur grows smoothly with r rather than in whole pixels. See
// blurRadius.
func boxBlur(f []float32, w, h int, r float64) {
	tmp := make([]float32, max(w, h))
	for range 3 {
		for y := 0; y < h; y++ {
			blurLine(f[y*w:(y+1)*w], 1, w, r, tmp)
		}
		for x := 0; x < w; x++ {
			blurLine(f[x:], w, h, r, tmp)
		}
	}
}

// blurLine box-blurs n samples spaced stride apart, clamping at the ends,
// over a box of radius r: every sample within floor(r) fully, and the two
// at floor(r)+1 weighted by what is left.
func blurLine(f []float32, stride, n int, r float64, tmp []float32) {
	for i := 0; i < n; i++ {
		tmp[i] = f[i*stride]
	}
	k := int(r)
	frac := float32(r - float64(k))
	at := func(i int) float32 { return tmp[max(0, min(n-1, i))] }
	var sum float32
	for i := -k; i <= k; i++ {
		sum += at(i)
	}
	inv := 1 / (float32(2*k+1) + 2*frac)
	for i := 0; i < n; i++ {
		v := sum
		if frac > 0 {
			v += frac * (at(i-k-1) + at(i+k+1))
		}
		f[i*stride] = v * inv
		sum += at(i+k+1) - at(i-k)
	}
}

// apply tints the surface by the relief.
func (rl *relief) apply(s *raster.Surface, pal Palette) {
	if rl.amount == nil {
		return
	}
	img := s.RGBA()
	dark, light := pal.Shade, pal.Highlight
	for y := 0; y < img.Rect.Dy(); y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < img.Rect.Dx(); x++ {
			a := rl.amount[y*img.Rect.Dx()+x]
			var to color.RGBA
			var t float32
			switch {
			case isNaN32(a) || a == 0:
				continue
			case a < 0:
				to, t = dark, -a*maxShadow
			default:
				to, t = light, a*maxLight
			}
			if to == (color.RGBA{}) {
				continue
			}
			px := row[4*x : 4*x+3 : 4*x+3]
			px[0] = mixByte(px[0], to.R, t)
			px[1] = mixByte(px[1], to.G, t)
			px[2] = mixByte(px[2], to.B, t)
		}
	}
}

func mixByte(a, b uint8, t float32) uint8 {
	return uint8(float32(a) + (float32(b)-float32(a))*t + 0.5)
}

// mix is c moved t of the way toward to: the colour a surface is drawn in
// where the shade reaches t.
func mix(c, to color.RGBA, t float32) color.RGBA {
	return color.RGBA{R: mixByte(c.R, to.R, t), G: mixByte(c.G, to.G, t), B: mixByte(c.B, to.B, t), A: c.A}
}

// metresPerPixel is the ground distance one output pixel spans at latitude
// lat, in degrees.
func metresPerPixel(p projection, lat float64) float64 {
	const equator = 40075016.686
	return equator * math.Cos(lat*math.Pi/180) / p.scale
}

// latOfWorldY is the latitude, in degrees, of a world y coordinate.
func latOfWorldY(y float64) float64 {
	return math.Atan(math.Sinh(math.Pi*(1-2*y))) * 180 / math.Pi
}

func isNaN32(f float32) bool { return f != f }

// shadedSurfaces are the palette colours the shade lies over: the
// background and the fills drawn before it. See shadeIndex.
func (p Palette) shadedSurfaces() []namedColour {
	all := []namedColour{
		{name: "Background", c: p.Background},
		{name: "Land", c: p.Land, role: RoleLand},
		{name: "Green", c: p.Green, role: RoleGreen},
		{name: "Built", c: p.Built, role: RoleBuilt},
	}
	return slices.DeleteFunc(all, func(c namedColour) bool { return c.role != RoleBackground && p.Omits(c.role) })
}

// shadedExtremes is each shaded surface at the darkest and lightest the
// shade takes it: the colours an overlay can lie on wherever the ground is
// steep.
func (p Palette) shadedExtremes() []namedColour {
	var out []namedColour
	for _, c := range p.shadedSurfaces() {
		if (p.Shade != color.RGBA{}) {
			out = append(out, namedColour{name: c.name + " in shadow", c: mix(c.c, p.Shade, maxShadow), role: c.role})
		}
		if (p.Highlight != color.RGBA{}) {
			out = append(out, namedColour{name: c.name + " in sunlight", c: mix(c.c, p.Highlight, maxLight), role: c.role})
		}
	}
	return out
}

// shades reports whether the palette draws any shading at all.
func (p Palette) shades() bool {
	return p.Shade != (color.RGBA{}) || p.Highlight != (color.RGBA{})
}

// joinCredits is the map's credit and the terrain's, as one line: both are
// owed wherever the image is shown, and a reader looks for them in one place.
func joinCredits(m, t string) string {
	switch {
	case t == "":
		return m
	case m == "":
		return t
	}
	return m + " | " + t
}
