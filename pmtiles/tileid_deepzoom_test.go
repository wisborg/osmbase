package pmtiles_test

import (
	"testing"

	"github.com/wisborg/osmbase/pmtiles"
)

// The existing tileid tests pin zooms 0 to 7 exhaustively and the
// specification's zoom-12 anchor. Between zoom 8 and zoom 31 that single
// anchor is the only thing checked, and it is one point out of 16 million.
// The two tests here extend the curve's defining properties into that range
// and to the deepest zoom the format can address.

// TestTileID_ConsecutiveIDsAreAdjacentAtDeepZooms carries the Hilbert
// adjacency property into the zooms real archives actually use.
//
// Successive positions on a Hilbert curve are always neighbouring cells. That
// is the whole reason PMTiles orders tiles this way -- it is what makes a
// geographic neighbourhood a contiguous run of bytes and therefore what makes
// fetching one cell a handful of range requests -- and it is a property a
// Z-order curve does not have. The existing adjacency test stops at zoom 6;
// the tile source this library reads runs to zoom 15, and the cell zoom is 12.
//
// Windows rather than whole zooms, because zoom 15 has a billion tiles. The
// windows are placed at the start of the zoom, at its midpoint and at its very
// end so that the high bits of the offset -- which is where a rotation applied
// at the wrong level of the recursion shows -- are all exercised.
func TestTileID_ConsecutiveIDsAreAdjacentAtDeepZooms(t *testing.T) {
	const window = 4096
	for _, z := range []uint8{8, 12, 15, 20, pmtiles.MaxZoom} {
		// The zoom's block of IDs starts after every shallower zoom's tiles
		// and holds 4^z of them, both derived here rather than taken from the
		// code under test.
		var base uint64
		for i := uint8(0); i < z; i++ {
			base += uint64(1) << (2 * i)
		}
		count := uint64(1) << (2 * z)

		starts := []uint64{0, count/2 - window/2, count - window}
		for _, start := range starts {
			for off := start; off < start+window-1; off++ {
				za, ax, ay, err := pmtiles.IDToZxy(base + off)
				if err != nil {
					t.Fatalf("IDToZxy(%d): %v", base+off, err)
				}
				zb, bx, by, err := pmtiles.IDToZxy(base + off + 1)
				if err != nil {
					t.Fatalf("IDToZxy(%d): %v", base+off+1, err)
				}
				if za != z || zb != z {
					t.Fatalf("IDs %d and %d are at zooms %d and %d, and both are inside zoom %d's block", base+off, base+off+1, za, zb, z)
				}
				if diff(ax, bx)+diff(ay, by) != 1 {
					t.Fatalf("at zoom %d, IDs %d and %d are tiles (%d,%d) and (%d,%d), which are not neighbours", z, base+off, base+off+1, ax, ay, bx, by)
				}
				// Round trip every one of them as well, so this test is not
				// blind to a conversion that is self-consistently adjacent and
				// still maps two coordinates onto one ID.
				if id, err := pmtiles.ZxyToID(za, ax, ay); err != nil || id != base+off {
					t.Fatalf("tile %d/%d/%d came from ID %d and converts back to %d (err %v)", za, ax, ay, base+off, id, err)
				}
			}
		}
	}
}

// TestTileID_TheDeepestZoomIsAddressableAndTheNextOneIsNot checks the claim
// MaxZoom is chosen for: zoom 31's last tile still fits in the uint64 the
// format defines, and zoom 32's would not.
//
// Both bounds are computed here from the format's own definition -- the first
// tile of zoom z is at (4^z-1)/3 and the zoom holds 4^z tiles -- rather than
// read back from the code. Without this, MaxZoom could be raised to 32 and
// every existing test would still pass while the arithmetic wrapped and served
// a shallow tile for a deep coordinate.
func TestTileID_TheDeepestZoomIsAddressableAndTheNextOneIsNot(t *testing.T) {
	// (4^31-1)/3 and 4^31, built by summation so that nothing here overflows.
	var base uint64
	for i := uint8(0); i < pmtiles.MaxZoom; i++ {
		base += uint64(1) << (2 * i)
	}
	count := uint64(1) << (2 * pmtiles.MaxZoom)
	last := base + count - 1

	if last >= 1<<63 {
		t.Errorf("the last tile of zoom %d has ID %d, which is at or past 2^63; the comment on MaxZoom says it lands below", pmtiles.MaxZoom, last)
	}

	// The four corners and the centre of the deepest zoom, round tripped.
	n := uint32(1) << pmtiles.MaxZoom
	for _, c := range []struct{ x, y uint32 }{
		{0, 0}, {n - 1, 0}, {0, n - 1}, {n - 1, n - 1}, {n / 2, n / 2},
	} {
		id, err := pmtiles.ZxyToID(pmtiles.MaxZoom, c.x, c.y)
		if err != nil {
			t.Fatalf("ZxyToID(%d, %d, %d): %v", pmtiles.MaxZoom, c.x, c.y, err)
		}
		if id < base || id > last {
			t.Errorf("tile %d/%d/%d has ID %d, outside zoom %d's block [%d, %d]", pmtiles.MaxZoom, c.x, c.y, id, pmtiles.MaxZoom, base, last)
		}
		gz, gx, gy, err := pmtiles.IDToZxy(id)
		if err != nil {
			t.Fatalf("IDToZxy(%d): %v", id, err)
		}
		if gz != pmtiles.MaxZoom || gx != c.x || gy != c.y {
			t.Errorf("tile %d/%d/%d became ID %d and came back as %d/%d/%d", pmtiles.MaxZoom, c.x, c.y, id, gz, gx, gy)
		}
	}

	// A zoom one deeper must be refused rather than wrapped: (4^32-1)/3 plus
	// 4^32 exceeds uint64, so the ID it would compute belongs to some other
	// tile and the reader would serve it.
	if id, err := pmtiles.ZxyToID(pmtiles.MaxZoom+1, 0, 0); err == nil {
		t.Errorf("ZxyToID(%d, 0, 0) returned ID %d, and zoom %d does not fit in a tile ID", pmtiles.MaxZoom+1, id, pmtiles.MaxZoom+1)
	}
}
