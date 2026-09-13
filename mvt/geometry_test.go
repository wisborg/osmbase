package mvt_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// decodeOneFeature builds a tile holding a single feature and returns it
// decoded, so the geometry tests read as geometry rather than as protobuf.
func decodeOneFeature(t *testing.T, f osmbasetest.FeatureSpec) mvt.Feature {
	t.Helper()
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{
		Layers: []osmbasetest.LayerSpec{{Name: "l", Features: []osmbasetest.FeatureSpec{f}}},
	})
	if err != nil {
		t.Fatalf("building the fixture tile: %v", err)
	}
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(tile.Layers) != 1 || len(tile.Layers[0].Features) != 1 {
		t.Fatalf("decoded %d layers; want one layer of one feature", len(tile.Layers))
	}
	return tile.Layers[0].Features[0]
}

// TestDecodeGeometry_MultiPolygonCommandStream decodes a command stream
// written out by hand, so that nothing in this test depends on the fixture
// builder being right.
//
// The stream and every coordinate below are derived here rather than taken
// from the decoder. Commands are (id | count<<3), so 9 is MoveTo once, 26 is
// LineTo three times and 15 is ClosePath once; parameters are zigzag deltas
// from a cursor that starts at (0,0) and is NOT reset by ClosePath.
//
//	9  0 0            MoveTo   +0,+0    -> (0,0)      ring A starts
//	26 20 0  0 20  19 0
//	                  LineTo  +10,+0    -> (10,0)
//	                  LineTo   +0,+10   -> (10,10)
//	                  LineTo  -10,+0    -> (0,10)
//	15                ClosePath          cursor stays at (0,10)
//	9  22 2           MoveTo  +11,+1    -> (11,11)    ring B starts
//	26 18 0  0 18  17 0
//	                  LineTo   +9,+0    -> (20,11)
//	                  LineTo   +0,+9    -> (20,20)
//	                  LineTo   -9,+0    -> (11,20)
//	15                ClosePath          cursor stays at (11,20)
//	9  4 13           MoveTo   +2,-7    -> (13,13)    ring C starts
//	26 0 8  8 0  0 7
//	                  LineTo   +0,+4    -> (13,17)
//	                  LineTo   +4,+0    -> (17,17)
//	                  LineTo   +0,-4    -> (17,13)
//	15                ClosePath
//
// Twice the signed area by the surveyor's formula is +200 for ring A, +162 for
// ring B and -32 for ring C, so A and B are exterior rings and C is a hole in
// B. The areas are 100, 81 and 16 square units, which are the 10x10, 9x9 and
// 4x4 squares the coordinates describe -- an independent check that the
// coordinates above were read correctly.
//
// The ClosePath behaviour is the part worth a test of its own: ring B's
// opening MoveTo is +11,+1 from (0,10), which only reaches (11,11) because the
// cursor stayed where ring A's last LineTo left it. A decoder that returned
// the cursor to a ring's first point would put ring B at (11,1), and every
// ring after the first would be displaced by the height of the one before it.
func TestDecodeGeometry_MultiPolygonCommandStream(t *testing.T) {
	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type: mvt.GeomPolygon,
		RawGeometry: []uint32{
			9, 0, 0, 26, 20, 0, 0, 20, 19, 0, 15,
			9, 22, 2, 26, 18, 0, 0, 18, 17, 0, 15,
			9, 4, 13, 26, 0, 8, 8, 0, 0, 7, 15,
		},
	})

	want := []mvt.Polygon{
		{Exterior: mvt.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}}},
		{
			Exterior: mvt.Ring{{11, 11}, {20, 11}, {20, 20}, {11, 20}},
			Holes:    []mvt.Ring{{{13, 13}, {13, 17}, {17, 17}, {17, 13}}},
		},
	}
	if !reflect.DeepEqual(f.Geometry.Polygons, want) {
		t.Errorf("polygons =\n%+v\nwant\n%+v", f.Geometry.Polygons, want)
	}
	if f.Geometry.Points != nil || f.Geometry.Lines != nil {
		t.Error("a polygon feature came back with point or line geometry as well")
	}
}

