package pmtiles_test

import (
	"testing"

	"github.com/wisborg/osmbase/pmtiles"
)

// TestZxyToID_ReproducesTheSpecificationAnchors pins the conversion against
// the table in the PMTiles v3 specification itself.
//
// The zoom-12 row is the one that matters. A Z-order curve, or a Hilbert curve
// with the quadrant rotation applied the wrong way round, reproduces every
// zoom-0 and zoom-1 row exactly -- at zoom 1 the curve visits all four
// quadrants whichever way it turns -- so an implementation checked against the
// first five rows alone can be entirely wrong and look right.
func TestZxyToID_ReproducesTheSpecificationAnchors(t *testing.T) {
	cases := []struct {
		z    uint8
		x, y uint32
		want uint64
	}{
		{0, 0, 0, 0},
		{1, 0, 0, 1},
		{1, 0, 1, 2},
		{1, 1, 1, 3},
		{1, 1, 0, 4},
		{2, 0, 0, 5},
		{12, 3423, 1763, 19078479},
	}
	for _, c := range cases {
		got, err := pmtiles.ZxyToID(c.z, c.x, c.y)
		if err != nil {
			t.Fatalf("ZxyToID(%d, %d, %d): %v", c.z, c.x, c.y, err)
		}
		if got != c.want {
			t.Errorf("ZxyToID(%d, %d, %d) = %d, want %d", c.z, c.x, c.y, got, c.want)
		}
		z, x, y, err := pmtiles.IDToZxy(c.want)
		if err != nil {
			t.Fatalf("IDToZxy(%d): %v", c.want, err)
		}
		if z != c.z || x != c.x || y != c.y {
			t.Errorf("IDToZxy(%d) = %d/%d/%d, want %d/%d/%d", c.want, z, x, y, c.z, c.x, c.y)
		}
	}
}

// TestTileID_RoundTripsOverWholeZoomLevels converts every tile of zooms 0
// through 7 and back.
//
// Exhaustive rather than sampled because the failure this guards against is
// not uniform: a mirrored quadrant is correct for half the tiles in it, so a
// handful of spot checks pass while a quarter of the world is wrong.
func TestTileID_RoundTripsOverWholeZoomLevels(t *testing.T) {
	for z := uint8(0); z <= 7; z++ {
		n := uint32(1) << z
		for x := uint32(0); x < n; x++ {
			for y := uint32(0); y < n; y++ {
				id, err := pmtiles.ZxyToID(z, x, y)
				if err != nil {
					t.Fatalf("ZxyToID(%d, %d, %d): %v", z, x, y, err)
				}
				gz, gx, gy, err := pmtiles.IDToZxy(id)
				if err != nil {
					t.Fatalf("IDToZxy(%d): %v", id, err)
				}
				if gz != z || gx != x || gy != y {
					t.Fatalf("tile %d/%d/%d became ID %d and came back as %d/%d/%d", z, x, y, id, gz, gx, gy)
				}
			}
		}
	}
}

// TestTileID_FillsEachZoomExactlyOnce checks that a zoom's tiles occupy the
// whole of that zoom's block of IDs and no ID twice.
//
// The first ID of zoom z is the number of tiles in every shallower zoom,
// 1+4+16+... = (4^z-1)/3, and the zoom holds 4^z tiles. Both numbers are
// derived here from that definition rather than copied from the code, so a
// conversion that placed a zoom at the wrong base -- or one that mapped two
// tiles onto one ID, which a round trip alone would not catch -- fails.
func TestTileID_FillsEachZoomExactlyOnce(t *testing.T) {
	for z := uint8(0); z <= 7; z++ {
		var base uint64
		for i := uint8(0); i < z; i++ {
			base += uint64(1) << (2 * i)
		}
		count := uint64(1) << (2 * z)

		seen := make(map[uint64]bool, count)
		n := uint32(1) << z
		for x := uint32(0); x < n; x++ {
			for y := uint32(0); y < n; y++ {
				id, err := pmtiles.ZxyToID(z, x, y)
				if err != nil {
					t.Fatalf("ZxyToID(%d, %d, %d): %v", z, x, y, err)
				}
				if id < base || id >= base+count {
					t.Fatalf("tile %d/%d/%d has ID %d, outside zoom %d's block [%d, %d)", z, x, y, id, z, base, base+count)
				}
				if seen[id] {
					t.Fatalf("two tiles of zoom %d share ID %d", z, id)
				}
				seen[id] = true
			}
		}
		if uint64(len(seen)) != count {
			t.Errorf("zoom %d produced %d distinct IDs, want %d", z, len(seen), count)
		}
	}
}

