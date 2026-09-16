package slice

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wisborg/osmbase/mercator"
)

// DefaultCellZoom is the zoom a new store uses for its cells when the caller
// names none.
//
// Zoom 12 is about 9.8 km times the cosine of the latitude, so 8.2 km at
// Sydney's: a 10 km run touches one to four cells and a 100 km ride fifteen to
// twenty-five. Zoom 13 gives finer eviction and less waste on a short run, at
// four times the cell directories; zoom 11 wastes twenty megabytes of city on
// a five kilometre run.
//
// It is a DEFAULT and never a constant the rest of this package reads. Every
// path, every lookup and every coverage answer takes the cell zoom from the
// store's own manifest, so a store written with a different one keeps working
// and a later change to this number re-keys a new store rather than silently
// orphaning the cells of an old one. See docs/architecture.md, "The cache".
const DefaultCellZoom = 12

// MaxCellZoom bounds what a store may be created with. It is not a property of
// the grid -- the tile scheme goes to zoom 31 -- but of the arithmetic below:
// a cell holds 4^(maxZoom-cellZoom) tiles, so a deep cell zoom with a deeper
// maximum is a directory nobody can fetch and nothing can evict in one piece.
const MaxCellZoom = 20

// Bounds is a geographic rectangle in degrees.
//
// It is the same four numbers render.Bounds carries, and it is declared again
// here rather than imported because this package must not depend on the
// renderer: the store is what the renderer reads FROM, and an import in that
// direction would put the two in a cycle the moment the renderer's source
// interface moved. The design gives both of these to a root osmbase package
// that does not exist yet; when it does, both become aliases for one type.
type Bounds struct {
	West, South, East, North float64
}

// validate refuses a rectangle that names no area.
//
// A rectangle crossing the antimeridian -- west 179, east -179 -- is refused
// rather than handled, for the same reason the renderer refuses it: handling
// it means splitting every cell range in two, and the alternative to refusing
// is quietly answering about the other 358 degrees of the world.
func (b Bounds) validate() error {
	for _, c := range []struct {
		name  string
		v     float64
		limit float64
	}{
		{"west", b.West, 180}, {"east", b.East, 180},
		{"south", b.South, 90}, {"north", b.North, 90},
	} {
		if c.v != c.v {
			return fmt.Errorf("slice: the %s edge is not a coordinate", c.name)
		}
		if c.v < -c.limit || c.v > c.limit {
			return fmt.Errorf("slice: the %s edge is %g, outside -%g to %g", c.name, c.v, c.limit, c.limit)
		}
	}
	if b.East < b.West {
		return fmt.Errorf("slice: the east edge (%g) is west of the west edge (%g); an area crossing the antimeridian has to be asked about as two", b.East, b.West)
	}
	if b.North < b.South {
		return fmt.Errorf("slice: the north edge (%g) is south of the south edge (%g)", b.North, b.South)
	}
	return nil
}

// Cell is one fetch-and-eviction unit: a tile at the store's cell zoom,
// standing for itself and every tile beneath it.
//
// It carries no zoom, because within one store there is only one cell zoom and
// it is recorded in the store's manifest. A Cell from one store means nothing
// in a store built with a different cell zoom, which is why the manifest
// re-keys the directory rather than being read as advice.
type Cell struct {
	X, Y uint32
}

func (c Cell) String() string { return fmt.Sprintf("%d_%d", c.X, c.Y) }

// TileRef names one stored tile. It is the STORAGE unit, and it is keyed on
// the coordinates and nothing else.
//
// Not the view, not the pixel size, not the style. Re-rendering the same route
// at 1080p and at 4K reads the same files, and changing the theme reads the
// same files. This is the explicit fix for a defect in fitdash's own imagery
// cache, which hashes the requested pixel width and height into its key and
// therefore misses on every resolution change, re-fetching imagery it already
// holds. Anything that puts a second dimension in this struct reintroduces it.
type TileRef struct {
	Z    uint8
	X, Y uint32
}

func (t TileRef) String() string { return fmt.Sprintf("%d/%d/%d", t.Z, t.X, t.Y) }

// ZoomRange is a closed range of zoom levels: a slice is a zoom RANGE over an
// area, and both ends of it come from the caller.
//
// A run wants zoom 12 to 15 over eight kilometres. A flight wants zoom 0 to 5
// over the whole world, which is every tile on earth in 19.5 MB. Neither is
// the special case; the pair is the input. See docs/architecture.md, "Depth
// follows the extent, and a global track is the cheap case".
type ZoomRange struct {
	Min, Max uint8
}

