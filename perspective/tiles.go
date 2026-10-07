package perspective

import (
	"container/list"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"sync"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/render"
)

// TileSize is the side of a map tile in a Tiles pyramid, in pixels.
const TileSize = 512

// DrawTile draws the map tile z/x/y -- the slippy-map tile, the ground from
// its west edge to its east and north to south -- at TileSize pixels square.
//
// Every tile has to be drawn as part of one map, so that the tiles meet
// without seams: the same stacking of overlapping areas and the same
// contour interval in all of them (render.Options.Areas and
// ContourInterval), and no names in the image, which would be cut at its
// edges and differ either side of them. The names come back beside it
// instead (render.Options.LiftLabels), and Picture.DrawNames stands them on
// the picture.
//
// The names' faces are drawn with afterwards, by DrawNames, one picture at
// a time. A font face is not safe for concurrent use, so they must not be
// faces anything else draws with meanwhile -- another tile being drawn, in
// particular, as tiles are drawn in parallel for frames drawn in parallel.
// Faces made for each tile are safe.
type DrawTile func(ctx context.Context, z uint8, x, y uint32) (*Tile, error)

// Tile is one tile of a pyramid: its map, and the names placed on it,
// lifted off it. Of the names, a pyramid keeps those centred on the tile's
// own ground, so that each belongs to the one tile it is in.
type Tile struct {
	Image  *image.RGBA
	Places []render.PointLabel
	Lines  []render.LineLabel
}

// Tiles is a map as a pyramid of fixed tiles, drawn when first seen and
// kept: what a moving camera drapes over the ground instead of one map.
//
// One map drawn for the whole of a flight is either too coarse near the
// camera or too large to draw; one drawn for every frame draws each piece of
// ground a little differently every time -- its names placed afresh, its
// lines cut at a different edge -- and the ground shimmers. A tile is drawn
// once and looks the same in every frame that shows it. The pyramid serves
// each part of the picture from the zoom whose pixels are about the size of
// the picture's there: fine under the camera, coarse toward the horizon.
//
// It is safe for frames drawn in parallel: a tile wanted by two at once is
// drawn once, and both wait for it.
type Tiles struct {
	draw             DrawTile
	minZoom, maxZoom uint8
	budget           int // tiles kept

	mu    sync.Mutex
	tiles map[tileKey]*list.Element
	lru   *list.List // of *tileEntry, most recently used first
	drawn int
}

type tileKey struct {
	z    uint8
	x, y uint32
}

type tileEntry struct {
	key  tileKey
	done chan struct{}
	tile *Tile
	err  error
}

// NewTiles is a pyramid of the tiles draw draws, from zoom minZoom to
// maxZoom,
// keeping about budget bytes of them in memory; 0 keeps 1 GiB. Tiles beyond
// the budget are dropped, the least recently used first, and drawn again
// if they are wanted again.
func NewTiles(draw DrawTile, minZoom, maxZoom uint8, budget int) (*Tiles, error) {
	if draw == nil {
		return nil, fmt.Errorf("perspective: a pyramid of tiles needs something to draw them")
	}
	if minZoom > maxZoom || maxZoom > mercator.MaxZoom {
		return nil, fmt.Errorf("perspective: zooms %d to %d are not a pyramid", minZoom, maxZoom)
	}
	if budget <= 0 {
		budget = 1 << 30
	}
	return &Tiles{
		draw: draw, minZoom: minZoom, maxZoom: maxZoom,
		budget: max(4, budget/(TileSize*TileSize*4)),
		tiles:  map[tileKey]*list.Element{}, lru: list.New(),
	}, nil
}

// Drawn is how many tiles have been drawn so far, including any drawn again
// after being dropped.
func (t *Tiles) Drawn() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.drawn
}

// tile is tile z/x/y, drawn now if it has not been.
func (t *Tiles) tile(ctx context.Context, k tileKey) (*Tile, error) {
	t.mu.Lock()
	if el, ok := t.tiles[k]; ok {
		t.lru.MoveToFront(el)
		e := el.Value.(*tileEntry)
		t.mu.Unlock()
		<-e.done
		return e.tile, e.err
	}
	e := &tileEntry{key: k, done: make(chan struct{})}
	t.tiles[k] = t.lru.PushFront(e)
	t.drawn++
	for t.lru.Len() > t.budget {
		old := t.lru.Back()
		t.lru.Remove(old)
		delete(t.tiles, old.Value.(*tileEntry).key)
	}
	t.mu.Unlock()

	tile, err := t.draw(ctx, k.z, k.x, k.y)
	if err == nil && (tile == nil || tile.Image == nil || tile.Image.Rect != image.Rect(0, 0, TileSize, TileSize)) {
		err = fmt.Errorf("perspective: tile %d/%d/%d came back not %d pixels square", k.z, k.x, k.y, TileSize)
	}
	if err == nil {
		tile = ownNames(tile, k)
	}
	if err != nil {
		err = fmt.Errorf("perspective: drawing tile %d/%d/%d: %w", k.z, k.x, k.y, err)
		// Not kept: a cancelled or failed draw is tried again next time.
		t.mu.Lock()
		if el, ok := t.tiles[k]; ok && el.Value == e {
			t.lru.Remove(el)
			delete(t.tiles, k)
		}
		t.mu.Unlock()
	}
	e.tile, e.err = tile, err
	close(e.done)
	return tile, err
}

