package acquire

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// TileIndex is the half of an archive a plan is made from: where each tile
// lives, without reading it.
//
// *pmtiles.Reader satisfies it. The interface exists so that a test can plan
// against something that is not an archive at all, and so that this package's
// planning half can be read without reference to a format.
type TileIndex interface {
	Locate(z uint8, x, y uint32) (pmtiles.Location, bool, error)
}

// Archive is what a fetch plans against and reads from.
//
// It is a PAIR rather than a single interface because the two halves come from
// different places: the index is a *pmtiles.Reader, which walks directories,
// and the bytes are whatever that reader was built over -- an *os.File or a
// *RangeReader. Binding them together once, here, is what stops a plan made
// against one archive being fetched from another. That failure would not look
// like a failure: the offsets would be in range, the bytes would arrive, and
// the store would fill with plausible wrong tiles under the right names.
type Archive struct {
	// Index answers where a tile lives.
	Index TileIndex
	// Bytes is the archive itself, read in coalesced ranges.
	Bytes io.ReaderAt
	// Name is the archive's URL or path, for messages and for the store's
	// manifest. It must already be safe to print: a RangeReader's URL method
	// gives the redacted form, and that is the one to pass.
	Name string
}

func (a Archive) validate() error {
	switch {
	case a.Index == nil:
		return fmt.Errorf("acquire: archive %q has no index to plan from", a.Name)
	case a.Bytes == nil:
		return fmt.Errorf("acquire: archive %q has no bytes to read", a.Name)
	case a.Name == "":
		return fmt.Errorf("acquire: an archive needs a name, so a store can record where its tiles came from")
	}
	return nil
}

// MaxPlanCells bounds how many cells one fetch may cover.
//
// The cost being bounded is PLANNING, not the download: a cell is eighty-five
// directory lookups, and although the leaf cache makes most of them free, a
// request every so often against a remote archive is not. 1,024 cells is about
// 250 km across at the default cell zoom, which is exactly where DepthFor
// hands over to a global slice -- so the limit is reached by the rule rather
// than by a user, and a user who overrides the depth gets an error that says
// what to do instead.
const MaxPlanCells = 1024

// AutoZoom asks PlanFor to choose the depth from the area's extent rather than
// being told it.
//
// It is -1 because zero is a real zoom -- the whole world in one tile -- so it
// cannot double as the sentinel.
const AutoZoom = -1

// Request is what to fetch.
type Request struct {
	// Bounds is the ground to cover. It is ignored when World is set.
	Bounds slice.Bounds

	// World asks for every tile on earth at shallow zooms instead of cells
	// over an area. See Depth.World.
	World bool

	// MaxZoom is the deepest zoom to take, or AutoZoom to choose it from the
	// extent.
	MaxZoom int

	// CellZoom is the store's own cell zoom, from its store.json. It is passed
	// in rather than read from the store so that a plan can be made for a
	// store that does not exist yet, which is what a dry run against a fresh
	// machine does.
	CellZoom uint8

	// SourceZoom is what the archive offers, from its header. The depth is
	// capped at its maximum: public builds stop at zoom 15 and there is
	// nothing deeper to fetch.
	SourceZoom slice.ZoomRange

	// Limits bound coalescing. The zero value is the defaults.
	Limits Limits
}

// Group is one unit of a fetch: the shared overview, or one cell.
//
// A fetch works group by group, and that granularity is the store's rather
// than this package's. A cell is written with its cell.json LAST, so a fetch
// killed part way through leaves finished cells and one unfinished directory
// that the store reads back as incomplete and a later run resumes. Coalescing
// within a group rather than across the whole plan follows from the same
// thing: the buffer a fetch holds is bounded by one cell, and a cell's tiles
// are written together.
type Group struct {
	// Cell is the cell this group fills. Overview is true instead for the
	// shallow tiles above the cell zoom, which belong to no cell, are shared
	// between every cell beneath them, and are never evicted.
	Cell     slice.Cell
	Overview bool

	// Refs is every tile of the group, in the order the store enumerates them.
	// It is what FillOverview is given; a cell group is filled by zoom range
	// instead, which the store enumerates for itself from the same function.
	Refs []slice.TileRef

	// Tiles are the ones this fetch will actually transfer: present in the
	// archive and not already on disk. Ascending by offset.
	Tiles []PlannedTile

	// Held is tiles already in the store and Absent tiles the archive does not
	// hold. Absent is not a gap -- most coordinates in most archives have no
	// tile -- which is why a cell over open water is a few kilobytes.
	Held, Absent int

	// Ranges are the requests this group costs, and Transfer their total.
	// Bytes is what lands on disk, which differs from Transfer in both
	// directions: coalescing adds bytes nobody wants, and a run of identical
	// tiles is transferred once and written many times.
	Ranges   []Range
	Transfer int64
	Bytes    int64
}

