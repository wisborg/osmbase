package terrain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// Every place here is invented as tiles, not coordinates, except that the
// cell is the zoom-12 tile over Hornsby station, inside Mapterhorn's region
// 6/58/38, so the arithmetic is the real one.
var (
	cell    = slice.Cell{X: 3767, Y: 2455}
	hornsby = slice.TileRef{Z: 6, X: 58, Y: 38}
)

// writeArchive writes an archive of tiles of type typ, each tile's bytes its
// own name, to path.
func writeArchive(t *testing.T, path string, typ pmtiles.TileType, comp pmtiles.Compression, zoom slice.ZoomRange, refs []slice.TileRef) {
	t.Helper()
	var at []osmbasetest.ArchiveTile
	for _, r := range refs {
		id, err := pmtiles.ZxyToID(r.Z, r.X, r.Y)
		if err != nil {
			t.Fatal(err)
		}
		at = append(at, osmbasetest.ArchiveTile{ID: id, Data: []byte(typ.String() + " " + r.String())})
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles: at, TileType: typ, TileCompression: comp, MinZoom: zoom.Min, MaxZoom: zoom.Max,
		MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
}

// layoutDir writes a terrain layout into a directory: a global archive of
// the overview above the cell and the cell's zoom 12; a regional archive of
// its zooms 13 and 14; and a coverage archive of vector tiles.
func layoutDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	global, err := slice.AncestorTiles(cell, 12, slice.ZoomRange{Min: 0, Max: 11})
	if err != nil {
		t.Fatal(err)
	}
	global = append(global, slice.TileRef{Z: 12, X: cell.X, Y: cell.Y})
	writeArchive(t, filepath.Join(dir, "planet.pmtiles"), pmtiles.TileTypeWebP, pmtiles.CompressionNone, slice.ZoomRange{Min: 0, Max: 12}, global)
	deep, err := slice.PyramidTiles(cell, 12, slice.ZoomRange{Min: 13, Max: 14})
	if err != nil {
		t.Fatal(err)
	}
	writeArchive(t, filepath.Join(dir, "6-58-38.pmtiles"), pmtiles.TileTypeWebP, pmtiles.CompressionNone, slice.ZoomRange{Min: 13, Max: 14}, deep)
	writeArchive(t, filepath.Join(dir, "coverage.pmtiles"), pmtiles.TileTypeMVT, pmtiles.CompressionGzip, slice.ZoomRange{Min: 0, Max: 12}, global)
	return dir
}

func opener(source string) (*fetch.Archive, error) { return fetch.Open(source, fetch.Options{}) }

// cellBounds is the area of the cell, a little inside its edges.
func cellBounds() slice.Bounds {
	b := tileBounds(slice.TileRef{Z: 12, X: cell.X, Y: cell.Y})
	dx, dy := (b.East-b.West)/10, (b.North-b.South)/10
	return slice.Bounds{West: b.West + dx, East: b.East - dx, South: b.South + dy, North: b.North - dy}
}

// A layout read from a directory has its global archive, its coverage and
// its regions, each region's area read from its name; a directory with no
// global archive is refused.
func TestReadDir(t *testing.T) {
	dir := layoutDir(t)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "6-99-0.pmtiles"), []byte("x"), 0o644) // x beyond zoom 6
	l, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(l.Global, "planet.pmtiles") || !strings.HasSuffix(l.Coverage, "coverage.pmtiles") || len(l.Regions) != 1 || l.Regions[0].Tile != hornsby {
		t.Errorf("layout %+v", l)
	}
	if r, ok := l.regionFor(cell, 12); !ok || r.Tile != hornsby {
		t.Errorf("the cell's region: %+v, %v", r, ok)
	}
	if _, ok := l.regionFor(slice.Cell{X: 0, Y: 0}, 12); ok {
		t.Error("a cell far from every region found one")
	}
	if _, err := ReadDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "planet.pmtiles") {
		t.Errorf("a directory with no global archive: %v", err)
	}
}

