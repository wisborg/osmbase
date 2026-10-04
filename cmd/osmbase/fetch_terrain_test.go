package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// terrainDir writes a terrain layout of one global archive holding the zoom-0
// tile, as WebP.
func terrainDir(t *testing.T) string {
	t.Helper()
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:           []osmbasetest.ArchiveTile{{ID: 0, Data: []byte("RIFF....WEBPVP8L")}},
		TileType:        pmtiles.TileTypeWebP,
		TileCompression: pmtiles.CompressionNone,
		MinZoom:         0, MaxZoom: 12,
		MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planet.pmtiles"), built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// --terrain fetches the map and the terrain for one area, from a directory
// of archives that asks nobody; the terrain goes to a store beside the map's,
// which keeps its one source, so nothing that draws from it has a choice to
// make; and a dry run writes no terrain.
func TestFetchTerrainKeepsItBesideTheMap(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	store := filepath.Join(t.TempDir(), "s")
	dir := terrainDir(t)

	r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--terrain", "--terrain-source", dir, "--dry-run")
	if r.code != 0 {
		t.Fatalf("dry run: exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "terrain      "+dir) || !strings.Contains(r.stdout, "store        "+store+"-terrain") {
		t.Errorf("the dry run's terrain plan:\n%s", r.stdout)
	}
	if n := countTerrain(t, store+"-terrain"); n != 0 {
		t.Errorf("a dry run wrote %d terrain tiles", n)
	}

	r = runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--terrain", "--terrain-source", dir, "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if n := countTerrain(t, store+"-terrain"); n != 1 {
		t.Errorf("%d terrain tiles fetched, want 1\n%s", n, r.stdout)
	}
	st, err := slice.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if sources, _ := st.Sources(); len(sources) != 1 || sources[0].TileType != "mvt" {
		t.Errorf("the map's store holds %+v; want its one map source", sources)
	}
	if !strings.Contains(r.stdout, "terrain tiles") {
		t.Errorf("the terrain fetch was not reported:\n%s", r.stdout)
	}

	r = runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--terrain", "--terrain-source", t.TempDir(), "--yes")
	if r.code == 0 || !strings.Contains(r.stderr, "planet.pmtiles") {
		t.Errorf("a terrain source with no global archive: exit %d\n%s", r.code, r.stderr)
	}
}

// countTerrain counts the WebP tiles under a terrain store.
func countTerrain(t *testing.T, root string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".webp") {
			n++
		}
		return nil
	})
	return n
}
