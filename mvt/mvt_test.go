package mvt_test

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// buildTile is the fixture path every test in this file uses.
func buildTile(t *testing.T, spec osmbasetest.TileSpec) []byte {
	t.Helper()
	data, err := osmbasetest.BuildTile(spec)
	if err != nil {
		t.Fatalf("building the fixture tile: %v", err)
	}
	return data
}

// TestDecode_ResolvesTagsAgainstTheLayerTables covers all seven value types
// the schema defines.
//
// Attributes are not stored with their features: a layer holds a table of keys
// and a table of values, and a feature holds pairs of indices into them. Every
// expected value below is stated as the number that was put in, so a decoder
// that read a varint as a zigzag, or a fixed32 as a fixed64, disagrees.
func TestDecode_ResolvesTagsAgainstTheLayerTables(t *testing.T) {
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "places",
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{{1, 1}}},
			Tags: []osmbasetest.Tag{
				{Key: "name", Value: mvt.StringValue("somewhere")},
				{Key: "ratio", Value: mvt.FloatValue(2.5)},
				{Key: "area", Value: mvt.DoubleValue(1234.5)},
				{Key: "count", Value: mvt.IntValue(42)},
				{Key: "population", Value: mvt.UintValue(1 << 40)},
				{Key: "delta", Value: mvt.SintValue(-7)},
				{Key: "capital", Value: mvt.BoolValue(true)},
			},
		}},
	}}})

	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	layer, ok := tile.Layer("places")
	if !ok {
		t.Fatal("the tile has no layer named \"places\"")
	}
	if len(layer.Features) != 1 {
		t.Fatalf("layer has %d features, want 1", len(layer.Features))
	}
	f := layer.Features[0]

	want := map[string]mvt.Value{
		"name":       mvt.StringValue("somewhere"),
		"ratio":      mvt.FloatValue(2.5),
		"area":       mvt.DoubleValue(1234.5),
		"count":      mvt.IntValue(42),
		"population": mvt.UintValue(1 << 40),
		"delta":      mvt.SintValue(-7),
		"capital":    mvt.BoolValue(true),
	}
	if len(f.Tags) != len(want) {
		t.Errorf("feature has %d tags, want %d", len(f.Tags), len(want))
	}
	for key, w := range want {
		got, ok := f.Tag(key)
		if !ok {
			t.Errorf("feature has no tag %q", key)
			continue
		}
		if got != w {
			t.Errorf("tag %q = %+v, want %+v", key, got, w)
		}
	}

	// Float64 folds every numeric kind and refuses the two that are not
	// numbers, so a style asking for a threshold cannot be handed a string
	// length or a boolean rendered as 1.
	for key, want := range map[string]float64{
		"ratio": 2.5, "area": 1234.5, "count": 42, "population": 1 << 40, "delta": -7,
	} {
		v, _ := f.Tag(key)
		got, ok := v.Float64()
		if !ok || got != want {
			t.Errorf("tag %q as a float = %v, %v; want %v, true", key, got, ok, want)
		}
	}
	for _, key := range []string{"name", "capital"} {
		v, _ := f.Tag(key)
		if got, ok := v.Float64(); ok {
			t.Errorf("tag %q converted to the number %v, and it is not a number", key, got)
		}
	}
	if s, ok := f.Tags["name"].Text(); !ok || s != "somewhere" {
		t.Errorf("Text() on the name = %q, %v", s, ok)
	}
	if _, ok := f.Tags["count"].Text(); ok {
		t.Error("Text() turned an integer into a string")
	}
}

