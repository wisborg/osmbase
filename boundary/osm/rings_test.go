package osm

import (
	"math"
	"slices"
	"testing"
)

// at gives each node id a distinct coordinate, so a test that joined on
// coordinates rather than on ids would still look right until it is asked
// not to.
//
// Placed around a circle rather than along a line. The first version of this
// spread the points along lat == lon, which made every "ring" in this file
// collinear and enclosing nothing -- the joining was being tested correctly
// and the geometry was degenerate throughout, which only showed up once
// winding learned to say so. The golden angle keeps successive ids far apart
// and the varying radius keeps any three of them off a straight line.
func at(id int64) Point {
	const goldenAngle = 2.399963229728653
	a := float64(id) * goldenAngle
	r := 0.01 + 0.001*float64(id%5)
	return Point{Lat: 55 + r*math.Sin(a), Lon: 9 + r*math.Cos(a)}
}

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

// onOctagon places node ids 1 to 8 at the eighths of a circle of one degree
// about 20E 10N.
//
// Derived from the geometry, not from what winding() happens to return: node
// n sits at longitude 20+cos θ and latitude 10+sin θ with θ = 2π(n-1)/8, and
// increasing θ is by definition the counterclockwise direction in a plane
// whose x is longitude east and whose y is latitude north. So an outline
// through ascending ids winds counterclockwise and one through descending ids
// winds clockwise, and that is known before any of this code runs.
//
// It exists because at() puts every node on one line: 55+id/100 by 9+id/100
// is a straight line, so every ring at() builds encloses zero area and has no
// orientation for a test to be wrong about. Any test that means to say
// something about shape needs coordinates with a shape.
func onOctagon(id int64) Point {
	th := 2 * math.Pi * float64(id-1) / 8
	return Point{Lat: 10 + math.Sin(th), Lon: 20 + math.Cos(th)}
}

// shaped builds a way through the given node ids, taking each id's
// coordinate from place.
func shaped(id int64, role string, place func(int64) Point, nodes ...int64) Way {
	w := Way{ID: id, Role: role, Nodes: slices.Clone(nodes)}
	for _, n := range nodes {
		w.Points = append(w.Points, place(n))
	}
	return w
}

// arc builds a way through the given node ids with octagon geometry.
func arc(id int64, role string, nodes ...int64) Way {
	return shaped(id, role, onOctagon, nodes...)
}

// corners lists the coordinates of the node ids, in the order given.
func corners(place func(int64) Point, ids ...int64) []Point {
	out := make([]Point, 0, len(ids))
	for _, id := range ids {
		out = append(out, place(id))
	}
	return out
}

// assertRingIsCycle checks that got is want read as a cycle: a ring has no
// first corner, so assembly may begin anywhere in it, but the ORDER of the
// corners and the direction they are walked in are the geometry itself and
// have to match exactly.
func assertRingIsCycle(t *testing.T, got Ring, want []Point) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the ring has %d corners, want %d:\n got %v\nwant %v", len(got), len(want), got, want)
	}
	start := slices.Index(got, want[0])
	if start < 0 {
		t.Fatalf("the ring does not contain the outline's first corner %v:\n got %v", want[0], got)
	}
	for i := range want {
		if g := got[(start+i)%len(got)]; g != want[i] {
			t.Fatalf("the ring's corner %d after %v is %v, want %v.\nThe corners are in the wrong order, so the polygon is not the outline:\n got %v\nwant a cycle of %v",
				i, want[0], g, want[i], got, want)
		}
	}
}