func (z ZoomRange) String() string { return fmt.Sprintf("%d-%d", z.Min, z.Max) }

// Empty reports whether the range covers no zoom at all.
//
// It is exported alongside EmptyZoomRange because the zero value is a TRAP: it
// reads as "zoom 0 to 0", so a caller that built a range and then asked
// whether it held anything would be told it holds the whole world at its
// shallowest. Anything outside this package that carries a ZoomRange needs
// both halves of that.
func (z ZoomRange) Empty() bool { return z.Max < z.Min }

func (z ZoomRange) empty() bool { return z.Empty() }

// contains reports whether a zoom is inside the range.
func (z ZoomRange) contains(zoom uint8) bool { return zoom >= z.Min && zoom <= z.Max }

// union is the narrowest range holding both, which is how a manifest's depth
// grows as fetches land: a store that held the world at 0 to 5 and then took a
// city at 12 to 15 holds 0 to 15, with nothing at all in the middle for that
// city's neighbours. The range is a statement about the ends, not a promise
// about every zoom between them -- coverage is the question that answers that,
// cell by cell.
func (z ZoomRange) union(o ZoomRange) ZoomRange {
	switch {
	case z.empty():
		return o
	case o.empty():
		return z
	}
	return ZoomRange{Min: min(z.Min, o.Min), Max: max(z.Max, o.Max)}
}

// emptyZoom is the range of a slice that holds nothing yet.
//
// A range whose maximum is below its minimum covers no zoom at all, which is
// exactly the statement being made. The zero value cannot be used for it: it
// reads as "zoom 0 to 0", so a brand-new source with nothing in it would claim
// to hold the whole world at its shallowest, and a coverage report would say
// so.
var emptyZoom = EmptyZoomRange()

// EmptyZoomRange is the range of a slice that holds nothing. See emptyZoom for
// why the zero value will not do.
func EmptyZoomRange() ZoomRange { return ZoomRange{Min: 1, Max: 0} }

// Validate refuses a range that covers no zoom or reaches past the tile grid.
//
// It is exported because the acquisition step is handed a zoom range from an
// archive's header, which is a number somebody else wrote, and the check it
// needs is the one this package already makes rather than a second spelling of
// it.
func (z ZoomRange) Validate() error { return z.validate() }

func (z ZoomRange) validate() error {
	if z.empty() {
		return fmt.Errorf("slice: zoom range %d to %d runs backwards", z.Min, z.Max)
	}
	if z.Max > mercator.MaxZoom {
		return fmt.Errorf("slice: zoom %d is deeper than zoom %d, the deepest the tile grid addresses", z.Max, mercator.MaxZoom)
	}
	return nil
}

// cellOf returns the cell a tile belongs to: its ancestor at the cell zoom.
//
// A tile shallower than the cell zoom has no cell. That is not an edge case to
// be smoothed over -- it is the whole overview half of the store, and the
// boolean is what keeps the two apart at every call site.
func cellOf(t TileRef, cellZoom uint8) (Cell, bool) {
	if t.Z < cellZoom {
		return Cell{}, false
	}
	shift := t.Z - cellZoom
	return Cell{X: t.X >> shift, Y: t.Y >> shift}, true
}

// cellsFor returns every cell the rectangle touches, rounded OUTWARD to whole
// cells, in row-major order.
//
// Rounding outward is nearly free where it matters. A zoom-12 cell's pyramid
// is 8.9 MB for the City of London and 6 KB mid-Pacific, so the cell a route
// clips the corner of costs what is in it -- which, for the empty ground that
// generous rounding tends to pick up, is close to nothing.
//
// What it is NOT free for is an edge that lands exactly on a cell boundary,
// and that is what the slack is about. The bounds are degrees, they reach here
// through a projection and often through an inverse projection before that, so
// a boundary meant to be exact arrives as itself plus or minus a few parts in
// 10^15. Without the slack that difference decides whether a whole extra
// column of cells is asked for -- cells overlapping by a nanometre, each one a
// download and a directory. The renderer makes the same allowance for the same
// reason, at a much smaller cost; here a spurious cell is megabytes.
//
// A billionth of a cell is about thirty micrometres of ground at zoom 12.
//
// The order is row-major and deterministic because it is the order a fetch
// plan will be built in and the order an eviction report will list, and
// neither should depend on how a map felt like iterating.
// CellsForZoom is cellsFor for a caller that has no Store.
//
// Store.CellsFor is the ordinary way in, and it is the right one wherever a
// store exists, because the store's own cell zoom is the authority for how it
// is keyed. This exists for the one case that has no store yet: a dry run on a
// machine that has never fetched has to cost an area without bringing a store
// into being, since writing one is exactly what a dry run promises not to do.
func CellsForZoom(b Bounds, cellZoom uint8) ([]Cell, error) { return cellsFor(b, cellZoom) }

