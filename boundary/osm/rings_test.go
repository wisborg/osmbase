package osm

import (
	"math"
	"slices"
	"testing"
)

// at gives each node id a distinct coordinate, so a test that joined on
// coordinates rather than on ids would still look right until it is asked
// not to.
func at(id int64) Point { return Point{Lat: 55 + float64(id)/100, Lon: 9 + float64(id)/100} }

// seg builds a way through the given node ids, with the role.
func seg(id int64, role string, nodes ...int64) Way {
	w := Way{ID: id, Role: role, Nodes: slices.Clone(nodes)}
	for _, n := range nodes {
		w.Points = append(w.Points, at(n))
	}
	return w
}

// A square drawn as four separate coordinates, so winding is unambiguous.
func square4(id int64, role string, nodes ...int64) Way {
	w := Way{ID: id, Role: role, Nodes: slices.Clone(nodes)}
	corner := map[int64]Point{
		1: {Lat: 0, Lon: 0},
		2: {Lat: 0, Lon: 1},
		3: {Lat: 1, Lon: 1},
		4: {Lat: 1, Lon: 0},
	}
	for _, n := range nodes {
		w.Points = append(w.Points, corner[n])
	}
	return w
}

func TestWaysJoinIntoARing(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "outer", 1, 2, 3),
		seg(11, "outer", 3, 4, 1),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 || len(got.Inner) != 0 || len(got.Open) != 0 {
		t.Fatalf("got %d outer, %d inner, %d open; want one closed outer", len(got.Outer), len(got.Inner), len(got.Open))
	}
	// Four corners, with the closing vertex not repeated.
	if len(got.Outer[0]) != 4 {
		t.Errorf("the ring has %d points, want 4 with no repeated closing vertex: %v", len(got.Outer[0]), got.Outer[0])
	}
}

// A relation's ways arrive in no order and no direction: cut at every
// junction, listed as the mapper happened to add them, half of them running
// the other way. Every arrangement of the same outline must give the same
// ring.
func TestWaysJoinWhateverOrderAndDirectionTheyArriveIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"in order, all forward", []Way{seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1)}},
		{"shuffled", []Way{seg(12, "", 3, 1), seg(10, "", 1, 2), seg(11, "", 2, 3)}},
		{"some reversed", []Way{seg(10, "", 2, 1), seg(11, "", 3, 2), seg(12, "", 3, 1)}},
		{"all reversed", []Way{seg(10, "", 2, 1), seg(11, "", 3, 2), seg(12, "", 1, 3)}},
		{"shuffled and reversed", []Way{seg(11, "", 3, 2), seg(12, "", 1, 3), seg(10, "", 2, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != 1 || len(got.Open) != 0 {
				t.Fatalf("got %d rings and %d open chains, want one ring", len(got.Outer), len(got.Open))
			}
			if len(got.Outer[0]) != 3 {
				t.Errorf("the ring has %d points, want 3", len(got.Outer[0]))
			}
		})
	}
}

// A run may start in the middle of an outline, in which case extending the
// tail exhausts one direction and the other is still open behind it.
func TestAChainStartedInTheMiddleExtendsBothWays(t *testing.T) {
	// Deliberately started at the middle way, and the outline is open, so
	// only the backward extension can pick up the first way.
	got, err := Assemble([]Way{
		seg(11, "", 2, 3),
		seg(10, "", 1, 2),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Open) != 1 {
		t.Fatalf("got %d open chains, want 1: %+v", len(got.Open), got)
	}
	c := got.Open[0]
	if c.Ways != 2 {
		t.Errorf("the chain joined %d ways, want both", c.Ways)
	}
	if len(c.Points) != 3 {
		t.Errorf("the chain has %d points, want 3", len(c.Points))
	}
	if (c.From != 1 || c.To != 3) && (c.From != 3 || c.To != 1) {
		t.Errorf("the chain runs from %d to %d, want the loose ends 1 and 3", c.From, c.To)
	}
}

// An outline that cannot close is ordinary input, not a failure: most of the
// ways Denmark's boundary relations name lie outside a Denmark extract.
func TestAnOutlineThatCannotCloseIsReportedNotDropped(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "outer", 1, 2, 3),
		// The way from 3 back to 1 is beyond the extract.
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 0 {
		t.Errorf("got %d closed rings from an outline with a gap", len(got.Outer))
	}
	if len(got.Open) != 1 {
		t.Fatalf("got %d open chains, want 1", len(got.Open))
	}
	c := got.Open[0]
	// A single way that joined to nothing comes back the way the file had
	// it, so the ends read in the order its nodes did.
	if c.From != 1 || c.To != 3 {
		t.Errorf("the gap is reported between %d and %d, want 1 and 3", c.From, c.To)
	}
	if c.Role != "outer" {
		t.Errorf("the chain has role %q, want outer", c.Role)
	}
	if len(c.Points) != 3 {
		t.Errorf("the chain kept %d points, want all 3 -- a caller drawing a map wants them", len(c.Points))
	}
}

