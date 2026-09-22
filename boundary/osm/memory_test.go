package osm

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
)

func TestIDSetSortsDeduplicatesAndSearches(t *testing.T) {
	s := newIDSet(100)
	for _, id := range []int64{50, 10, 50, 30, 10, 10} {
		if err := s.add(id); err != nil {
			t.Fatalf("add(%d): %v", id, err)
		}
	}
	s.freeze()

	if s.len() != 3 {
		t.Errorf("the set holds %d ids, want 3 distinct", s.len())
	}
	for _, id := range []int64{10, 30, 50} {
		if !s.has(id) {
			t.Errorf("has(%d) = false", id)
		}
	}
	for _, id := range []int64{9, 11, 49, 51, 0, -10} {
		if s.has(id) {
			t.Errorf("has(%d) = true, and it was never added", id)
		}
	}
}

// Deduplication is about size, not correctness: adjacent boundary ways share
// their end nodes and two areas share their whole common border, so the same
// id arrives many times. A set that kept them would be several times larger
// than the geometry it describes.
func TestIDSetDeduplicationIsWorthIt(t *testing.T) {
	s := newIDSet(1 << 20)
	const distinct = 1000
	for round := 0; round < 8; round++ {
		for i := 0; i < distinct; i++ {
			if err := s.add(int64(i)); err != nil {
				t.Fatalf("add: %v", err)
			}
		}
	}
	s.freeze()
	if s.len() != distinct {
		t.Errorf("the set holds %d ids after 8 rounds of the same %d", s.len(), distinct)
	}
}

// The cap is checked before the append, not after the pass. This is the
// first structure in the library sized by the file rather than by one block,
// which makes it exactly where the recurring finding would land next.
func TestIDSetRefusesBeforeItAllocates(t *testing.T) {
	const max = 1 << 16
	s := newIDSet(max)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	var err error
	for i := 0; i < max*64; i++ {
		if err = s.add(int64(i)); err != nil {
			break
		}
	}
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatalf("the set accepted %d ids with a cap of %d", max*64, max)
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Errorf("add: %v, want an error naming the limit", err)
	}
	if s.len() > max {
		t.Errorf("the set holds %d ids, past its cap of %d", s.len(), max)
	}
	// Eight bytes an id, plus append's doublings on the way there. What this
	// catches is growth in proportion to what was offered rather than to the
	// cap: 64 times the cap is 4 million ids, which would be tens of
	// megabytes.
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 4<<20 {
		t.Errorf("filling a set capped at %d allocated %d bytes", max, grew)
	}
}

func TestAFrozenSetRefusesMore(t *testing.T) {
	s := newIDSet(10)
	_ = s.add(1)
	s.freeze()
	if err := s.add(2); err == nil {
		t.Error("a frozen set accepted another id; the pass order is what guarantees it is complete")
	}
}

