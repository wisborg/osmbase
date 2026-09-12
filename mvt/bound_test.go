package mvt_test

import (
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// The vertex bound is two rules, and every existing test exercises them at the
// one extent where the two are indistinguishable.
//
// The rule is: a coordinate may lie up to 256 tile widths from the origin --
// which is the buffer a producer may write, with four orders of magnitude of
// headroom -- but never more than 2^20 units, because that ceiling is what
// keeps the surveyor's formula in Ring.Winding inside an int64 and therefore
// keeps the SIGN of a ring's area right, and the sign is what decides whether
// a ring is a hole.
//
// At the default extent of 4096 those two give the same number: 4096 * 256 is
// exactly 2^20. So a suite that only ever builds 4096-unit layers cannot tell
// the multiplier from the ceiling, cannot tell either from a constant, and
// would pass unchanged if the bound stopped depending on the extent at all.
// Extents other than 4096 are ordinary -- see TestDecode_ExtentIsPerLayer,
// which builds a 512-unit layer -- so this is not an exotic case.
//
// Every expected bound below is min(extent*256, 2^20), worked out in the table
// from that rule rather than read back from the decoder.

// zz encodes a geometry parameter, so these tests do not depend on the fixture
// builder's encoder for the one number they are about.
func zz(v int32) uint32 { return uint32(v<<1) ^ uint32(v>>31) }

// decodeVertexAt builds a one-feature tile in a layer of the given extent,
// whose whole geometry is a MoveTo to (v, v), and returns the decode error.
func decodeVertexAt(t *testing.T, extent uint32, v int32) error {
	t.Helper()
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name:     "l",
		Extent:   extent,
		Features: []osmbasetest.FeatureSpec{{Type: mvt.GeomPoint, RawGeometry: []uint32{9, zz(v), zz(v)}}},
	}}})
	if err != nil {
		t.Fatalf("building the fixture tile: %v", err)
	}
	_, err = mvt.Decode(data)
	return err
}

// TestDecodeGeometry_TheVertexBoundScalesWithTheLayersExtent is the paired
// test: at each extent the last vertex inside the bound decodes and the first
// one outside it is refused.
//
// A decoder that used a constant bound, or that took the extent from the wrong
// layer, passes every other test in this package: at 4096 the constant and the
// computed bound agree exactly. Here a 256-unit layer -- a coarse landcover
// layer, which is a real shape -- has a bound sixteen times smaller, and a
// vertex a quarter of a million units out is geometry no such tile can carry.
func TestDecodeGeometry_TheVertexBoundScalesWithTheLayersExtent(t *testing.T) {
	cases := []struct {
		name   string
		extent uint32
		bound  int32
	}{
		// 256 * 256 = 65536, well below the ceiling.
		{"a coarse layer", 256, 65536},
		// 512 * 256 = 131072. This is the extent TestDecode_ExtentIsPerLayer
		// already builds, so it is not a hypothetical shape.
		{"the coarse layer the suite already builds", 512, 131072},
		// 4096 * 256 = 1048576, which is the ceiling exactly.
		{"the default extent, where the two rules meet", 4096, 1 << 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := decodeVertexAt(t, c.extent, c.bound); err != nil {
				t.Errorf("a vertex at %d in a layer of extent %d was refused: %v", c.bound, c.extent, err)
			}
			if err := decodeVertexAt(t, c.extent, -c.bound); err != nil {
				t.Errorf("a vertex at %d in a layer of extent %d was refused: %v", -c.bound, c.extent, err)
			}
			err := decodeVertexAt(t, c.extent, c.bound+1)
			if err == nil {
				t.Errorf("a vertex at %d was accepted in a layer of extent %d, whose bound is %d", c.bound+1, c.extent, c.bound)
			} else if !strings.Contains(err.Error(), "units from the origin") {
				t.Errorf("error was %q, and it should say how far out the vertex was", err)
			}
			if err := decodeVertexAt(t, c.extent, -(c.bound + 1)); err == nil {
				t.Errorf("a vertex at %d was accepted in a layer of extent %d, whose bound is %d", -(c.bound + 1), c.extent, c.bound)
			}
		})
	}
}

// TestDecodeGeometry_TheVertexBoundIsCappedHoweverLargeTheExtent is the other
// half, and it is the half that protects Ring.Winding.
//
// 256 tile widths of a 65536-unit layer would be 2^24, and a ring at that
// distance makes the cross products in the surveyor's formula reach 2^48 --
// which a long ring accumulates past an int64, flipping the sign of the area
// and turning an exterior into a hole silently. The ceiling is therefore not a
// tidy-up: it is what makes the integer arithmetic in Winding provable.
//
// A large extent is legal and a producer may use one; the decoder's answer is
// to keep the absolute ceiling rather than to scale past it.
func TestDecodeGeometry_TheVertexBoundIsCappedHoweverLargeTheExtent(t *testing.T) {
	for _, extent := range []uint32{8192, 65536, 1 << 20} {
		// The ceiling, not extent*256.
		const ceiling = int32(1 << 20)
		if err := decodeVertexAt(t, extent, ceiling); err != nil {
			t.Errorf("a vertex at the ceiling %d in a layer of extent %d was refused: %v", ceiling, extent, err)
		}
		err := decodeVertexAt(t, extent, ceiling+1)
		if err == nil {
			t.Errorf("a vertex at %d was accepted in a layer of extent %d; the bound is capped at %d whatever the extent, because that cap is what keeps Ring.Winding exact", ceiling+1, extent, ceiling)
			continue
		}
		if !strings.Contains(err.Error(), "units from the origin") {
			t.Errorf("error was %q, and it should say how far out the vertex was", err)
		}
	}
}

