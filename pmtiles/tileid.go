package pmtiles

import "fmt"

// PMTiles addresses a tile by a single number rather than by z/x/y: the tile
// IDs of every zoom are laid end to end, and within a zoom the order is that
// zoom's Hilbert curve. The point of the Hilbert order is locality -- tiles
// that are near each other on the ground are near each other in the archive --
// which is what makes a cell's sub-pyramid a handful of byte ranges instead of
// one request per tile.
//
// There is no way to shortcut the curve. The conversion below is the standard
// Hilbert xy<->d pair, and it is the one piece of this format where a
// plausible-looking wrong answer is easy to produce: get the quadrant rotation
// backwards and zoom 0 and zoom 1 still come out exactly right, because at
// zoom 1 the curve visits all four quadrants whatever the rotation does. The
// first zoom that disagrees is 2. Hence the spec's zoom-12 anchor, and hence
// the exhaustive round-trip test.

// MaxZoom is the deepest zoom a tile ID can address.
//
// The first tile of zoom z has ID (4^z-1)/3 and the Hilbert offset within the
// zoom is below 4^z, so zoom 31 lands just under 2^63 and zoom 32 would not
// fit the uint64 the format defines. The public vector tile builds this
// library reads stop at 15, so this is a guard against arithmetic that
// silently wrapped, not a restriction anybody will meet.
const MaxZoom = 31

// ZxyToID returns the tile ID of the tile at z/x/y.
//
// It fails rather than wrapping for a coordinate outside its zoom, because the
// wrapped answer is a valid ID of some other tile and would be served happily
// by the rest of the reader.
func ZxyToID(z uint8, x, y uint32) (uint64, error) {
	if z > MaxZoom {
		return 0, fmt.Errorf("pmtiles: zoom %d is deeper than the deepest addressable zoom %d", z, MaxZoom)
	}
	n := uint64(1) << z
	if uint64(x) >= n || uint64(y) >= n {
		return 0, fmt.Errorf("pmtiles: tile %d/%d/%d is outside zoom %d, which is %d tiles square", z, x, y, z, n)
	}
	return zoomBase(z) + hilbertToOffset(n, uint64(x), uint64(y)), nil
}

// IDToZxy is ZxyToID's inverse.
func IDToZxy(id uint64) (z uint8, x, y uint32, err error) {
	for zz := uint8(0); zz <= MaxZoom; zz++ {
		base := zoomBase(zz)
		if id < base+uint64(1)<<(2*zz) {
			hx, hy := offsetToHilbert(uint64(1)<<zz, id-base)
			return zz, uint32(hx), uint32(hy), nil
		}
	}
	return 0, 0, 0, fmt.Errorf("pmtiles: tile ID %d is beyond the deepest addressable zoom %d", id, MaxZoom)
}

// zoomBase is the ID of the first tile of zoom z: the number of tiles in every
// shallower zoom, which is the geometric sum 1+4+16+... = (4^z-1)/3.
func zoomBase(z uint8) uint64 { return (uint64(1)<<(2*z) - 1) / 3 }

// hilbertToOffset returns the position of (x, y) along the order-n Hilbert
// curve. n is the side of the square and is always a power of two.
//
// Each iteration takes one bit of x and y, most significant first, adds the
// number of cells the curve covers before reaching this quadrant, and then
// reflects the remaining coordinates into the orientation that quadrant's
// sub-curve is drawn in. The reflection is the whole algorithm; without it
// this computes a Z-order curve, which agrees with Hilbert at zooms 0 and 1.
func hilbertToOffset(n, x, y uint64) uint64 {
	var d uint64
	for s := n / 2; s > 0; s /= 2 {
		var rx, ry uint64
		if x&s > 0 {
			rx = 1
		}
		if y&s > 0 {
			ry = 1
		}
		d += s * s * ((3 * rx) ^ ry)
		rotate(s, &x, &y, rx, ry)
	}
	return d
}

// offsetToHilbert returns the (x, y) at position d along the order-n Hilbert
// curve, building the coordinates from the least significant bit upwards.
func offsetToHilbert(n, d uint64) (x, y uint64) {
	t := d
	for s := uint64(1); s < n; s *= 2 {
		rx := 1 & (t / 2)
		ry := 1 & (t ^ rx)
		rotate(s, &x, &y, rx, ry)
		x += s * rx
		y += s * ry
		t /= 4
	}
	return x, y
}

// rotate reflects a quadrant of side s into the orientation its sub-curve
// needs. Only the lower quadrants (ry == 0) are rotated: the left one by a
// transpose, the right one by a transpose about the other diagonal.
func rotate(s uint64, x, y *uint64, rx, ry uint64) {
	if ry != 0 {
		return
	}
	if rx == 1 {
		*x = s - 1 - *x
		*y = s - 1 - *y
	}
	*x, *y = *y, *x
}