// TestTileID_ConsecutiveIDsAreAdjacentTiles is the property that makes this a
// Hilbert curve and not merely a bijection.
//
// Successive positions on a Hilbert curve are always neighbouring cells, which
// is the entire reason PMTiles orders tiles this way: it is what makes a
// geographic neighbourhood a contiguous run of bytes, and therefore what makes
// fetching one area a handful of range requests. A Z-order curve is also a
// bijection and also round trips, and it jumps across the whole square every
// time a high bit rolls over -- so this is the assertion that separates the
// two.
func TestTileID_ConsecutiveIDsAreAdjacentTiles(t *testing.T) {
	for z := uint8(1); z <= 6; z++ {
		// The zoom's block of IDs starts after every shallower zoom's tiles.
		var base uint64
		for i := uint8(0); i < z; i++ {
			base += uint64(1) << (2 * i)
		}
		for id := uint64(0); id < uint64(1)<<(2*z)-1; id++ {
			_, ax, ay, err := pmtiles.IDToZxy(base + id)
			if err != nil {
				t.Fatalf("IDToZxy(%d): %v", base+id, err)
			}
			_, bx, by, err := pmtiles.IDToZxy(base + id + 1)
			if err != nil {
				t.Fatalf("IDToZxy(%d): %v", base+id+1, err)
			}
			dx, dy := diff(ax, bx), diff(ay, by)
			if dx+dy != 1 {
				t.Fatalf("at zoom %d, IDs %d and %d are tiles (%d,%d) and (%d,%d), which are not neighbours", z, base+id, base+id+1, ax, ay, bx, by)
			}
		}
	}
}

func diff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// TestZxyToID_RefusesCoordinatesOutsideTheirZoom. A wrapped coordinate does
// not produce a wrong-looking ID; it produces a perfectly valid ID of a
// different tile, which the reader would then serve.
func TestZxyToID_RefusesCoordinatesOutsideTheirZoom(t *testing.T) {
	cases := []struct {
		name string
		z    uint8
		x, y uint32
	}{
		{"x past the eastern edge", 2, 4, 0},
		{"y past the southern edge", 2, 0, 4},
		{"both past the edge", 0, 1, 1},
		{"deeper than the format can address", pmtiles.MaxZoom + 1, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if id, err := pmtiles.ZxyToID(c.z, c.x, c.y); err == nil {
				t.Errorf("ZxyToID(%d, %d, %d) returned ID %d, want an error", c.z, c.x, c.y, id)
			}
		})
	}
}

// TestIDToZxy_RefusesAnIDBeyondTheDeepestZoom. Tile IDs run out before uint64
// does, so there are values that name no tile at all.
func TestIDToZxy_RefusesAnIDBeyondTheDeepestZoom(t *testing.T) {
	// The ID one past the last tile of MaxZoom: the base of MaxZoom plus its
	// tile count, i.e. (4^32-1)/3 computed without overflowing.
	var base uint64
	for i := uint8(0); i <= pmtiles.MaxZoom; i++ {
		base += uint64(1) << (2 * i)
	}
	if z, x, y, err := pmtiles.IDToZxy(base); err == nil {
		t.Errorf("IDToZxy(%d) = %d/%d/%d, want an error", base, z, x, y)
	}
}
