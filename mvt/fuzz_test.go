package mvt_test

import (
	"testing"
	"time"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// FuzzDecode is checked in for the same reason as the ones in the pmtiles
// package: a tile is bytes somebody else produced, and running a fuzzer once
// says something about an afternoon while a target in the tree keeps saying it.
//
// It asserts the decoder's invariants rather than only the absence of a crash,
// and two of them are worth more than the rest.
//
// Ring orientation, because that is what the whole normalisation step exists
// for: the rasterizer sums winding and takes the absolute value, so a hole
// wound like its exterior fills solid and the result is a believable map with
// an island missing. No amount of valid-looking output proves that did not
// happen; the winding does. See docs/architecture.md, trap T13.
//
// The coordinate bound, because the cursor is an int32 accumulating unbounded
// deltas, and a wrapped vertex is indistinguishable from a real one -- and
// because Ring.Winding's integer arithmetic is exact only within that bound.
//
// The time ceiling is deliberately loose, for a fuzz worker sharing a machine.
// Its slope is two microseconds per byte, several hundred times what a linear
// decode of real data costs, and it is here because the defect review found in
// this package was a quadratic scan over layer names: 320 KB took 1.19 s
// against a ceiling of 0.89 s here, so this would have caught it.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add(seedTile(f))
	f.Add(seedPolygonTile(f))
	f.Add([]byte{0x1a, 0x03, 0x0a, 0x01, 'l'})
	f.Add([]byte{0x00, 0x01})

	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		tile, err := mvt.Decode(data)
		if elapsed := time.Since(start); elapsed > ceiling(len(data)) {
			t.Fatalf("decoding %d bytes took %v, past the ceiling of %v", len(data), elapsed, ceiling(len(data)))
		}
		if err != nil {
			return
		}

		// Structural amplification bounds. Every layer, feature and vertex
		// costs at least a byte on the wire, so a decode producing more of any
		// of them than the input has bytes has invented them.
		if len(tile.Layers) > len(data) {
			t.Fatalf("%d bytes decoded to %d layers", len(data), len(tile.Layers))
		}
		var features, vertices int
		seen := map[string]bool{}
		for _, l := range tile.Layers {
			if l.Name == "" {
				t.Fatal("a layer came back with no name")
			}
			if seen[l.Name] {
				t.Fatalf("two layers named %q", l.Name)
			}
			seen[l.Name] = true
			if l.Extent == 0 {
				t.Fatalf("layer %q has an extent of 0", l.Name)
			}
			if l.Version != 1 && l.Version != 2 {
				t.Fatalf("layer %q has version %d", l.Name, l.Version)
			}
			features += len(l.Features)

			bound := int64(l.Extent) * 256
			if bound > 1<<20 {
				bound = 1 << 20
			}
			for _, ft := range l.Features {
				vertices += checkFeature(t, l, ft, bound)
			}
		}
		if features > len(data) {
			t.Fatalf("%d bytes decoded to %d features", len(data), features)
		}
		if vertices > len(data) {
			t.Fatalf("%d bytes decoded to %d vertices", len(data), vertices)
		}
	})
}

// checkFeature asserts one feature's invariants and returns its vertex count.
func checkFeature(t *testing.T, l mvt.Layer, f mvt.Feature, bound int64) int {
	t.Helper()

	// Exactly one kind of geometry, chosen by the type. A feature carrying two
	// would be one whose type and content disagree.
	kinds := 0
	for _, populated := range []bool{len(f.Geometry.Points) > 0, len(f.Geometry.Lines) > 0, len(f.Geometry.Polygons) > 0} {
		if populated {
			kinds++
		}
	}
	if kinds > 1 {
		t.Fatalf("layer %q: a %s feature has more than one kind of geometry", l.Name, f.Type)
	}

	n := 0
	check := func(p mvt.Point) {
		if int64(p.X) < -bound || int64(p.X) > bound || int64(p.Y) < -bound || int64(p.Y) > bound {
			t.Fatalf("layer %q: a vertex at (%d, %d) is outside the bound of %d", l.Name, p.X, p.Y, bound)
		}
		n++
	}
	for _, p := range f.Geometry.Points {
		check(p)
	}
	for _, line := range f.Geometry.Lines {
		if len(line) < 2 {
			t.Fatalf("layer %q: a linestring part has %d points", l.Name, len(line))
		}
		for _, p := range line {
			check(p)
		}
	}
	for _, poly := range f.Geometry.Polygons {
		if w := poly.Exterior.Winding(); w != mvt.ExteriorWinding && w != 0 {
			t.Fatalf("layer %q: an exterior ring came back wound %d, want %d", l.Name, w, mvt.ExteriorWinding)
		}
		for _, p := range poly.Exterior {
			check(p)
		}
		for _, hole := range poly.Holes {
			if w := hole.Winding(); w != mvt.HoleWinding && w != 0 {
				t.Fatalf("layer %q: a hole came back wound %d, want %d", l.Name, w, mvt.HoleWinding)
			}
			for _, p := range hole {
				check(p)
			}
		}
	}
	return n
}

func ceiling(n int) time.Duration {
	return 250*time.Millisecond + time.Duration(n)*2*time.Microsecond
}

func seedTile(f *testing.F) []byte {
	f.Helper()
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{
		{
			Name: "roads",
			Features: []osmbasetest.FeatureSpec{{
				ID:    7,
				HasID: true,
				Type:  mvt.GeomLineString,
				Tags: []osmbasetest.Tag{
					{Key: "kind", Value: mvt.StringValue("path")},
					{Key: "min_zoom", Value: mvt.SintValue(-3)},
					{Key: "bridge", Value: mvt.BoolValue(true)},
				},
				Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{X: 0, Y: 0}, {X: 100, Y: 100}}}},
			}},
		},
		{
			Name:     "places",
			Extent:   512,
			Features: []osmbasetest.FeatureSpec{{Type: mvt.GeomPoint, Geometry: mvt.Geometry{Points: []mvt.Point{{X: 5, Y: 9}}}}},
		},
	}})
	if err != nil {
		f.Fatalf("building the seed tile: %v", err)
	}
	return data
}

func seedPolygonTile(f *testing.F) []byte {
	f.Helper()
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "water",
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{
				{
					Exterior: mvt.Ring{{X: 0, Y: 0}, {X: 100, Y: 0}, {X: 100, Y: 100}, {X: 0, Y: 100}},
					Holes:    []mvt.Ring{{{X: 20, Y: 20}, {X: 20, Y: 40}, {X: 40, Y: 40}, {X: 40, Y: 20}}},
				},
				{Exterior: mvt.Ring{{X: 200, Y: 200}, {X: 220, Y: 200}, {X: 220, Y: 220}}},
			}},
		}},
	}}})
	if err != nil {
		f.Fatalf("building the seed tile: %v", err)
	}
	return data
}