// ownNames is the tile with only the names centred on its own ground: a
// render places names past the edges of the view it draws, and the tile
// beside it places the same names again, from its own side.
func ownNames(t *Tile, k tileKey) *Tile {
	n := math.Exp2(float64(k.z))
	in := func(c render.Coord) bool {
		x, y := mercator.Project(c.Lon, c.Lat)
		return math.Floor(x*n) == float64(k.x) && math.Floor(y*n) == float64(k.y)
	}
	out := &Tile{Image: t.Image}
	for _, l := range t.Places {
		if in(l.At) {
			out.Places = append(out.Places, l)
		}
	}
	for _, l := range t.Lines {
		if in(l.At) {
			out.Lines = append(out.Lines, l)
		}
	}
	return out
}

// lodBias shifts the zoom each pixel is drawn from: 0 is one tile pixel to
// one pixel of the supersampled frame, which the supersampling averages into
// two to one in the image. Negative is sharper and shimmers sooner.
const lodBias = 0.0

// pyramidSampler is a frame's view of a pyramid: the scene's map pixels
// turned into world coordinates, and the tile last used at each zoom so
// that a run of pixels in one tile looks it up once. One per frame; not
// safe for concurrent use, as a frame is drawn by one goroutine.
type pyramidSampler struct {
	ctx context.Context
	t   *Tiles
	// World coordinates of map pixel (u, v): x0 + u/scale, y0 + v/scale.
	x0, y0, scale float64
	w, h          float64 // the scene view's size, for the fade at its edge
	last          [mercator.MaxZoom + 1]struct {
		x, y uint32
		img  *image.RGBA
	}
	// used is every tile the frame drew from: whose names may be in it.
	used map[tileKey]*Tile
	// lastLOD is the zoom the last pixel was drawn at, clamped to the
	// pyramid's.
	lastLOD float32
	err     error
}

func newPyramidSampler(ctx context.Context, t *Tiles, v render.View) (*pyramidSampler, error) {
	nw, err := v.Coord(0, 0)
	if err != nil {
		return nil, err
	}
	se, err := v.Coord(float64(v.Width), float64(v.Height))
	if err != nil {
		return nil, err
	}
	x0, y0 := mercator.Project(nw.Lon, nw.Lat)
	x1, _ := mercator.Project(se.Lon, se.Lat)
	if !(x1 > x0) {
		return nil, fmt.Errorf("perspective: the scene's view has no width")
	}
	return &pyramidSampler{ctx: ctx, t: t, x0: x0, y0: y0, scale: float64(v.Width) / (x1 - x0), w: float64(v.Width), h: float64(v.Height), used: map[tileKey]*Tile{}}, nil
}

// at is the map's colour at map pixel (u, v), for a pixel of the frame
// spanning span map pixels: from the zoom whose tile pixels are that size,
// blended with the next finer one by how far between the two it falls.
func (s *pyramidSampler) at(u, v, span float64) color.RGBA {
	wx, wy := s.x0+u/s.scale, s.y0+v/s.scale
	// The zoom at which one tile pixel is span map pixels.
	lod := math.Log2(s.scale/(TileSize*math.Max(span, 1e-9))) + lodBias
	lo, hi := float64(s.t.minZoom), float64(s.t.maxZoom)
	lod = math.Max(lo, math.Min(hi, lod))
	s.lastLOD = float32(lod)
	z0 := math.Floor(lod)
	frac := lod - z0
	c0 := s.sample(uint8(z0), wx, wy)
	if frac < 1.0/256 || z0 >= hi {
		return c0
	}
	return mixRGBA(c0, s.sample(uint8(z0)+1, wx, wy), frac)
}

