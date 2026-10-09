package fetch_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/slice"
)

// Only a yes is a yes; nothing is read when nobody can answer or the answer
// was given in advance; and an input that ends is no answer rather than a
// yes. Each is a different thing for the caller to say, so each is its own
// outcome.
func TestConsentAsksOnlyWhenSomebodyCanAnswer(t *testing.T) {
	yes := func() bool { return true }
	for _, tc := range []struct {
		name  string
		c     fetch.Consent
		want  fetch.Answer
		asked bool
	}{
		{"y", fetch.Consent{In: strings.NewReader("y\n"), Answerable: yes}, fetch.Accepted, true},
		{"YES", fetch.Consent{In: strings.NewReader("YES\n"), Answerable: yes}, fetch.Accepted, true},
		{"no", fetch.Consent{In: strings.NewReader("n\n"), Answerable: yes}, fetch.Declined, true},
		{"just enter", fetch.Consent{In: strings.NewReader("\n"), Answerable: yes}, fetch.Declined, true},
		{"yessir is not yes", fetch.Consent{In: strings.NewReader("yessir\n"), Answerable: yes}, fetch.Declined, true},
		{"the input ended", fetch.Consent{In: strings.NewReader(""), Answerable: yes}, fetch.NoAnswer, true},
		{"nobody there", fetch.Consent{In: strings.NewReader("y\n"), Answerable: func() bool { return false }}, fetch.Unattended, false},
		{"answered in advance", fetch.Consent{Yes: true, Answerable: func() bool { return false }}, fetch.Accepted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w bytes.Buffer
			if got := tc.c.Ask(&w, "Fetch? [y/N] "); got != tc.want {
				t.Errorf("Ask = %v, want %v", got, tc.want)
			}
			if asked := w.String() == "Fetch? [y/N] "; asked != tc.asked {
				t.Errorf("the question was written: %v, want %v (%q)", asked, tc.asked, w.String())
			}
		})
	}
}

// Fill fetches an area into a store, capping the depth at the archive's
// deepest zoom rather than asking the planner for a zoom it would refuse,
// and reports the plan before fetching.
func TestFillFetchesAnAreaToTheArchivesDeepestZoom(t *testing.T) {
	const archiveMax = 3
	a, err := fetch.Open(writeDeepArchive(t, archiveMax), fetch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	root := filepath.Join(t.TempDir(), "store")
	b := slice.Bounds{West: 0, South: 0, East: 10, North: 10}

	var planned *acquire.Plan
	res, err := fetch.Fill(context.Background(), root, a, "(c) test", acquire.Request{Bounds: b, MaxZoom: 9},
		func(p *acquire.Plan) { planned = p }, nil)
	if err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if planned == nil || planned.Empty() {
		t.Fatal("the plan was not reported, or planned nothing")
	}
	if res.Written == 0 {
		t.Error("nothing was fetched")
	}

	st, err := slice.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Sources()
	src, err := st.Source(ms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if held, wanted, _ := src.HeldAt(b, archiveMax); held != wanted {
		t.Errorf("after Fill the store holds %d of %d tiles at the archive's deepest zoom", held, wanted)
	}

	// And a store asked about zoom 9 over this archive is asked about 3, the
	// deepest it could ever hold.
	if z := fetch.DrawnZoom(src, 9); z != archiveMax {
		t.Errorf("DrawnZoom = %d, want %d", z, archiveMax)
	}
	if z := fetch.DrawnZoom(src, 2); z != 2 {
		t.Errorf("DrawnZoom for a shallower view = %d, want 2", z)
	}
	if z := fetch.DrawnZoom(nil, 9); z != 9 {
		t.Errorf("DrawnZoom with no source = %d, want 9", z)
	}

	// A second Fill of the same area has nothing to do, and says so by an
	// empty plan rather than by fetching again.
	res, err = fetch.Fill(context.Background(), root, a, "(c) test", acquire.Request{Bounds: b, MaxZoom: 9},
		func(p *acquire.Plan) { planned = p }, nil)
	if err != nil || res.Written != 0 {
		t.Errorf("a second Fill wrote %d tiles (%v), want none", res.Written, err)
	}
}

// A fill of several areas fills each cell to the depth its area asks for:
// one area to zoom 14 and another to the cell zoom alone come out recorded
// as exactly that, the shallow one not dragged to the deep one's depth.
func TestFillOfAreasFillsEachCellToItsOwnDepth(t *testing.T) {
	a, err := fetch.Open(writeDeepArchive(t, 14), fetch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	root := filepath.Join(t.TempDir(), "store")
	deep := slice.Bounds{West: 151.100, South: -33.710, East: 151.101, North: -33.709}
	shallow := slice.Bounds{West: 150.100, South: -33.710, East: 150.101, North: -33.709}
	_, err = fetch.Fill(context.Background(), root, a, "(c) test", acquire.Request{Areas: []acquire.Area{
		{Bounds: deep, MaxZoom: 14}, {Bounds: shallow, MaxZoom: slice.DefaultCellZoom},
	}}, nil, nil)
	if err != nil {
		t.Fatalf("Fill: %v", err)
	}
	st, err := slice.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Sources()
	src, err := st.Source(ms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		b    slice.Bounds
		want uint8
	}{{deep, 14}, {shallow, slice.DefaultCellZoom}} {
		cells, _ := slice.CellsForZoom(c.b, slice.DefaultCellZoom)
		for _, cell := range cells {
			info, complete, err := src.Cell(cell)
			if err != nil || !complete {
				t.Fatalf("cell %v not complete (%v)", cell, err)
			}
			if info.Zoom.Max != c.want {
				t.Errorf("cell %v filled to zoom %d, want %d", cell, info.Zoom.Max, c.want)
			}
		}
	}
}

// A fetch made of one group -- here every tile above the cell zoom, as a
// flight's corridor is -- says how far it has got as each of the group's
// ranges arrives, not only when all of them have: before this, such a
// fetch showed nothing moved until it was done.
func TestFillReportsProgressWithinAGroup(t *testing.T) {
	a, err := fetch.Open(writeDeepArchive(t, 3), fetch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	root := filepath.Join(t.TempDir(), "store")
	var planned *acquire.Plan
	var seen []acquire.Progress
	_, err = fetch.Fill(context.Background(), root, a, "(c) test",
		acquire.Request{Bounds: slice.Bounds{West: -170, South: -80, East: 170, North: 80}, MaxZoom: 3,
			Limits: acquire.Limits{MaxRequest: 64, MaxGap: 0}},
		func(p *acquire.Plan) { planned = p }, func(pr acquire.Progress) { seen = append(seen, pr) })
	if err != nil {
		t.Fatal(err)
	}
	if planned == nil || len(planned.Groups) != 1 || planned.Requests < 3 {
		t.Fatalf("precondition: want one group of several requests, got %d groups, %d requests", len(planned.Groups), planned.Requests)
	}
	before := 0
	prev := int64(0)
	for _, pr := range seen {
		if pr.DoneTransfer < prev {
			t.Errorf("progress went back from %d to %d", prev, pr.DoneTransfer)
		}
		prev = pr.DoneTransfer
		if !pr.GroupDone && pr.DoneTransfer > 0 && pr.DoneTransfer < planned.Transfer {
			before++
		}
	}
	if before == 0 {
		t.Errorf("no progress reported before the group finished; calls: %+v", seen)
	}
	if last := seen[len(seen)-1]; !last.GroupDone || last.DoneTransfer != planned.Transfer {
		t.Errorf("the last call %+v, want the group done at the plan's %d bytes", last, planned.Transfer)
	}
}