// Label names a group for a progress line.
func (g Group) Label() string {
	if g.Overview {
		return "overview"
	}
	return "cell " + g.Cell.String()
}

// PlannedTile is one tile a fetch will store, and where its bytes are.
type PlannedTile struct {
	Ref slice.TileRef
	At  pmtiles.Location
}

// Plan is exactly what a fetch will do and exactly what it will cost.
//
// Every number here is read from the archive's own directories, so none of it
// is an estimate. That is a property of the format rather than a claim about
// this code, and it is worth building the interface around: a confirmation
// prompt that can state the true cost is a different thing from one that
// guesses. See docs/architecture.md, "Acquisition: the only network access".
type Plan struct {
	// Archive is where the tiles come from, and Bounds the ground covered.
	Archive string
	Bounds  slice.Bounds

	// Depth is the zoom chosen and, when it was chosen rather than given, why.
	Depth Depth

	// CellZoom is the store's, Zoom the range the cells are taken over, and
	// Overview the shallow range above them. Zoom is empty for a global slice,
	// which lives entirely in the overview.
	CellZoom uint8
	Zoom     slice.ZoomRange
	Overview slice.ZoomRange

	// Cells is every cell the area touches, in row-major order, whether or not
	// this fetch has anything to get for it. CellsToFetch counts the ones that
	// do.
	Cells        []slice.Cell
	CellsToFetch int

	// Groups are the units a fetch works in: the overview first, then the
	// cells. The overview is first so that a fetch interrupted after one cell
	// has still left the shallow tiles that make a partly covered area degrade
	// into a coarser map instead of vanishing.
	Groups []Group

	// Tiles is what will be written, Held what is already on disk, and Absent
	// what the archive does not hold.
	Tiles, Held, Absent int

	// Bytes is what will land on disk and Transfer what will come over the
	// wire. They are different questions: coalescing fetches some bytes nobody
	// asked for, and a run of identical tiles is transferred once and written
	// as many files.
	Bytes    int64
	Transfer int64

	// Requests is how many range requests the transfer takes, and Waste how
	// much of Transfer is data no tile in this plan needed. Transfer minus
	// Waste is the distinct tile bytes.
	Requests int
	Waste    int64

	Limits Limits
}

// Empty reports whether there is nothing left to fetch, which is what a
// finished area looks like when the command is run again.
func (p *Plan) Empty() bool { return p.Tiles == 0 }

