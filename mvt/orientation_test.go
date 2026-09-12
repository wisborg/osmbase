package mvt_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// Ring orientation is load bearing rather than cosmetic. The rasterizer this
// library draws with sums winding across a path and takes the ABSOLUTE value,
// so a ring whose orientation disagrees with its role does not fail: it fills.
// A hole wound like its exterior fills solid, which turns a lake with an
// island into a plain lake, and two copies of a ring with opposite winding
// cancel to nothing, which makes a lake vanish. Both are believable pictures.
// See docs/architecture.md, trap T13.
//
// The existing tests cover one exterior with one hole, both windings. What is
// added here is the island case under both windings, and the degenerate ring,
// which the decoder has a documented policy for and no test.

// reverseRings returns the geometry with every ring wound the other way, which
// is what a producer that writes a whole tile backwards emits. Reversing twice
// restores the original point order exactly, so a decoder that normalises
// correctly must return the SAME geometry for a fixture and its reverse -- not
// merely a valid one.
func reverseRings(g mvt.Geometry) mvt.Geometry {
	out := mvt.Geometry{Polygons: make([]mvt.Polygon, len(g.Polygons))}
	for i, p := range g.Polygons {
		out.Polygons[i].Exterior = reverse(p.Exterior)
		for _, h := range p.Holes {
			out.Polygons[i].Holes = append(out.Polygons[i].Holes, reverse(h))
		}
	}
	return out
}

// TestNormalisePolygons_AnIslandInALakeSurvivesATileWoundBackwards pairs with
// the existing island test, which only ever sees the specification's winding.
//
// An island is encoded as a SECOND ring wound like the exterior, not as a hole,
// and the decoder decides that relatively -- a ring wound like the feature's
// first ring starts a new polygon. Reading orientation absolutely instead
// works perfectly on a tile written the right way round and turns the island
// into a hole on a tile written backwards, which cuts the island out of the
// lake and leaves a hole in the water where land should be. Neither errors and
// both draw.
//
// The two rings here are concentric squares with the same winding, so their
// roles cannot be told apart by containment -- which is the point: containment
// would classify the inner one as a hole and erase every island in the world.
func TestNormalisePolygons_AnIslandInALakeSurvivesATileWoundBackwards(t *testing.T) {
	lakeAndIsland := mvt.Geometry{Polygons: []mvt.Polygon{
		{Exterior: mvt.Ring{{0, 0}, {100, 0}, {100, 100}, {0, 100}}},
		{Exterior: mvt.Ring{{40, 40}, {60, 40}, {60, 60}, {40, 60}}},
	}}
	// Both rings are wound the specification's way in the forward fixture, so
	// the reversed one below really is the other direction.
	for i, p := range lakeAndIsland.Polygons {
		if w := p.Exterior.Winding(); w != mvt.ExteriorWinding {
			t.Fatalf("fixture ring %d is wound %d, want %d", i, w, mvt.ExteriorWinding)
		}
	}

	forward := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: lakeAndIsland})
	backward := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: reverseRings(lakeAndIsland)})

	if !reflect.DeepEqual(forward.Geometry, backward.Geometry) {
		t.Fatalf("the same lake and island wound the two ways decoded differently:\nforward:  %+v\nbackward: %+v", forward.Geometry, backward.Geometry)
	}
	if !reflect.DeepEqual(forward.Geometry, lakeAndIsland) {
		t.Errorf("the correctly wound lake and island was changed on decode:\ngot  %+v\nwant %+v", forward.Geometry, lakeAndIsland)
	}
	// Said again as the property rather than as equality, because the equality
	// above would also hold if both decoded to one polygon with a hole.
	for _, g := range []struct {
		name string
		geom mvt.Geometry
	}{{"forward", forward.Geometry}, {"backward", backward.Geometry}} {
		if len(g.geom.Polygons) != 2 {
			t.Errorf("%s: decoded %d polygons, want 2; the island must not become a hole", g.name, len(g.geom.Polygons))
			continue
		}
		for i, p := range g.geom.Polygons {
			if len(p.Holes) != 0 {
				t.Errorf("%s: polygon %d picked up %d holes", g.name, i, len(p.Holes))
			}
			if w := p.Exterior.Winding(); w != mvt.ExteriorWinding {
				t.Errorf("%s: polygon %d's exterior came back wound %d, want %d", g.name, i, w, mvt.ExteriorWinding)
			}
		}
	}
}