// The corners of an assembled ring have to come out in the order the outline
// runs, not merely in the right number.
//
// The expected ring is the octagon 1..8 read as a cycle, which is fixed by
// the fixture and not by this code: whichever way the chain happened to be
// built, an OUTER ring is reoriented counterclockwise, and ascending ids are
// counterclockwise by construction.
//
// Counting corners is not enough, and this is not hypothetical. A reverse()
// that turned the node ids round and left the coordinates alone, or an append
// that reversed a way's ids without reversing its points, produces a ring of
// exactly the right length whose corners are shuffled -- a self-crossing
// polygon that contains the wrong places -- and every other test in this file
// passes.
func TestARingsCornersRunInTheOutlineOrder(t *testing.T) {
	want := corners(onOctagon, 1, 2, 3, 4, 5, 6, 7, 8)

	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"in order, all forward", []Way{
			arc(10, "outer", 1, 2, 3), arc(11, "outer", 3, 4, 5),
			arc(12, "outer", 5, 6, 7), arc(13, "outer", 7, 8, 1)}},
		{"shuffled", []Way{
			arc(12, "outer", 5, 6, 7), arc(10, "outer", 1, 2, 3),
			arc(13, "outer", 7, 8, 1), arc(11, "outer", 3, 4, 5)}},
		{"some reversed", []Way{
			arc(10, "outer", 3, 2, 1), arc(11, "outer", 3, 4, 5),
			arc(12, "outer", 7, 6, 5), arc(13, "outer", 7, 8, 1)}},
		{"all reversed", []Way{
			arc(10, "outer", 3, 2, 1), arc(11, "outer", 5, 4, 3),
			arc(12, "outer", 7, 6, 5), arc(13, "outer", 1, 8, 7)}},
		// The chain has to start in the middle and extend backwards, which
		// is the path that reverses the whole chain.
		{"shuffled and reversed", []Way{
			arc(11, "outer", 5, 4, 3), arc(13, "outer", 1, 8, 7),
			arc(10, "outer", 1, 2, 3), arc(12, "outer", 5, 6, 7)}},
		// One way already spans two of the fragments, so the ring is three
		// ways rather than four.
		{"unequal fragments", []Way{
			arc(10, "outer", 3, 4, 5, 6, 7), arc(11, "outer", 1, 2, 3), arc(12, "outer", 7, 8, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != 1 || len(got.Open) != 0 {
				t.Fatalf("got %d rings and %d open chains, want one ring: %+v", len(got.Outer), len(got.Open), got)
			}
			assertRingIsCycle(t, got.Outer[0], want)
		})
	}
}

// The same outline as a hole comes out clockwise, and in the reverse order --
// which is what makes the test above discriminating rather than a statement
// about the fixture. An implementation that emitted the corners in ascending
// order whatever the role would pass one of these and fail the other.
func TestAHolesCornersRunTheOtherWayRound(t *testing.T) {
	got, err := Assemble([]Way{
		arc(10, "inner", 1, 2, 3), arc(11, "inner", 3, 4, 5),
		arc(12, "inner", 5, 6, 7), arc(13, "inner", 7, 8, 1),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Inner) != 1 || len(got.Outer) != 0 || len(got.Open) != 0 {
		t.Fatalf("got %d inner, %d outer, %d open, want one hole", len(got.Inner), len(got.Outer), len(got.Open))
	}
	assertRingIsCycle(t, got.Inner[0], corners(onOctagon, 8, 7, 6, 5, 4, 3, 2, 1))
}

// An open chain's coordinates have to run from the end it says it starts at
// to the end it says it stops at, in the outline's order.
//
// From and To are node ids and Points are coordinates, and nothing in the
// type system ties them together: a chain that reversed its ids without
// reversing its geometry reports two correct loose ends attached to a line
// drawn backwards, and the existing tests -- which check the ends and the
// count -- cannot tell.
func TestAnOpenChainsPointsRunFromItsFirstEndToItsSecond(t *testing.T) {
	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"forward", []Way{arc(10, "", 1, 2, 3), arc(11, "", 3, 4, 5)}},
		// Started at the middle way, so the first way can only be picked up
		// by the backward extension, which reverses the chain.
		{"backward extension", []Way{arc(11, "", 3, 4, 5), arc(10, "", 1, 2, 3)}},
		{"members reversed", []Way{arc(11, "", 5, 4, 3), arc(10, "", 3, 2, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Open) != 1 || len(got.Outer) != 0 {
				t.Fatalf("got %d open chains and %d rings, want one chain: %+v", len(got.Open), len(got.Outer), got)
			}
			c := got.Open[0]

			// The outline is 1-2-3-4-5 whichever way the chain came out, so
			// the expected geometry follows from the end it reports.
			var want []Point
			var wantTo int64
			switch c.From {
			case 1:
				want, wantTo = corners(onOctagon, 1, 2, 3, 4, 5), 5
			case 5:
				want, wantTo = corners(onOctagon, 5, 4, 3, 2, 1), 1
			default:
				t.Fatalf("the chain starts at node %d, want a loose end: 1 or 5", c.From)
			}
			if c.To != wantTo {
				t.Fatalf("the chain runs from node %d to node %d, want the other loose end %d", c.From, c.To, wantTo)
			}
			if !slices.Equal(c.Points, want) {
				t.Errorf("the chain runs\n got %v\nwant %v\nThe coordinates do not follow the node ids the chain reports.", c.Points, want)
			}
		})
	}
}

