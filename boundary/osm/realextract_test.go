package osm

import (
	"bytes"
	"flag"
	"io"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/boundary"
)

// extractPath names a real OpenStreetMap extract to measure against.
//
//	go test ./boundary/osm/ -run TestMeasureARealExtract -v \
//	    -extract .scratch/denmark-latest.osm.pbf
//
// A flag rather than a fixture because a country extract is half a gigabyte
// of somebody else's build: it cannot be committed, it is absent on a fresh
// clone, and a test that silently skipped when it was missing would be
// asserting nothing while looking like coverage. Skipping is correct here
// only because the flag makes the skip deliberate -- you asked for a
// measurement and did not say what to measure.
var extractPath = flag.String("extract", "", "path to a real .osm.pbf to measure the pipeline against")

// The gate docs/locate.md sets before this design is trusted.
//
//	"how many distinct nodes the admin boundaries reference. If it is a few
//	million the sorted-slice approach is a few tens of megabytes and the
//	design holds. If it is far larger, stop and reconsider."
//
// "A few million" is the plan's words; this reads it as ten, which is an
// order of magnitude of slack and still only 80 MB of ids. Past that the
// on-disk intermediate the plan names as the alternative starts to be the
// honest answer, and this test is where that conversation should begin.
const gateDistinctNodes = 10_000_000

// TestMeasureARealExtract runs the three passes over a real extract and
// reports what they cost.
//
// This is the measurement part 4 was supposed to produce and did not: what
// was measured before was the MULTIPLIER -- eight bytes a distinct id, on
// synthetic ids -- and not the count it multiplies. The count is the number
// the design's viability actually rests on.
func TestMeasureARealExtract(t *testing.T) {
	if *extractPath == "" {
		t.Skip("no -extract given; this measures a real country extract, which is not committed")
	}

	info, err := os.Stat(*extractPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	open := Open(func() (io.ReadCloser, error) { return os.Open(*extractPath) })

	t.Logf("extract: %s, %.1f MB", *extractPath, float64(info.Size())/(1<<20))

	for _, tc := range []struct {
		name   string
		levels []int
	}{
		// Every level, which is the worst case and the one the limits have
		// to survive.
		{"all levels", nil},
		// What locate will actually ask for: 8 is the municipality, 9 and 10
		// are suburb and neighbourhood. This is the working number.
		{"levels 8 to 10", []int{8, 9, 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Levels: tc.levels, Limits: DefaultLimits()}

			start := time.Now()
			found, wantedWays, err := readRelations(open, opts)
			if err != nil {
				t.Fatalf("pass 1: %v", err)
			}
			pass1 := time.Since(start)

			var wayRefs int
			for _, r := range found {
				wayRefs += len(r.wayIDs)
			}

			start = time.Now()
			wayNodes, wantedNodes, err := readWays(open, wantedWays, opts.Limits)
			if err != nil {
				t.Fatalf("pass 2: %v", err)
			}
			pass2 := time.Since(start)

			var nodeRefs, waysHeld int
			for _, refs := range wayNodes {
				if refs != nil {
					waysHeld++
				}
				nodeRefs += len(refs)
			}

			start = time.Now()
			points, err := readNodes(open, wantedNodes)
			if err != nil {
				t.Fatalf("pass 3: %v", err)
			}
			pass3 := time.Since(start)

			var m runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m)

			t.Logf("  boundaries kept        %9d", len(found))
			t.Logf("  way references         %9d", wayRefs)
			t.Logf("  distinct ways wanted   %9d  (%d held; the rest are beyond the extract)",
				wantedWays.len(), waysHeld)
			t.Logf("  node references        %9d", nodeRefs)
			t.Logf("  DISTINCT NODES         %9d  <- the gate", wantedNodes.len())
			t.Logf("  coordinates held       %9d  (%.1f MB)", len(points), float64(len(points)*16)/(1<<20))
			t.Logf("  id sets                          %.1f MB",
				float64((wantedWays.len()+wantedNodes.len())*8)/(1<<20))
			t.Logf("  live heap after the passes       %.1f MB", float64(m.HeapAlloc)/(1<<20))
			t.Logf("  peak heap seen by the runtime    %.1f MB", float64(m.HeapSys)/(1<<20))
			t.Logf("  passes took %v / %v / %v (total %v)",
				pass1.Round(time.Millisecond), pass2.Round(time.Millisecond),
				pass3.Round(time.Millisecond), (pass1 + pass2 + pass3).Round(time.Millisecond))

			// The gate itself. A failure here is not a bug in the code --
			// it is the plan's own instruction to stop and reconsider the
			// design, and it should read that way.
			if n := wantedNodes.len(); n > gateDistinctNodes {
				t.Errorf("the boundaries reference %d distinct nodes, past the %d this design assumes.\n"+
					"docs/locate.md says to stop and reconsider at this point: the in-memory node set is "+
					"%.0f MB of ids alone, and an on-disk intermediate is the alternative it names.",
					n, gateDistinctNodes, float64(n*8)/(1<<20))
			}

			// Levels 8 to 10 are what locate will ask for, so a boundary
			// count of zero there means the extract or the tagging is not
			// what this pipeline expects, whatever the memory says.
			if len(found) == 0 {
				t.Errorf("no boundaries at %v; the pipeline found nothing to measure", tc.levels)
			}
			if !slices.IsSorted(wantedNodes.ids) {
				t.Error("the frozen node set is not sorted; every lookup in pass 3 was unreliable")
			}
		})
	}
}

