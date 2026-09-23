package osm

import (
	"testing"
	"time"
)

// A member way that is already a closed loop is a ring in its own right, and
// it must not be joined to anything through its own seam node. Left in the
// index it is reachable there, and depending on which way the file lists
// first either two islands come out as one figure of eight, or the ring is
// swallowed into a chain that never closes and vanishes from every
// containment answer.
func TestAClosedWayIsARingAndIsNotWeldedToItsNeighbours(t *testing.T) {
	// A ring on nodes 1-2-3-4, and a separate outline touching it at node 1.
	ring := seg(10, "", 1, 2, 3, 4, 1)
	spur := seg(11, "", 1, 5, 6)

	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"the ring listed first", []Way{ring, spur}},
		{"the spur listed first", []Way{spur, ring}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != 1 {
				t.Errorf("got %d rings, want the closed way to stay a ring", len(got.Outer))
			}
			if len(got.Outer) == 1 && len(got.Outer[0]) != 4 {
				t.Errorf("the ring has %d points, want 4 -- it was welded to the spur", len(got.Outer[0]))
			}
			if len(got.Open) != 1 {
				t.Errorf("got %d open chains, want the spur reported", len(got.Open))
			}
		})
	}
}

func TestTwoClosedWaysSharingANodeAreTwoRings(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "", 1, 2, 3, 1),
		seg(11, "", 1, 4, 5, 1),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 2 {
		t.Fatalf("got %d rings, want 2 -- they touch at node 1 and are not one loop", len(got.Outer))
	}
	for i, r := range got.Outer {
		if len(r) != 3 {
			t.Errorf("ring %d has %d points, want 3", i, len(r))
		}
	}
}

// Where two outlines of one relation touch at a node, the junction offers
// more than one way to continue. Taking the wrong fork walks out of one lobe
// into the other and closes a figure of eight around both -- a ring enclosing
// the wrong area, with no gap reported and nothing to say it happened.
func TestTwoOutlinesTouchingAtANodeDoNotBecomeOne(t *testing.T) {
	// Two triangles sharing node 1, each built from three open fragments, so
	// the junction at node 1 has four ways on it.
	a := []Way{seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1)}
	b := []Way{seg(20, "", 1, 4), seg(21, "", 4, 5), seg(22, "", 5, 1)}

	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"in order", append(append([]Way{}, a...), b...)},
		{"starting mid-outline", []Way{a[2], a[0], a[1], b[1], b[2], b[0]}},
		{"interleaved", []Way{a[0], b[0], a[1], b[1], a[2], b[2]}},
		{"reversed throughout", []Way{
			seg(12, "", 1, 3), seg(11, "", 3, 2), seg(10, "", 2, 1),
			seg(22, "", 1, 5), seg(21, "", 5, 4), seg(20, "", 4, 1),
		}},
		// Ordered so that the first way listed at the shared node belongs to
		// the OTHER lobe. Taking the first available fork here walks out of
		// one triangle into the other and closes a figure of eight around
		// both; every other ordering happens to guess right.
		{"the wrong fork listed first", []Way{a[2], b[0], a[0], b[1], b[2], a[1]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			for _, r := range got.Outer {
				if len(r) > 3 {
					t.Errorf("a ring of %d points came out; the two triangles were welded into one loop", len(r))
				}
			}
			if len(got.Outer)+len(got.Open) == 0 {
				t.Fatal("nothing came out at all")
			}
			// Whatever it does with the ambiguity, it must not invent a ring
			// around both lobes -- so anything it could not resolve has to
			// come back as a reported gap.
			var points int
			for _, r := range got.Outer {
				points += len(r)
			}
			for _, c := range got.Open {
				points += len(c.Points)
			}
			if points == 0 {
				t.Error("the geometry was dropped entirely")
			}
		})
	}
}

// A loop that encloses nothing is not a ring. Naming the same way twice is
// the clearest way to build one: out along it and back, ending where it
// started with zero area.
func TestALoopEnclosingNothingIsReportedNotReturnedAsARing(t *testing.T) {
	w := seg(10, "", 1, 2, 3)
	got, err := Assemble([]Way{w, w})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 0 {
		t.Errorf("got %d rings from a way named twice, want none: %v", len(got.Outer), got.Outer)
	}
	if len(got.Open) == 0 {
		t.Error("the geometry vanished; it should be reported as a gap")
	}
}

