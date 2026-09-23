package osm

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// ring builds a closed way whose points make a square of the given size, so
// an assembled area has real extent to test a point against.
func squareWay(id int64, role string, lat, lon, side float64, nodes ...int64) Way {
	w := Way{ID: id, Role: role, Nodes: append(append([]int64{}, nodes...), nodes[0])}
	corners := []Point{
		{Lat: lat, Lon: lon},
		{Lat: lat, Lon: lon + side},
		{Lat: lat + side, Lon: lon + side},
		{Lat: lat + side, Lon: lon},
	}
	for i := range w.Nodes {
		w.Points = append(w.Points, corners[i%len(corners)])
	}
	return w
}

func TestAreasCarryTheAdminLevelAsTheirKind(t *testing.T) {
	got, rep, err := Areas([]Boundary{{
		ID: 1, Name: "Hornsby", AdminLevel: 9,
		Ways: []Way{squareWay(10, "outer", -33.7, 151.1, 0.1, 1, 2, 3, 4)},
	}})
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d areas, want 1", len(got))
	}
	if got[0].Name != "Hornsby" || got[0].Kind != "9" {
		t.Errorf("area = %q kind %q, want Hornsby kind 9", got[0].Name, got[0].Kind)
	}
	if rep.Complete != 1 || rep.Outlines != 1 {
		t.Errorf("report = %+v, want one complete boundary with one ring", rep)
	}
}

// A boundary with no closed outline has nothing to test a point against. It
// is dropped rather than written as a nameless hole in the map, and the
// report is how that is said.
func TestABoundaryWithNoOutlineIsDroppedAndCounted(t *testing.T) {
	got, rep, err := Areas([]Boundary{
		{ID: 1, Name: "Whole", AdminLevel: 9,
			Ways: []Way{squareWay(10, "outer", -33.7, 151.1, 0.1, 1, 2, 3, 4)}},
		{ID: 2, Name: "Cut off", AdminLevel: 9,
			Ways: []Way{{ID: 11, Nodes: []int64{1, 2}, Points: []Point{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}}}},
	})
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Whole" {
		t.Errorf("got %d areas, want only the one with an outline", len(got))
	}
	if rep.Unclosed != 1 || rep.Complete != 1 {
		t.Errorf("report = %+v, want one complete and one unclosed", rep)
	}
}

// boundary.inRing treats longitude as linear and says a source that does not
// pre-split at the seam "would be wrong in a way this cannot detect". This is
// that new source, so a ring which has wrapped is counted and left out rather
// than handed over to be tested by something that cannot see the problem.
func TestARingCrossingTheAntimeridianIsReportedNotWrittenSilently(t *testing.T) {
	w := Way{
		ID: 10, Role: "outer",
		Nodes: []int64{1, 2, 3, 4, 1},
		Points: []Point{
			{Lat: -16, Lon: 179.5},
			{Lat: -16, Lon: -179.5}, // a step of 359 degrees: the seam
			{Lat: -17, Lon: -179.5},
			{Lat: -17, Lon: 179.5},
			{Lat: -16, Lon: 179.5},
		},
	}
	got, rep, err := Areas([]Boundary{{ID: 1, Name: "Across the seam", AdminLevel: 4, Ways: []Way{w}}})
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	if rep.WrappedRings != 1 {
		t.Errorf("report = %+v, want the wrapped ring counted", rep)
	}
	if len(got) != 0 {
		t.Errorf("got %d areas, want none -- the only ring cannot be tested against", len(got))
	}
	if rep.Unclosed != 1 {
		t.Errorf("report = %+v, want the boundary counted as having no usable outline", rep)
	}
}

func TestAreasReportsAFailureToAssemble(t *testing.T) {
	_, _, err := Areas([]Boundary{{
		Name: "Broken",
		Ways: []Way{{ID: 1, Nodes: []int64{1, 2, 3}, Points: []Point{{}, {}}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "Broken") {
		t.Errorf("Areas: %v, want an error naming the boundary", err)
	}
}

// The credit has one spelling, and it is the one the derived file carries.
// A caller assembling its own is a caller that can leave it out.
func TestProvenanceCarriesTheCreditAndTheLevels(t *testing.T) {
	before := time.Now().Add(-time.Second)
	p := Provenance("denmark-latest.osm.pbf", []int{7, 8, 9})

	if p.Attribution != Attribution {
		t.Errorf("attribution = %q, want %q", p.Attribution, Attribution)
	}
	if !strings.Contains(p.Attribution, "ODbL") {
		t.Errorf("attribution %q does not name the licence share-alike attaches under", p.Attribution)
	}
	if p.Source != "denmark-latest.osm.pbf" {
		t.Errorf("source = %q", p.Source)
	}
	if !slices.Equal(p.Levels, []int{7, 8, 9}) {
		t.Errorf("levels = %v, want 7,8,9", p.Levels)
	}
	if p.Created.Before(before) || p.Created.After(time.Now().Add(time.Second)) {
		t.Errorf("created = %v, want about now", p.Created)
	}

	// Cloned, so a caller mutating its slice afterwards cannot change what
	// the file will say it was built for.
	levels := []int{7}
	q := Provenance("s", levels)
	levels[0] = 99
	if q.Levels[0] != 7 {
		t.Error("the provenance shares the caller's slice")
	}
}