// TestNormalisePolygons_APondOnAnIslandInALakeKeepsItsHoleWithItsOwnExterior
// is the four-ring shape, which is where "attach the hole to the polygon in
// progress" and "attach it to the first polygon" stop agreeing.
//
// The wire order is exterior, hole, exterior, hole: the lake, the island cut
// out of it, the pond on that island, and the islet cut out of the pond. A
// decoder that hung every hole on the first polygon produces a lake with two
// holes and a pond with none -- the islet fills solid and the pond is a
// mystery hole in the lake -- and it produces the right number of polygons
// while doing it, so a count-only assertion passes.
//
// Both windings again, because the relative anchor is what makes the third
// ring an exterior rather than another hole.
func TestNormalisePolygons_APondOnAnIslandInALakeKeepsItsHoleWithItsOwnExterior(t *testing.T) {
	nested := mvt.Geometry{Polygons: []mvt.Polygon{
		{
			Exterior: mvt.Ring{{0, 0}, {200, 0}, {200, 200}, {0, 200}},
			Holes:    []mvt.Ring{{{20, 20}, {20, 180}, {180, 180}, {180, 20}}},
		},
		{
			Exterior: mvt.Ring{{60, 60}, {140, 60}, {140, 140}, {60, 140}},
			Holes:    []mvt.Ring{{{90, 90}, {90, 110}, {110, 110}, {110, 90}}},
		},
	}}

	forward := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: nested})
	backward := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: reverseRings(nested)})

	if !reflect.DeepEqual(forward.Geometry, nested) {
		t.Errorf("the correctly wound fixture was changed on decode:\ngot  %+v\nwant %+v", forward.Geometry, nested)
	}
	if !reflect.DeepEqual(backward.Geometry, nested) {
		t.Errorf("the backwards-wound fixture did not normalise to the same thing:\ngot  %+v\nwant %+v", backward.Geometry, nested)
	}
	// Each polygon owns exactly one hole. This is the assertion that separates
	// "hole goes on the polygon in progress" from "hole goes on the first".
	for _, g := range []struct {
		name string
		geom mvt.Geometry
	}{{"forward", forward.Geometry}, {"backward", backward.Geometry}} {
		if len(g.geom.Polygons) != 2 {
			t.Fatalf("%s: decoded %d polygons, want 2", g.name, len(g.geom.Polygons))
		}
		for i, p := range g.geom.Polygons {
			if len(p.Holes) != 1 {
				t.Errorf("%s: polygon %d has %d holes, want 1", g.name, i, len(p.Holes))
				continue
			}
			if w := p.Holes[0].Winding(); w != mvt.HoleWinding {
				t.Errorf("%s: polygon %d's hole is wound %d, want %d; wound like its exterior it fills solid", g.name, i, w, mvt.HoleWinding)
			}
		}
	}
}

// TestNormalisePolygons_ARingWithNoAreaBecomesAHoleAndDoesNotStealTheNextOne
// pins the documented policy for a ring that encloses nothing, and the reason
// the policy is that one rather than another.
//
// A degenerate ring has no orientation to read, so it cannot say whether it
// opens a polygon or cuts one. The decoder attaches it as a hole of the
// polygon in progress, where it draws nothing either way. The alternatives
// both damage the geometry AROUND it, which is why this is worth a test rather
// than being a detail: start a new polygon with it and every ring after it
// attaches to a polygon with no outline, so the lake below fills solid and its
// island is cut out of nothing; drop it and the decode disagrees with the
// bytes.
//
// The fixture is a lake, then three collinear points, then the island. The
// island must still be a hole of the LAKE.
func TestNormalisePolygons_ARingWithNoAreaBecomesAHoleAndDoesNotStealTheNextOne(t *testing.T) {
	flat := mvt.Ring{{5, 5}, {10, 10}, {15, 15}}
	if w := flat.Winding(); w != 0 {
		t.Fatalf("the fixture's collinear ring is wound %d, and it must enclose no area for this test to mean anything", w)
	}
	lake := mvt.Ring{{0, 0}, {100, 0}, {100, 100}, {0, 100}}
	island := mvt.Ring{{40, 40}, {40, 60}, {60, 60}, {60, 40}}
	if lake.Winding() != mvt.ExteriorWinding || island.Winding() != mvt.HoleWinding {
		t.Fatalf("the fixture's lake and island are wound %d and %d, want %d and %d", lake.Winding(), island.Winding(), mvt.ExteriorWinding, mvt.HoleWinding)
	}

	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type: mvt.GeomPolygon,
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
			Exterior: lake,
			Holes:    []mvt.Ring{flat, island},
		}}},
	})

	if len(f.Geometry.Polygons) != 1 {
		t.Fatalf("decoded %d polygons, want 1; a ring with no area must not open one", len(f.Geometry.Polygons))
	}
	p := f.Geometry.Polygons[0]
	if !reflect.DeepEqual(p.Exterior, lake) {
		t.Errorf("exterior = %+v, want the lake %+v", p.Exterior, lake)
	}
	if len(p.Holes) != 2 {
		t.Fatalf("the polygon has %d holes, want 2: the degenerate ring and the island", len(p.Holes))
	}
	// The degenerate ring comes back with its points in the order it was
	// written. There is no direction to correct, so reversing it would change
	// the bytes' meaning for nothing.
	if !reflect.DeepEqual(p.Holes[0], flat) {
		t.Errorf("the ring with no area came back as %+v, want %+v unchanged", p.Holes[0], flat)
	}
	if !reflect.DeepEqual(p.Holes[1], island) {
		t.Errorf("the island came back as %+v, want %+v; it belongs to the lake, not to the degenerate ring", p.Holes[1], island)
	}
}