// PlanFor works out exactly which tiles an area needs, where they are, and
// what they cost.
//
// It reads the archive's directories and NOTHING ELSE. No byte of the tile
// data section is touched, which is what makes a dry run a real dry run and
// what makes the figure it reports the one the download will actually move.
//
// dst is the store's source, and it may be nil: a nil store holds nothing,
// which is what a plan costs on a machine that has never fetched. A store that
// does hold some of these tiles is asked, tile by tile, so a RESUMED fetch
// costs only what is missing -- the same question slice.Source.Fill asks when
// it skips a tile, asked through the same method, so the two cannot disagree
// about what is already there.
func PlanFor(ctx context.Context, a Archive, dst *slice.Source, req Request) (*Plan, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if req.CellZoom == 0 || req.CellZoom > slice.MaxCellZoom {
		return nil, fmt.Errorf("acquire: cell zoom %d is not a zoom a store keys its cells at", req.CellZoom)
	}
	if err := req.SourceZoom.Validate(); err != nil {
		return nil, fmt.Errorf("acquire: the archive's own zoom range: %w", err)
	}

	depth, err := depthFor(req)
	if err != nil {
		return nil, err
	}

	p := &Plan{
		Archive:  a.Name,
		Bounds:   req.Bounds,
		Depth:    depth,
		CellZoom: req.CellZoom,
		Limits:   req.Limits,
	}
	if depth.World {
		p.Bounds = WorldBounds()
	}

	// The overview: the shallow tiles above the cell zoom, shared between
	// every cell beneath them. For a global slice they are the whole thing.
	overviewRefs, err := overviewTiles(p.Bounds, req.CellZoom, depth)
	if err != nil {
		return nil, err
	}
	p.Overview = rangeOf(overviewRefs)
	if len(overviewRefs) > 0 {
		g, err := planGroup(ctx, a, dst, Group{Overview: true, Refs: overviewRefs}, req.Limits)
		if err != nil {
			return nil, err
		}
		p.Groups = append(p.Groups, g)
	}

	// The cells, if the depth reaches them at all. A depth shallower than the
	// cell zoom is a legitimate request -- a coarse slice over a wide area --
	// and it simply has no cell half.
	if !depth.World && depth.Max >= req.CellZoom {
		p.Zoom = slice.ZoomRange{Min: req.CellZoom, Max: depth.Max}
		cells, err := cellsOf(p.Bounds, req.CellZoom)
		if err != nil {
			return nil, err
		}
		p.Cells = cells
		for _, c := range cells {
			refs, err := slice.PyramidTiles(c, req.CellZoom, p.Zoom)
			if err != nil {
				return nil, err
			}
			g, err := planGroup(ctx, a, dst, Group{Cell: c, Refs: refs}, req.Limits)
			if err != nil {
				return nil, err
			}
			if len(g.Tiles) > 0 {
				p.CellsToFetch++
			}
			p.Groups = append(p.Groups, g)
		}
	} else if !depth.World {
		// The cells are still reported, because "this area touches four cells
		// and none of them is being filled" is the fact that explains why a
		// render here will be coarse.
		cells, err := cellsOf(p.Bounds, req.CellZoom)
		if err != nil {
			return nil, err
		}
		p.Cells = cells
		p.Zoom = slice.EmptyZoomRange()
	} else {
		p.Zoom = slice.EmptyZoomRange()
	}

	var useful int64
	for _, g := range p.Groups {
		p.Tiles += len(g.Tiles)
		p.Held += g.Held
		p.Absent += g.Absent
		p.Bytes += g.Bytes
		p.Transfer += g.Transfer
		p.Requests += len(g.Ranges)
		useful += usefulBytes(locationsOf(g.Tiles))
	}
	p.Waste = p.Transfer - useful
	return p, nil
}

// depthFor resolves the request's depth, whether it was given or is to be
// chosen.
func depthFor(req Request) (Depth, error) {
	if req.World {
		d := Depth{Max: WorldMaxZoom, World: true}
		if req.MaxZoom != AutoZoom {
			if req.MaxZoom < 0 || req.MaxZoom > 255 {
				return Depth{}, fmt.Errorf("acquire: zoom %d is not a zoom level", req.MaxZoom)
			}
			d.Max = uint8(req.MaxZoom)
		}
		if d.Max > req.SourceZoom.Max {
			d.Max = req.SourceZoom.Max
		}
		return d, nil
	}
	if req.MaxZoom == AutoZoom {
		return DepthFor(req.Bounds, req.SourceZoom.Max)
	}
	if req.MaxZoom < 0 || req.MaxZoom > 255 {
		return Depth{}, fmt.Errorf("acquire: zoom %d is not a zoom level", req.MaxZoom)
	}
	if err := validBounds(req.Bounds); err != nil {
		return Depth{}, err
	}
	if uint8(req.MaxZoom) > req.SourceZoom.Max {
		return Depth{}, fmt.Errorf("acquire: zoom %d is deeper than the zoom %d this archive holds, and there is nothing below that to fetch", req.MaxZoom, req.SourceZoom.Max)
	}
	return Depth{Max: uint8(req.MaxZoom)}, nil
}