// TestAssembleARealExtract runs ring assembly over every boundary a real
// extract holds and reports how many outlines actually close.
//
// The synthetic tests establish that the joining rules are right. This
// establishes that they are the rules real data needs, which is a different
// claim: a relation's ways are cut at every junction, listed in no order and
// in no direction, and a country extract cuts some of them off entirely.
func TestAssembleARealExtract(t *testing.T) {
	if *extractPath == "" {
		t.Skip("no -extract given")
	}
	open := Open(func() (io.ReadCloser, error) { return os.Open(*extractPath) })

	boundaries, err := Read(open, Options{Levels: []int{8, 9, 10}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var whole, partial, none int
	var rings, holes, chains, points, revisited int
	worst := ""
	worstOpen := 0

	for _, b := range boundaries {
		got, err := Assemble(b.Ways)
		if err != nil {
			t.Fatalf("Assemble(%q): %v", b.Name, err)
		}
		rings += len(got.Outer)
		holes += len(got.Inner)
		chains += len(got.Open)
		for _, r := range got.Outer {
			points += len(r)
			if w := r.winding(); w != counterclockwise {
				t.Errorf("%q has an outer ring winding %d", b.Name, w)
			}
		}
		for _, r := range got.Inner {
			if w := r.winding(); w != clockwise {
				t.Errorf("%q has an inner ring winding %d", b.Name, w)
			}
		}

		assertAssemblyInvariants(t, b.Name, b.Ways, got)
		assertOrderDoesNotMatter(t, b.Name, b.Ways, got)
		for _, r := range got.Outer {
			revisited += repeatedCorners(r)
		}

		switch {
		case len(got.Outer) > 0 && len(got.Open) == 0:
			whole++
		case len(got.Outer) > 0:
			partial++
		default:
			none++
		}
		if len(got.Open) > worstOpen {
			worstOpen, worst = len(got.Open), b.Name
		}
	}

	t.Logf("%d boundaries at levels 8-10", len(boundaries))
	t.Logf("  closed completely        %6d", whole)
	t.Logf("  closed with gaps left    %6d", partial)
	t.Logf("  did not close at all     %6d", none)
	t.Logf("  outer rings %d, holes %d, open chains %d, points %d", rings, holes, chains, points)
	t.Logf("  corners a ring visits twice %d (not zero means two outlines were welded at a junction)", revisited)
	if worst != "" {
		t.Logf("  most fragmented outline: %d open chains", worstOpen)
	}

	// The closure rate is deliberately reported and NOT asserted. It measures
	// how complete the extract is, not whether assembly works: Sydney closes
	// 471 of 522 because a bounding-box cut severs the outlines at its edge,
	// and Denmark closes 3 of 23 at these levels because more than half the
	// ways its boundary relations name lie outside the country file. A
	// threshold here would fail on the data and blame the code.
	//
	// What IS asserted below holds whatever the extract contains.
}

// assertAssemblyInvariants checks what must be true of any assembly, however
// complete or broken the data behind it is.
//
// The three here were chosen because each can actually fail. An invariant
// that cannot is worse than none: it reads as a guarantee in the test and
// costs a reader the time to work out that it is not one. Two earlier
// invariants were exactly that -- "no more points came out than went in",
// which the used[] flags make structurally impossible, and "no open chain
// runs from a node back to itself", which is the definition of closed() and
// so cannot describe anything the code puts in Open.
func assertAssemblyInvariants(t *testing.T, name string, in []Way, got Rings) {
	t.Helper()

	// 1. Every point is accounted for, exactly.
	//
	//	out = in - (ways - groups) - rings
	//
	// where a group is a ring or an open chain: joining drops one point at
	// each join, because the two ways share that node, and one more when a
	// ring closes, because the closing vertex is not repeated. Nothing else
	// is dropped and nothing is duplicated. Unlike the inequality this
	// replaces, it fails if a way is consumed twice, if a join forgets to
	// drop the shared node, if a closure keeps the repeated vertex, or if a
	// way is silently discarded -- and on a real extract it is checked
	// against outlines of hundreds of ways, which is where an off-by-one in
	// the accounting shows up and a four-way fixture does not.
	var inPoints int
	for _, w := range in {
		inPoints += len(w.Points)
	}
	outPoints, rings, groups := pointsOut(got)
	if want := inPoints - (len(in) - groups) - rings; outPoints != want {
		t.Errorf("%s: %d points went in from %d ways, forming %d groups of which %d closed, so %d should have come out; %d did",
			name, inPoints, len(in), groups, rings, want, outPoints)
	}

	// 2. An open chain's geometry runs between the two node ids it reports.
	//
	// From and To are ids and Points are coordinates, and nothing ties them
	// together: a chain whose ids were reversed without its geometry reports
	// two correct loose ends attached to a line drawn backwards. That is
	// invisible in any count, and on a real extract most chains have been
	// reversed at least once, because a run that starts in the middle of an
	// outline is the normal case.
	where := map[int64]Point{}
	for _, w := range in {
		for i, id := range w.Nodes {
			where[id] = w.Points[i]
		}
	}
	for _, c := range got.Open {
		if len(c.Points) == 0 {
			continue // A way with no references at all has no ends.
		}
		if p, ok := where[c.From]; ok && p != c.Points[0] {
			t.Errorf("%s: a chain says it starts at node %d, which is at %v, but its first point is %v",
				name, c.From, p, c.Points[0])
		}
		if p, ok := where[c.To]; ok && p != c.Points[len(c.Points)-1] {
			t.Errorf("%s: a chain says it ends at node %d, which is at %v, but its last point is %v",
				name, c.To, p, c.Points[len(c.Points)-1])
		}
	}
}

// assertOrderDoesNotMatter checks that the same ways in a different order
// assemble to the same shapes.
//
// A relation's members arrive in whatever order a mapper added them, and that
// order carries no information -- so it must not change the answer. It is the
// property the join is least able to guarantee on its own: the walk takes one
// fork at a time, and at a node where more than one unused way is available
// the fork it takes depends on where the ways sit in the list. Two outlines
// of one relation meeting at a node is the shape that offers such a fork.
//
// A failure here is not a flake. It says this extract contains a junction the
// join guesses at, and that the geometry it produced for that relation
// depends on the order of a list nobody controls.
func assertOrderDoesNotMatter(t *testing.T, name string, in []Way, want Rings) {
	t.Helper()
	if len(in) < 2 {
		return
	}
	rng := rand.New(rand.NewSource(int64(len(in))))
	for attempt := 0; attempt < 5; attempt++ {
		ways := slices.Clone(in)
		rng.Shuffle(len(ways), func(i, j int) { ways[i], ways[j] = ways[j], ways[i] })
		got, err := Assemble(ways)
		if err != nil {
			t.Fatalf("%s: Assemble(shuffled): %v", name, err)
		}
		if a, b := shapes(want), shapes(got); a != b {
			t.Errorf("%s: listed as the file has them the ways assemble to %s; shuffled they assemble to %s",
				name, a, b)
			return
		}
	}
}

// shapes summarises an assembly by the sizes of what came out of it, which is
// what must not depend on the order of the input.
func shapes(r Rings) string {
	size := func(n int) string { return strconv.Itoa(n) }
	var outer, inner, open []string
	for _, x := range r.Outer {
		outer = append(outer, size(len(x)))
	}
	for _, x := range r.Inner {
		inner = append(inner, size(len(x)))
	}
	for _, c := range r.Open {
		open = append(open, size(len(c.Points)))
	}
	slices.Sort(outer)
	slices.Sort(inner)
	slices.Sort(open)
	return "outer[" + strings.Join(outer, " ") + "] inner[" + strings.Join(inner, " ") +
		"] open[" + strings.Join(open, " ") + "]"
}

// repeatedCorners counts the corners a ring visits more than once.
//
// Reported rather than asserted, because two distinct nodes at one coordinate
// are legitimate where an extract has been cut. But a ring that returns to a
// corner is the signature of two outlines welded at a junction, so a count
// that is not zero is the thing to look at first.
func repeatedCorners(r Ring) int {
	seen := make(map[Point]bool, len(r))
	n := 0
	for _, p := range r {
		if seen[p] {
			n++
		}
		seen[p] = true
	}
	return n
}

// TestARealExtractSurvivesTheDerivedFile runs the whole pipeline: extract to
// boundaries, boundaries to rings, rings to areas, areas through the file,
// and out the other side as containment answers.
//
// The assertion is that the answers are the SAME on either side. That is what
// the file is for, and it needs no coordinate written into this repository:
// the sample points are computed from the extract at run time, so nothing
// here records where anybody is.
func TestARealExtractSurvivesTheDerivedFile(t *testing.T) {
	if *extractPath == "" {
		t.Skip("no -extract given")
	}
	open := Open(func() (io.ReadCloser, error) { return os.Open(*extractPath) })

	boundaries, err := Read(open, Options{Levels: []int{8, 9, 10}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	areas, rep, err := Areas(boundaries)
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	t.Logf("%d boundaries -> %d areas; %+v", len(boundaries), len(areas), rep)
	if len(areas) == 0 {
		t.Fatal("no areas came out of the extract")
	}

	var buf bytes.Buffer
	if err := boundary.WriteDerived(&buf, areas); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	t.Logf("derived file: %.2f MB for %d areas", float64(buf.Len())/(1<<20), len(areas))

	before := boundary.NewSet(areas)
	after, err := boundary.ReadDerived(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}
	if after.Len() != before.Len() {
		t.Fatalf("wrote %d areas and read back %d", before.Len(), after.Len())
	}

	// Sample points taken from the geometry itself: the midpoint of each
	// ring's bounding box, which is inside for most shapes and outside for
	// the rest -- and both are answers the file has to preserve.
	var checked, inside int
	for _, b := range boundaries {
		rings, err := Assemble(b.Ways)
		if err != nil || len(rings.Outer) == 0 {
			continue
		}
		for _, r := range rings.Outer {
			lat, lon := midpoint(r)
			was, okBefore := before.At(lat, lon)
			is, okAfter := after.At(lat, lon)
			checked++
			if okBefore != okAfter || was.Name != is.Name {
				t.Fatalf("at %.5f,%.5f the file changed the answer from %q (%v) to %q (%v)",
					lat, lon, was.Name, okBefore, is.Name, okAfter)
			}
			if okBefore {
				inside++
			}
		}
	}
	t.Logf("checked %d points, %d of them inside some area", checked, inside)
	if inside == 0 {
		t.Error("no sample point landed inside any area; the check proved nothing")
	}
}

// midpoint is the centre of a ring's bounding box.
func midpoint(r Ring) (lat, lon float64) {
	west, south := r[0].Lon, r[0].Lat
	east, north := west, south
	for _, p := range r {
		west, east = min(west, p.Lon), max(east, p.Lon)
		south, north = min(south, p.Lat), max(north, p.Lat)
	}
	return (south + north) / 2, (west + east) / 2
}