// Joining accounts for every point it was given: one is dropped at each join
// because the two ways share that node, and one more when a ring closes
// because the closing vertex is not repeated. Nothing else is dropped and
// nothing is duplicated.
//
//	out = in - (ways - groups) - rings
//
// where a group is a ring or an open chain. The identity is exact and
// derived from the rule, not read off a run: a way consumed twice, a join
// that forgot to drop the shared node, a closure that kept the repeated
// vertex, or a way silently discarded all break it, and each of those is
// invisible in a count of rings.
func TestJoiningAccountsForEveryPoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"one ring from two ways", []Way{seg(10, "", 1, 2, 3), seg(11, "", 3, 4, 1)}},
		{"one ring from four ways", []Way{
			arc(10, "", 1, 2, 3), arc(11, "", 3, 4, 5), arc(12, "", 5, 6, 7), arc(13, "", 7, 8, 1)}},
		{"a ring already closed as one way", []Way{seg(10, "", 1, 2, 3, 4, 1)}},
		{"two rings and a fragment", []Way{
			seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1),
			seg(20, "", 5, 6), seg(21, "", 6, 7), seg(22, "", 7, 5),
			seg(30, "", 8, 9)}},
		{"an outer and a hole", []Way{
			arc(10, "outer", 1, 2, 3, 4, 5), arc(11, "outer", 5, 6, 7, 8, 1),
			seg(20, "inner", 20, 21, 22, 20)}},
		{"out and back, which encloses nothing", []Way{seg(10, "", 1, 2), seg(11, "", 2, 1)}},
		{"a way with one node", []Way{seg(10, "", 1, 2, 3), seg(11, "", 3, 4, 1), {ID: 12, Nodes: []int64{7}, Points: []Point{at(7)}}}},
		{"a way with no nodes at all", []Way{seg(10, "", 1, 2, 3), seg(11, "", 3, 4, 1), {ID: 12}}},
		{"nothing at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			var in int
			for _, w := range tc.ways {
				in += len(w.Points)
			}
			out, rings, groups := pointsOut(got)
			want := in - (len(tc.ways) - groups) - rings
			if out != want {
				t.Errorf("%d points went in and %d came out; %d ways formed %d groups of which %d closed, so %d should have come out",
					in, out, len(tc.ways), groups, rings, want)
			}
		})
	}
}

// pointsOut totals the points Assemble handed back, and counts the closed
// rings and the groups (rings plus open chains) they came in.
func pointsOut(r Rings) (points, rings, groups int) {
	for _, x := range r.Outer {
		points += len(x)
	}
	for _, x := range r.Inner {
		points += len(x)
	}
	for _, c := range r.Open {
		points += len(c.Points)
	}
	rings = len(r.Outer) + len(r.Inner)
	return points, rings, rings + len(r.Open)
}