// TestDecode_SintValuesAtTheEndsOfTheirRange takes the 64-bit zigzag through
// the whole path a real tile uses: encoded by the fixture builder, decoded by
// the value decoder.
//
// The 32-bit zigzag on the geometry path is bounded long before its extremes,
// so this is the only encoding in the format where a value at the limit of its
// width actually travels end to end -- and the two ends are where a shift or a
// cast of the wrong width is the difference between a number and its negation.
func TestDecode_SintValuesAtTheEndsOfTheirRange(t *testing.T) {
	wanted := []int64{0, -1, 1, -2, 2, math.MaxInt64, math.MinInt64, math.MaxInt64 - 1, math.MinInt64 + 1}

	tags := make([]osmbasetest.Tag, 0, len(wanted))
	for i, v := range wanted {
		tags = append(tags, osmbasetest.Tag{Key: fmt.Sprintf("k%d", i), Value: mvt.SintValue(v)})
	}
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "l",
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPoint,
			Tags:     tags,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1, Y: 1}}},
		}},
	}}})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	f := tile.Layers[0].Features[0]
	for i, want := range wanted {
		key := fmt.Sprintf("k%d", i)
		got, ok := f.Tag(key)
		if !ok {
			t.Errorf("no tag %q", key)
			continue
		}
		if got.Kind != mvt.ValueSint || got.Sint != want {
			t.Errorf("tag %q = %+v, want an sint of %d", key, got, want)
		}
	}
}

// TestDecode_AnAbsentTagIsNotAnEmptyValue. A feature with no name and a
// feature named "" are different features, and a style that cannot tell them
// apart will put a label on one of them. Membership is the test; the value is
// meaningless without it.
func TestDecode_AnAbsentTagIsNotAnEmptyValue(t *testing.T) {
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "roads",
		Features: []osmbasetest.FeatureSpec{
			{
				Type:     mvt.GeomPoint,
				Geometry: mvt.Geometry{Points: []mvt.Point{{1, 1}}},
				Tags:     []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("")}},
			},
			{
				Type:     mvt.GeomPoint,
				Geometry: mvt.Geometry{Points: []mvt.Point{{2, 2}}},
			},
		},
	}}})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	named, unnamed := tile.Layers[0].Features[0], tile.Layers[0].Features[1]

	v, ok := named.Tag("name")
	if !ok {
		t.Error("the feature with an empty name reports no name tag at all")
	}
	if v.Kind != mvt.ValueString || v.Str != "" {
		t.Errorf("the empty name decoded as %+v", v)
	}
	if _, ok := unnamed.Tag("name"); ok {
		t.Error("the feature with no tags reports a name")
	}
}

// TestDecode_FeatureIDIsOptionalAndZeroIsALegalID. The schema makes the id
// optional and zero is a perfectly good identifier, so the presence flag is
// the only thing that separates "feature zero" from "no identifier at all".
func TestDecode_FeatureIDIsOptionalAndZeroIsALegalID(t *testing.T) {
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "l",
		Features: []osmbasetest.FeatureSpec{
			{Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{1, 1}}}},
			{ID: 0, HasID: true, Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{2, 2}}}},
			{ID: 99, HasID: true, Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{3, 3}}}},
		},
	}}})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	features := tile.Layers[0].Features
	if features[0].HasID {
		t.Errorf("the feature with no id reports id %d", features[0].ID)
	}
	if !features[1].HasID || features[1].ID != 0 {
		t.Errorf("feature zero came back as id %d, present %v", features[1].ID, features[1].HasID)
	}
	if !features[2].HasID || features[2].ID != 99 {
		t.Errorf("feature 99 came back as id %d, present %v", features[2].ID, features[2].HasID)
	}
}

// TestDecode_ExtentIsPerLayer. The extent is the tile's own coordinate range
// and it is declared per layer, not per tile, so a decoder that read one and
// applied it to all would scale a whole layer wrongly. An absent extent means
// the schema's default of 4096.
func TestDecode_ExtentIsPerLayer(t *testing.T) {
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{
		{Name: "fine", Extent: 4096},
		{Name: "coarse", Extent: 512},
		{Name: "unstated", OmitExtent: true},
	}})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	for name, want := range map[string]uint32{"fine": 4096, "coarse": 512, "unstated": mvt.DefaultExtent} {
		l, ok := tile.Layer(name)
		if !ok {
			t.Fatalf("no layer %q", name)
		}
		if l.Extent != want {
			t.Errorf("layer %q has extent %d, want %d", name, l.Extent, want)
		}
	}
}

// TestTile_LayerReportsAbsenceRatherThanFailing. Most layers are missing from
// most tiles -- there is no water inland and no building on a trail -- and
// that is the correct picture of the place, not a fault. See
// docs/architecture.md, trap T9.
func TestTile_LayerReportsAbsenceRatherThanFailing(t *testing.T) {
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{Name: "roads"}}})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := tile.Layer("roads"); !ok {
		t.Error("the layer that is there was not found")
	}
	l, ok := tile.Layer("water")
	if ok {
		t.Errorf("found a layer %q that is not in the tile", l.Name)
	}
	if len(l.Features) != 0 {
		t.Error("the absent layer came back with features")
	}
}