// sample is the colour at world (wx, wy) at zoom z, interpolated between the
// four nearest tile pixels, from whichever tiles they are in.
func (s *pyramidSampler) sample(z uint8, wx, wy float64) color.RGBA {
	n := TileSize * math.Exp2(float64(z))
	gx, gy := wx*n-0.5, wy*n-0.5
	x0, y0 := math.Floor(gx), math.Floor(gy)
	fx, fy := gx-x0, gy-y0
	var r, g, b [4]float64
	for i, d := range [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		r[i], g[i], b[i] = s.texel(z, int64(x0+d[0]), int64(y0+d[1]))
	}
	lerp := func(c [4]float64) uint8 {
		top := c[0] + (c[1]-c[0])*fx
		bot := c[2] + (c[3]-c[2])*fx
		return uint8(top + (bot-top)*fy + 0.5)
	}
	return color.RGBA{R: lerp(r), G: lerp(g), B: lerp(b), A: 0xff}
}

// texel is pixel (px, py) of the whole world drawn at zoom z, clamped to
// the world.
func (s *pyramidSampler) texel(z uint8, px, py int64) (float64, float64, float64) {
	last := int64(TileSize)<<z - 1
	px, py = max(0, min(last, px)), max(0, min(last, py))
	tx, ty := uint32(px/TileSize), uint32(py/TileSize)
	c := &s.last[z]
	if c.img == nil || c.x != tx || c.y != ty {
		k := tileKey{z: z, x: tx, y: ty}
		tile, err := s.t.tile(s.ctx, k)
		if err != nil {
			if s.err == nil {
				s.err = err
			}
			return 0, 0, 0
		}
		s.used[k] = tile
		c.x, c.y, c.img = tx, ty, tile.Image
	}
	o := c.img.PixOffset(int(px%TileSize), int(py%TileSize))
	return float64(c.img.Pix[o]), float64(c.img.Pix[o+1]), float64(c.img.Pix[o+2])
}

// edgeFade is as texture's: the scene's area fades into the haze at its
// edge, whatever the tiles beyond it hold.
func (s *pyramidSampler) edgeFade(u, v float64) float64 {
	return fadeAt(u, v, s.w, s.h)
}

func (s *pyramidSampler) lod() float32 { return s.lastLOD }

// Lattice is a grid of points fixed to the ground, a whole number of cells
// apart: where a moving camera's mesh puts its vertices.
//
// A mesh laid over each frame's own view puts its vertices somewhere a
// little different in every frame, and reads the ground's height there: the
// hills are the same, but their outline is drawn through different points
// of them, and from one frame to the next a ridge wobbles. On a lattice
// every frame reads the height at the same points, and only the camera
// moves.
type Lattice struct {
	cell float64 // in Web Mercator's world units
}

// NewLattice is a lattice of cells about metres across at latitude lat --
// the middle of a flight: Web Mercator's scale changes with latitude, and a
// lattice has to be one size everywhere to be fixed.
func NewLattice(lat, metres float64) (Lattice, error) {
	if !(metres > 0) {
		return Lattice{}, fmt.Errorf("perspective: a lattice of %g m cells", metres)
	}
	return Lattice{cell: metres / metresPerUnit(lat)}, nil
}

// View is the scene view for the ground in b: b grown to whole cells of the
// lattice, at step pixels a cell, so that Scene.Step = step puts a vertex on
// every point of the lattice inside it.
func (l Lattice) View(b render.Bounds, step int) (render.View, error) {
	if step <= 0 {
		step = DefaultStep
	}
	x0, y0 := mercator.Project(b.West, b.North)
	x1, y1 := mercator.Project(b.East, b.South)
	// A vertex is at the centre of its cell of the grid, so the grid's
	// edges are half a cell off the lattice.
	snapLo := func(v float64) float64 { return (math.Floor(v/l.cell+0.5) - 0.5) * l.cell }
	snapHi := func(v float64) float64 { return (math.Ceil(v/l.cell-0.5) + 0.5) * l.cell }
	gx0, gy0, gx1, gy1 := snapLo(x0), snapLo(y0), snapHi(x1), snapHi(y1)
	cols := int(math.Round((gx1 - gx0) / l.cell))
	rows := int(math.Round((gy1 - gy0) / l.cell))
	if cols < 2 || rows < 2 {
		return render.View{}, fmt.Errorf("perspective: %d by %d cells is no mesh", cols, rows)
	}
	west, north := mercator.Unproject(gx0, gy0)
	east, south := mercator.Unproject(gx1, gy1)
	return render.View{Bounds: render.Bounds{West: west, South: south, East: east, North: north}, Width: cols * step, Height: rows * step}, nil
}