// Assemble reads its argument and must not write to it. A caller may hold the
// ways for a second relation, draw them, or assemble them again.
//
// Worth its own test because the protection is three slices.Clone calls in
// three different functions, none of which has an observable effect on the
// ring that comes back: drop one and the chain grows into the caller's
// backing array or reverses the caller's node list in place, and every
// assertion about the returned rings still holds.
func TestAssembleLeavesTheWaysItWasGivenUnchanged(t *testing.T) {
	// Deliberately includes all three paths that reverse anything: ways that
	// arrive pointing the wrong way, a chain reversed and then put back
	// because nothing was behind it, and -- the one that matters most -- a
	// chain reversed by the backward extension and LEFT that way, which is
	// where an in-place reversal of the caller's slice is never undone.
	ways := []Way{
		arc(11, "outer", 5, 4, 3), arc(10, "outer", 1, 2, 3),
		arc(12, "outer", 5, 6, 7), arc(13, "outer", 7, 8, 1),
		seg(20, "inner", 32, 33, 34), seg(21, "inner", 30, 31, 32),
		seg(22, "inner", 40, 41),
	}
	before := make([]Way, len(ways))
	for i, w := range ways {
		before[i] = Way{ID: w.ID, Role: w.Role, Nodes: slices.Clone(w.Nodes), Points: slices.Clone(w.Points)}
	}

	first, err := Assemble(ways)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for i := range ways {
		if !slices.Equal(ways[i].Nodes, before[i].Nodes) {
			t.Errorf("way %d's node ids came back as %v, want %v", ways[i].ID, ways[i].Nodes, before[i].Nodes)
		}
		if !slices.Equal(ways[i].Points, before[i].Points) {
			t.Errorf("way %d's points came back as %v, want %v", ways[i].ID, ways[i].Points, before[i].Points)
		}
	}

	// And the same input assembles to the same answer, which is the property
	// the caller actually relies on and the one an aliased slice breaks.
	second, err := Assemble(ways)
	if err != nil {
		t.Fatalf("Assemble again: %v", err)
	}
	if len(first.Outer) != len(second.Outer) || len(first.Open) != len(second.Open) {
		t.Fatalf("assembling twice gave %d/%d rings and %d/%d chains", len(first.Outer), len(second.Outer), len(first.Open), len(second.Open))
	}
	for i := range first.Outer {
		if !slices.Equal(first.Outer[i], second.Outer[i]) {
			t.Errorf("ring %d differs between two assemblies of the same ways:\nfirst  %v\nsecond %v", i, first.Outer[i], second.Outer[i])
		}
	}
	for i := range first.Open {
		if !slices.Equal(first.Open[i].Points, second.Open[i].Points) {
			t.Errorf("open chain %d differs between two assemblies of the same ways", i)
		}
	}
}

// A ring assembled from several ways is oriented by its role, whichever way
// the file stored it.
//
// The existing orientation tests hand Assemble a single way that is already
// closed, so the whole join path is absent from them: a reversal applied to
// one way and not to a chain of four would pass those and fail here.
func TestAMultiWayRingIsOrientedByItsRole(t *testing.T) {
	ccw := [][]int64{{1, 2, 3}, {3, 4, 5}, {5, 6, 7}, {7, 8, 1}}
	cw := [][]int64{{1, 8, 7}, {7, 6, 5}, {5, 4, 3}, {3, 2, 1}}

	for _, stored := range []struct {
		name  string
		parts [][]int64
	}{
		{"stored counterclockwise", ccw},
		{"stored clockwise", cw},
	} {
		for _, role := range []struct {
			name string
			role string
			want int
		}{
			{"as an outer", "outer", counterclockwise},
			{"as a hole", "inner", clockwise},
		} {
			t.Run(stored.name+" "+role.name, func(t *testing.T) {
				var ways []Way
				for i, p := range stored.parts {
					ways = append(ways, arc(int64(10+i), role.role, p...))
				}
				got, err := Assemble(ways)
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				rings := got.Outer
				if role.role == "inner" {
					rings = got.Inner
				}
				if len(rings) != 1 {
					t.Fatalf("got %d %s rings, want 1: %+v", len(rings), role.role, got)
				}
				if w := rings[0].winding(); w != role.want {
					t.Errorf("the ring winds %d, want %d", w, role.want)
				}
			})
		}
	}
}

