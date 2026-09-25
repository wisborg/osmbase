package slice_test

import (
	"context"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/slice"
)

// tileBoundsInside is the ground one tile covers, shrunk a hair so rounding at its
// edges does not reach the neighbours.
func tileBoundsInside(t *testing.T, z uint8, x, y uint32) slice.Bounds {
	t.Helper()
	w, s, e, n, err := mercator.TileBounds(z, x, y)
	if err != nil {
		t.Fatal(err)
	}
	d := (e - w) * 1e-6
	return slice.Bounds{West: w + d, South: s + d, East: e - d, North: n - d}
}

// A complete cell holds every tile beneath it at the zooms it was fetched
// over, and none at a zoom it was not -- which is the question a render at
// that zoom is asking.
func TestHeldAtCountsACompleteCellAtItsZooms(t *testing.T) {
	st, src, _ := filled(t)
	b := tileBoundsInside(t, st.CellZoom(), testCell.X, testCell.Y)
	for _, tc := range []struct {
		zoom         uint8
		held, wanted int
	}{
		{12, 1, 1},
		{13, 4, 4},
		{14, 16, 16},
		{15, 0, 64}, // below the range the cell was fetched over
	} {
		held, wanted, err := src.HeldAt(b, tc.zoom)
		if err != nil {
			t.Fatal(err)
		}
		if held != tc.held || wanted != tc.wanted {
			t.Errorf("zoom %d: %d of %d held, want %d of %d", tc.zoom, held, wanted, tc.held, tc.wanted)
		}
	}
}

// A tile the archive did not have is not missing from a complete cell.
// Counting files instead would call every stretch of open ocean missing, and
// a render over it would offer to download it on every run, for ever.
func TestHeldAtCountsATileTheArchiveLackedAsHeld(t *testing.T) {
	st := newStore(t, slice.Config{})
	tiles := pyramid(t, testCell, st.CellZoom(), testZooms)
	var gone slice.TileRef
	for ref := range tiles {
		if ref.Z == 14 {
			gone = ref
			break
		}
	}
	delete(tiles, gone)
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	if _, err := src.Fill(context.Background(), r, testCell, testZooms); err != nil {
		t.Fatal(err)
	}
	if src.Has(gone) {
		t.Fatal("precondition: the tile the archive lacked is on disk")
	}
	held, wanted, err := src.HeldAt(tileBoundsInside(t, st.CellZoom(), testCell.X, testCell.Y), 14)
	if err != nil {
		t.Fatal(err)
	}
	if held != wanted {
		t.Errorf("%d of %d held; the tile the archive lacked was counted as missing", held, wanted)
	}
}

// Above the cell zoom there is no cell, and a tile is held when it is on
// disk. A country-sized view is all tiles like this.
func TestHeldAtCountsOverviewTilesOnDisk(t *testing.T) {
	st := newStore(t, slice.Config{})
	anc := slice.TileRef{Z: 10, X: testCell.X >> 2, Y: testCell.Y >> 2}
	r := archive(t, map[slice.TileRef][]byte{anc: []byte("overview tile")})
	src := addSource(t, st, r, "archive.pmtiles")
	if _, err := src.FillOverview(context.Background(), r, []slice.TileRef{anc}); err != nil {
		t.Fatal(err)
	}
	b := tileBoundsInside(t, 10, anc.X, anc.Y)
	if held, wanted, _ := src.HeldAt(b, 10); held != 1 || wanted != 1 {
		t.Errorf("zoom 10: %d of %d held, want 1 of 1", held, wanted)
	}
	if held, wanted, _ := src.HeldAt(b, 11); held != 0 || wanted != 4 {
		t.Errorf("zoom 11: %d of %d held, want 0 of 4", held, wanted)
	}
}
