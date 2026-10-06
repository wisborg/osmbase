package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
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
	// How to draw it is the last thing said, after the terrain's summary,
	// and says to draw the terrain too.
	if i, j := strings.Index(r.stdout, "terrain tiles"), strings.Index(r.stdout, "Now: osmbase render"); j < i ||
		!strings.Contains(r.stdout[j:], "--terrain --out map.png") {
		t.Errorf("the next step is not last, or omits --terrain:\n%s", r.stdout)
	}

	r = runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--terrain", "--terrain-source", t.TempDir(), "--yes")
	if r.code == 0 || !strings.Contains(r.stderr, "planet.pmtiles") {
		t.Errorf("a terrain source with no global archive: exit %d\n%s", r.code, r.stderr)
	}
}

// countTerrain counts the elevation tiles under a terrain store.
func countTerrain(t *testing.T, root string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && (strings.HasSuffix(path, ".webp") || strings.HasSuffix(path, ".png")) {
			n++
		}
		return nil
	})
	return n
}

// terrainPNG is a Terrarium PNG tile of a ridge running north to south,
// rising 400 m from either edge to the middle.
func terrainPNG(t *testing.T) []byte {
	t.Helper()
	const n = 64
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			h := 400 - math.Abs(float64(x)-n/2)*400/(n/2) + 32768
			img.SetNRGBA(x, y, color.NRGBA{uint8(int(h) >> 8), uint8(int(h)), 0, 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// render --terrain offers to fetch the terrain the view lacks -- with no
// yes, it fetches nothing, says how to, and refuses to draw an
// unshaded map as if shaded; with --yes it fetches into the store beside the
// map's -- then shades from it, credits the elevation, and hands over the
// full notice. A second render of the same view asks nothing. --terrain-store
// alone draws nothing, and --contours=false drops only the contours.
func TestRenderTerrainShadesFromTheStoreBesideTheMap(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	store := filepath.Join(t.TempDir(), "s")
	out := filepath.Join(t.TempDir(), "map.png")

	if r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
		t.Fatalf("fetch: exit %d\n%s", r.code, r.stderr)
	}
	r := runCLI(t, "render", "--store", store, "--terrain", "--lat", "0", "--lon", "0", "--zoom", "1", "--width", "256", "--height", "256", "--out", out)
	if !strings.Contains(r.stderr, "Fetch the terrain now?") || !strings.Contains(r.stderr, "no terrain was fetched") {
		t.Errorf("with nobody to say yes, the offer does not ask, or does not say it fetched nothing:\n%s", r.stderr)
	}
	if r.code == 0 || !strings.Contains(r.stderr, "osmbase fetch --terrain") {
		t.Errorf("no terrain: exit %d, want a refusal naming the fetch\nstderr: %s\nstdout: %s", r.code, r.stderr, r.stdout)
	}
	r = runCLI(t, "render", "--store", store, "--terrain-store", store+"-terrain", "--lat", "0", "--lon", "0", "--zoom", "1", "--width", "256", "--height", "256", "--out", out)
	if r.code == 0 || !strings.Contains(r.stderr, "add --terrain") {
		t.Errorf("--terrain-store alone: exit %d\n%s", r.code, r.stderr)
	}

	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:           []osmbasetest.ArchiveTile{{ID: 0, Data: terrainPNG(t)}},
		TileType:        pmtiles.TileTypePNG,
		TileCompression: pmtiles.CompressionNone,
		MinZoom:         0, MaxZoom: 12,
		MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "planet.pmtiles"), built.Bytes, 0o644)
	r = runCLI(t, "render", "--store", store, "--terrain", "--terrain-source", dir, "--yes", "--lat", "0", "--lon", "0", "--zoom", "1", "--width", "256", "--height", "256", "--out", out)
	if r.code != 0 {
		t.Fatalf("render: exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "copied from "+dir+", contacting nobody") || !strings.Contains(r.stderr, "terrain tiles in") {
		t.Errorf("the render's terrain fetch:\n%s", r.stderr)
	}
	if n := countTerrain(t, store+"-terrain"); n == 0 {
		t.Error("the render fetched no terrain into the store beside the map's")
	}
	if !strings.Contains(r.stdout, "100.0% of the image, from zoom") || !strings.Contains(r.stdout, "Elevation: "+dir) {
		t.Errorf("the report says nothing of the terrain:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "Wherever you publish it, give this notice with it:\nElevation: "+dir) {
		t.Errorf("the report does not hand over the elevation's notice:\n%s", r.stdout)
	}

	r = runCLI(t, "render", "--store", store, "--terrain", "--lat", "0", "--lon", "0", "--zoom", "1", "--width", "256", "--height", "256", "--out", out)
	if r.code != 0 || strings.Contains(r.stderr, "Fetch the terrain") || strings.Contains(r.stderr, "no terrain was fetched") {
		t.Errorf("a second render of the same view: exit %d, and it offered again:\n%s", r.code, r.stderr)
	}

	// Contours are drawn by default, at a zoom deep enough for them, and
	// --contours=false leaves them out but keeps the shading; without
	// --terrain the flag has nothing to say.
	deep := []string{"render", "--store", store, "--terrain", "--lat", "0", "--lon", "0", "--zoom", "12", "--width", "256", "--height", "256", "--out", out}
	if r := runCLI(t, deep...); r.code != 0 || !strings.Contains(r.stdout, "contours") {
		t.Errorf("contours by default: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, append(deep, "--contours=false")...)
	if r.code != 0 || strings.Contains(r.stdout, "contours") || !strings.Contains(r.stdout, "100.0% of the image, from zoom") {
		t.Errorf("--contours=false: exit %d, want the terrain without contours\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, "render", "--store", store, "--contours=false", "--lat", "0", "--lon", "0", "--zoom", "1", "--width", "256", "--height", "256", "--out", out)
	if r.code == 0 || !strings.Contains(r.stderr, "add --terrain") {
		t.Errorf("--contours without --terrain: exit %d\n%s", r.code, r.stderr)
	}
}

// render3d draws from the map store and the terrain beside it, writes the
// image, and hands over the elevation's notice; without a point to look at,
// or without terrain, it refuses rather than drawing.
func TestRender3dDrawsFromTheStores(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	store := filepath.Join(t.TempDir(), "s")
	out := filepath.Join(t.TempDir(), "map3d.png")
	if r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
		t.Fatalf("fetch: exit %d\n%s", r.code, r.stderr)
	}
	look := []string{"render3d", "--store", store, "--lat", "10", "--lon", "20", "--radius", "20", "--width", "160", "--height", "100", "--map-size", "256", "--out", out}
	if r := runCLI(t, look...); r.code == 0 || !strings.Contains(r.stderr, "osmbase fetch --terrain") {
		t.Errorf("no terrain: exit %d\n%s", r.code, r.stderr)
	}

	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:    []osmbasetest.ArchiveTile{{ID: 0, Data: terrainPNG(t)}},
		TileType: pmtiles.TileTypePNG, TileCompression: pmtiles.CompressionNone,
		MinZoom: 0, MaxZoom: 12, MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "planet.pmtiles"), built.Bytes, 0o644)
	if r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--terrain", "--terrain-source", dir, "--yes"); r.code != 0 {
		t.Fatalf("terrain fetch: exit %d\n%s", r.code, r.stderr)
	}

	r := runCLI(t, look...)
	if r.code != 0 {
		t.Fatalf("render3d: exit %d\n%s", r.code, r.stderr)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil || img.Bounds().Dx() != 160 || img.Bounds().Dy() != 100 {
		t.Errorf("the image: %v, %v", img.Bounds(), err)
	}
	if !strings.Contains(r.stdout, "looking north") || !strings.Contains(r.stdout, "give this notice with it:\nElevation: ") {
		t.Errorf("the report:\n%s", r.stdout)
	}

	if r := runCLI(t, "render3d", "--store", store, "--out", out); r.code != 2 || !strings.Contains(r.stderr, "--lat and --lon") {
		t.Errorf("no point to look at: exit %d\n%s", r.code, r.stderr)
	}
}