// TestDecode_SkipsFieldsItDoesNotKnow. A producer may add fields, and a tile
// carrying one must still decode: refusing it would make this decoder break on
// the next schema revision for data it can read perfectly well.
func TestDecode_SkipsFieldsItDoesNotKnow(t *testing.T) {
	unknown := func(field, wire int, payload []byte) []byte {
		b := binary.AppendUvarint(nil, uint64(field)<<3|uint64(wire))
		switch wire {
		case 0:
			return binary.AppendUvarint(b, 12345)
		case 1:
			return binary.LittleEndian.AppendUint64(b, 7)
		case 2:
			b = binary.AppendUvarint(b, uint64(len(payload)))
			return append(b, payload...)
		case 5:
			return binary.LittleEndian.AppendUint32(b, 7)
		}
		return b
	}
	var featureExtra, layerExtra, tileExtra []byte
	for _, wire := range []int{0, 1, 2, 5} {
		featureExtra = append(featureExtra, unknown(90, wire, []byte("ignored"))...)
		layerExtra = append(layerExtra, unknown(91, wire, []byte("ignored"))...)
		tileExtra = append(tileExtra, unknown(92, wire, []byte("ignored"))...)
	}

	data := buildTile(t, osmbasetest.TileSpec{
		Extra: tileExtra,
		Layers: []osmbasetest.LayerSpec{{
			Name:  "roads",
			Extra: layerExtra,
			Features: []osmbasetest.FeatureSpec{{
				Type:     mvt.GeomPoint,
				Geometry: mvt.Geometry{Points: []mvt.Point{{7, 9}}},
				Extra:    featureExtra,
			}},
		}},
	})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(tile.Layers) != 1 || len(tile.Layers[0].Features) != 1 {
		t.Fatalf("decoded %d layers, want one layer of one feature", len(tile.Layers))
	}
	pts := tile.Layers[0].Features[0].Geometry.Points
	if len(pts) != 1 || pts[0] != (mvt.Point{X: 7, Y: 9}) {
		t.Errorf("the known fields decoded to %+v, want one point at (7, 9)", pts)
	}
}

// TestDecode_AcceptsRepeatedFieldsThatAreNotPacked. Packed and repeated are
// two valid protobuf encodings of one field, and a producer may use either.
// Here the geometry is written one varint at a time.
func TestDecode_AcceptsRepeatedFieldsThatAreNotPacked(t *testing.T) {
	var unpacked []byte
	for _, g := range []uint32{9, 4, 6} { // MoveTo once, +2, +3
		unpacked = binary.AppendUvarint(unpacked, uint64(4)<<3|0)
		unpacked = binary.AppendUvarint(unpacked, uint64(g))
	}
	data := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name:     "l",
		Features: []osmbasetest.FeatureSpec{{Type: mvt.GeomPoint, Extra: unpacked}},
	}}})
	tile, err := mvt.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	pts := tile.Layers[0].Features[0].Geometry.Points
	if len(pts) != 1 || pts[0] != (mvt.Point{X: 2, Y: 3}) {
		t.Errorf("unpacked geometry decoded to %+v, want one point at (2, 3)", pts)
	}
}