// TestRing_WindingIsExactAtTheExtremesOfTheDecodersOwnBound is the assertion
// the vertex ceiling exists to make true, said at the magnitude that matters.
//
// TestRing_Winding's largest case is a square at 30,000 units, where twice the
// area is about 2*10^2 and the cross products are about 10^9 -- nine orders of
// magnitude inside an int64, so it tests nothing about overflow. The bound the
// decoder enforces is 2^20, thirty-five times further out, and a ring spanning
// the whole of it is the worst case this package can legally produce.
//
// Both windings, and a long ring as well as a short one: the sum is what
// overflows, so a ring of many vertices is a different risk from a ring of few
// at the same distance. Every expected answer is derived from the shape -- with
// y running down the screen, a ring visiting its points clockwise as drawn is
// an exterior -- and the reverse of an exterior is a hole.
func TestRing_WindingIsExactAtTheExtremesOfTheDecodersOwnBound(t *testing.T) {
	const b = int32(1 << 20)

	square := mvt.Ring{{X: -b, Y: -b}, {X: b, Y: -b}, {X: b, Y: b}, {X: -b, Y: b}}

	// A ring hugging the whole bound with many vertices: down the left edge in
	// steps, then back along the right. Drawn clockwise with y down, so an
	// exterior.
	const steps = 2000
	zigzag := make(mvt.Ring, 0, 2*steps)
	for i := 0; i < steps; i++ {
		y := int32(-b + int32(int64(2*b)*int64(i)/int64(steps)))
		zigzag = append(zigzag, mvt.Point{X: b, Y: y})
	}
	for i := steps - 1; i >= 0; i-- {
		y := int32(-b + int32(int64(2*b)*int64(i)/int64(steps)))
		zigzag = append(zigzag, mvt.Point{X: -b, Y: y})
	}

	cases := []struct {
		name string
		ring mvt.Ring
	}{
		{"a square spanning the whole bound", square},
		{"a two-thousand-vertex ring at the bound", zigzag},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ring.Winding(); got != mvt.ExteriorWinding {
				t.Errorf("Winding() = %d, want %d; the area overflowed and the sign is what decides whether a ring is a hole", got, mvt.ExteriorWinding)
			}
			rev := make(mvt.Ring, len(c.ring))
			for i, p := range c.ring {
				rev[len(c.ring)-1-i] = p
			}
			if got := rev.Winding(); got != mvt.HoleWinding {
				t.Errorf("the same ring reversed has Winding() = %d, want %d", got, mvt.HoleWinding)
			}
		})
	}
}

// TestNormalisePolygons_AHoleAtTheExtremesOfTheBoundStillCutsItsExterior
// carries the previous test through the decoder, which is where a flipped sign
// stops being an arithmetic detail and becomes a lake with no island.
//
// The exterior spans the whole bound and the hole is a large square inside it.
// If the area of either overflowed, the hole would be read as a second
// exterior and would be handed to the rasterizer wound the same way as the
// ring it is meant to cut out of -- which, with absolute accumulated winding,
// fills solid. See docs/architecture.md, trap T13.
func TestNormalisePolygons_AHoleAtTheExtremesOfTheBoundStillCutsItsExterior(t *testing.T) {
	const b = int32(1 << 20)
	const h = b / 2
	in := mvt.Geometry{Polygons: []mvt.Polygon{{
		Exterior: mvt.Ring{{X: -b, Y: -b}, {X: b, Y: -b}, {X: b, Y: b}, {X: -b, Y: b}},
		Holes:    []mvt.Ring{{{X: -h, Y: -h}, {X: -h, Y: h}, {X: h, Y: h}, {X: h, Y: -h}}},
	}}}
	if w := in.Polygons[0].Exterior.Winding(); w != mvt.ExteriorWinding {
		t.Fatalf("the fixture's exterior is wound %d, want %d", w, mvt.ExteriorWinding)
	}
	if w := in.Polygons[0].Holes[0].Winding(); w != mvt.HoleWinding {
		t.Fatalf("the fixture's hole is wound %d, want %d", w, mvt.HoleWinding)
	}

	f := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: in})
	if len(f.Geometry.Polygons) != 1 {
		t.Fatalf("decoded %d polygons, want 1; the hole was read as a second exterior", len(f.Geometry.Polygons))
	}
	p := f.Geometry.Polygons[0]
	if len(p.Holes) != 1 {
		t.Fatalf("the polygon has %d holes, want 1", len(p.Holes))
	}
	if w := p.Holes[0].Winding(); w != mvt.HoleWinding {
		t.Errorf("the hole came back wound %d, want %d; wound like its exterior it fills solid", w, mvt.HoleWinding)
	}
	if w := p.Exterior.Winding(); w != mvt.ExteriorWinding {
		t.Errorf("the exterior came back wound %d, want %d", w, mvt.ExteriorWinding)
	}
}
