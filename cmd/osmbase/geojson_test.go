package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// TestWriteFeatureCollection_ExactBytesOfASmallTile pins the whole output of a
// one-feature tile.
//
// The coordinates are worked out from the definition rather than from this
// program: the tile is the whole world at zoom 0 with an extent of 4096, so
// tile x 2048 is half way across, which is longitude (0.5*360)-180 = 0, and
// tile y 2048 is half way down, which is the equator.
//
// The bytes are compared whole because everything about this output is a
// promise to a parser: the member names, the feature per line, the trailing
// newline, the identifier appearing both as GeoJSON's own "id" and as a
// property a viewer will show.
func TestWriteFeatureCollection_ExactBytesOfASmallTile(t *testing.T) {
	layer := mvt.Layer{
		Name:   "places",
		Extent: 4096,
		Features: []mvt.Feature{{
			ID:       7,
			HasID:    true,
			Type:     mvt.GeomPoint,
			Tags:     map[string]mvt.Value{"name": mvt.StringValue("Null Island")},
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 2048, Y: 2048}}},
		}},
	}
	var buf bytes.Buffer
	written, skipped, err := writeFeatureCollection(&buf, []mvt.Layer{layer}, 0, 0, 0)
	if err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	if written != 1 || skipped != 0 {
		t.Errorf("wrote %d features and skipped %d, want 1 and 0", written, skipped)
	}
	want := `{"type":"FeatureCollection","features":[
{"type":"Feature","id":7,"properties":{"@layer":"places","@id":7,"name":"Null Island"},"geometry":{"type":"Point","coordinates":[0,0]}}
]}
`
	if got := buf.String(); got != want {
		t.Errorf("output was:\n%s\nwant:\n%s", got, want)
	}
}

// TestWriteFeatureCollection_EmptyIsStillAValidCollection checks the shape of
// nothing.
//
// A tile with no features is an ordinary answer -- there is no water inland
// and no building on a trail -- so the output has to be a document a parser
// accepts rather than an empty file or a broken array.
func TestWriteFeatureCollection_EmptyIsStillAValidCollection(t *testing.T) {
	var buf bytes.Buffer
	written, _, err := writeFeatureCollection(&buf, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	if written != 0 {
		t.Errorf("wrote %d features from no layers", written)
	}
	if got, want := buf.String(), "{\"type\":\"FeatureCollection\",\"features\":[]}\n"; got != want {
		t.Errorf("output was %q, want %q", got, want)
	}
	var doc any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Errorf("the empty collection does not parse: %v", err)
	}
}

// TestWriteFeatureCollection_GeometryTypesAndTheirMultiForms checks that one
// part becomes a singular type and several become a Multi one, which is the
// distinction the vector tile format does not make and GeoJSON does.
func TestWriteFeatureCollection_GeometryTypesAndTheirMultiForms(t *testing.T) {
	cases := []struct {
		name     string
		feature  mvt.Feature
		wantType string
	}{
		{"one point", mvt.Feature{Type: mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1, Y: 1}}}}, "Point"},
		{"several points", mvt.Feature{Type: mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1, Y: 1}, {X: 2, Y: 2}}}}, "MultiPoint"},
		{"one line", mvt.Feature{Type: mvt.GeomLineString,
			Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{X: 0, Y: 0}, {X: 9, Y: 9}}}}}, "LineString"},
		{"two lines", mvt.Feature{Type: mvt.GeomLineString,
			Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{X: 0, Y: 0}, {X: 9, Y: 9}}, {{X: 1, Y: 0}, {X: 8, Y: 9}}}}}, "MultiLineString"},
		{"one polygon", mvt.Feature{Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{Exterior: square(0, 0, 100)}}}}, "Polygon"},
		{"two polygons", mvt.Feature{Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{Exterior: square(0, 0, 100)}, {Exterior: square(200, 200, 100)}}}}, "MultiPolygon"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, _, err := writeFeatureCollection(&buf, []mvt.Layer{{Name: "l", Extent: 4096, Features: []mvt.Feature{c.feature}}}, 0, 0, 0); err != nil {
				t.Fatalf("writeFeatureCollection: %v", err)
			}
			collection := parseCollection(t, buf.Bytes())
			if len(collection.Features) != 1 {
				t.Fatalf("wrote %d features, want 1", len(collection.Features))
			}
			if got := collection.Features[0].Geometry.Type; got != c.wantType {
				t.Errorf("geometry type is %q, want %q", got, c.wantType)
			}
		})
	}
}

