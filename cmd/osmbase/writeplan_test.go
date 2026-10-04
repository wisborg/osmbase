package main

import (
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/slice"
)

// The count to fetch is the plan's Tiles, which already leaves out what the
// store holds and what the archive lacks: a plan for 20 more tiles over an
// area of which 59 are held and 3 absent says 20, not 20 less 62.
func TestWritePlan_CountsWhatWillBeFetched(t *testing.T) {
	var b strings.Builder
	writePlan(&b, &acquire.Plan{
		Archive: "planet.pmtiles", Zoom: slice.ZoomRange{Min: 12, Max: 14}, Cells: []slice.Cell{{X: 1, Y: 1}},
		CellsToFetch: 1, Tiles: 20, Held: 59, Absent: 3,
	}, "/store")
	if !strings.Contains(b.String(), "20 to fetch, 59 already held, 3 not in the archive") {
		t.Errorf("the plan reads:\n%s", b.String())
	}
}