// TestDecodeGeometry_EveryCommandType covers each geometry shape the schema
// defines, through the fixture builder.
func TestDecodeGeometry_EveryCommandType(t *testing.T) {
	cases := []struct {
		name string
		typ  mvt.GeomType
		in   mvt.Geometry
	}{
		{
			name: "a single point",
			typ:  mvt.GeomPoint,
			in:   mvt.Geometry{Points: []mvt.Point{{25, 17}}},
		},
		{
			name: "a multipoint, which is one MoveTo repeated",
			typ:  mvt.GeomPoint,
			in:   mvt.Geometry{Points: []mvt.Point{{5, 7}, {3, 2}, {0, 0}, {-4, 9}}},
		},
		{
			name: "a linestring",
			typ:  mvt.GeomLineString,
			in:   mvt.Geometry{Lines: [][]mvt.Point{{{2, 2}, {2, 10}, {10, 10}}}},
		},
		{
			name: "a multi-part linestring, which is a second MoveTo",
			typ:  mvt.GeomLineString,
			in: mvt.Geometry{Lines: [][]mvt.Point{
				{{2, 2}, {2, 10}},
				{{100, 100}, {90, 80}, {70, 70}},
			}},
		},
		{
			name: "a line that leaves the tile, which the buffer allows",
			typ:  mvt.GeomLineString,
			in:   mvt.Geometry{Lines: [][]mvt.Point{{{-64, 10}, {4160, 10}}}},
		},
		{
			name: "a polygon",
			typ:  mvt.GeomPolygon,
			in: mvt.Geometry{Polygons: []mvt.Polygon{
				{Exterior: mvt.Ring{{0, 0}, {8, 0}, {8, 8}, {0, 8}}},
			}},
		},
		{
			name: "a polygon with two holes",
			typ:  mvt.GeomPolygon,
			in: mvt.Geometry{Polygons: []mvt.Polygon{{
				Exterior: mvt.Ring{{0, 0}, {100, 0}, {100, 100}, {0, 100}},
				Holes: []mvt.Ring{
					{{10, 10}, {10, 20}, {20, 20}, {20, 10}},
					{{50, 50}, {50, 60}, {60, 60}, {60, 50}},
				},
			}}},
		},
		{
			name: "a multipolygon whose second part has a hole",
			typ:  mvt.GeomPolygon,
			in: mvt.Geometry{Polygons: []mvt.Polygon{
				{Exterior: mvt.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}}},
				{
					Exterior: mvt.Ring{{20, 20}, {40, 20}, {40, 40}, {20, 40}},
					Holes:    []mvt.Ring{{{25, 25}, {25, 30}, {30, 30}, {30, 25}}},
				},
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: c.typ, Geometry: c.in})
			if !reflect.DeepEqual(f.Geometry, c.in) {
				t.Errorf("geometry =\n%+v\nwant\n%+v", f.Geometry, c.in)
			}
		})
	}
}

// TestDecodeGeometry_ZigzagBoundaryValues walks a cursor by parameters at the
// edges of what a tile may reach.
//
// Zigzag maps a signed delta onto an unsigned varint by interleaving the
// signs: 0,-1,1,-2,2 become 0,1,2,3,4, so an encoded 2n is +n and an encoded
// 2n-1 is -n. The expected values below are derived from that rule and not
// from the decoder.
//
// The extremes of the int32 range are NOT here, because the decoder refuses a
// vertex that far out -- see TestDecodeGeometry_RefusesACoordinateNoTileCanReach
// for that, and TestUnzigzag for the arithmetic at its own limits. What is here
// is the largest delta a vertex may legally land on, which at the default
// extent is 256 tile widths.
func TestDecodeGeometry_ZigzagBoundaryValues(t *testing.T) {
	cases := []struct {
		encoded uint32
		want    int32
	}{
		{0, 0},
		{1, -1},
		{2, 1},
		{3, -2},
		{4, 2},
		{2 * 1048576, 1048576},    // the largest vertex the bound allows
		{2*1048576 - 1, -1048576}, // and the same distance the other way
	}
	for _, c := range cases {
		f := decodeOneFeature(t, osmbasetest.FeatureSpec{
			Type:        mvt.GeomPoint,
			RawGeometry: []uint32{9, c.encoded, c.encoded},
		})
		want := []mvt.Point{{X: c.want, Y: c.want}}
		if !reflect.DeepEqual(f.Geometry.Points, want) {
			t.Errorf("parameter %#x decoded to %+v, want %+v", c.encoded, f.Geometry.Points, want)
		}
	}
}