// The pipeline must keep the nodes its boundaries run through, and not the
// extract's other nodes. That is the whole memory argument: in a country
// extract the overwhelming majority of nodes are buildings, addresses and
// traffic signals that no boundary touches.
func TestOnlyTheNodesTheBoundariesTouchAreKept(t *testing.T) {
	const irrelevant = 20_000

	e := osmbasetest.NewExtract()
	// The boundary's own nodes.
	for i := int64(1); i <= 4; i++ {
		e.Node(i, 55.7+float64(i)/100, 9.5+float64(i)/100)
	}
	// Everything else in the region, which is most of a real extract.
	for i := int64(1000); i < 1000+irrelevant; i++ {
		e.Node(i, 55.0+float64(i%100)/1000, 9.0+float64(i%100)/1000, "building", "yes")
	}
	e.Way(10, []int64{1, 2, 3, 4, 1})
	// Ways that are not part of any boundary, referencing the other nodes.
	for i := int64(20); i < 40; i++ {
		e.Way(i, []int64{1000 + i, 1001 + i, 1002 + i}, "highway", "residential")
	}
	e.Relation(100, []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "Horsens")

	open := from(e.Bytes())

	_, wantedWays, err := readRelations(open, Options{Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if wantedWays.len() != 1 {
		t.Errorf("pass 1 wants %d ways, want 1; the other %d are not boundaries", wantedWays.len(), 20)
	}

	_, wantedNodes, err := readWays(open, wantedWays, DefaultLimits())
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	// Four distinct: the way closes, so its first and last id are the same.
	if wantedNodes.len() != 4 {
		t.Errorf("pass 2 wants %d nodes, want 4; the extract holds %d", wantedNodes.len(), irrelevant+4)
	}

	points, err := readNodes(open, wantedNodes)
	if err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	// len(points) is wantedNodes.len() by construction, so counting them
	// asserts nothing the line above has not. What has to be true and is not
	// guaranteed is that each coordinate landed at the POSITION its id
	// occupies in the frozen set -- that is the whole reason the set is a
	// sorted slice rather than a map, and a pass 3 that filled the array in
	// arrival order instead would put every coordinate on the wrong node.
	//
	// The ids are 1 to 4 and the fixture puts node i at 55.7+i/100,
	// 9.5+i/100, so position i-1 must hold exactly that.
	for i := int64(1); i <= 4; i++ {
		if !wantedNodes.has(i) {
			t.Errorf("pass 2 did not want node %d, which the boundary way names", i)
			continue
		}
		p := points[i-1]
		wantLat, wantLon := 55.7+float64(i)/100, 9.5+float64(i)/100
		if math.Abs(p.Lat-wantLat) > 1e-7 || math.Abs(p.Lon-wantLon) > 1e-7 {
			t.Errorf("node %d came back at %v, want %v,%v", i, p, wantLat, wantLon)
		}
	}
	// And nothing from the other twenty thousand, which sit in a different
	// square of the map entirely.
	for _, p := range points {
		if p.Lat < 55.1 {
			t.Errorf("a coordinate at %v is one of the building nodes, which no boundary touches", p)
		}
	}
}

// BenchmarkIDSet records the number the design rests on: what an id costs in
// a sorted slice against a map.
//
// The plan called for measuring this before choosing, because the whole
// three-pass design is justified by the node set fitting in memory. Run with
// -benchmem; the figure that matters is B/op divided by the entry count.
func BenchmarkIDSet(b *testing.B) {
	for _, n := range []int{1 << 16, 1 << 20} {
		ids := make([]int64, n)
		for i := range ids {
			// Spread out, as real OSM ids are: consecutive ids would let a
			// map's hashing look better than it is on real data.
			ids[i] = int64(i) * 7919
		}

		b.Run(fmt.Sprintf("sorted slice/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				s := newIDSet(n * 2)
				for _, id := range ids {
					_ = s.add(id)
				}
				s.freeze()
				if !s.has(ids[n/2]) {
					b.Fatal("built a set that does not hold its own ids")
				}
			}
		})

		b.Run(fmt.Sprintf("map/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				m := make(map[int64]struct{})
				for _, id := range ids {
					m[id] = struct{}{}
				}
				if _, ok := m[ids[n/2]]; !ok {
					b.Fatal("built a set that does not hold its own ids")
				}
			}
		})
	}
}

// TestIDSetRetainedSize measures what the design actually rests on: the bytes
// still held per id once the set is built, which is what decides whether a
// country's boundary nodes fit in memory.
//
// Separate from the benchmark because B/op counts every byte allocated on the
// way, including append's discarded intermediates, and the question here is
// what survives. Both structures are kept alive across the measurement so the
// collector cannot answer it for us.
func TestIDSetRetainedSize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two structures of a million entries")
	}
	// Not a power of two, and every id added three times. Both matter, and
	// an earlier version of this test had neither -- which is why it read 8
	// bytes an id from a set that was really holding an array sized by the
	// number of add calls. A power-of-two count with no duplicates is the one
	// arrangement in which append's last growth step lands exactly on the
	// length, leaving no spare array to notice.
	//
	// Three times is conservative against the real pass: a boundary way is a
	// member of both neighbouring relations, and its end nodes belong to the
	// way on either side.
	const n = 1_000_003
	const duplication = 3

	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i) * 7919
	}

	slicePer := retainedPerEntry(t, n, func() any {
		s := newIDSet(n * duplication * 2)
		for range duplication {
			for _, id := range ids {
				_ = s.add(id)
			}
		}
		s.freeze()
		return s
	})

	mapPer := retainedPerEntry(t, n, func() any {
		m := make(map[int64]struct{}, 0)
		for _, id := range ids {
			m[id] = struct{}{}
		}
		return m
	})

	t.Logf("retained per id: sorted slice %d bytes, map %d bytes", slicePer, mapPer)

	// One int64 an entry and nothing else, once freeze has handed back the
	// array append grew. slices.Clip does not do that -- it lowers the cap
	// and leaves the array reachable -- so this is the assertion that tells
	// the two apart, and the duplication above is what gives it something to
	// see.
	if slicePer > 12 {
		t.Errorf("the sorted slice retains %d bytes an id, want about 8", slicePer)
	}
	// The plan chose the slice on the expectation that a map costs several
	// times as much. Asserted so that if Go's map ever becomes as compact as
	// a sorted slice, this says so and the simpler structure can be used.
	if mapPer < 3*slicePer {
		t.Errorf("a map retains %d bytes an id against the slice's %d; the reason for the slice is gone",
			mapPer, slicePer)
	}
}

// retainedPerEntry builds a structure and reports the heap it still occupies,
// divided by the number of entries.
func retainedPerEntry(t *testing.T, entries int, build func() any) int {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	held := build()

	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int(after.HeapAlloc - before.HeapAlloc)

	runtime.KeepAlive(held)
	return grew / entries
}