// The shoelace sum is taken relative to the ring's first point. Without that,
// each term is the size of the coordinates themselves while the answer for a
// small ring is vanishingly smaller, and the sign is lost to cancellation.
func TestWindingSurvivesASmallRingFarFromTheOrigin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lat, lon float64
	}{
		{"near Sydney", -33.8688, 151.2093},
		{"near Horsens", 55.8607, 9.8503},
		{"at the origin", 0, 0},
		{"near the far corner", -89.9, 179.9},
	} {
		for _, side := range []float64{1e-4, 1e-6, 1e-7, 1e-8} {
			t.Run(tc.name, func(t *testing.T) {
				// Counterclockwise in (lon, lat).
				ccw := Ring{
					{Lat: tc.lat, Lon: tc.lon},
					{Lat: tc.lat, Lon: tc.lon + side},
					{Lat: tc.lat + side, Lon: tc.lon + side},
					{Lat: tc.lat + side, Lon: tc.lon},
				}
				if w := ccw.winding(); w != counterclockwise {
					t.Errorf("a counterclockwise square of side %g %s winds %d", side, tc.name, w)
				}
				cw := Ring{ccw[0], ccw[3], ccw[2], ccw[1]}
				if w := cw.winding(); w != clockwise {
					t.Errorf("a clockwise square of side %g %s winds %d", side, tc.name, w)
				}
			})
		}
	}
}

// The endpoint index is built once and never pruned, so a plain scan rescans
// every way already consumed. A relation naming one two-node way many times
// -- a few kilobytes of packed varints -- then costs time in the square of
// the member count.
func TestAssemblyIsNotQuadraticInTheMemberCount(t *testing.T) {
	if testing.Short() {
		t.Skip("builds relations of tens of thousands of ways")
	}

	elapsed := func(n int) time.Duration {
		ways := make([]Way, n)
		for i := range ways {
			ways[i] = seg(int64(i), "", 1, 2)
		}
		start := time.Now()
		if _, err := Assemble(ways); err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		return time.Since(start)
	}

	small := elapsed(16_000)
	large := elapsed(64_000)

	// Four times the ways. Linear is about four times the work and quadratic
	// is sixteen; anything past eight is the quadratic scan returning.
	if large > 8*small+50*time.Millisecond {
		t.Errorf("16000 ways took %v and 64000 took %v; four times the input should not cost eight times the time",
			small, large)
	}
}

