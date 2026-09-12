package pmtiles_test

import (
	"testing"

	"github.com/wisborg/osmbase/pmtiles"
)

// MaxZoom is guarded today only by a CONSTANT EXPRESSION in another test file:
// tileid_deepzoom_test.go computes uint64(1) << (2 * pmtiles.MaxZoom), which
// the compiler rejects for MaxZoom 32 because the constant overflows. That is
// a real guard and it is the wrong shape for one. It reports as "this package
// does not build", it names no property, and the obvious repair for somebody
// raising MaxZoom is to change the expression in the test -- after which the
// arithmetic wraps at run time and nothing says so.
//
// The two tests below say it at run time instead, in arithmetic that stays
// inside uint64 whatever MaxZoom is, so raising MaxZoom produces a failure
// that names what broke.

// zoomBaseByHand is the ID of the first tile of zoom z: the number of tiles in
// every shallower zoom, 1 + 4 + 16 + ... Summed rather than computed as
// (4^z-1)/3 so that the test does not repeat the code's own expression, and so
// that nothing here is a constant the compiler evaluates.
func zoomBaseByHand(z uint8) uint64 {
	var base uint64
	for i := uint8(0); i < z; i++ {
		base += uint64(1) << (2 * i)
	}
	return base
}

// TestTileID_EveryZoomStartsWhereTheFormatSaysAndComesBack walks every zoom
// the format claims to address and checks the two things a wrapped zoom base
// breaks.
//
// The first tile of zoom z must land on the sum above, and must convert back.
// At zoom 32 the sum wraps -- 4^32 is 2^64 -- so the base is congruent to a
// value that ALSO belongs to zoom 31's block, ZxyToID happily returns it, and
// IDToZxy then cannot find a zoom that holds it. That is the failure this
// reports, at run time and by name.
func TestTileID_EveryZoomStartsWhereTheFormatSaysAndComesBack(t *testing.T) {
	var previousBase uint64
	for z := uint8(0); z <= pmtiles.MaxZoom; z++ {
		base := zoomBaseByHand(z)
		if z > 0 && base <= previousBase {
			t.Fatalf("zoom %d starts at ID %d, which is not past zoom %d's start at %d; the tile ID space has wrapped", z, base, z-1, previousBase)
		}
		previousBase = base

		id, err := pmtiles.ZxyToID(z, 0, 0)
		if err != nil {
			t.Fatalf("ZxyToID(%d, 0, 0): %v", z, err)
		}
		if id != base {
			t.Errorf("the first tile of zoom %d has ID %d, want %d", z, id, base)
		}
		gz, gx, gy, err := pmtiles.IDToZxy(id)
		if err != nil {
			t.Fatalf("IDToZxy(%d), the first tile of zoom %d: %v", id, z, err)
		}
		if gz != z || gx != 0 || gy != 0 {
			t.Errorf("the first tile of zoom %d came back as %d/%d/%d", z, gz, gx, gy)
		}
	}
}

// TestTileID_TheDeepestZoomsLastTileFitsAndIsTheLargestID is the claim the
// value of MaxZoom is chosen for, said without a constant shift.
//
// The last tile of the deepest zoom must have an ID above every shallower
// zoom's and below 2^63, which is what the comment on MaxZoom promises. The
// coordinate is the far corner of that zoom, built from a runtime shift so
// that a MaxZoom of 32 gives 2^32 in uint64 arithmetic rather than an
// untyped constant the compiler refuses.
func TestTileID_TheDeepestZoomsLastTileFitsAndIsTheLargestID(t *testing.T) {
	z := uint8(pmtiles.MaxZoom)
	n := uint64(1) << z
	if n == 0 {
		t.Fatalf("zoom %d is %d tiles square, which has wrapped; no coordinate of it is addressable", z, n)
	}
	base := zoomBaseByHand(z)

	far, err := pmtiles.ZxyToID(z, uint32(n-1), uint32(n-1))
	if err != nil {
		t.Fatalf("ZxyToID(%d, %d, %d): %v", z, n-1, n-1, err)
	}
	if far <= base {
		t.Errorf("the far corner of zoom %d has ID %d, which is not past that zoom's first tile at %d", z, far, base)
	}
	if far >= 1<<63 {
		t.Errorf("the deepest tile has ID %d, at or past 2^63; the comment on MaxZoom says the whole space lands below it", far)
	}
	gz, gx, gy, err := pmtiles.IDToZxy(far)
	if err != nil {
		t.Fatalf("IDToZxy(%d): %v", far, err)
	}
	if gz != z || uint64(gx) != n-1 || uint64(gy) != n-1 {
		t.Errorf("the far corner of zoom %d came back as %d/%d/%d", z, gz, gx, gy)
	}

	// And a zoom one deeper is refused rather than wrapped, because the ID it
	// would compute belongs to a tile that already exists.
	if id, err := pmtiles.ZxyToID(z+1, 0, 0); err == nil {
		t.Errorf("ZxyToID(%d, 0, 0) returned ID %d, and that zoom does not fit in a tile ID", z+1, id)
	}
}