// TestWriteFeatureCollection_RingsAreClosedAndWoundTheWayTheRFCWants is the
// test the winding comment in writeFeatureCollection exists for.
//
// MVT and GeoJSON want OPPOSITE windings, and the reason it is easy to get
// wrong is that converting to degrees negates a ring's signed area without
// changing the picture at all -- latitude runs opposite to tile y. So a
// reasonable-looking argument concludes that the two already agree. They do
// not, and the check below is made in the output's own coordinates: the
// shoelace area of the exterior ring, in longitude and latitude, must be
// POSITIVE (counterclockwise, right-hand rule) and the hole's must be
// negative.
//
// The closure check is separate and just as load bearing: a vector tile ring
// leaves its closing point implicit and RFC 7946 requires four positions with
// the first repeated as the last.
func TestWriteFeatureCollection_RingsAreClosedAndWoundTheWayTheRFCWants(t *testing.T) {
	// An MVT exterior: positive area with y running down, which is clockwise
	// drawn on a map. A hole is the other way round.
	exterior := mvt.Ring{{X: 1024, Y: 1024}, {X: 3072, Y: 1024}, {X: 3072, Y: 3072}, {X: 1024, Y: 3072}}
	hole := mvt.Ring{{X: 1536, Y: 1536}, {X: 1536, Y: 2560}, {X: 2560, Y: 2560}, {X: 2560, Y: 1536}}
	if got := exterior.Winding(); got != mvt.ExteriorWinding {
		t.Fatalf("the fixture's exterior is wound %d, want %d; the fixture is wrong, not the code", got, mvt.ExteriorWinding)
	}
	if got := hole.Winding(); got != mvt.HoleWinding {
		t.Fatalf("the fixture's hole is wound %d, want %d; the fixture is wrong, not the code", got, mvt.HoleWinding)
	}

	layer := mvt.Layer{Name: "water", Extent: 4096, Features: []mvt.Feature{{
		Type:     mvt.GeomPolygon,
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{Exterior: exterior, Holes: []mvt.Ring{hole}}}},
	}}}
	var buf bytes.Buffer
	if _, _, err := writeFeatureCollection(&buf, []mvt.Layer{layer}, 0, 0, 0); err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	rings := parseCollection(t, buf.Bytes()).Features[0].Geometry.polygon(t)
	if len(rings) != 2 {
		t.Fatalf("the polygon has %d rings, want an exterior and a hole", len(rings))
	}
	for i, ring := range rings {
		if len(ring) != 5 {
			t.Errorf("ring %d has %d positions, want 5: four corners and the first repeated", i, len(ring))
		}
		if ring[0] != ring[len(ring)-1] {
			t.Errorf("ring %d is not closed: it starts at %v and ends at %v", i, ring[0], ring[len(ring)-1])
		}
	}
	if a := shoelace(rings[0]); a <= 0 {
		t.Errorf("the exterior ring's signed area in degrees is %v; RFC 7946 wants it counterclockwise, which is positive", a)
	}
	if a := shoelace(rings[1]); a >= 0 {
		t.Errorf("the hole's signed area in degrees is %v; RFC 7946 wants it clockwise, which is negative", a)
	}
}

// TestWriteFeatureCollection_CoordinatesAreLongitudeThenLatitude checks the
// member order GeoJSON defines and most people say the other way round.
//
// The fixture point is in the north-east of the world square, so the two
// numbers are far apart and of different signs; swapping them would put it in
// the southern hemisphere off the coast of Africa rather than producing
// something subtly off.
func TestWriteFeatureCollection_CoordinatesAreLongitudeThenLatitude(t *testing.T) {
	// x 3072 of 4096 is three quarters across the world: longitude +90.
	// y 1024 of 4096 is a quarter of the way down the projection, which is
	// latitude atan(sinh(pi/2)) = 66.5132604 degrees north.
	layer := mvt.Layer{Name: "places", Extent: 4096, Features: []mvt.Feature{{
		Type:     mvt.GeomPoint,
		Geometry: mvt.Geometry{Points: []mvt.Point{{X: 3072, Y: 1024}}},
	}}}
	var buf bytes.Buffer
	if _, _, err := writeFeatureCollection(&buf, []mvt.Layer{layer}, 0, 0, 0); err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	pos := parseCollection(t, buf.Bytes()).Features[0].Geometry.point(t)
	wantLat := math.Atan(math.Sinh(math.Pi/2)) * 180 / math.Pi
	if math.Abs(pos[0]-90) > 1e-6 {
		t.Errorf("the first number is %v; it must be the longitude, 90", pos[0])
	}
	if math.Abs(pos[1]-wantLat) > 1e-6 {
		t.Errorf("the second number is %v; it must be the latitude, %v", pos[1], wantLat)
	}
}