// TestNormalisePolygons_OrientationIsTheSameWhicheverWayTheInputIsWound is the
// point of normalising at all.
//
// The rasterizer this library draws with sums winding across a path and takes
// the absolute value, so an exterior and its hole must arrive wound in
// opposite directions or the hole fills solid -- a lake with an island becomes
// a lake, which is a believable picture and therefore a bug nobody reports.
// Producers do get this wrong, and a whole tile written the other way round is
// the common way they do.
//
// The same polygon is encoded twice, once with every ring reversed, and the
// decoded result must be identical -- not merely valid. Reversing a ring twice
// restores the original point order exactly, so equality is the right
// assertion and a weaker one would pass on a decoder that only got the winding
// right by accident. See docs/architecture.md, trap T13.
func TestNormalisePolygons_OrientationIsTheSameWhicheverWayTheInputIsWound(t *testing.T) {
	forward := mvt.Geometry{Polygons: []mvt.Polygon{{
		Exterior: mvt.Ring{{0, 0}, {100, 0}, {100, 100}, {0, 100}},
		Holes:    []mvt.Ring{{{20, 20}, {20, 40}, {40, 40}, {40, 20}}},
	}}}
	// Sanity: the fixture is wound the way the specification says, so the
	// reversed case below really is the other way round.
	if w := forward.Polygons[0].Exterior.Winding(); w != mvt.ExteriorWinding {
		t.Fatalf("the fixture's exterior is wound %d, want %d", w, mvt.ExteriorWinding)
	}
	if w := forward.Polygons[0].Holes[0].Winding(); w != mvt.HoleWinding {
		t.Fatalf("the fixture's hole is wound %d, want %d", w, mvt.HoleWinding)
	}

	reversed := mvt.Geometry{Polygons: []mvt.Polygon{{
		Exterior: reverse(forward.Polygons[0].Exterior),
		Holes:    []mvt.Ring{reverse(forward.Polygons[0].Holes[0])},
	}}}

	a := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: forward})
	b := decodeOneFeature(t, osmbasetest.FeatureSpec{Type: mvt.GeomPolygon, Geometry: reversed})

	if !reflect.DeepEqual(a.Geometry, b.Geometry) {
		t.Fatalf("the same polygon wound the two ways decoded differently:\nforward:  %+v\nreversed: %+v", a.Geometry, b.Geometry)
	}
	if !reflect.DeepEqual(a.Geometry, forward) {
		t.Errorf("the correctly wound polygon was changed on decode:\ngot  %+v\nwant %+v", a.Geometry, forward)
	}
	for i, p := range b.Geometry.Polygons {
		if w := p.Exterior.Winding(); w != mvt.ExteriorWinding {
			t.Errorf("polygon %d's exterior came back wound %d, want %d", i, w, mvt.ExteriorWinding)
		}
		for j, h := range p.Holes {
			if w := h.Winding(); w != mvt.HoleWinding {
				t.Errorf("polygon %d hole %d came back wound %d, want %d", i, j, w, mvt.HoleWinding)
			}
		}
	}
}

// TestNormalisePolygons_ARingWoundLikeItsExteriorIsANewPolygon pins the rule
// that separates an island from a hole.
//
// The schema says orientation alone decides, and an island in a lake is
// encoded as a second polygon wound like an exterior rather than as a ring
// inside the first. So a nested ring wound the same way as the exterior is a
// new polygon even though it sits inside one, and treating it as a hole
// because it happens to be enclosed would erase islands.
func TestNormalisePolygons_ARingWoundLikeItsExteriorIsANewPolygon(t *testing.T) {
	// Two concentric squares, both wound clockwise: the outer lake and the
	// island in the middle of it.
	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type: mvt.GeomPolygon,
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{
			{Exterior: mvt.Ring{{0, 0}, {100, 0}, {100, 100}, {0, 100}}},
			{Exterior: mvt.Ring{{40, 40}, {60, 40}, {60, 60}, {40, 60}}},
		}},
	})
	if len(f.Geometry.Polygons) != 2 {
		t.Fatalf("decoded %d polygons, want 2", len(f.Geometry.Polygons))
	}
	for i, p := range f.Geometry.Polygons {
		if len(p.Holes) != 0 {
			t.Errorf("polygon %d picked up %d holes", i, len(p.Holes))
		}
	}
}

