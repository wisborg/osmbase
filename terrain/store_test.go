package terrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

func TestRootIsBesideTheMapStore(t *testing.T) {
	if got := Root("/cache/osmbase/"); got != "/cache/osmbase-terrain" {
		t.Errorf("Root = %q", got)
	}
}

// writeCoverage replaces a layout's coverage with real vector tiles: every
// tile over the cell covered by glo30.
func writeCoverage(t *testing.T, dir string) {
	t.Helper()
	tile, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "coverage", Extent: 4096,
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPolygon,
			Tags: []osmbasetest.Tag{{Key: "source", Value: mvt.StringValue("glo30")}},
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
				Exterior: mvt.Ring{{X: 0, Y: 0}, {X: 4096, Y: 0}, {X: 4096, Y: 4096}, {X: 0, Y: 4096}},
			}}},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := slice.AncestorTiles(cell, 12, slice.ZoomRange{Min: 0, Max: 11})
	if err != nil {
		t.Fatal(err)
	}
	refs = append(refs, slice.TileRef{Z: 12, X: cell.X, Y: cell.Y})
	var at []osmbasetest.ArchiveTile
	for _, r := range refs {
		id, _ := pmtiles.ZxyToID(r.Z, r.X, r.Y)
		at = append(at, osmbasetest.ArchiveTile{ID: id, Data: tile})
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles: at, TileType: pmtiles.TileTypeMVT, TileCompression: pmtiles.CompressionGzip, MinZoom: 0, MaxZoom: 12,
		MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coverage.pmtiles"), built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The lifecycle a drawing program goes through: nothing there, measured as
// lacking from the disk alone; filled from the archives; measured again as
// lacking nothing; opened to draw from, with its credit for a view.
func TestMeasureFillOpenAndCredit(t *testing.T) {
	dir := layoutDir(t)
	writeCoverage(t, dir)
	os.WriteFile(filepath.Join(dir, SourcesName), []byte(`[{"source": "glo30", "producer": "DLR"}]`), 0o644)
	root := filepath.Join(t.TempDir(), "maps-terrain")
	b := cellBounds()

	if _, err := Open(root); !errors.Is(err, ErrNoTerrain) {
		t.Errorf("opening no store: %v, want ErrNoTerrain", err)
	}
	s, short := Measure(root, b, 15)
	if !short || !s.Empty || s.MapZoom() != 15 {
		t.Errorf("measuring no store: %+v, %v", s, short)
	}

	l, err := Locate(context.Background(), dir, func(string) { t.Error("a directory announced a request") })
	if err != nil {
		t.Fatal(err)
	}
	var reported *Plan
	res, err := Fill(context.Background(), l, root, b, s.MapZoom(), opener, func(p *Plan) { reported = p }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reported == nil || res.Written == 0 {
		t.Fatalf("Fill reported %v and wrote %d tiles", reported != nil, res.Written)
	}
	if s, short := Measure(root, b, 15); short {
		t.Errorf("after the fill, still lacking: %+v", s)
	}

	ts, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Heights() == nil || ts.Root() != root {
		t.Errorf("heights %v, root %q", ts.Heights(), ts.Root())
	}
	short2, full, err := ts.Credit(b, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(short2, "mapterhorn.com/attribution") || !strings.Contains(full, "Copernicus WorldDEM-30") {
		t.Errorf("credit %q, notice %q", short2, full)
	}

	// The same view's sources, owed once through Notice, are its notice --
	// however many views named them.
	ids, err := ts.SourcesIn(b, 15)
	if err != nil {
		t.Fatal(err)
	}
	if got := ts.ShortCredit(ids); got != short2 {
		t.Errorf("ShortCredit = %q, want Credit's %q", got, short2)
	}
	if got := ts.Notice(append(slices.Clone(ids), ids...)); got != full {
		t.Errorf("Notice of the view's sources twice = %q, want its own notice %q", got, full)
	}
}

// Filling a store that already holds the view fetches nothing and is not an
// error; the plan is still reported, so a program can say so.
func TestFillWithNothingToFetch(t *testing.T) {
	dir := layoutDir(t)
	l, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := Fill(context.Background(), l, root, cellBounds(), 15, opener, nil, nil); err != nil {
		t.Fatal(err)
	}
	var empty bool
	res, err := Fill(context.Background(), l, root, cellBounds(), 15, opener, func(p *Plan) { empty = p.Empty() }, func(acquire.Progress) {})
	if err != nil || !empty || res.Written != 0 {
		t.Errorf("second fill: empty %v, wrote %d, %v", empty, res.Written, err)
	}
}

// deepElsewhere adds to the layout in dir a region far from the cell whose
// data goes to zoom 17, and fills a cell of it into the store at root. The
// store's elevation then reaches 17, as it does after any fetch over metre
// LiDAR, so a view of the cell is measured as deep as it asks rather than
// capped at what the layout's one region offers.
func deepElsewhere(t *testing.T, dir, root string) {
	t.Helper()
	far := slice.TileRef{Z: 12, X: 10, Y: 10} // in region 6/0/0
	deep, err := slice.PyramidTiles(slice.Cell{X: far.X, Y: far.Y}, 12, slice.ZoomRange{Min: 13, Max: 13})
	if err != nil {
		t.Fatal(err)
	}
	writeArchive(t, filepath.Join(dir, "6-0-0.pmtiles"), pmtiles.TileTypeWebP, pmtiles.CompressionNone, slice.ZoomRange{Min: 13, Max: 17}, deep)
	l, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := tileBounds(far)
	dx, dy := (b.East-b.West)/10, (b.North-b.South)/10
	b = slice.Bounds{West: b.West + dx, East: b.East - dx, South: b.South + dy, North: b.North - dy}
	if _, err := Fill(context.Background(), l, root, b, 15, opener, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// Where the archives stop shallower than a map asks -- the region at zoom 14
// for a map at 16, or no region at all and the global archive at 12 -- one
// fill is enough: measured again, the area lacks nothing. Measured at the
// depth asked for instead, it was short however often it was filled, and
// every render of it offered the same download.
func TestMeasureAfterAFillShallowerThanAsked(t *testing.T) {
	for _, tc := range []struct {
		name    string
		region  bool
		mapZoom uint8
	}{
		{"the region stops at 14", true, 16},
		{"no region", false, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := layoutDir(t)
			if !tc.region {
				if err := os.Remove(filepath.Join(dir, "6-58-38.pmtiles")); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(t.TempDir(), "maps-terrain")
			deepElsewhere(t, dir, root)
			l, err := ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			b := cellBounds()
			if _, err := Fill(context.Background(), l, root, b, tc.mapZoom, opener, nil, nil); err != nil {
				t.Fatal(err)
			}
			if s, short := Measure(root, b, tc.mapZoom); short {
				t.Errorf("after the fill, still lacking: %+v", s)
			}
		})
	}
}

// A store filled before the depth was recorded plans as empty -- every tile
// the archives have is on disk -- and still measures short. Filling it again
// records the depth without fetching anything.
func TestFillWithNothingToFetchRecordsTheDepth(t *testing.T) {
	dir := layoutDir(t)
	root := filepath.Join(t.TempDir(), "maps-terrain")
	deepElsewhere(t, dir, root)
	l, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := cellBounds()
	if _, err := Fill(context.Background(), l, root, b, 16, opener, nil, nil); err != nil {
		t.Fatal(err)
	}
	// As such a store has it: the cell recorded only as deep as its tiles.
	paths, err := filepath.Glob(filepath.Join(root, "*", "cells", "*", "cell.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no cell.json under %s: %v", root, err)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(data), `"Max": 15`, `"Max": 14`, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, short := Measure(root, b, 16); !short {
		t.Fatal("precondition: the store measures short")
	}

	var empty bool
	res, err := Fill(context.Background(), l, root, b, 16, opener, func(p *Plan) { empty = p.Empty() }, nil)
	if err != nil || !empty || res.Written != 0 {
		t.Fatalf("second fill: empty %v, wrote %d, %v", empty, res.Written, err)
	}
	if s, short := Measure(root, b, 16); short {
		t.Errorf("after a fill with nothing to fetch, still lacking: %+v", s)
	}
}

// Ground given as areas goes through the same lifecycle as one area:
// measured as lacking from the disk, filled, measured as lacking nothing --
// here with a deep area and a shallower one over the same cell, which has
// to come out complete to the deeper.
func TestMeasureAndFillAreas(t *testing.T) {
	dir := layoutDir(t)
	root := filepath.Join(t.TempDir(), "maps-terrain")
	b := cellBounds()
	areas := []acquire.Area{{Bounds: b, MaxZoom: 16}, {Bounds: b, MaxZoom: 13}}
	if s, short := MeasureAreas(root, areas); !short || !s.Empty {
		t.Errorf("measuring no store: %+v, %v", s, short)
	}
	l, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := FillAreas(context.Background(), l, root, areas, opener, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written == 0 {
		t.Fatal("nothing written")
	}
	if s, short := MeasureAreas(root, areas); short {
		t.Errorf("after the fill, still lacking: %+v", s)
	}
	if s, short := Measure(root, b, 16); short {
		t.Errorf("the cell is not complete to the deeper area's depth: %+v", s)
	}
}