// winding is the sign of the whole ring's area, not of the turn at one
// corner.
//
// The rings this handles are administrative outlines: a coastline, a border
// following a river, an area with a notch cut out of it. Almost none of them
// is convex, and at most corners of a non-convex ring the turn goes the
// opposite way to the ring as a whole. An implementation that looked at the
// first three corners, or at a bounding box, gives the right answer for every
// square in this file and the wrong one for a real boundary.
func TestWindingFollowsTheWholeRingNotItsCorners(t *testing.T) {
	// An arrowhead: four corners of a rectangle with the middle of one side
	// pushed inwards. Walked from (4,0) the FIRST turn is clockwise while
	// the ring as a whole is counterclockwise, by the shoelace worked out by
	// hand: the terms are 4, 0, 8, 0, 0, summing to +12.
	arrow := Ring{{Lon: 4, Lat: 0}, {Lon: 2, Lat: 1}, {Lon: 4, Lat: 2}, {Lon: 0, Lat: 2}, {Lon: 0, Lat: 0}}
	reversed := slices.Clone(arrow)
	slices.Reverse(reversed)

	for _, tc := range []struct {
		name string
		ring Ring
		want int
	}{
		{"a non-convex ring whose first turn goes the other way", arrow, counterclockwise},
		{"the same ring walked backwards", reversed, clockwise},
		// A comb: three teeth, so most corners turn clockwise. Area by hand:
		// the 6x2 block is 12, less three 1x1 notches, is 9.
		{"a comb, mostly clockwise corners", Ring{
			{Lon: 0, Lat: 0}, {Lon: 6, Lat: 0}, {Lon: 6, Lat: 2},
			{Lon: 5, Lat: 2}, {Lon: 5, Lat: 1}, {Lon: 4, Lat: 1}, {Lon: 4, Lat: 2},
			{Lon: 3, Lat: 2}, {Lon: 3, Lat: 1}, {Lon: 2, Lat: 1}, {Lon: 2, Lat: 2},
			{Lon: 1, Lat: 2}, {Lon: 1, Lat: 1}, {Lon: 0, Lat: 1},
		}, counterclockwise},
		// Collinear corners enclose nothing, and the coordinates are exactly
		// representable so the shoelace is exactly zero rather than rounding
		// noise.
		{"a ring on one straight line", Ring{{Lon: 0, Lat: 0}, {Lon: 1, Lat: 1}, {Lon: 2, Lat: 2}, {Lon: 3, Lat: 3}}, 0},
		{"two corners are not a ring", Ring{{Lon: 0, Lat: 0}, {Lon: 1, Lat: 1}}, 0},
		{"no corners at all", Ring{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ring.winding(); got != tc.want {
				t.Errorf("winding = %d, want %d", got, tc.want)
			}
		})
	}
}

// The shoelace is taken over longitude and latitude as though they were plane
// coordinates, which is not an area: a degree of longitude is a different
// distance from a degree of latitude everywhere but the equator. The claim
// that this is harmless is that the SIGN survives moving the ring anywhere on
// the globe and stretching either axis, and that is what is checked here.
//
// The one transform that does break it is wrapping across the antimeridian,
// where a ring's longitudes jump from +180 to -180 and the shoelace reads the
// short way round as the long way. This test deliberately does not go there:
// see the note in the report.
func TestWindingSurvivesMovingAndStretchingTheRing(t *testing.T) {
	base := Ring{{Lon: 4, Lat: 0}, {Lon: 2, Lat: 1}, {Lon: 4, Lat: 2}, {Lon: 0, Lat: 2}, {Lon: 0, Lat: 0}}

	for _, tc := range []struct {
		name       string
		dLon, dLat float64
		sLon, sLat float64
	}{
		{"where it was built", 0, 0, 1, 1},
		{"moved to Denmark", 9, 55, 1, 1},
		{"moved to Sydney", 151, -33, 1, 1},
		{"moved just short of the antimeridian", 175, -17, 0.001, 0.001},
		{"longitude squeezed, as a projection would", 9, 55, 0.5, 1},
		{"latitude stretched", 9, 55, 1, 3},
		{"both axes tiny, a neighbourhood rather than a country", 9, 55, 0.001, 0.001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			moved := make(Ring, len(base))
			for i, p := range base {
				moved[i] = Point{Lon: tc.dLon + p.Lon*tc.sLon, Lat: tc.dLat + p.Lat*tc.sLat}
			}
			if got := moved.winding(); got != counterclockwise {
				t.Errorf("winding = %d, want counterclockwise -- the sign is supposed to survive this", got)
			}
		})
	}
}