// TestWriteFeatureCollection_SkipsWhatGeoJSONCannotExpressAndCountsIt covers
// the features that do not reach the output.
//
// Each of them is counted rather than dropped quietly, because a feature
// missing from the file is a difference between the archive and what the user
// is looking at, and a silent one is discovered as a missing building.
func TestWriteFeatureCollection_SkipsWhatGeoJSONCannotExpressAndCountsIt(t *testing.T) {
	layer := mvt.Layer{Name: "odd", Extent: 4096, Features: []mvt.Feature{
		{Type: mvt.GeomUnknown},
		{Type: mvt.GeomPoint},
		{Type: mvt.GeomPolygon, Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
			// Two points enclose no area, so there is no ring to write. The
			// vector tile format allows this; RFC 7946 needs four positions.
			Exterior: mvt.Ring{{X: 10, Y: 10}, {X: 20, Y: 20}},
		}}}},
		{Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1, Y: 1}}}},
	}}
	var buf bytes.Buffer
	written, skipped, err := writeFeatureCollection(&buf, []mvt.Layer{layer}, 0, 0, 0)
	if err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	if written != 1 || skipped != 3 {
		t.Errorf("wrote %d and skipped %d, want 1 written and 3 skipped", written, skipped)
	}
	if n := len(parseCollection(t, buf.Bytes()).Features); n != 1 {
		t.Errorf("the document holds %d features, want 1", n)
	}
}

// TestWriteFeatureCollection_UsesEachLayersOwnExtent is the per-layer
// transform.
//
// The extent is a property of the LAYER, and a tile whose landcover is at 512
// and whose buildings are at 4096 is legal and happens. Using one layer's
// extent for another places its geometry at a fraction of its size in the
// right tile, which looks like a level of detail rather than a bug -- so the
// two features below sit at the same place on the ground and must come out
// with the same coordinates.
func TestWriteFeatureCollection_UsesEachLayersOwnExtent(t *testing.T) {
	coarse := mvt.Layer{Name: "coarse", Extent: 512, Features: []mvt.Feature{{
		Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{X: 384, Y: 128}}},
	}}}
	fine := mvt.Layer{Name: "fine", Extent: 4096, Features: []mvt.Feature{{
		Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{X: 3072, Y: 1024}}},
	}}}
	var buf bytes.Buffer
	if _, _, err := writeFeatureCollection(&buf, []mvt.Layer{coarse, fine}, 0, 0, 0); err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	features := parseCollection(t, buf.Bytes()).Features
	if len(features) != 2 {
		t.Fatalf("wrote %d features, want 2", len(features))
	}
	a, b := features[0].Geometry.point(t), features[1].Geometry.point(t)
	if a != b {
		t.Errorf("the same place is %v at extent 512 and %v at extent 4096", a, b)
	}
}

// TestWriteFeatureCollection_LayerAndIdPropertiesDoNotOverwriteATag is why the
// synthetic properties are prefixed.
//
// A tile schema is free to have a tag called "layer" -- OSM does, for bridges
// and tunnels -- and a synthetic property that took its name would replace the
// feature's real value with the name of the layer, silently.
func TestWriteFeatureCollection_LayerAndIdPropertiesDoNotOverwriteATag(t *testing.T) {
	layer := mvt.Layer{Name: "roads", Extent: 4096, Features: []mvt.Feature{{
		ID:    3,
		HasID: true,
		Type:  mvt.GeomPoint,
		Tags: map[string]mvt.Value{
			"layer": mvt.IntValue(-1),
			"id":    mvt.StringValue("w12345"),
		},
		Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1, Y: 1}}},
	}}}
	var buf bytes.Buffer
	if _, _, err := writeFeatureCollection(&buf, []mvt.Layer{layer}, 0, 0, 0); err != nil {
		t.Fatalf("writeFeatureCollection: %v", err)
	}
	props := parseCollection(t, buf.Bytes()).Features[0].Properties
	want := map[string]any{
		"@layer": "roads",
		"@id":    float64(3),
		"layer":  float64(-1),
		"id":     "w12345",
	}
	for k, v := range want {
		if got, ok := props[k]; !ok || got != v {
			t.Errorf("property %q is %v (present %v), want %v", k, got, ok, v)
		}
	}
}