// A hosted layout's index lists the global archive and the regions, with
// their URLs, and the coverage is taken to sit beside them; an index with no
// global archive is refused.
func TestReadIndex(t *testing.T) {
	idx := `{"version": 1, "items": [
		{"name": "planet.pmtiles", "url": "https://example.org/terrain/planet.pmtiles", "min_zoom": 0, "max_zoom": 12},
		{"name": "6-58-38.pmtiles", "url": "https://mirror.example.org/6-58-38.pmtiles", "min_zoom": 13, "max_zoom": 14},
		{"name": "6-34-20.pmtiles", "min_zoom": 13, "max_zoom": 17}
	]}`
	l, err := ReadIndex("https://example.org/terrain/", strings.NewReader(idx))
	if err != nil {
		t.Fatal(err)
	}
	if l.Global != "https://example.org/terrain/planet.pmtiles" || l.Coverage != "https://example.org/terrain/coverage.pmtiles" || l.Sources != "https://example.org/terrain/attribution.json" {
		t.Errorf("global %q, coverage %q, sources %q", l.Global, l.Coverage, l.Sources)
	}
	if len(l.Regions) != 2 || l.Regions[0].Tile != (slice.TileRef{Z: 6, X: 34, Y: 20}) || l.Regions[0].Source != "https://example.org/terrain/6-34-20.pmtiles" || l.Regions[1].Source != "https://mirror.example.org/6-58-38.pmtiles" {
		t.Errorf("regions %+v", l.Regions)
	}
	if _, err := ReadIndex("x", strings.NewReader(`{"items": [{"name": "6-1-1.pmtiles"}]}`)); err == nil {
		t.Error("an index with no global archive was read")
	}
	if _, err := ReadIndex("x", strings.NewReader(`not json`)); err == nil {
		t.Error("an index that is not JSON was read")
	}
}