// A loop of fewer than three corners encloses nothing, however the file
// spells it: a way whose two nodes are the same one, a way that runs out to a
// node and back, or two ways that do the same between them. All three are
// reported as open chains rather than handed on as polygons.
//
// Paired with the case that must still close, because an implementation that
// called everything degenerate would pass the first four rows alone.
func TestALoopOfFewerThanThreeCornersIsNotARing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ways      []Way
		wantRings int
	}{
		{"a way whose two nodes are the same node", []Way{arc(10, "", 1, 1)}, 0},
		{"a way out to one node and back", []Way{arc(10, "", 1, 2, 1)}, 0},
		{"two ways out and back", []Way{arc(10, "", 1, 2), arc(11, "", 2, 1)}, 0},
		{"three ways out, back and out again", []Way{arc(10, "", 1, 2), arc(11, "", 2, 1), arc(12, "", 1, 3)}, 0},
		// Three corners is the smallest thing that encloses anything, and it
		// must close.
		{"three corners in one way", []Way{arc(10, "", 1, 2, 3, 1)}, 1},
		{"three corners in three ways", []Way{arc(10, "", 1, 2), arc(11, "", 2, 3), arc(12, "", 3, 1)}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != tc.wantRings {
				t.Errorf("got %d rings, want %d: %+v", len(got.Outer), tc.wantRings, got)
			}
			if tc.wantRings == 0 && len(got.Open) == 0 {
				t.Error("nothing closed and nothing was reported open; the ways vanished")
			}
			// Whatever came back, every point is accounted for.
			in := 0
			for _, w := range tc.ways {
				in += len(w.Points)
			}
			out, rings, groups := pointsOut(got)
			if want := in - (len(tc.ways) - groups) - rings; out != want {
				t.Errorf("%d points in, %d out, want %d", in, out, want)
			}
		})
	}
}

// A hole that touches its shell at one node is two rings, not one.
//
// It is the reason the roles are split before anything is joined rather than
// after: node 1 belongs to three ways of the shell and the hole between them,
// so a joiner that indexed every way's endpoints together would walk out of
// the shell into the hole and hand back a single figure-eight.
func TestAHoleTouchingItsShellStaysItsOwnRing(t *testing.T) {
	// A unit square with a triangular bite out of its lower-left corner. The
	// bite shares corner 1 with the square.
	place := func(id int64) Point {
		return map[int64]Point{
			1: {Lon: 0, Lat: 0}, 2: {Lon: 4, Lat: 0}, 3: {Lon: 4, Lat: 4}, 4: {Lon: 0, Lat: 4},
			5: {Lon: 2, Lat: 0}, 6: {Lon: 0, Lat: 2},
		}[id]
	}
	got, err := Assemble([]Way{
		shaped(10, "outer", place, 1, 2, 3), shaped(11, "outer", place, 3, 4, 1),
		shaped(20, "inner", place, 1, 5), shaped(21, "inner", place, 5, 6), shaped(22, "inner", place, 6, 1),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 || len(got.Inner) != 1 || len(got.Open) != 0 {
		t.Fatalf("got %d outer, %d inner, %d open; want a shell and its hole: %+v",
			len(got.Outer), len(got.Inner), len(got.Open), got)
	}
	if n := len(got.Outer[0]); n != 4 {
		t.Errorf("the shell has %d corners, want the square's 4: %v", n, got.Outer[0])
	}
	if n := len(got.Inner[0]); n != 3 {
		t.Errorf("the hole has %d corners, want the triangle's 3: %v", n, got.Inner[0])
	}
	if w := got.Outer[0].winding(); w != counterclockwise {
		t.Errorf("the shell winds %d, want counterclockwise", w)
	}
	if w := got.Inner[0].winding(); w != clockwise {
		t.Errorf("the hole winds %d, want clockwise", w)
	}
}

// The guard that keeps a way too short to join out of the endpoint index has
// to hold even when that way sits on a node the outline is using.
//
// A one-node way is malformed but permitted, and Read hands it through. Let
// it into the index and the joiner picks it up as though it were a fragment
// of the outline: it contributes no points, so nothing about the ring looks
// wrong, and it stops being reported as the broken way it is. The outline
// closes, the count of points balances, and the only trace is a chain that
// is no longer there.
//
// The one-node way is listed BETWEEN the outline's two ways on purpose. Put
// first it is consumed before anything can join to it and the bug is
// invisible; the order here is the one where the ring's tail reaches its node
// while it is still unused.
func TestAWayTooShortToJoinIsNotJoinedTo(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "", 1, 2, 3),
		{ID: 12, Nodes: []int64{3}, Points: []Point{at(3)}},
		seg(11, "", 3, 4, 1),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 {
		t.Errorf("got %d rings, want the outline still closed", len(got.Outer))
	}
	if len(got.Open) != 1 {
		t.Fatalf("got %d open chains, want the one-node way still reported: %+v", len(got.Open), got)
	}
	if c := got.Open[0]; c.Ways != 1 || len(c.Points) != 1 || c.From != 3 || c.To != 3 {
		t.Errorf("the one-node way is reported as %+v, want one way, one point, and node 3 at both ends -- it was joined to the outline instead", c)
	}
}