// TestValueJSON_EveryKindOfAttribute checks the seven value kinds the vector
// tile format has, which JSON has three of.
//
// The uint64 case is the one that would survive a lazy implementation and be
// wrong: folding every number through a float64 loses integers above 2^53, and
// the loss is silent and small enough to look like a rounding.
func TestValueJSON_EveryKindOfAttribute(t *testing.T) {
	cases := []struct {
		name string
		in   mvt.Value
		want string
	}{
		{"string", mvt.StringValue("George Street"), `"George Street"`},
		{"string needing escapes", mvt.StringValue("a \"quoted\" name\n"), `"a \"quoted\" name\n"`},
		{"bool", mvt.BoolValue(true), "true"},
		{"int", mvt.IntValue(-7), "-7"},
		{"sint", mvt.SintValue(-7), "-7"},
		{"uint beyond float precision", mvt.UintValue(9007199254740993), "9007199254740993"},
		{"double", mvt.DoubleValue(1.5), "1.5"},
		{"float", mvt.FloatValue(2.25), "2.25"},
		{"not a number", mvt.DoubleValue(math.NaN()), `"NaN"`},
		{"infinity", mvt.DoubleValue(math.Inf(1)), `"+Inf"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := valueJSON(c.in); got != c.want {
				t.Errorf("valueJSON(%v) = %s, want %s", c.in, got, c.want)
			}
		})
	}
}

// TestRun_GeojsonOfAFixtureTileParsesAndHoldsEveryLayer is the end-to-end
// path: an archive on disk, through the reader and the decoder, out as a
// document a parser accepts.
func TestRun_GeojsonOfAFixtureTileParsesAndHoldsEveryLayer(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())
	r := runCLI(t, "geojson", path, "--lat", "0", "--lon", "0", "--zoom", "0")
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	collection := parseCollection(t, []byte(r.stdout))
	if len(collection.Features) != 3 {
		t.Fatalf("the document holds %d features, want the fixture's 3", len(collection.Features))
	}
	layers := map[string]bool{}
	for _, f := range collection.Features {
		layers[f.Properties["@layer"].(string)] = true
	}
	for _, want := range []string{"places", "roads", "water"} {
		if !layers[want] {
			t.Errorf("no feature came from layer %q", want)
		}
	}
	// The counts go to stderr so that stdout is nothing but the document.
	if !strings.Contains(r.stderr, "3 features written") {
		t.Errorf("stderr does not report what was written:\n%s", r.stderr)
	}
}

// TestRun_GeojsonLayerFilter covers --layer, and the error for a name the tile
// does not have.
//
// The error matters more than the filter: without it a typo produces a valid
// empty FeatureCollection, and an empty map is indistinguishable from an area
// with no data.
func TestRun_GeojsonLayerFilter(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())

	r := runCLI(t, "geojson", path, "--lat", "0", "--lon", "0", "--zoom", "0", "--layer", "roads")
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	collection := parseCollection(t, []byte(r.stdout))
	if len(collection.Features) != 1 {
		t.Fatalf("the document holds %d features, want the one road", len(collection.Features))
	}
	if got := collection.Features[0].Properties["@layer"]; got != "roads" {
		t.Errorf("the feature came from layer %v, want roads", got)
	}

	r = runCLI(t, "geojson", path, "--lat", "0", "--lon", "0", "--zoom", "0", "--layer", "rodes")
	if r.code != 2 {
		t.Errorf("exit code %d for a layer that does not exist, want 2", r.code)
	}
	for _, want := range []string{`"rodes"`, "places", "roads", "water"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the error should name the typo and list the real layers; %q is missing:\n%s", want, r.stderr)
		}
	}
	if r.stdout != "" {
		t.Errorf("a failed run wrote to stdout:\n%s", r.stdout)
	}
}

// TestRun_GeojsonIsDeterministic runs the same command twice over the same
// archive and compares the bytes.
//
// Tags live in a map, and Go randomises map iteration order deliberately. A
// document whose property order moved between runs could not be diffed, could
// not be committed, and would make any future golden test flap.
func TestRun_GeojsonIsDeterministic(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())
	first := runCLI(t, "geojson", path, "--lat", "0", "--lon", "0", "--zoom", "0")
	for i := 0; i < 5; i++ {
		again := runCLI(t, "geojson", path, "--lat", "0", "--lon", "0", "--zoom", "0")
		if again.stdout != first.stdout {
			t.Fatalf("run %d differs from the first:\n%s\n%s", i+2, first.stdout, again.stdout)
		}
	}
}

// TestRun_GeojsonAgreesWithTheCoordinateAsked closes the loop the whole
// command rests on: a feature written for a coordinate must come back to the
// tile that coordinate is in.
//
// The fixture tile is at zoom 2 in the south-east of the world and holds a
// point at the centre of the tile, so the coordinate that comes out must be
// inside the tile's own bounds. An origin off by one tile passes every other
// test in this file.
func TestRun_GeojsonAgreesWithTheCoordinateAsked(t *testing.T) {
	const z = 2
	// The tile holding the Sydney Opera House at zoom 2.
	x, y, err := mercator.TileAt(z, 151.2153, -33.8568)
	if err != nil {
		t.Fatalf("TileAt: %v", err)
	}
	spec := osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name:   "places",
		Extent: 4096,
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 2048, Y: 2048}}},
		}},
	}}}
	path := fixtureArchive(t, z, x, y, spec)

	r := runCLI(t, "geojson", path, "--lat", "-33.8568", "--lon", "151.2153", "--zoom", "2")
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	pos := parseCollection(t, []byte(r.stdout)).Features[0].Geometry.point(t)
	west, south, east, north, err := mercator.TileBounds(z, x, y)
	if err != nil {
		t.Fatalf("TileBounds: %v", err)
	}
	if pos[0] < west || pos[0] > east || pos[1] < south || pos[1] > north {
		t.Errorf("the point came out at %v, outside tile %d/%d/%d, which is west %v south %v east %v north %v",
			pos, z, x, y, west, south, east, north)
	}
}

// square returns an MVT exterior ring: clockwise on a map, which is positive
// area with y running down.
func square(x, y, size int32) mvt.Ring {
	return mvt.Ring{{X: x, Y: y}, {X: x + size, Y: y}, {X: x + size, Y: y + size}, {X: x, Y: y + size}}
}

// shoelace returns twice the signed area of a closed ring of [lon, lat]
// positions. Positive is counterclockwise by the right-hand rule, which is
// what RFC 7946 wants of an exterior ring.
func shoelace(ring [][2]float64) float64 {
	var sum float64
	for i := 0; i < len(ring)-1; i++ {
		sum += ring[i][0]*ring[i+1][1] - ring[i+1][0]*ring[i][1]
	}
	return sum
}

// The types below parse the output as a GeoJSON consumer would, rather than as
// the writer that produced it: the point of the check is that somebody else's
// parser can read it.
type collection struct {
	Type     string        `json:"type"`
	Features []jsonFeature `json:"features"`
}

type jsonFeature struct {
	Type       string         `json:"type"`
	ID         *uint64        `json:"id"`
	Properties map[string]any `json:"properties"`
	Geometry   jsonGeometry   `json:"geometry"`
}

type jsonGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func (g jsonGeometry) point(t *testing.T) [2]float64 {
	t.Helper()
	var pos [2]float64
	if err := json.Unmarshal(g.Coordinates, &pos); err != nil {
		t.Fatalf("reading a Point's coordinates: %v", err)
	}
	return pos
}

func (g jsonGeometry) polygon(t *testing.T) [][][2]float64 {
	t.Helper()
	var rings [][][2]float64
	if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
		t.Fatalf("reading a Polygon's coordinates: %v", err)
	}
	return rings
}

func parseCollection(t *testing.T, b []byte) collection {
	t.Helper()
	var c collection
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("the output does not parse as JSON: %v\n%s", err, b)
	}
	if c.Type != "FeatureCollection" {
		t.Fatalf("the document is a %q, want a FeatureCollection", c.Type)
	}
	return c
}
