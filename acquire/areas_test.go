package acquire

import (
	"bytes"
	"context"
	"testing"

	"github.com/wisborg/osmbase/slice"
)

func areasPlan(t *testing.T, areas ...Area) *Plan {
	t.Helper()
	p, err := PlanFor(context.Background(),
		Archive{Index: &everyTile{}, Bytes: bytes.NewReader(nil), Name: "test.pmtiles"}, nil,
		Request{Areas: areas, CellZoom: slice.DefaultCellZoom, SourceZoom: slice.ZoomRange{Min: 0, Max: 15}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A cell is filled to the deepest depth any area asks of it, and only the
// cells of areas deep enough to reach the cell zoom are filled at all.
func TestAreas_EachCellToTheDeepestAskedOfIt(t *testing.T) {
	street := slice.Bounds{West: 151.10, South: -33.71, East: 151.11, North: -33.70}
	hills := slice.Bounds{West: 151.05, South: -33.75, East: 151.15, North: -33.65}
	wide := slice.Bounds{West: 150.0, South: -35.0, East: 152.0, North: -33.0}
	p := areasPlan(t, Area{Bounds: street, MaxZoom: 15}, Area{Bounds: hills, MaxZoom: 13}, Area{Bounds: wide, MaxZoom: 9})

	deep, _ := slice.CellsForZoom(street, slice.DefaultCellZoom)
	all, _ := slice.CellsForZoom(hills, slice.DefaultCellZoom)
	isDeep := map[slice.Cell]bool{}
	for _, c := range deep {
		isDeep[c] = true
	}
	if len(p.Cells) != len(all) {
		t.Errorf("%d cells planned, want the %d of the two areas reaching the cell zoom -- not the wide shallow one's", len(p.Cells), len(all))
	}
	for _, g := range p.Groups {
		if g.Overview {
			continue
		}
		want := uint8(13)
		if isDeep[g.Cell] {
			want = 15
		}
		if g.Depth != want {
			t.Errorf("cell %v filled to zoom %d, want %d", g.Cell, g.Depth, want)
		}
		top := uint8(0)
		for _, r := range g.Refs {
			top = max(top, r.Z)
		}
		if top != want {
			t.Errorf("cell %v lists tiles to zoom %d, want %d", g.Cell, top, want)
		}
	}
	if p.Depth.Max != 15 || p.Zoom.Max != 15 || p.Zoom.Min != slice.DefaultCellZoom {
		t.Errorf("depth %d, zooms %v; want 15 and the cell zoom to 15", p.Depth.Max, p.Zoom)
	}
}

// Shallow areas strung along a corridor ask for the shallow tiles along it,
// not for those of the rectangle round it: here a diagonal across a few
// degrees, where the rectangle is several times the corridor.
func TestAreas_ACorridorListsItsOwnOverview(t *testing.T) {
	var areas []Area
	for i := range 20 {
		lon, lat := 140.0+float64(i)*0.5, -20.0-float64(i)*0.5
		areas = append(areas, Area{Bounds: slice.Bounds{West: lon, South: lat - 0.5, East: lon + 0.5, North: lat}, MaxZoom: 10})
	}
	p := areasPlan(t, areas...)
	if len(p.Cells) != 0 {
		t.Errorf("%d cells planned for ground seen at zoom 10, above the cell zoom", len(p.Cells))
	}
	if len(p.Groups) != 1 || !p.Groups[0].Overview {
		t.Fatalf("%d groups, want the overview alone", len(p.Groups))
	}
	box := slice.Bounds{West: 140, South: -30, East: 150, North: -20}
	n, err := slice.CellCount(box, 10)
	if err != nil {
		t.Fatal(err)
	}
	corridor := 0
	for _, r := range p.Groups[0].Refs {
		if r.Z == 10 {
			corridor++
		}
	}
	if corridor == 0 || corridor*4 > n {
		t.Errorf("%d tiles at zoom 10 along the corridor, against %d for the rectangle round it; want a small share", corridor, n)
	}
}

// An area asking deeper than the archive goes is taken as deep as it goes.
func TestAreas_ADepthIsCappedAtTheArchives(t *testing.T) {
	street := slice.Bounds{West: 151.10, South: -33.71, East: 151.11, North: -33.70}
	p := areasPlan(t, Area{Bounds: street, MaxZoom: 18})
	if p.Depth.Max != 15 {
		t.Errorf("depth %d from an area asking 18 of an archive holding 15", p.Depth.Max)
	}
}

// More areas than one plan takes are split into batches each plan takes,
// in their order, losing none: a strip of street-level areas along a
// corridor long enough to be over MaxPlanCells cells.
func TestSplitAreasKeepsEachBatchWithinAPlan(t *testing.T) {
	var areas []Area
	for i := range 1500 {
		lon := -100.0 + float64(i)*0.09 // about one zoom 12 cell apart
		areas = append(areas, Area{Bounds: slice.Bounds{West: lon, South: -30.01, East: lon + 0.01, North: -30.0}, MaxZoom: 14})
	}
	batches, err := SplitAreas(areas, slice.DefaultCellZoom, 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) < 2 {
		t.Fatalf("%d batches for areas over %d cells", len(batches), MaxPlanCells)
	}
	n := 0
	for i, b := range batches {
		for _, a := range b {
			if a != areas[n] {
				t.Fatalf("batch %d: area %d out of order", i, n)
			}
			n++
		}
		p, err := PlanFor(context.Background(),
			Archive{Index: &everyTile{}, Bytes: bytes.NewReader(nil), Name: "test.pmtiles"}, nil,
			Request{Areas: b, CellZoom: slice.DefaultCellZoom, SourceZoom: slice.ZoomRange{Min: 0, Max: 15}})
		if err != nil {
			t.Fatalf("batch %d does not plan: %v", i, err)
		}
		if len(p.Cells) > MaxPlanCells {
			t.Errorf("batch %d plans %d cells", i, len(p.Cells))
		}
	}
	if n != len(areas) {
		t.Errorf("%d of %d areas in the batches", n, len(areas))
	}
	// The whole would not have planned.
	if _, err := PlanFor(context.Background(),
		Archive{Index: &everyTile{}, Bytes: bytes.NewReader(nil), Name: "test.pmtiles"}, nil,
		Request{Areas: areas, CellZoom: slice.DefaultCellZoom, SourceZoom: slice.ZoomRange{Min: 0, Max: 15}}); err == nil {
		t.Error("precondition: all the areas planned as one, so the split proves nothing")
	}
}