// One relation may enclose several separate areas, and a gap in one must not
// cost the others.
func TestSeveralRingsAndAGapCoexist(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1), // a ring
		seg(20, "", 5, 6), seg(21, "", 6, 7), seg(22, "", 7, 5), // another
		seg(30, "", 8, 9), // a fragment
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 2 {
		t.Errorf("got %d rings, want 2", len(got.Outer))
	}
	if len(got.Open) != 1 {
		t.Errorf("got %d open chains, want 1", len(got.Open))
	}
}

// A way that is already a closed loop on its own is a ring without joining.
func TestAWayClosedOnItsOwnIsARing(t *testing.T) {
	got, err := Assemble([]Way{seg(10, "", 1, 2, 3, 4, 1)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 || len(got.Open) != 0 {
		t.Fatalf("got %d rings and %d open, want one ring", len(got.Outer), len(got.Open))
	}
	if len(got.Outer[0]) != 4 {
		t.Errorf("the ring has %d points, want 4 with the closing vertex dropped", len(got.Outer[0]))
	}
}

// The role decides which bucket a ring lands in, and an empty role means
// outer -- the convention on boundary relations, and common enough that
// treating it as its own kind would leave most outlines in neither bucket.
func TestRolesSortTheRings(t *testing.T) {
	got, err := Assemble([]Way{
		square4(10, "outer", 1, 2, 3, 4, 1),
		seg(20, "inner", 5, 6, 7, 5),
		seg(30, "", 8, 9, 10, 8),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 2 {
		t.Errorf("got %d outer rings, want 2 -- the empty role is an outer", len(got.Outer))
	}
	if len(got.Inner) != 1 {
		t.Errorf("got %d inner rings, want 1", len(got.Inner))
	}
}

// RFC 7946: an outer ring winds counterclockwise and a hole clockwise,
// whichever way the file happened to store them.
func TestRingsAreOrientedWhicheverWayTheyArrive(t *testing.T) {
	// The same square, drawn both ways round.
	ccw := []int64{1, 2, 3, 4, 1}
	cw := []int64{1, 4, 3, 2, 1}

	for _, tc := range []struct {
		name  string
		nodes []int64
	}{
		{"already counterclockwise", ccw},
		{"stored clockwise", cw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble([]Way{square4(10, "outer", tc.nodes...)})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != 1 {
				t.Fatalf("got %d outer rings", len(got.Outer))
			}
			if w := got.Outer[0].winding(); w != counterclockwise {
				t.Errorf("the outer ring winds %d, want counterclockwise", w)
			}
		})

		t.Run(tc.name+" as a hole", func(t *testing.T) {
			got, err := Assemble([]Way{square4(10, "inner", tc.nodes...)})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Inner) != 1 {
				t.Fatalf("got %d inner rings", len(got.Inner))
			}
			if w := got.Inner[0].winding(); w != clockwise {
				t.Errorf("the inner ring winds %d, want clockwise", w)
			}
		})
	}
}

// Reorienting a ring must not change the loop, only the direction it is
// walked in. The set of points has to survive.
func TestOrientingKeepsEveryPoint(t *testing.T) {
	got, err := Assemble([]Way{square4(10, "outer", 1, 4, 3, 2, 1)}) // clockwise input
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	r := got.Outer[0]
	if len(r) != 4 {
		t.Fatalf("the ring has %d points, want 4", len(r))
	}
	var sumLat, sumLon float64
	for _, p := range r {
		sumLat += p.Lat
		sumLon += p.Lon
	}
	// The unit square's corners sum to 2,2 whichever way round they are.
	if math.Abs(sumLat-2) > 1e-9 || math.Abs(sumLon-2) > 1e-9 {
		t.Errorf("the reoriented ring's points sum to %f,%f, want 2,2 -- a point was lost or duplicated", sumLat, sumLon)
	}
}

// Joining is on node id, not on coordinates. Two distinct nodes at the same
// place are ordinary where an extract has been cut, and welding them would
// join outlines that merely touch.
func TestOutlinesThatMerelyTouchAreNotJoined(t *testing.T) {
	// Two open outlines whose ends sit at exactly the same two coordinates
	// and share no node id. Joined by coordinate they would close into a
	// ring; joined by id they stay apart, which is the truth.
	a := Way{ID: 10, Nodes: []int64{1, 2, 3}, Points: []Point{at(1), at(2), at(3)}}
	b := Way{ID: 11, Nodes: []int64{98, 4, 99}, Points: []Point{at(3), at(4), at(1)}}

	got, err := Assemble([]Way{a, b})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 0 {
		t.Errorf("two outlines sharing coordinates but no node were welded into %d ring(s)", len(got.Outer))
	}
	if len(got.Open) != 2 {
		t.Errorf("got %d open chains, want 2", len(got.Open))
	}
}

// Two points joined back to themselves enclose nothing. Reading that as a
// ring would put a degenerate polygon into the output, which is worse than a
// reported gap.
func TestARingMustEncloseSomething(t *testing.T) {
	got, err := Assemble([]Way{seg(10, "", 1, 2), seg(11, "", 2, 1)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 0 {
		t.Errorf("two ways out and back enclosed %d ring(s)", len(got.Outer))
	}
}

// A way with fewer than two nodes joins to nothing. Malformed, and reported
// rather than dropped, so an outline that comes up short says why.
func TestADegenerateWayIsReported(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "", 1, 2, 3), seg(11, "", 3, 4, 1),
		{ID: 12, Nodes: []int64{7}, Points: []Point{at(7)}},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 {
		t.Errorf("got %d rings, want the good outline still assembled", len(got.Outer))
	}
	if len(got.Open) != 1 {
		t.Errorf("got %d open chains, want the degenerate way reported", len(got.Open))
	}
}

func TestAWayWhoseIdsAndPointsDisagreeIsRefused(t *testing.T) {
	_, err := Assemble([]Way{{ID: 10, Nodes: []int64{1, 2, 3}, Points: []Point{at(1), at(2)}}})
	if err == nil {
		t.Error("a way with three ids and two points was accepted")
	}
}

func TestAssemblingNothingGivesNothing(t *testing.T) {
	got, err := Assemble(nil)
	if err != nil {
		t.Fatalf("Assemble(nil): %v", err)
	}
	if len(got.Outer) != 0 || len(got.Inner) != 0 || len(got.Open) != 0 {
		t.Errorf("Assemble(nil) = %+v, want nothing", got)
	}
}

// A way with no references at all is degenerate but permitted, and Read
// hands one straight through -- so assembly has to survive it. It has no
// ends to report, which is the case that dereferenced an empty slice.
func TestAWayWithNoNodesIsReportedNotFatal(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "", 1, 2, 3), seg(11, "", 3, 4, 1),
		{ID: 12}, // no nodes, no points
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 {
		t.Errorf("got %d rings, want the good outline still assembled", len(got.Outer))
	}
	if len(got.Open) != 1 {
		t.Fatalf("got %d open chains, want the empty way reported", len(got.Open))
	}
	if c := got.Open[0]; len(c.Points) != 0 || c.From != 0 || c.To != 0 {
		t.Errorf("the empty way is reported as %+v, want no points and no ends", c)
	}
}
