package osm

import (
	"flag"
	"io"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"
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
	var rings, holes, chains, points int
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
func assertAssemblyInvariants(t *testing.T, name string, in []Way, got Rings) {
	t.Helper()

	var inPoints, outPoints int
	for _, w := range in {
		inPoints += len(w.Points)
	}
	for _, r := range got.Outer {
		outPoints += len(r)
	}
	for _, r := range got.Inner {
		outPoints += len(r)
	}
	for _, c := range got.Open {
		outPoints += len(c.Points)
	}

	// Joining only ever removes points -- one per join, one more per closure
	// -- so more coming out than went in means a way was used twice, which is
	// how an outline acquires a second lap of itself.
	if outPoints > inPoints {
		t.Errorf("%s: %d points went in and %d came out; a way was used more than once",
			name, inPoints, outPoints)
	}

	// An open chain whose ends are the same node is a closed ring that was
	// not recognised as one.
	for _, c := range got.Open {
		if c.From == c.To && len(c.Points) > 3 {
			t.Errorf("%s: an open chain runs from node %d back to itself; it is a ring", name, c.From)
		}
	}
}