func cellsFor(b Bounds, cellZoom uint8) ([]Cell, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if err := checkCellZoom(cellZoom); err != nil {
		return nil, err
	}
	n := math.Exp2(float64(cellZoom))
	last := uint32(1)<<cellZoom - 1
	// The north-west corner gives the LOW indices because tile y runs south
	// from the top of the projection. Reversing this pair is the classic way
	// to get an empty cell list for a perfectly good rectangle.
	wx0, wy0 := mercator.Project(b.West, b.North)
	wx1, wy1 := mercator.Project(b.East, b.South)
	x0, x1 := cellSpan(wx0, wx1, n, last)
	y0, y1 := cellSpan(wy0, wy1, n, last)

	cells := make([]Cell, 0, (int(x1-x0)+1)*(int(y1-y0)+1))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			cells = append(cells, Cell{X: x, Y: y})
		}
	}
	return cells, nil
}

// cellSpan turns one axis of a rectangle, in normalised world units, into the
// first and last cell index it covers.
func cellSpan(worldMin, worldMax, n float64, last uint32) (uint32, uint32) {
	lo := gridIndex(math.Floor(worldMin*n+cellEdgeSlack), last)
	// Ceiling minus one, so a rectangle whose edge falls exactly on a cell
	// boundary does not ask for the cell beyond it, which would cover no
	// ground at all.
	hi := gridIndex(math.Ceil(worldMax*n-cellEdgeSlack)-1, last)
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// cellEdgeSlack is how much of a cell's width an overlap has to exceed before
// the cell is worth fetching. See cellsFor.
const cellEdgeSlack = 1e-9

// gridIndex clamps a cell index to the grid.
//
// A rectangle may legitimately extend past the top or bottom of the world --
// the Mercator square is finite and a bounding box around a track near the
// pole is not -- and past the antimeridian only through arithmetic, since
// validate refuses a rectangle that crosses it. Clamping rather than wrapping
// means the ground beyond the edge has no cell, which is the truth: there is
// nothing there to fetch.
func gridIndex(v float64, last uint32) uint32 {
	if v < 0 {
		return 0
	}
	if v > float64(last) {
		return last
	}
	return uint32(v)
}

// PyramidTiles lists every tile of a cell's sub-pyramid within a zoom range,
// shallowest first and row-major within each zoom.
//
// It is exported because the fetch planner is a separate step and must agree
// with the store about which tiles a cell consists of, down to the order. Two
// implementations of this list is how a fetch comes to write tiles the store
// never looks for.
//
// Zooms shallower than the cell zoom are not part of any cell and are skipped:
// they are the overview, shared between cells and stored once per source.
func PyramidTiles(c Cell, cellZoom uint8, z ZoomRange) ([]TileRef, error) {
	if err := z.validate(); err != nil {
		return nil, err
	}
	if err := checkCellZoom(cellZoom); err != nil {
		return nil, err
	}
	if z.Max > cellZoom+depthLimit {
		return nil, fmt.Errorf("slice: zoom %d is %d levels below the cell zoom %d, which is %d tiles in one cell; the deepest public tile builds stop at 15", z.Max, z.Max-cellZoom, cellZoom, 1<<(2*(z.Max-cellZoom)))
	}
	var out []TileRef
	for zoom := max(z.Min, cellZoom); zoom <= z.Max; zoom++ {
		shift := zoom - cellZoom
		n := uint32(1) << shift
		for y := uint32(0); y < n; y++ {
			for x := uint32(0); x < n; x++ {
				out = append(out, TileRef{Z: zoom, X: c.X<<shift | x, Y: c.Y<<shift | y})
			}
		}
	}
	return out, nil
}

// depthLimit caps how far below the cell zoom PyramidTiles will enumerate.
//
// Six levels is 4,096 tiles in one cell, which is already far past anything a
// public build offers: they stop at zoom 15, three levels below the default
// cell zoom, and there is nothing deeper to fetch. The cap is here so that a
// transposed argument asks for an error instead of for a billion tile
// references. See docs/architecture.md, trap T4.
const depthLimit = 6

// AncestorTiles lists the shallow tiles above a cell, from zoom z.Min up to
// but not including the cell zoom, shallowest first.
//
// These are the twelve files a default store keeps per source rather than per
// cell. They are shared between every cell that descends from them, they are
// never evicted, and they are what makes a partially evicted area degrade into
// a coarser map instead of vanishing.
func AncestorTiles(c Cell, cellZoom uint8, z ZoomRange) ([]TileRef, error) {
	if err := z.validate(); err != nil {
		return nil, err
	}
	if err := checkCellZoom(cellZoom); err != nil {
		return nil, err
	}
	var out []TileRef
	for zoom := z.Min; zoom < cellZoom && zoom <= z.Max; zoom++ {
		shift := cellZoom - zoom
		out = append(out, TileRef{Z: zoom, X: c.X >> shift, Y: c.Y >> shift})
	}
	return out, nil
}

// WorldTiles lists every tile on earth within a zoom range, shallowest first
// and row-major within each zoom.
//
// This is the whole of what a global slice needs, and it is a list rather than
// a special case for a reason worth stating: zooms 0 to 5 are 1,365 tiles and
// 19.5 MB, which is what one city cell costs at street detail. A track from
// Melbourne to Toulouse wants no bounding box, no cell arithmetic and no
// corridor -- it wants this list. See docs/architecture.md, "Depth follows the
// extent, and a global track is the cheap case".
//
// The range is capped because the count is 4^z: zoom 8 is 87,381 tiles for the
// whole range and zoom 10 is 1.4 million, which is not an overview any more,
// it is a planet download by another name.
func WorldTiles(z ZoomRange) ([]TileRef, error) {
	if err := z.validate(); err != nil {
		return nil, err
	}
	if z.Max > worldZoomLimit {
		return nil, fmt.Errorf("slice: zoom %d is %d tiles for the whole world; a global set is for the shallow zooms, and anything deeper wants cells over an area", z.Max, uint64(1)<<(2*z.Max))
	}
	var out []TileRef
	for zoom := z.Min; zoom <= z.Max; zoom++ {
		n := uint32(1) << zoom
		for y := uint32(0); y < n; y++ {
			for x := uint32(0); x < n; x++ {
				out = append(out, TileRef{Z: zoom, X: x, Y: y})
			}
		}
	}
	return out, nil
}

// worldZoomLimit is the deepest zoom WorldTiles will enumerate the planet at.
// Zoom 8 is 65,536 tiles at that level alone.
const worldZoomLimit = 8

func checkCellZoom(z uint8) error {
	if z > MaxCellZoom {
		return fmt.Errorf("slice: cell zoom %d is deeper than %d; a cell that small is a directory per street", z, MaxCellZoom)
	}
	return nil
}

// Paths.
//
// Every component below is a number this package computed or a hex source ID
// it derived, so nothing a user typed reaches a path element. That is worth
// keeping: a source name is a URL, and a URL contains slashes and dots and
// occasionally a credential.

func (s *Source) sourceDir() string { return filepath.Join(s.store.root, s.id) }

func (s *Source) overviewPath(t TileRef) string {
	return filepath.Join(s.sourceDir(), "overview", u8(t.Z), u32(t.X), u32(t.Y)+tileExt)
}

func (s *Source) cellsDir() string { return filepath.Join(s.sourceDir(), "cells") }

func (s *Source) cellDir(c Cell) string {
	return filepath.Join(s.cellsDir(), u32(c.X)+"_"+u32(c.Y))
}

// tilePath is where a tile lives, which depends only on its coordinates and on
// the store's cell zoom.
func (s *Source) tilePath(t TileRef) string {
	c, ok := cellOf(t, s.store.cellZoom)
	if !ok {
		return s.overviewPath(t)
	}
	return filepath.Join(s.cellDir(c), u8(t.Z), u32(t.X), u32(t.Y)+tileExt)
}

// tileExt names what is in the file and not how it is encoded.
//
// The bytes are stored exactly as the archive held them, which for every real
// build means gzip-compressed MVT -- so the file is not directly readable and
// the extension does not say so. The encoding is in the manifest, once per
// source, because that is where it can be changed by a source that uses
// another one rather than by renaming ten thousand files.
const tileExt = ".mvt"

// parseCellDir reads a cell directory name back into a Cell.
func parseCellDir(name string) (Cell, bool) {
	xs, ys, ok := strings.Cut(name, "_")
	if !ok {
		return Cell{}, false
	}
	x, err := strconv.ParseUint(xs, 10, 32)
	if err != nil {
		return Cell{}, false
	}
	y, err := strconv.ParseUint(ys, 10, 32)
	if err != nil {
		return Cell{}, false
	}
	return Cell{X: uint32(x), Y: uint32(y)}, true
}

func u8(v uint8) string   { return strconv.FormatUint(uint64(v), 10) }
func u32(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