// overviewTiles lists the shallow tiles a fetch needs, deduplicated and in a
// deterministic order.
//
// For a global slice that is every tile on earth to the chosen depth. For an
// area it is the ancestor chain above each of its cells, which neighbouring
// cells share almost entirely -- twelve tiles for one cell at the default cell
// zoom, and not many more for a hundred of them.
//
// The list is capped at the cell zoom because that is the boundary the store
// draws: a tile at or below the cell zoom belongs to a cell and is written by
// Fill, and FillOverview refuses it rather than putting a second copy in the
// shared directory.
func overviewTiles(b slice.Bounds, cellZoom uint8, d Depth) ([]slice.TileRef, error) {
	top := d.Max
	if top >= cellZoom {
		top = cellZoom - 1
	}
	if d.World {
		// WorldTiles has its own cap, and it is the right one: 4^z tiles is a
		// planet download by another name past a certain zoom, and the message
		// that says so belongs beside the enumeration rather than here.
		return slice.WorldTiles(slice.ZoomRange{Min: 0, Max: top})
	}
	if cellZoom == 0 {
		return nil, nil
	}
	cells, err := cellsOf(b, cellZoom)
	if err != nil {
		return nil, err
	}
	seen := make(map[slice.TileRef]bool)
	var out []slice.TileRef
	for _, c := range cells {
		refs, err := slice.AncestorTiles(c, cellZoom, slice.ZoomRange{Min: 0, Max: top})
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// cellsOf is slice's own cell arithmetic with this package's cap on top of it.
func cellsOf(b slice.Bounds, cellZoom uint8) ([]slice.Cell, error) {
	cells, err := slice.CellsForZoom(b, cellZoom)
	if err != nil {
		return nil, err
	}
	if len(cells) > MaxPlanCells {
		return nil, fmt.Errorf("acquire: that area is %d cells at cell zoom %d, and one fetch plans at most %d; ask for a smaller area, or for the whole world at a shallow zoom, which is every tile on earth for what one city costs at street detail",
			len(cells), cellZoom, MaxPlanCells)
	}
	return cells, nil
}

// planGroup locates every tile of one group and coalesces what is missing.
func planGroup(ctx context.Context, a Archive, dst *slice.Source, g Group, lim Limits) (Group, error) {
	for _, ref := range g.Refs {
		if err := ctx.Err(); err != nil {
			return Group{}, fmt.Errorf("acquire: planning %s of %s: %w", g.Label(), a.Name, err)
		}
		// Asked BEFORE the archive is consulted, so a resumed fetch costs
		// nothing at all for ground it already holds -- not even the directory
		// lookup. Has is the same method Fill uses to decide whether to skip a
		// tile, which is what keeps the plan and the fill from disagreeing
		// about what is on disk.
		if dst != nil && dst.Has(ref) {
			g.Held++
			continue
		}
		loc, ok, err := a.Index.Locate(ref.Z, ref.X, ref.Y)
		if err != nil {
			return Group{}, fmt.Errorf("acquire: locating tile %s in %s: %w", ref, a.Name, err)
		}
		if !ok {
			g.Absent++
			continue
		}
		g.Tiles = append(g.Tiles, PlannedTile{Ref: ref, At: loc})
		g.Bytes += loc.Length
	}
	// Ascending by offset, which is the order the ranges are built in and the
	// order a fetch reads them. Refs arrive in the store's enumeration order,
	// which is by zoom and then row-major, and that is not the archive's.
	sortByOffset(g.Tiles)

	ranges, err := coalesce(locationsOf(g.Tiles), lim)
	if err != nil {
		return Group{}, err
	}
	g.Ranges = ranges
	g.Transfer = totalBytes(ranges)
	return g, nil
}

func locationsOf(tiles []PlannedTile) []pmtiles.Location {
	out := make([]pmtiles.Location, len(tiles))
	for i, t := range tiles {
		out[i] = t.At
	}
	return out
}

func sortByOffset(tiles []PlannedTile) {
	// An insertion-order-stable sort, so two tiles sharing one location keep
	// the order the store enumerated them in and a plan is reproducible.
	slices.SortStableFunc(tiles, func(a, b PlannedTile) int {
		if a.At.Offset != b.At.Offset {
			return cmpInt64(a.At.Offset, b.At.Offset)
		}
		return cmpInt64(a.At.Length, b.At.Length)
	})
}

// rangeOf is the zoom range a list of tile references spans, or the empty
// range for an empty list.
func rangeOf(refs []slice.TileRef) slice.ZoomRange {
	if len(refs) == 0 {
		return slice.EmptyZoomRange()
	}
	lo, hi := refs[0].Z, refs[0].Z
	for _, r := range refs[1:] {
		lo, hi = min(lo, r.Z), max(hi, r.Z)
	}
	return slice.ZoomRange{Min: lo, Max: hi}
}