// TestDecode_RejectsTilesItCannotTrust. Every case is one where carrying on
// produces features rather than a failure, and features that came out of
// misread bytes are drawn with nothing to show they are wrong.
func TestDecode_RejectsTilesItCannotTrust(t *testing.T) {
	point := osmbasetest.FeatureSpec{
		Type:     mvt.GeomPoint,
		Geometry: mvt.Geometry{Points: []mvt.Point{{1, 1}}},
	}

	cases := []struct {
		name    string
		build   func(t *testing.T) []byte
		wantMsg string
	}{
		{
			name: "a layer with no name",
			build: func(t *testing.T) []byte {
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{Name: ""}}})
			},
			wantMsg: "no name",
		},
		{
			name: "two layers with the same name",
			build: func(t *testing.T) []byte {
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{
					{Name: "roads"}, {Name: "roads"},
				}})
			},
			wantMsg: "two layers named",
		},
		{
			name: "a layer declaring a version this decoder does not implement",
			build: func(t *testing.T) []byte {
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{Name: "l", Version: 3}}})
			},
			wantMsg: "version 3",
		},
		{
			name: "a layer with an extent of zero",
			build: func(t *testing.T) []byte {
				// Extent is written explicitly, so this needs the raw path: a
				// varint 0 in field 5 on a layer that omits its own.
				extra := binary.AppendUvarint(nil, uint64(5)<<3|0)
				extra = binary.AppendUvarint(extra, 0)
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
					Name: "l", OmitExtent: true, Extra: extra,
				}}})
			},
			wantMsg: "extent of 0",
		},
		{
			name: "a tag index naming a key the layer does not have",
			build: func(t *testing.T) []byte {
				f := point
				// Field 2, packed, one pair: key 5, value 0. The layer has one
				// key, so index 5 names nothing.
				f.Extra = append(binary.AppendUvarint(nil, uint64(2)<<3|2), 2, 5, 0)
				f.Tags = []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("x")}}
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
					Name: "l", Features: []osmbasetest.FeatureSpec{f},
				}}})
			},
			wantMsg: "refers to key 5",
		},
		{
			name: "a tag index naming a value the layer does not have",
			build: func(t *testing.T) []byte {
				f := point
				f.Extra = append(binary.AppendUvarint(nil, uint64(2)<<3|2), 2, 0, 9)
				f.Tags = []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("x")}}
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
					Name: "l", Features: []osmbasetest.FeatureSpec{f},
				}}})
			},
			wantMsg: "refers to value 9",
		},
		{
			name: "an odd number of tag indices",
			build: func(t *testing.T) []byte {
				f := point
				f.Extra = append(binary.AppendUvarint(nil, uint64(2)<<3|2), 1, 0)
				f.Tags = []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("x")}}
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
					Name: "l", Features: []osmbasetest.FeatureSpec{f},
				}}})
			},
			wantMsg: "key/value pairs",
		},
		{
			name: "a geometry type the schema does not define",
			build: func(t *testing.T) []byte {
				f := point
				f.Type = mvt.GeomType(7)
				f.RawGeometry = []uint32{9, 2, 2}
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
					Name: "l", Features: []osmbasetest.FeatureSpec{f},
				}}})
			},
			wantMsg: "geometry type 7",
		},
		{
			name: "a protobuf group, which the schema never uses",
			build: func(t *testing.T) []byte {
				return append(buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{Name: "l"}}}),
					binary.AppendUvarint(nil, uint64(40)<<3|3)...)
			},
			wantMsg: "group",
		},
		{
			name: "a field number of zero",
			build: func(t *testing.T) []byte {
				return []byte{0x00, 0x01}
			},
			wantMsg: "field number 0",
		},
		{
			name: "a layer length running past the end of the tile",
			build: func(t *testing.T) []byte {
				return []byte{0x1a, 0x40, 0x01, 0x02}
			},
			wantMsg: "only 2 remain",
		},
		{
			name: "a value carrying none of the seven types",
			build: func(t *testing.T) []byte {
				// An empty value message in field 4 of a layer.
				extra := append(binary.AppendUvarint(nil, uint64(4)<<3|2), 0)
				return buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
					Name: "l", Extra: extra,
				}}})
			},
			wantMsg: "none of the seven types",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tile, err := mvt.Decode(c.build(t))
			if err == nil {
				t.Fatalf("Decode accepted the tile and returned %+v", tile)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should say %q", err, c.wantMsg)
			}
			if !strings.HasPrefix(err.Error(), "mvt: ") {
				t.Errorf("error was %q, and every error from this package names it", err)
			}
		})
	}
}

// TestDecode_RejectsALayerWithNoVersion. The version field is required, and a
// tile that omits it is not one this decoder can reason about -- version 1 and
// version 2 differ in what a producer is allowed to emit.
func TestDecode_RejectsALayerWithNoVersion(t *testing.T) {
	// Build a layer by hand with only a name: field 1, "l".
	layer := []byte{0x0a, 0x01, 'l'}
	tile := append([]byte{0x1a, byte(len(layer))}, layer...)
	if _, err := mvt.Decode(tile); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("Decode gave %v, want an error about the missing version", err)
	}
}