// TestNormalisePolygons_ALeadingRingWithNoAreaDoesNotSwallowTheRealExterior.
//
// The decoder reads the winding of the FIRST ring to decide which direction
// means "exterior" in this feature. When the first ring encloses no area there
// is nothing to read, and it falls back to the specification's convention. If
// that fallback were wrong -- or missing, leaving the anchor at zero -- every
// following ring would differ from the anchor and become a hole, so a feature
// whose first ring is degenerate would come back as one empty polygon with all
// its real geometry hanging off it as holes. That draws nothing at all.
func TestNormalisePolygons_ALeadingRingWithNoAreaDoesNotSwallowTheRealExterior(t *testing.T) {
	flat := mvt.Ring{{0, 0}, {4, 4}, {8, 8}}
	real := mvt.Ring{{20, 20}, {80, 20}, {80, 80}, {20, 80}}
	if flat.Winding() != 0 || real.Winding() != mvt.ExteriorWinding {
		t.Fatalf("fixture windings are %d and %d, want 0 and %d", flat.Winding(), real.Winding(), mvt.ExteriorWinding)
	}

	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type: mvt.GeomPolygon,
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{
			{Exterior: flat},
			{Exterior: real},
		}},
	})

	if len(f.Geometry.Polygons) != 2 {
		t.Fatalf("decoded %d polygons, want 2; the real exterior must not become a hole of the degenerate one", len(f.Geometry.Polygons))
	}
	if !reflect.DeepEqual(f.Geometry.Polygons[1].Exterior, real) {
		t.Errorf("the second polygon's exterior = %+v, want %+v", f.Geometry.Polygons[1].Exterior, real)
	}
	if len(f.Geometry.Polygons[1].Holes) != 0 {
		t.Errorf("the real polygon picked up %d holes", len(f.Geometry.Polygons[1].Holes))
	}
}

// TestDecodeGeometry_APolygonFeatureWithNoGeometryIsEmptyRatherThanAFailure.
// A feature can legitimately carry no geometry field at all, and the ring
// grouping has to cope with being handed nothing. Producing a zero-value
// Polygon instead would give the rasterizer an exterior of no points.
func TestDecodeGeometry_APolygonFeatureWithNoGeometryIsEmptyRatherThanAFailure(t *testing.T) {
	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type:        mvt.GeomPolygon,
		RawGeometry: []uint32{},
	})
	if f.Type != mvt.GeomPolygon {
		t.Errorf("type = %v, want polygon", f.Type)
	}
	if !f.Geometry.Empty() {
		t.Errorf("geometry = %+v, want nothing", f.Geometry)
	}
	if f.Geometry.Polygons != nil {
		t.Errorf("polygons = %+v, want nil rather than an empty polygon", f.Geometry.Polygons)
	}
}

// TestDecodeGeometry_RejectsStreamsTheExistingTableMisses covers two malformed
// streams the decoder guards against and nothing exercised.
//
// A ClosePath before any MoveTo in a POLYGON reaches a different guard from
// the same command in a linestring, which is rejected earlier for being the
// wrong geometry type. And a linestring whose final part has one point is
// flushed at the end of the stream rather than by the next MoveTo, which is a
// second place the same check has to be made -- a stream ending in a stray
// MoveTo would otherwise yield a "line" of one point that strokes to nothing.
func TestDecodeGeometry_RejectsStreamsTheExistingTableMisses(t *testing.T) {
	cases := []struct {
		name    string
		typ     mvt.GeomType
		raw     []uint32
		wantMsg string
	}{
		{
			name:    "a ClosePath opening a polygon",
			typ:     mvt.GeomPolygon,
			raw:     []uint32{15},
			wantMsg: "before any MoveTo",
		},
		{
			name:    "a linestring whose last part is left with one point",
			typ:     mvt.GeomLineString,
			raw:     []uint32{9, 2, 2, 10, 4, 4, 9, 6, 6},
			wantMsg: "1 points",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{
				Layers: []osmbasetest.LayerSpec{{
					Name:     "l",
					Features: []osmbasetest.FeatureSpec{{Type: c.typ, RawGeometry: c.raw}},
				}},
			})
			if err != nil {
				t.Fatalf("building the fixture tile: %v", err)
			}
			tile, err := mvt.Decode(data)
			if err == nil {
				t.Fatalf("Decode accepted the stream and returned %+v", tile)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should say %q", err, c.wantMsg)
			}
		})
	}
}
