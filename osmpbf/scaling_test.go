package osmpbf

import (
	"math"
	"strings"
	"testing"
)

// A declared scale that does not fit in the field must be an error, not a
// different scale. Narrowed first, 4294967396 becomes 100 and the block is
// silently given a granularity it never asked for -- which is the failure
// MaxGranularity exists to prevent, arriving by the one route the check at
// the end of the decode cannot see.
func TestAGranularityThatDoesNotFitIsRefusedNotNarrowed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared uint64
		narrows  int32
	}{
		{"wraps to the default", 1<<32 + 100, 100},
		{"wraps to a legal granularity", 1<<32 + 1_000_000_000, 1_000_000_000},
		{"wraps to zero", 1 << 32, 0},
		{"wraps twice", 2<<32 + 500, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if int32(tc.declared) != tc.narrows {
				t.Fatalf("the fixture is wrong: %d narrows to %d, not %d",
					tc.declared, int32(tc.declared), tc.narrows)
			}
			b, err := DecodePrimitiveBlock(primitiveBlock(stringTable(""), pbVarint(17, tc.declared)))
			if err == nil {
				t.Fatalf("a granularity of %d was accepted as %d", tc.declared, b.Granularity)
			}
			if !strings.Contains(err.Error(), "granularity") {
				t.Errorf("DecodePrimitiveBlock: %v, want an error naming the granularity", err)
			}
		})
	}
}

func TestADateGranularityThatDoesNotFitIsRefused(t *testing.T) {
	data := primitiveBlock(stringTable(""), pbVarint(18, math.MaxInt32+1))
	if b, err := DecodePrimitiveBlock(data); err == nil {
		t.Errorf("a date granularity past the field's range was accepted as %d", b.DateGranularity)
	}
}

// The origin is added to a scaled coordinate in int64. An unbounded one puts
// the block somewhere else with no error, so it is bounded to the coordinate
// system it is an origin within.
func TestOffsetsAreBoundedToTheCoordinateSystem(t *testing.T) {
	var past int64 = -180_000_000_001
	for _, tc := range []struct {
		name  string
		field int
		v     uint64
	}{
		{"a latitude offset past the coordinate system", 19, uint64(int64(180_000_000_001))},
		{"a latitude offset past it the other way", 19, uint64(past)},
		{"a longitude offset past the coordinate system", 20, uint64(int64(180_000_000_001))},
		{"an offset of the largest int64 there is", 19, uint64(math.MaxInt64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePrimitiveBlock(primitiveBlock(stringTable(""), pbVarint(tc.field, tc.v)))
			if err == nil {
				t.Fatal("an origin outside the coordinate system was accepted")
			}
			if !strings.Contains(err.Error(), "offset") {
				t.Errorf("DecodePrimitiveBlock: %v, want an error naming the offset", err)
			}
		})
	}
}

// The offsets a real file uses must still be read. They are almost always
// zero, but the field exists to be non-zero.
func TestARealisticOffsetIsAccepted(t *testing.T) {
	var negative int64 = -1_000_000_000
	extra := pbVarint(19, uint64(negative))
	extra = append(extra, pbVarint(20, 2_000_000_000)...)
	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable(""), extra))
	if err != nil {
		t.Fatalf("an ordinary origin was refused: %v", err)
	}
	if b.LatOffset != -1_000_000_000 || b.LonOffset != 2_000_000_000 {
		t.Errorf("offsets = %d,%d, want -1000000000,2000000000", b.LatOffset, b.LonOffset)
	}
}

// A coordinate is multiplied by the block's granularity in int64, where Go
// wraps silently: an unbounded one does not fail, it yields a confident
// position somewhere else. Bounding it is what makes Degrees total, and the
// bound is on the scaled value, so it depends on the block.
func TestANodeOffTheEarthIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name        string
		granularity uint64
		lat, lon    int64
	}{
		{name: "a latitude past the pole", granularity: 100, lat: 900_000_001},
		{name: "a latitude past the other pole", granularity: 100, lat: -900_000_001},
		{name: "a longitude past the meridian", granularity: 100, lon: 1_800_000_001},
		{name: "a longitude past it the other way", granularity: 100, lon: -1_800_000_001},
		{name: "a coordinate that would overflow the multiply", granularity: 100, lat: 1 << 60},
		// The bound is on the scaled value, so what counts as off the Earth
		// depends on the block: at a nanodegree per unit, 91 is past the pole.
		{name: "past the pole at a coarser granularity", granularity: 1_000_000_000, lat: 91},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := primitiveBlock(stringTable(""), pbVarint(17, tc.granularity),
				denseNodes([]int64{1}, []int64{tc.lat}, []int64{tc.lon}, nil))
			b, err := DecodePrimitiveBlock(data)
			if err != nil {
				t.Fatalf("DecodePrimitiveBlock: %v", err)
			}
			if err := b.EachNode(func(Node) error { return nil }); err == nil {
				t.Error("a node outside the coordinate system was accepted")
			}
		})
	}
}

// And a node anywhere real must still decode, at either granularity.
func TestNodesOnTheEarthAreAccepted(t *testing.T) {
	for _, tc := range []struct {
		name        string
		granularity uint64
		lat, lon    int64
	}{
		{name: "the default granularity", granularity: 100, lat: 557_000_000},
		{name: "exactly the pole", granularity: 100, lat: 900_000_000},
		{name: "the southern hemisphere", granularity: 100, lat: -338_680_000},
		{name: "a coarser granularity", granularity: 1000, lat: 55_700_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := primitiveBlock(stringTable(""), pbVarint(17, tc.granularity),
				denseNodes([]int64{1}, []int64{tc.lat}, []int64{0}, nil))
			b, err := DecodePrimitiveBlock(data)
			if err != nil {
				t.Fatalf("DecodePrimitiveBlock: %v", err)
			}
			var seen bool
			if err := b.EachNode(func(n Node) error {
				seen = true
				lat, _ := b.Degrees(n.Lat, n.Lon)
				if lat < -90 || lat > 90 {
					t.Errorf("Degrees gave latitude %f from %d units of %d nanodegrees", lat, n.Lat, tc.granularity)
				}
				return nil
			}); err != nil {
				t.Fatalf("EachNode: %v", err)
			}
			if !seen {
				t.Error("the node did not come back")
			}
		})
	}
}

// Entries left over after the last node mean the tag column was longer than
// the id column -- the same "the rows do not line up" evidence the three
// coordinate runs are checked for, and what a producer drifting one node out
// of step leaves behind.
func TestATagRunThatEndsLateIsRefused(t *testing.T) {
	kv := tagRun([][]int32{{1, 2}, nil, {1, 2}}) // three nodes' worth
	b := blockWith(t, []string{"", "name", "Horsens"},
		denseNodes([]int64{1, 2}, []int64{0, 0}, []int64{0, 0}, kv)) // two nodes

	err := b.EachNode(func(Node) error { return nil })
	if err == nil {
		t.Fatal("a tag run longer than its nodes was accepted")
	}
	if !strings.Contains(err.Error(), "left over") {
		t.Errorf("EachNode: %v, want an error about the leftover entries", err)
	}
}