// A terrain fetch fills one elevation source from both archives -- the
// cell's zoom 12 from the global one, its 13 and 14 from its region -- as
// .webp files, records the depth it reached, and fills a coverage source of
// vector tiles beside it; a second fetch of the same area fetches nothing.
func TestPrepareAndFetch(t *testing.T) {
	l, err := ReadDir(layoutDir(t))
	if err != nil {
		t.Fatal(err)
	}
	st, err := slice.Create(t.TempDir(), slice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	req := acquire.Request{Bounds: cellBounds(), MaxZoom: 15, CellZoom: st.CellZoom()}
	p, err := Prepare(context.Background(), l, st, req, opener)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// --max-zoom 15 for the map is 14 for terrain, which is as deep as the
	// region goes.
	if p.Global.Zoom != (slice.ZoomRange{Min: 12, Max: 12}) || len(p.Regions) != 1 || p.Regions[0].Plan.Zoom != (slice.ZoomRange{Min: 12, Max: 14}) || p.Coverage == nil {
		t.Fatalf("plan: global %v, %d regions, coverage %v", p.Global.Zoom, len(p.Regions), p.Coverage != nil)
	}
	if p.Remote() || p.Empty() {
		t.Errorf("remote %v, empty %v", p.Remote(), p.Empty())
	}
	res, err := p.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 12 overview tiles and zoom 12 from the global archive, 4 + 16 from the
	// region, and the coverage's 13.
	if res.Written != 13+20+13 {
		t.Errorf("wrote %d tiles, want %d", res.Written, 13+20+13)
	}

	sources, err := st.Sources()
	if err != nil {
		t.Fatal(err)
	}
	var elevation, coverage *slice.Manifest
	for i := range sources {
		switch sources[i].TileType {
		case "webp":
			elevation = &sources[i]
		case "mvt":
			coverage = &sources[i]
		}
	}
	if len(sources) != 2 || elevation == nil || coverage == nil {
		t.Fatalf("sources %+v", sources)
	}
	if elevation.Source != l.Name || elevation.SourceZoom != (slice.ZoomRange{Min: 0, Max: 14}) || elevation.Zoom != (slice.ZoomRange{Min: 0, Max: 14}) {
		t.Errorf("elevation %+v", *elevation)
	}
	src, err := st.Source(elevation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []slice.TileRef{{Z: 12, X: cell.X, Y: cell.Y}, {Z: 14, X: cell.X << 2, Y: cell.Y << 2}, {Z: 3, X: cell.X >> 9, Y: cell.Y >> 9}} {
		data, ok, err := src.Tile(ref.Z, ref.X, ref.Y)
		if err != nil || !ok || string(data) != "webp "+ref.String() {
			t.Errorf("tile %s: %q, %v, %v", ref, data, ok, err)
		}
	}
	webp := 0
	filepath.WalkDir(filepath.Join(st.Root(), elevation.ID), func(path string, d os.DirEntry, err error) error {
		if strings.HasSuffix(path, ".webp") {
			webp++
		}
		return nil
	})
	if webp != 13+20 {
		t.Errorf("%d .webp files in the elevation source, want %d", webp, 13+20)
	}

	again, err := Prepare(context.Background(), l, st, req, opener)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !again.Empty() {
		t.Errorf("a second fetch of the same area plans %+v", again.Totals())
	}
}

// A map depth that does not reach below the global archive opens no region;
// an area outside every region takes the global archive alone; and a global
// archive of vector tiles is refused, since it is not terrain.
func TestPrepareOpensRegionsOnlyWhenNeeded(t *testing.T) {
	dir := layoutDir(t)
	l, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := slice.Create(t.TempDir(), slice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	counting := func(s string) (*fetch.Archive, error) {
		if strings.Contains(s, "6-58-38") {
			opened++
		}
		return opener(s)
	}
	p, err := Prepare(context.Background(), l, st, acquire.Request{Bounds: cellBounds(), MaxZoom: 13, CellZoom: 12}, counting)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if opened != 0 || len(p.Regions) != 0 {
		t.Errorf("a terrain depth of 12 opened the region %d times", opened)
	}

	far := tileBounds(slice.TileRef{Z: 12, X: 0, Y: 1000})
	p, err = Prepare(context.Background(), l, st, acquire.Request{Bounds: far, MaxZoom: 15, CellZoom: 12}, counting)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if opened != 0 || len(p.Regions) != 0 {
		t.Errorf("an area outside every region opened one")
	}

	vec := filepath.Join(t.TempDir(), "planet.pmtiles")
	writeArchive(t, vec, pmtiles.TileTypeMVT, pmtiles.CompressionGzip, slice.ZoomRange{Min: 0, Max: 12}, []slice.TileRef{{Z: 0}})
	if _, err := Prepare(context.Background(), Layout{Name: "v", Global: vec}, st, acquire.Request{Bounds: cellBounds(), MaxZoom: 13, CellZoom: 12}, opener); err == nil || !strings.Contains(err.Error(), "images of elevation") {
		t.Errorf("a vector global archive: %v", err)
	}
}

// The automatic depth is one zoom shallower than a map's of the same area
// would be.
func TestPrepareTakesAZoomLessThanTheMap(t *testing.T) {
	l, err := ReadDir(layoutDir(t))
	if err != nil {
		t.Fatal(err)
	}
	st, err := slice.Create(t.TempDir(), slice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	b := cellBounds()
	mapDepth, err := acquire.DepthFor(b, slice.MaxCellZoom)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(context.Background(), l, st, acquire.Request{Bounds: b, MaxZoom: acquire.AutoZoom, CellZoom: 12}, opener)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	want := min(int(mapDepth.Max)-1, 14)
	if len(p.Regions) != 1 || int(p.Regions[0].Plan.Zoom.Max) != want {
		t.Errorf("the map's depth is %d; terrain planned %v, want down to %d", mapDepth.Max, p.Regions, want)
	}
}

// The list of sources is copied into the store's root once, and refused when
// it is not a list of sources.
func TestFetchCopiesTheListOfSources(t *testing.T) {
	dir := layoutDir(t)
	list := filepath.Join(dir, SourcesName)
	os.WriteFile(list, []byte(`[{"source": "glo30", "producer": "DLR"}]`), 0o644)
	l, err := ReadDir(dir)
	if err != nil || l.Sources != list {
		t.Fatalf("sources %q, %v", l.Sources, err)
	}
	root := t.TempDir()
	st, err := slice.Create(root, slice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	req := acquire.Request{Bounds: cellBounds(), MaxZoom: 15, CellZoom: st.CellZoom()}
	fetchOnce := func() (*Plan, error) {
		p, err := Prepare(context.Background(), l, st, req, opener)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		_, err = p.Fetch(context.Background(), nil)
		return p, err
	}
	if _, err := fetchOnce(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, SourcesName)); err != nil || !strings.Contains(string(b), "glo30") {
		t.Fatalf("store's list: %q, %v", b, err)
	}
	if sources, _ := st.Sources(); len(sources) != 2 {
		t.Errorf("%d sources in the store, want 2: the list is not one", len(sources))
	}
	p, err := Prepare(context.Background(), l, st, req, opener)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if p.Sources != "" || !p.Empty() {
		t.Errorf("a second fetch copies %q again, empty %v", p.Sources, p.Empty())
	}

	os.Remove(filepath.Join(root, SourcesName))
	os.WriteFile(list, []byte(`<html>not found</html>`), 0o644)
	if _, err := fetchOnce(); err == nil || !strings.Contains(err.Error(), "not a list") {
		t.Errorf("a list that is not one: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, SourcesName)); err == nil {
		t.Error("the bad list was saved")
	}
}