// TestDecode_SurvivesEveryTruncation feeds every prefix of a working tile back
// in. A short read is the ordinary failure for bytes that came off a range
// request or a half-written cache file.
//
// A prefix that ends exactly on a layer boundary is a complete, shorter tile
// and decodes without error, because protobuf has no end marker and cannot
// tell a truncated stream from a smaller one. So the assertion is not that
// every prefix fails: it is that a prefix which decodes yields layers
// IDENTICAL to the ones the whole tile yields. Anything else would mean the
// decoder invented content out of a partial message.
func TestDecode_SurvivesEveryTruncation(t *testing.T) {
	full := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{
		{
			Name: "roads",
			Features: []osmbasetest.FeatureSpec{{
				ID:       7,
				HasID:    true,
				Type:     mvt.GeomLineString,
				Tags:     []osmbasetest.Tag{{Key: "kind", Value: mvt.StringValue("path")}},
				Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{0, 0}, {100, 100}, {200, 50}}}},
			}},
		},
		{
			Name: "water",
			Features: []osmbasetest.FeatureSpec{{
				Type: mvt.GeomPolygon,
				Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
					Exterior: mvt.Ring{{0, 0}, {50, 0}, {50, 50}, {0, 50}},
					Holes:    []mvt.Ring{{{10, 10}, {10, 20}, {20, 20}, {20, 10}}},
				}}},
			}},
		},
	}})
	whole, err := mvt.Decode(full)
	if err != nil {
		t.Fatalf("the undamaged tile did not decode: %v", err)
	}
	survived := 0
	for n := 1; n < len(full); n++ {
		got, err := mvt.Decode(full[:n])
		if err != nil {
			continue
		}
		survived++
		if len(got.Layers) > len(whole.Layers) {
			t.Fatalf("a tile truncated to %d bytes decoded to %d layers, more than the whole tile's %d", n, len(got.Layers), len(whole.Layers))
		}
		for i, l := range got.Layers {
			if !reflect.DeepEqual(l, whole.Layers[i]) {
				t.Fatalf("a tile truncated to %d bytes decoded layer %d as\n%+v\nand the whole tile decodes it as\n%+v", n, i, l, whole.Layers[i])
			}
		}
	}
	// The fixture has two layers, so exactly one proper prefix -- the one
	// ending at the layer boundary -- is expected to decode. If none did, the
	// loop above asserted nothing.
	if survived != 1 {
		t.Errorf("%d truncations decoded cleanly, want exactly the one at the layer boundary", survived)
	}
	// Zero bytes is an empty tile rather than a damaged one: a protobuf
	// message with no fields is legal, and an archive can hold one.
	empty, err := mvt.Decode(nil)
	if err != nil {
		t.Errorf("an empty tile failed to decode: %v", err)
	}
	if len(empty.Layers) != 0 {
		t.Errorf("an empty tile decoded to %d layers", len(empty.Layers))
	}
}

// TestDecode_CorruptedBytesFailCleanly flips bytes through a working tile.
// What it looks for is a crash or an unnamed error, which is all a decoder can
// promise: protobuf has no checksum, so a flipped byte can perfectly well be
// another valid tile.
func TestDecode_CorruptedBytesFailCleanly(t *testing.T) {
	full := buildTile(t, osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "roads",
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPolygon,
			Tags:     []osmbasetest.Tag{{Key: "kind", Value: mvt.DoubleValue(math.Pi)}},
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{Exterior: mvt.Ring{{0, 0}, {9, 0}, {9, 9}}}}},
		}},
	}}})
	for i := range full {
		for _, flip := range []byte{0xff, 0x01, 0x80} {
			b := append([]byte(nil), full...)
			b[i] ^= flip
			_, err := mvt.Decode(b)
			if err != nil && !strings.HasPrefix(err.Error(), "mvt: ") {
				t.Fatalf("byte %d flipped with %#x gave an unnamed error: %v", i, flip, err)
			}
		}
	}
}