// A chain that has come round is finished, and must not be allowed to leave
// through the node it arrived at. Three open ways make the ring here, so the
// closure happens mid-walk rather than in the way itself -- which is the case
// that taking the closed ways out beforehand does not cover.
func TestAChainStopsWhenItComesRoundEvenWithAWayWaitingAtTheJunction(t *testing.T) {
	got, err := Assemble([]Way{
		seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1),
		seg(13, "", 1, 7), // touches the ring at node 1
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 {
		t.Fatalf("got %d rings, want 1; the chain left through the node it closed on", len(got.Outer))
	}
	if len(got.Outer[0]) != 3 {
		t.Errorf("the ring has %d points, want 3 -- the spur was swallowed", len(got.Outer[0]))
	}
	if len(got.Open) != 1 {
		t.Errorf("got %d open chains, want the spur reported", len(got.Open))
	}
}

// A closed member way sharing its seam node with an open one, where the open
// way is listed first. The chain reaches the seam from outside and the ring
// is the only thing waiting there, so nothing but taking closed ways out
// beforehand keeps it a ring.
func TestAClosedWayIsNotSwallowedByAChainThatReachesItsSeam(t *testing.T) {
	got, err := Assemble([]Way{
		seg(11, "", 6, 5, 1),       // arrives at the seam
		seg(10, "", 1, 2, 3, 4, 1), // the ring
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 {
		t.Fatalf("got %d rings, want the closed way kept as one", len(got.Outer))
	}
	if len(got.Outer[0]) != 4 {
		t.Errorf("the ring has %d points, want 4", len(got.Outer[0]))
	}
	if len(got.Open) != 1 || got.Open[0].Ways != 1 {
		t.Errorf("got %+v, want the arriving way reported alone", got.Open)
	}
}

// At a junction offering more than one continuation, the fork that closes the
// loop is taken and the others are left. Guessing walks out of one lobe into
// another and closes a figure of eight around both.
func TestAJunctionPrefersTheForkThatCloses(t *testing.T) {
	// A triangle 1-2-3, plus two spurs hanging off node 1, so arriving back
	// at 1 there are three ways to continue and only one closes.
	got, err := Assemble([]Way{
		seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1),
		seg(13, "", 1, 7), seg(14, "", 1, 8),
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 1 || len(got.Outer[0]) != 3 {
		t.Fatalf("got %d rings %v, want the triangle alone", len(got.Outer), got.Outer)
	}
	// The two spurs meet each other at node 1, so they come back as one
	// chain running 7 to 8 rather than as two.
	if len(got.Open) != 1 {
		t.Fatalf("got %d open chains, want the two spurs joined into one: %+v", len(got.Open), got.Open)
	}
	if c := got.Open[0]; c.Ways != 2 || len(c.Points) != 3 {
		t.Errorf("the leftover chain is %d ways and %d points, want 2 and 3", c.Ways, len(c.Points))
	}
}

// The legacy roles. enclave means a hole and exclave means an outline; they
// predate inner/outer and still appear. Assembling a hole as an outline means
// every point inside it answers "inside the area", which is the wrong way
// round.
func TestLegacyAndOddlyCasedRolesLandInTheRightBucket(t *testing.T) {
	for _, tc := range []struct {
		role  string
		inner bool
	}{
		{"inner", true},
		{"enclave", true},
		{"Inner", true},
		{"  inner  ", true},
		{"outer", false},
		{"exclave", false},
		{"", false},
		{"something a later schema adds", false},
	} {
		t.Run("role "+tc.role, func(t *testing.T) {
			got, err := Assemble([]Way{seg(10, tc.role, 1, 2, 3, 4, 1)})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if tc.inner && len(got.Inner) != 1 {
				t.Errorf("role %q gave %d inner rings, want 1", tc.role, len(got.Inner))
			}
			if !tc.inner && len(got.Outer) != 1 {
				t.Errorf("role %q gave %d outer rings, want 1", tc.role, len(got.Outer))
			}
		})
	}
}

// Zero is judged against the ring's own size. A collinear ring's true sum is
// zero and its computed one is whatever the rounding left; compared to exact
// zero that reads as wound, and a degenerate polygon reaches the output as a
// real one.
func TestACollinearRingEnclosesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Ring
	}{
		{"along a line of longitude", Ring{{Lat: 55.1, Lon: 9.3}, {Lat: 55.2, Lon: 9.3}, {Lat: 55.3, Lon: 9.3}}},
		{"along a diagonal", Ring{{Lat: 55.1, Lon: 9.1}, {Lat: 55.2, Lon: 9.2}, {Lat: 55.3, Lon: 9.3}}},
		{"a path retraced", Ring{{Lat: -33.1, Lon: 151.1}, {Lat: -33.2, Lon: 151.25}, {Lat: -33.3, Lon: 151.2}, {Lat: -33.2, Lon: 151.25}}},
		{"two points", Ring{{Lat: 55, Lon: 9}, {Lat: 56, Lon: 10}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := tc.r.winding(); w != 0 {
				t.Errorf("a ring enclosing nothing winds %d", w)
			}
		})
	}
}

// And a ring that does enclose something must not be mistaken for one that
// does not, however small it is.
func TestATinyRingStillEnclosesSomething(t *testing.T) {
	for _, side := range []float64{1e-5, 1e-7, 1e-9} {
		r := Ring{
			{Lat: -33.8688, Lon: 151.2093},
			{Lat: -33.8688, Lon: 151.2093 + side},
			{Lat: -33.8688 + side, Lon: 151.2093 + side},
		}
		if w := r.winding(); w == 0 {
			t.Errorf("a triangle of side %g was read as enclosing nothing", side)
		}
	}
}

// A way too short to join to anything must stay reported as the broken thing
// it is, even when it sits in the middle of an outline that closes around it.
//
// The ordering is the whole test. Listed first it is consumed before anything
// could join to it; listed last the outline has already closed. Listed in the
// middle, the ring's tail reaches its node while it is still unused -- and if
// it were in the endpoint index it would be joined, contributing no points,
// so nothing about the ring would look wrong and the broken way would quietly
// stop being reported.
func TestAWayTooShortToJoinStaysReportedWhereverItIsListed(t *testing.T) {
	// At node 2, which the outline passes THROUGH rather than closes on. At
	// the closing node the chain has already finished by the time the short
	// way could be reached, so the mistake would be invisible.
	short := Way{ID: 99, Nodes: []int64{2}, Points: []Point{at(2)}}
	a, b, c := seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1)

	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"listed first", []Way{short, a, b, c}},
		{"listed in the middle", []Way{a, short, b, c}},
		{"listed last", []Way{a, b, c, short}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != 1 || len(got.Outer[0]) != 3 {
				t.Errorf("got %d rings %v, want the triangle", len(got.Outer), got.Outer)
			}
			if len(got.Open) != 1 {
				t.Fatalf("got %d open chains, want the short way reported", len(got.Open))
			}
			if c := got.Open[0]; c.From != 2 || c.To != 2 || len(c.Points) != 1 {
				t.Errorf("the short way is reported as %+v, want its one node and one point", c)
			}
		})
	}
}

// The same short way, but in an outline that does not close. A ring gets a
// second chance -- the backward extension recovers whatever the forward one
// gave up on -- so it is the open outline that shows what a short way in the
// endpoint index costs: it makes the junction look like a fork, the chain
// stops there, and one outline comes back as two.
func TestAWayTooShortToJoinDoesNotSplitAnOpenOutline(t *testing.T) {
	short := Way{ID: 99, Nodes: []int64{2}, Points: []Point{at(2)}}
	got, err := Assemble([]Way{seg(10, "", 1, 2), short, seg(11, "", 2, 3)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Outer) != 0 {
		t.Errorf("got %d rings from an outline that cannot close", len(got.Outer))
	}
	if len(got.Open) != 2 {
		t.Fatalf("got %d open chains, want 2 -- the outline and the short way: %+v", len(got.Open), got.Open)
	}
	var longest int
	for _, c := range got.Open {
		longest = max(longest, len(c.Points))
	}
	if longest != 3 {
		t.Errorf("the longest chain has %d points, want 3 -- the outline was split at the junction", longest)
	}
}
