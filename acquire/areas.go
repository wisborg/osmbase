package acquire

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/wisborg/osmbase/slice"
)

// Area is one piece of ground a fetch covers and how deep it goes there.
//
// A fetch of one rectangle at one depth is the right shape for a map of one
// place: the whole of it is seen at about one scale. It is the wrong shape
// for ground seen at many scales at once, and badly wrong for ground strung
// out along a route. A camera flying after a run sees the ground under it at
// street detail and the hills on the horizon at a fraction of that; a camera
// following a flight across a continent sees a corridor of country at a
// depth no street map would take. One rectangle round all of it, at the
// deepest of those depths, asks for a quarter of a continent at street
// detail; at the shallowest, for ground too coarse to fly over. A list of
// areas, each at the depth it is seen at, asks for what is drawn.
type Area struct {
	Bounds  slice.Bounds
	MaxZoom uint8
}

// planAreas is PlanFor for a request of several areas: the union of what
// each asks for, every cell filled to the deepest any area asks of it, and
// the shallow tiles above the cells only where some area reaches them.
func planAreas(ctx context.Context, a Archive, dst *slice.Source, req Request) (*Plan, error) {
	p := &Plan{Archive: a.Name, CellZoom: req.CellZoom, Limits: req.Limits}
	overview := map[slice.TileRef]bool{}
	depths := map[slice.Cell]uint8{}
	first := true
	for _, ar := range req.Areas {
		if err := validBounds(ar.Bounds); err != nil {
			return nil, err
		}
		d := min(ar.MaxZoom, req.SourceZoom.Max)
		p.Depth.Max = max(p.Depth.Max, d)
		if first {
			p.Bounds, first = ar.Bounds, false
		} else {
			p.Bounds = slice.Bounds{
				West: math.Min(p.Bounds.West, ar.Bounds.West), South: math.Min(p.Bounds.South, ar.Bounds.South),
				East: math.Max(p.Bounds.East, ar.Bounds.East), North: math.Max(p.Bounds.North, ar.Bounds.North),
			}
		}
		refs, err := overviewTiles(ar.Bounds, req.CellZoom, Depth{Max: d})
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			overview[r] = true
		}
		if len(overview) > MaxOverviewTiles {
			return nil, fmt.Errorf("acquire: those areas are more than %d tiles above the cell zoom, which is not an overview any more; ask for shallower zooms or less ground", MaxOverviewTiles)
		}
		if d < req.CellZoom {
			continue
		}
		cells, err := slice.CellsForZoom(ar.Bounds, req.CellZoom)
		if err != nil {
			return nil, err
		}
		for _, c := range cells {
			depths[c] = max(depths[c], d)
		}
		if len(depths) > MaxPlanCells {
			return nil, fmt.Errorf("acquire: those areas are more than %d cells at cell zoom %d, and one fetch plans at most %d; ask for shallower zooms or less ground",
				len(depths), req.CellZoom, MaxPlanCells)
		}
	}
	p.Depth.Why = fmt.Sprintf("chosen for each of %d areas from the detail it is seen at", len(req.Areas))

	refs := make([]slice.TileRef, 0, len(overview))
	for r := range overview {
		refs = append(refs, r)
	}
	slices.SortFunc(refs, func(a, b slice.TileRef) int {
		return cmp.Or(cmp.Compare(a.Z, b.Z), cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X))
	})
	p.Overview = rangeOf(refs)
	if len(refs) > 0 {
		g, err := planGroup(ctx, a, dst, Group{Overview: true, Refs: refs}, req.Limits)
		if err != nil {
			return nil, err
		}
		p.Groups = append(p.Groups, g)
	}

	p.Zoom = slice.EmptyZoomRange()
	for c := range depths {
		p.Cells = append(p.Cells, c)
	}
	slices.SortFunc(p.Cells, func(a, b slice.Cell) int { return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X)) })
	for _, c := range p.Cells {
		z := slice.ZoomRange{Min: req.CellZoom, Max: depths[c]}
		if p.Zoom.Empty() {
			p.Zoom = z
		} else {
			p.Zoom.Max = max(p.Zoom.Max, z.Max)
		}
		cellRefs, err := slice.PyramidTiles(c, req.CellZoom, z)
		if err != nil {
			return nil, err
		}
		g, err := planGroup(ctx, a, dst, Group{Cell: c, Refs: cellRefs, Depth: z.Max}, req.Limits)
		if err != nil {
			return nil, err
		}
		if len(g.Tiles) > 0 {
			p.CellsToFetch++
		}
		p.Groups = append(p.Groups, g)
	}
	p.total()
	return p, nil
}