// TestRing_Winding derives the expected sign from the shape rather than from
// the code: with y running down the screen, a ring visiting its points
// clockwise as drawn has positive area by the surveyor's formula, which is
// what the schema calls an exterior.
func TestRing_Winding(t *testing.T) {
	cases := []struct {
		name string
		ring mvt.Ring
		want int
	}{
		{"a clockwise square, drawn down the screen", mvt.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}}, mvt.ExteriorWinding},
		{"the same square anticlockwise", mvt.Ring{{0, 10}, {10, 10}, {10, 0}, {0, 0}}, mvt.HoleWinding},
		{"a triangle", mvt.Ring{{0, 0}, {10, 0}, {0, 10}}, mvt.ExteriorWinding},
		{"three collinear points enclose nothing", mvt.Ring{{0, 0}, {5, 5}, {10, 10}}, 0},
		{"a square far from the origin, where the area is unchanged", mvt.Ring{{30000, 30000}, {30010, 30000}, {30010, 30010}, {30000, 30010}}, mvt.ExteriorWinding},
		{"two points are not a ring", mvt.Ring{{0, 0}, {10, 10}}, 0},
		{"no points at all", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ring.Winding(); got != c.want {
				t.Errorf("Winding() = %d, want %d", got, c.want)
			}
		})
	}
}

// TestDecodeGeometry_RejectsMalformedCommandStreams. Every case here is a
// stream a decoder could plausibly limp through and return geometry for, and
// geometry that came out of a misread stream is drawn with no sign that
// anything went wrong.
func TestDecodeGeometry_RejectsMalformedCommandStreams(t *testing.T) {
	cases := []struct {
		name    string
		typ     mvt.GeomType
		raw     []uint32
		wantMsg string
	}{
		{
			name:    "a MoveTo whose parameters are cut short",
			typ:     mvt.GeomPoint,
			raw:     []uint32{9, 4},
			wantMsg: "only 1 parameters follow",
		},
		{
			name:    "a LineTo claiming more points than there are parameters",
			typ:     mvt.GeomLineString,
			raw:     []uint32{9, 2, 2, 26, 4, 4},
			wantMsg: "claims 3 points",
		},
		{
			name:    "a command id the schema does not define",
			typ:     mvt.GeomLineString,
			raw:     []uint32{9, 2, 2, 3 | 1<<3, 4, 4},
			wantMsg: "not one of MoveTo, LineTo or ClosePath",
		},
		{
			name:    "a ClosePath repeated",
			typ:     mvt.GeomPolygon,
			raw:     []uint32{9, 0, 0, 26, 20, 0, 0, 20, 19, 0, 7 | 2<<3},
			wantMsg: "must repeat exactly once",
		},
		{
			name:    "a polygon ring that is never closed at the end of the stream",
			typ:     mvt.GeomPolygon,
			raw:     []uint32{9, 0, 0, 26, 20, 0, 0, 20, 19, 0},
			wantMsg: "not closed",
		},
		{
			// The same omission in the middle: the first ring has no
			// ClosePath and a second MoveTo starts another. Accepting this
			// while refusing the case above would make one mistake an error
			// or not depending on which ring it happened in.
			name:    "a polygon ring that is never closed mid-stream",
			typ:     mvt.GeomPolygon,
			raw:     []uint32{9, 0, 0, 26, 20, 0, 0, 20, 19, 0, 9, 22, 2, 26, 18, 0, 0, 18, 17, 0, 15},
			wantMsg: "not closed",
		},
		{
			name:    "a LineTo before any MoveTo",
			typ:     mvt.GeomLineString,
			raw:     []uint32{26, 2, 2, 4, 4, 6, 6},
			wantMsg: "before any MoveTo",
		},
		{
			name:    "a ClosePath in a linestring",
			typ:     mvt.GeomLineString,
			raw:     []uint32{9, 0, 0, 10, 20, 0, 15},
			wantMsg: "ClosePath",
		},
		{
			name:    "a LineTo in a point feature",
			typ:     mvt.GeomPoint,
			raw:     []uint32{9, 0, 0, 10, 20, 0},
			wantMsg: "LineTo",
		},
		{
			name:    "a MoveTo that moves no times",
			typ:     mvt.GeomPoint,
			raw:     []uint32{1},
			wantMsg: "repeats zero times",
		},
		{
			name:    "a ring opened by a MoveTo of two points",
			typ:     mvt.GeomPolygon,
			raw:     []uint32{1 | 2<<3, 0, 0, 4, 4, 26, 20, 0, 0, 20, 19, 0, 15},
			wantMsg: "must move exactly once",
		},
		{
			name:    "a linestring part of one point",
			typ:     mvt.GeomLineString,
			raw:     []uint32{9, 2, 2, 9, 4, 4, 10, 6, 6},
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
			if !strings.Contains(err.Error(), "layer \"l\"") {
				t.Errorf("error was %q, and it should name the layer the bad feature was in", err)
			}
		})
	}
}

