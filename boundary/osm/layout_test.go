package osm

import (
	"reflect"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
)

// neighbours is two boundaries sharing a border way, south of the equator and
// east of Greenwich so both signs of coordinate occur, with coordinates on a
// 1e-5 degree grid so they are exact at every granularity used below.
func neighbours(l osmbasetest.ExtractLayout) []byte {
	e := osmbasetest.NewExtract().
		Node(1, -33.70, 151.00).Node(2, -33.70, 151.10).Node(3, -33.60, 151.10).Node(4, -33.60, 151.00).
		Node(5, -33.70, 151.20).Node(6, -33.60, 151.20).
		Node(7, -33.65, 151.05, "place", "suburb", "name", "A point with tags").
		Way(10, []int64{1, 2}).Way(11, []int64{2, 3}).Way(12, []int64{3, 4, 1}).
		Way(13, []int64{2, 5, 6, 3})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"}, {Type: "way", ID: 11, Role: "outer"}, {Type: "way", ID: 12, Role: "outer"},
		{Type: "node", ID: 7, Role: "admin_centre"},
	}, "boundary", "administrative", "admin_level", "9", "name", "West")
	e.Relation(101, []osmbasetest.ExtractMember{
		{Type: "way", ID: 11, Role: "outer"}, {Type: "way", ID: 13, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "9", "name", "East")
	return e.Layout(l).Bytes()
}

// An extract reads the same however it is laid out in the file.
//
// The synthetic extracts every other test uses are one block per element
// kind, sorted, at the default granularity with no offsets -- which a real
// file is not: a country is thousands of blocks, some producers mix kinds in
// a block, a file need not be sorted, and a block may scale and shift its
// coordinates. Each layout here is one of those, and the last is all of them
// at once; each must give exactly the boundaries the plain layout gives. The
// plain layout's own coordinates are checked against the input, so agreement
// cannot be agreement on a wrong answer.
func TestAnExtractReadsTheSameHoweverItIsLaidOut(t *testing.T) {
	want, err := Read(from(neighbours(osmbasetest.ExtractLayout{})), Options{})
	if err != nil {
		t.Fatalf("the plain layout: %v", err)
	}
	if len(want) != 2 {
		t.Fatalf("the plain layout gave %d boundaries, want 2", len(want))
	}
	if p := want[0].Ways[0].Points[0]; p != (Point{Lat: -33.70, Lon: 151.00}) {
		t.Fatalf("the plain layout's first point is %+v, want -33.70, 151.00", p)
	}

	// blocks is the header plus the data blocks each layout should write,
	// for 7 nodes, 4 ways and 2 relations -- checked, so a layout option that
	// quietly did nothing cannot pass as one that was read correctly.
	for _, tc := range []struct {
		name   string
		layout osmbasetest.ExtractLayout
		blocks int
	}{
		{"a block per element", osmbasetest.ExtractLayout{PerBlock: 1}, 1 + 7 + 4 + 2},
		{"blocks of two", osmbasetest.ExtractLayout{PerBlock: 2}, 1 + 4 + 2 + 1},
		{"every kind in each block", osmbasetest.ExtractLayout{Mixed: true, PerBlock: 3}, 1 + 3},
		{"relations first", osmbasetest.ExtractLayout{Reversed: true}, 1 + 3},
		{"relations first, a block per element", osmbasetest.ExtractLayout{Reversed: true, PerBlock: 1}, 1 + 7 + 4 + 2},
		{"a coarser granularity", osmbasetest.ExtractLayout{Granularity: 1000}, 1 + 3},
		{"offsets both ways", osmbasetest.ExtractLayout{Granularity: 1000, LatOffset: -33_000_000_000, LonOffset: 151_000_000_000}, 1 + 3},
		{"all of it", osmbasetest.ExtractLayout{PerBlock: 2, Mixed: true, Reversed: true, Granularity: 1000, LatOffset: -1_000_000_000, LonOffset: 2_000_000_000}, 1 + 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := neighbours(tc.layout)
			if n := len(blobRanges(t, file)); n != tc.blocks {
				t.Fatalf("the layout wrote %d blocks, want %d", n, tc.blocks)
			}
			got, err := Read(from(file), Options{})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("read differently from the plain layout:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}
