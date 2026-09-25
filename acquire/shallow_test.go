package acquire

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// everyTile is an index holding every tile there is, each at its own offset,
// so a plan lists exactly the tiles it asked for and nothing is Absent.
type everyTile struct{ asked int }

func (e *everyTile) Locate(z uint8, x, y uint32) (pmtiles.Location, bool, error) {
	e.asked++
	return pmtiles.Location{Offset: int64(e.asked) * 10, Length: 10}, true, nil
}

func shallowPlan(t *testing.T, b slice.Bounds, maxZoom int) (*Plan, error) {
	t.Helper()
	return PlanFor(context.Background(),
		Archive{Index: &everyTile{}, Bytes: bytes.NewReader(nil), Name: "test.pmtiles"}, nil,
		Request{Bounds: b, MaxZoom: maxZoom, CellZoom: slice.DefaultCellZoom,
			SourceZoom: slice.ZoomRange{Min: 0, Max: 15}})
}

// denmark is roughly the country's box, which at the default cell zoom is
// several thousand cells -- past MaxPlanCells.
var denmark = slice.Bounds{West: 8.0, South: 54.5, East: 15.2, North: 57.8}

// A country fits an image at about zoom 7, which is above the cell grid. The
// planner reached those tiles through the cells beneath them, so asking for
// Denmark at zoom 7 enumerated several thousand zoom-12 cells to find a few
// dozen tiles, and was refused by the cell limit before planning any of them.
func TestAShallowPlanOverACountryListsItsTilesWithoutTheCells(t *testing.T) {
	if cells, _ := slice.CellsForZoom(denmark, slice.DefaultCellZoom); len(cells) <= MaxPlanCells {
		t.Fatalf("precondition: Denmark is %d cells, within the limit, so this proves nothing", len(cells))
	}
	p, err := shallowPlan(t, denmark, 7)
	if err != nil {
		t.Fatalf("planning Denmark at zoom 7: %v", err)
	}

	var want []slice.TileRef
	for z := uint8(0); z <= 7; z++ {
		cs, err := slice.CellsForZoom(denmark, z)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			want = append(want, slice.TileRef{Z: z, X: c.X, Y: c.Y})
		}
	}
	if len(p.Groups) != 1 || !p.Groups[0].Overview {
		t.Fatalf("got %d groups, want the one overview group", len(p.Groups))
	}
	got := p.Groups[0].Refs
	if !sameRefs(got, want) {
		t.Errorf("planned %d tiles, want the %d covering Denmark at zooms 0 to 7", len(got), len(want))
	}
	if len(got) > 100 {
		t.Errorf("planned %d tiles for a country at zoom 7; that is not an overview", len(got))
	}
}

// Listing the tiles from the bounds is the same set the cells' ancestors
// gave, wherever the old way worked -- so a small area plans exactly as it
// did before.
func TestAShallowPlanOverASmallAreaIsTheCellsAncestors(t *testing.T) {
	b := slice.Bounds{West: 151.0, South: -34.0, East: 151.4, North: -33.6}
	p, err := shallowPlan(t, b, 10)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	cells, err := slice.CellsForZoom(b, slice.DefaultCellZoom)
	if err != nil {
		t.Fatal(err)
	}
	var want []slice.TileRef
	for _, c := range cells {
		refs, err := slice.AncestorTiles(c, slice.DefaultCellZoom, slice.ZoomRange{Min: 0, Max: 10})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, refs...)
	}
	if !sameRefs(p.Groups[0].Refs, want) {
		t.Errorf("planned %v, want the cells' ancestors %v", p.Groups[0].Refs, dedupe(want))
	}
	// And the cells are still reported where there are few enough to list,
	// because "this area touches N cells and none is being filled" is what
	// explains a coarse render.
	if len(p.Cells) != len(cells) {
		t.Errorf("reported %d cells, want %d", len(p.Cells), len(cells))
	}
}

// Past the limit the cells are not listed at all, rather than enumerated to
// be counted: at zoom 12 a continent is millions of them.
func TestAShallowPlanOverAContinentDoesNotListItsCells(t *testing.T) {
	europe := slice.Bounds{West: -10, South: 35, East: 40, North: 70}
	p, err := shallowPlan(t, europe, 5)
	if err != nil {
		t.Fatalf("planning Europe at zoom 5: %v", err)
	}
	if p.Cells != nil {
		t.Errorf("listed %d cells for a continent", len(p.Cells))
	}
}

// The tiles are bounded instead. A whole-world box at zoom 11 is four million
// tiles, which is a planet download by another name, and the refusal says to
// ask for less.
func TestAShallowPlanIsBoundedInTiles(t *testing.T) {
	world := slice.Bounds{West: -180, South: -85, East: 180, North: 85}
	_, err := shallowPlan(t, world, 11)
	if err == nil {
		t.Fatal("a whole-world box at zoom 11 was planned")
	}
	if !strings.Contains(err.Error(), "tiles") {
		t.Errorf("error %q does not say what was too many", err)
	}
}

func dedupe(refs []slice.TileRef) []slice.TileRef {
	seen := map[slice.TileRef]bool{}
	var out []slice.TileRef
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func sameRefs(a, b []slice.TileRef) bool {
	a, b = dedupe(a), dedupe(b)
	key := func(r slice.TileRef) uint64 { return uint64(r.Z)<<56 | uint64(r.X)<<28 | uint64(r.Y) }
	sortRefs := func(rs []slice.TileRef) {
		slices.SortFunc(rs, func(x, y slice.TileRef) int {
			switch {
			case key(x) < key(y):
				return -1
			case key(x) > key(y):
				return 1
			}
			return 0
		})
	}
	sortRefs(a)
	sortRefs(b)
	return slices.Equal(a, b)
}
