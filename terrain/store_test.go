package terrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