// TestDecodeGeometry_RefusesACoordinateNoTileCanReach.
//
// The cursor is an int32 and the deltas are not bounded by anything in the
// format, so without a check a walk of two large deltas wraps it and lands on a
// vertex indistinguishable from one a producer meant -- a plausible coordinate
// in a plausible tile, which nothing downstream could notice.
//
// The bound refuses each step, which is why no case here actually reaches a
// wrap: the first vertex is already out of range. That is the design. A cursor
// inside the bound plus any int32 delta either stays in range or lands at least
// 2^31 - 2^20 from zero, so the bound catches the step before the wrap and
// there is no second step to take.
//
// It is deliberately enormous compared with a real buffer, which is a few
// hundred units past the edge of a 4096-unit tile. Anything reaching it is not
// a producer's geometry.
func TestDecodeGeometry_RefusesACoordinateNoTileCanReach(t *testing.T) {
	cases := []struct {
		name string
		raw  []uint32
	}{
		{
			// The largest positive delta the parameter encoding can express.
			// Walked twice it would wrap the cursor to -2; it never gets the
			// chance, because the first step is already refused.
			name: "a delta at the very top of int32",
			raw:  []uint32{9, 0xfffffffe, 0xfffffffe, 9, 0xfffffffe, 0xfffffffe},
		},
		{
			name: "a single delta beyond the bound",
			raw:  []uint32{9, 2 * (1048576 + 1), 0},
		},
		{
			name: "a single delta beyond the bound going the other way",
			raw:  []uint32{9, 0, 2*(1048576+1) - 1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{
				Layers: []osmbasetest.LayerSpec{{
					Name:     "l",
					Features: []osmbasetest.FeatureSpec{{Type: mvt.GeomPoint, RawGeometry: c.raw}},
				}},
			})
			if err != nil {
				t.Fatalf("building the fixture tile: %v", err)
			}
			tile, err := mvt.Decode(data)
			if err == nil {
				t.Fatalf("Decode accepted the geometry and returned %+v", tile.Layers[0].Features[0].Geometry)
			}
			if !strings.Contains(err.Error(), "units from the origin") {
				t.Errorf("error was %q, and it should say how far out the vertex was", err)
			}
		})
	}
}

// TestDecodeGeometry_ABufferedCoordinateIsStillAccepted is the paired case:
// geometry legitimately spills past the tile edge so a line crossing it can be
// stroked without a notch, and a bound tight enough to refuse that would be
// worse than no bound at all.
func TestDecodeGeometry_ABufferedCoordinateIsStillAccepted(t *testing.T) {
	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type: mvt.GeomLineString,
		Geometry: mvt.Geometry{Lines: [][]mvt.Point{
			{{X: -256, Y: -256}, {X: 4096 + 256, Y: 4096 + 256}},
		}},
	})
	want := [][]mvt.Point{{{X: -256, Y: -256}, {X: 4352, Y: 4352}}}
	if !reflect.DeepEqual(f.Geometry.Lines, want) {
		t.Errorf("lines = %+v, want %+v", f.Geometry.Lines, want)
	}
}

// TestDecodeGeometry_UnknownTypeDecodesToNothing. The schema's UNKNOWN
// geometry type is a feature this decoder can read and cannot interpret, which
// is different from a broken tile: the rest of the tile is fine and must
// still decode.
func TestDecodeGeometry_UnknownTypeDecodesToNothing(t *testing.T) {
	f := decodeOneFeature(t, osmbasetest.FeatureSpec{
		Type:        mvt.GeomUnknown,
		RawGeometry: []uint32{9, 4, 4},
	})
	if f.Type != mvt.GeomUnknown {
		t.Errorf("type = %v, want unknown", f.Type)
	}
	if !f.Geometry.Empty() {
		t.Errorf("geometry = %+v, want nothing", f.Geometry)
	}
}

func reverse(r mvt.Ring) mvt.Ring {
	out := make(mvt.Ring, len(r))
	for i, p := range r {
		out[len(r)-1-i] = p
	}
	return out
}
