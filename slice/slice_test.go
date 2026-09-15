package slice_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// Everything here is synthetic and offline. The archives are built in this
// file by osmbasetest, the stores are temporary directories, and no coordinate
// names anywhere real: the cells below are grid indices, and the rectangles
// are derived from those indices rather than from a place. A store is a record
// of where somebody was, so a fixture that used a real one would publish it.

// testCell and testZooms are the cell every test fills and how deep.
//
// The cell is an arbitrary pair of grid indices in the southern hemisphere's
// half of the grid; nothing in the store's behaviour depends on which, and
// choosing by arithmetic rather than by place is deliberate.
//
// Three zoom levels is 4^0 + 4^1 + 4^2 = 21 tiles in the cell, which is the
// number every count in this file is checked against and which is small enough
// to write out.
var (
	testCell  = slice.Cell{X: 1000, Y: 1500}
	testZooms = slice.ZoomRange{Min: 12, Max: 14}
)

const testCellTiles = 21

// ---------------------------------------------------------------------------
// Fixtures

// pyramid is the tile set of one cell, each tile carrying bytes that name it,
// so that a tile served from the wrong path is a comparison failure and not a
// test that passes on a coincidence.
func pyramid(t *testing.T, c slice.Cell, cellZoom uint8, z slice.ZoomRange) map[slice.TileRef][]byte {
	t.Helper()
	refs, err := slice.PyramidTiles(c, cellZoom, z)
	if err != nil {
		t.Fatalf("listing the tiles of cell %s: %v", c, err)
	}
	out := make(map[slice.TileRef][]byte, len(refs))
	for _, r := range refs {
		out[r] = []byte("tile " + r.String())
	}
	return out
}

// archive builds a synthetic PMTiles archive holding exactly these tiles.
//
// The tiles are gzipped, which is what every real build uses, so the round
// trip under test is the real one: the store writes the compressed bytes down
// untouched and decompresses them on the way out.
func archive(t *testing.T, tiles map[slice.TileRef][]byte) *pmtiles.Reader {
	t.Helper()
	var at []osmbasetest.ArchiveTile
	for ref, data := range tiles {
		id, err := pmtiles.ZxyToID(ref.Z, ref.X, ref.Y)
		if err != nil {
			t.Fatalf("tile %s has no archive ID: %v", ref, err)
		}
		at = append(at, osmbasetest.ArchiveTile{ID: id, Data: data})
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:           at,
		TileCompression: pmtiles.CompressionGzip,
		TileType:        pmtiles.TileTypeMVT,
		MinZoom:         0,
		MaxZoom:         15,
	})
	if err != nil {
		t.Fatalf("building the archive: %v", err)
	}
	r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes))
	if err != nil {
		t.Fatalf("opening the archive: %v", err)
	}
	return r
}

// describe is the caller's side of filling a store: everything the store
// records about a source comes from whoever opened the archive, because the
// store never opens one itself.
func describe(r *pmtiles.Reader, name string) slice.SourceDesc {
	h := r.Header()
	return slice.SourceDesc{
		Source:          name,
		Build:           "20260101",
		Schema:          "test schema v1",
		Attribution:     "(c) synthetic contributors",
		TileType:        h.TileType.String(),
		TileCompression: slice.Compression(h.TileCompression.String()),
		SourceZoom:      slice.ZoomRange{Min: h.MinZoom, Max: h.MaxZoom},
	}
}

// newStore makes a store in a temporary directory.
func newStore(t *testing.T, cfg slice.Config) *slice.Store {
	t.Helper()
	st, err := slice.Create(t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	return st
}

// addSource registers a source and returns it.
func addSource(t *testing.T, st *slice.Store, r *pmtiles.Reader, name string) *slice.Source {
	t.Helper()
	src, err := st.AddSource(describe(r, name))
	if err != nil {
		t.Fatalf("adding source %q: %v", name, err)
	}
	return src
}

// filled is the ordinary starting point: a store holding one cell of one
// source, plus the shallow ancestors above it.
func filled(t *testing.T) (*slice.Store, *slice.Source, map[slice.TileRef][]byte) {
	t.Helper()
	st := newStore(t, slice.Config{})
	tiles := pyramid(t, testCell, st.CellZoom(), testZooms)
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	if _, err := src.Fill(context.Background(), r, testCell, testZooms); err != nil {
		t.Fatalf("filling cell %s: %v", testCell, err)
	}
	return st, src, tiles
}

// countFiles returns every regular file under dir, relative to it and sorted,
// so a test can say exactly what a store is made of.
func countFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

// cellBounds is the rectangle one cell covers, pulled in by a thousandth of
// its width so that the rectangle is unambiguously inside the one cell rather
// than touching its neighbours' edges.
func cellBounds(t *testing.T, c slice.Cell, cellZoom uint8) slice.Bounds {
	t.Helper()
	west, south, east, north, err := mercator.TileBounds(cellZoom, c.X, c.Y)
	if err != nil {
		t.Fatalf("bounds of cell %s: %v", c, err)
	}
	dx, dy := (east-west)/1000, (north-south)/1000
	return slice.Bounds{West: west + dx, South: south + dy, East: east - dx, North: north - dy}
}

// ---------------------------------------------------------------------------
// Filling

// TestFill_StoresEveryTileOfTheCellAndReadsThemBackDecompressed is the first
// verifiable thing the build order asks of this package.
//
// The expected count is derived rather than observed: a cell holds its own
// tile plus every descendant down to the range's maximum, which over zooms 12
// to 14 is 4^0 + 4^1 + 4^2 = 21.
func TestFill_StoresEveryTileOfTheCellAndReadsThemBackDecompressed(t *testing.T) {
	st, src, tiles := filled(t)

	if len(tiles) != testCellTiles {
		t.Fatalf("the fixture holds %d tiles for zooms %s; a three-level cell is %d", len(tiles), testZooms, testCellTiles)
	}
	for ref, want := range tiles {
		got, ok, err := src.Tile(ref.Z, ref.X, ref.Y)
		if err != nil {
			t.Fatalf("reading tile %s: %v", ref, err)
		}
		if !ok {
			t.Fatalf("tile %s was filled and the store does not have it", ref)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("tile %s read back as %q, want %q", ref, got, want)
		}
	}

	info, complete, err := src.Cell(testCell)
	if err != nil {
		t.Fatalf("reading cell %s: %v", testCell, err)
	}
	if !complete {
		t.Fatalf("cell %s is not complete after a fill that finished", testCell)
	}
	if info.Tiles != testCellTiles {
		t.Errorf("cell %s records %d tiles, want %d", testCell, info.Tiles, testCellTiles)
	}
	if info.Zoom != testZooms {
		t.Errorf("cell %s records zooms %s, want %s", testCell, info.Zoom, testZooms)
	}
	if info.Bytes <= 0 {
		t.Errorf("cell %s records %d bytes, and 21 tiles are not free", testCell, info.Bytes)
	}

	if got := src.Manifest().Zoom; got != testZooms {
		t.Errorf("the manifest records zooms %s after filling %s", got, testZooms)
	}
	total, err := st.Bytes()
	if err != nil {
		t.Fatalf("measuring the store: %v", err)
	}
	if total < info.Bytes {
		t.Errorf("the store measures %d bytes and one cell in it measures %d", total, info.Bytes)
	}
}

// TestFill_ATileTheArchiveLacksIsAbsentRatherThanAFailure covers the ordinary
// case, not an edge one: most coordinates in most archives have no tile at
// all, which is why a cell over open water is a few kilobytes. A fill that
// treated that as an error could not fetch a coastline.
func TestFill_ATileTheArchiveLacksIsAbsentRatherThanAFailure(t *testing.T) {
	st := newStore(t, slice.Config{})
	tiles := pyramid(t, testCell, st.CellZoom(), testZooms)

	// Drop every tile of the deepest level: 4^2 = 16 of the 21.
	var dropped []slice.TileRef
	for ref := range tiles {
		if ref.Z == testZooms.Max {
			dropped = append(dropped, ref)
		}
	}
	for _, ref := range dropped {
		delete(tiles, ref)
	}
	if len(dropped) != 16 {
		t.Fatalf("dropped %d tiles at zoom %d, want 16", len(dropped), testZooms.Max)
	}

	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	f, err := src.Fill(context.Background(), r, testCell, testZooms)
	if err != nil {
		t.Fatalf("filling cell %s: %v", testCell, err)
	}
	if f.Written != 5 || f.Absent != 16 || f.Skipped != 0 {
		t.Errorf("fill wrote %d, missed %d, skipped %d; want 5 written and 16 absent", f.Written, f.Absent, f.Skipped)
	}
	if _, complete, err := src.Cell(testCell); err != nil || !complete {
		t.Errorf("cell %s is complete=%v (err %v); a cell whose archive has nothing deeper is still a finished fetch", testCell, complete, err)
	}
	for _, ref := range dropped {
		data, ok, err := src.Tile(ref.Z, ref.X, ref.Y)
		if err != nil {
			t.Errorf("reading absent tile %s returned an error: %v", ref, err)
		}
		if ok || data != nil {
			t.Errorf("tile %s came back present from a store that never had it", ref)
		}
	}
}

// failingArchive answers from a map and then refuses, which is what an
// interrupted download looks like from the store's side.
type failingArchive struct {
	tiles map[slice.TileRef][]byte
	after int
	calls int
}

func (a *failingArchive) RawTile(z uint8, x, y uint32) ([]byte, bool, error) {
	a.calls++
	if a.calls > a.after {
		return nil, false, fmt.Errorf("the network went away")
	}
	data, ok := a.tiles[slice.TileRef{Z: z, X: x, Y: y}]
	return data, ok, nil
}

// rawArchive is an archive that counts what was asked of it.
type rawArchive struct {
	tiles map[slice.TileRef][]byte
	calls int
}

func (a *rawArchive) RawTile(z uint8, x, y uint32) ([]byte, bool, error) {
	a.calls++
	data, ok := a.tiles[slice.TileRef{Z: z, X: x, Y: y}]
	return data, ok, nil
}

// raw re-reads the fixture's tiles as the archive stores them, so that a
// second archive built over the same tiles hands out the same bytes.
func raw(t *testing.T, r *pmtiles.Reader, tiles map[slice.TileRef][]byte) map[slice.TileRef][]byte {
	t.Helper()
	out := make(map[slice.TileRef][]byte, len(tiles))
	for ref := range tiles {
		data, ok, err := r.RawTile(ref.Z, ref.X, ref.Y)
		if err != nil || !ok {
			t.Fatalf("the fixture archive does not hold %s (ok=%v, err=%v)", ref, ok, err)
		}
		out[ref] = data
	}
	return out
}

// TestFill_AKilledFetchLeavesACellRecognisedAsIncomplete is why cell.json is
// written last.
//
// The tiles that did land are real tiles and are still served -- refusing them
// would turn an interrupted download into a blank map rather than a partial
// one -- but the cell must not claim to be finished, because a cell that
// claims to be finished and has holes in it is a hole nobody notices until
// somebody renders that square of the world.
func TestFill_AKilledFetchLeavesACellRecognisedAsIncomplete(t *testing.T) {
	st := newStore(t, slice.Config{})
	tiles := pyramid(t, testCell, st.CellZoom(), testZooms)
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")

	killed := &failingArchive{tiles: raw(t, r, tiles), after: 5}
	if _, err := src.Fill(context.Background(), killed, testCell, testZooms); err == nil {
		t.Fatal("a fill against an archive that failed part way through returned no error")
	}

	if _, complete, err := src.Cell(testCell); err != nil || complete {
		t.Errorf("cell %s reads back complete=%v (err %v) after an interrupted fetch", testCell, complete, err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), src.ID(), "cells", testCell.String(), "cell.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cell.json exists after an interrupted fetch: %v", err)
	}

	cov, err := src.Coverage(cellBounds(t, testCell, st.CellZoom()))
	if err != nil {
		t.Fatalf("measuring coverage: %v", err)
	}
	if cov.Cells != 1 || cov.Complete != 0 {
		t.Errorf("coverage says %d cells, %d complete; want one cell and none complete", cov.Cells, cov.Complete)
	}
	if len(cov.Partial) != 1 || cov.Partial[0] != testCell {
		t.Errorf("coverage lists partial cells %v, want just %s", cov.Partial, testCell)
	}
	if len(cov.Missing) != 0 {
		t.Errorf("coverage lists %v as missing; a cell with tiles in it is partial, not missing", cov.Missing)
	}

	// The five tiles that landed before the archive died are still readable.
	first, err := slice.PyramidTiles(testCell, st.CellZoom(), testZooms)
	if err != nil {
		t.Fatalf("listing the cell's tiles: %v", err)
	}
	for _, ref := range first[:5] {
		if _, ok, err := src.Tile(ref.Z, ref.X, ref.Y); err != nil || !ok {
			t.Errorf("tile %s was written before the fetch died and reads back ok=%v (err %v)", ref, ok, err)
		}
	}
}

// TestFill_ResumingAPartialCellRefetchesOnlyWhatIsMissing is the other half of
// cell-first storage: a killed fetch is finished rather than repeated.
//
// The counts are derived: the first archive answered five tiles before it
// failed, so a resume over a 21-tile cell asks for the other 16 and skips the
// five already on disk.
func TestFill_ResumingAPartialCellRefetchesOnlyWhatIsMissing(t *testing.T) {
	st := newStore(t, slice.Config{})
	tiles := pyramid(t, testCell, st.CellZoom(), testZooms)
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")

	stored := raw(t, r, tiles)
	if _, err := src.Fill(context.Background(), &failingArchive{tiles: stored, after: 5}, testCell, testZooms); err == nil {
		t.Fatal("the interrupted fill returned no error")
	}

	resume := &rawArchive{tiles: stored}
	f, err := src.Fill(context.Background(), resume, testCell, testZooms)
	if err != nil {
		t.Fatalf("resuming cell %s: %v", testCell, err)
	}
	if f.Skipped != 5 || f.Written != testCellTiles-5 {
		t.Errorf("the resume skipped %d and wrote %d; want 5 skipped and %d written", f.Skipped, f.Written, testCellTiles-5)
	}
	if resume.calls != testCellTiles-5 {
		t.Errorf("the resume asked the archive for %d tiles; the five already on disk should not have been asked for", resume.calls)
	}
	if _, complete, err := src.Cell(testCell); err != nil || !complete {
		t.Errorf("cell %s is complete=%v (err %v) after a resume that finished", testCell, complete, err)
	}
	for ref, want := range tiles {
		got, ok, err := src.Tile(ref.Z, ref.X, ref.Y)
		if err != nil || !ok {
			t.Fatalf("tile %s reads back ok=%v (err %v) after a resume", ref, ok, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("tile %s read back as %q, want %q", ref, got, want)
		}
	}
}

// TestFill_RefusesAZoomRangeThatBelongsToTheOtherHalfOfTheStore checks the one
// boundary in the whole design that has two answers: a tile shallower than the
// cell zoom is shared between cells and stored once per source, and one at or
// below it belongs to exactly one cell. Writing either through the other
// door would put a shared tile inside a cell that eviction can remove.
func TestFill_RefusesAZoomRangeThatBelongsToTheOtherHalfOfTheStore(t *testing.T) {
	st := newStore(t, slice.Config{})
	tiles := pyramid(t, testCell, st.CellZoom(), testZooms)
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	ctx := context.Background()

	if _, err := src.Fill(ctx, r, testCell, slice.ZoomRange{Min: 0, Max: 5}); err == nil {
		t.Error("filling a cell with a zoom range entirely above the cell zoom was accepted")
	}
	deep := slice.TileRef{Z: st.CellZoom(), X: testCell.X, Y: testCell.Y}
	if _, err := src.FillOverview(ctx, r, []slice.TileRef{deep}); err == nil {
		t.Errorf("writing tile %s into the shared overview was accepted; it belongs to a cell", deep)
	}
}

// ---------------------------------------------------------------------------
// Reopening

// TestOpen_ReopeningRecoversTheManifestCoverageAndByteCount is the property
// that makes the store a store rather than a cache of one process: everything
// it knows is on disk, and a program that starts tomorrow finds it.
func TestOpen_ReopeningRecoversTheManifestCoverageAndByteCount(t *testing.T) {
	st, src, _ := filled(t)
	if err := fillAncestors(t, st, src); err != nil {
		t.Fatalf("filling the overview: %v", err)
	}
	area := cellBounds(t, testCell, st.CellZoom())

	wantManifest := src.Manifest()
	wantCoverage, err := src.Coverage(area)
	if err != nil {
		t.Fatalf("measuring coverage: %v", err)
	}
	wantBytes, err := st.Bytes()
	if err != nil {
		t.Fatalf("measuring the store: %v", err)
	}

	reopened, err := slice.Open(st.Root())
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	if reopened.CellZoom() != st.CellZoom() {
		t.Errorf("the reopened store has cell zoom %d, want %d", reopened.CellZoom(), st.CellZoom())
	}
	sources, err := reopened.Sources()
	if err != nil {
		t.Fatalf("listing sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("the reopened store lists %d sources, want 1", len(sources))
	}
	if sources[0] != wantManifest {
		t.Errorf("the reopened manifest is\n%+v\nwant\n%+v", sources[0], wantManifest)
	}
	if sources[0].Attribution != wantManifest.Attribution || sources[0].Attribution == "" {
		t.Errorf("the reopened manifest carries attribution %q; the credit is data and has to survive a restart", sources[0].Attribution)
	}

	again, err := reopened.Source(wantManifest.ID)
	if err != nil {
		t.Fatalf("opening source %s: %v", wantManifest.ID, err)
	}
	gotCoverage, err := again.Coverage(area)
	if err != nil {
		t.Fatalf("measuring coverage after reopening: %v", err)
	}
	if fmt.Sprint(gotCoverage) != fmt.Sprint(wantCoverage) {
		t.Errorf("coverage after reopening is %+v, want %+v", gotCoverage, wantCoverage)
	}
	gotBytes, err := reopened.Bytes()
	if err != nil {
		t.Fatalf("measuring the reopened store: %v", err)
	}
	if gotBytes != wantBytes {
		t.Errorf("the reopened store measures %d bytes, want %d", gotBytes, wantBytes)
	}
}

// fillAncestors writes the shallow chain above the test cell.
func fillAncestors(t *testing.T, st *slice.Store, src *slice.Source) error {
	t.Helper()
	refs, err := slice.AncestorTiles(testCell, st.CellZoom(), slice.ZoomRange{Min: 0, Max: st.CellZoom() - 1})
	if err != nil {
		return err
	}
	tiles := make(map[slice.TileRef][]byte, len(refs))
	for _, r := range refs {
		tiles[r] = []byte("overview " + r.String())
	}
	a := archive(t, tiles)
	_, err = src.FillOverview(context.Background(), a, refs)
	return err
}

// TestOpen_AMissingStoreIsReportedAndNotCreated.
//
// A render opening a store that is not there wants to hear so: the honest
// answer is that there is no basemap. Creating one would leave an empty
// directory a render can never fill, because filling reaches the network and a
// render must not.
func TestOpen_AMissingStoreIsReportedAndNotCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-there")
	_, err := slice.Open(root)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("opening a missing store returned %v, want something errors.Is reports as fs.ErrNotExist", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("opening a missing store created %s", root)
	}
}

// TestCreate_AStoreKeptItsOwnCellZoomRatherThanTheDefault is the property that
// makes the cell zoom a recorded fact instead of a compiled-in one.
//
// A store written at one cell zoom and read at another finds nothing: every
// lookup goes to a path that does not exist, and the next fetch writes a
// second copy of the same ground beside the first. So the store on disk is the
// authority, even against a Config that says otherwise.
func TestCreate_AStoreKeptItsOwnCellZoomRatherThanTheDefault(t *testing.T) {
	const odd = 10
	if odd == slice.DefaultCellZoom {
		t.Fatalf("this test needs a cell zoom that is not the default %d", slice.DefaultCellZoom)
	}
	root := t.TempDir()
	st, err := slice.Create(root, slice.Config{CellZoom: odd})
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	if st.CellZoom() != odd {
		t.Fatalf("the new store has cell zoom %d, want %d", st.CellZoom(), odd)
	}

	cell := slice.Cell{X: 300, Y: 400}
	zooms := slice.ZoomRange{Min: odd, Max: odd + 1}
	tiles := pyramid(t, cell, odd, zooms)
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	if _, err := src.Fill(context.Background(), r, cell, zooms); err != nil {
		t.Fatalf("filling cell %s: %v", cell, err)
	}

	// Reopening, and asking again with a different Config, both give the
	// recorded zoom.
	for _, name := range []string{"Open", "Create"} {
		var again *slice.Store
		var err error
		if name == "Open" {
			again, err = slice.Open(root)
		} else {
			again, err = slice.Create(root, slice.Config{CellZoom: slice.DefaultCellZoom})
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if again.CellZoom() != odd {
			t.Errorf("%s gave cell zoom %d, want the recorded %d", name, again.CellZoom(), odd)
		}
		reopened, err := again.Source(src.ID())
		if err != nil {
			t.Fatalf("%s: opening the source: %v", name, err)
		}
		for ref, want := range tiles {
			got, ok, err := reopened.Tile(ref.Z, ref.X, ref.Y)
			if err != nil || !ok {
				t.Fatalf("%s: tile %s reads back ok=%v (err %v)", name, ref, ok, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: tile %s read back as %q, want %q", name, ref, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Keying

// TestTile_IsKeyedOnCoordinatesAloneSoTwoResolutionsShareOneStore is the
// explicit fix for a defect in fitdash's own imagery cache, which hashes the
// requested pixel width and height into its key and therefore misses on every
// resolution change, re-fetching imagery it already holds.
//
// The two renders here ask for the same ground at 256 and at 1024 pixels,
// which resolves to zoom 12 and zoom 14 -- different tiles, from one store,
// with no entry added by either. The check is that the set of files is byte
// for byte the same afterwards: a store that keyed on anything but the
// coordinates would have grown.
func TestTile_IsKeyedOnCoordinatesAloneSoTwoResolutionsShareOneStore(t *testing.T) {
	st := newStore(t, slice.Config{})
	refs, err := slice.PyramidTiles(testCell, st.CellZoom(), testZooms)
	if err != nil {
		t.Fatalf("listing the cell's tiles: %v", err)
	}
	// Real vector tiles this time, because a renderer is going to decode them.
	land, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "earth",
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{Exterior: mvt.Ring{
				{X: 0, Y: 0}, {X: mvt.DefaultExtent, Y: 0},
				{X: mvt.DefaultExtent, Y: mvt.DefaultExtent}, {X: 0, Y: mvt.DefaultExtent},
			}}}},
		}},
	}}})
	if err != nil {
		t.Fatalf("building a tile: %v", err)
	}
	tiles := make(map[slice.TileRef][]byte, len(refs))
	for _, r := range refs {
		tiles[r] = land
	}
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	if _, err := src.Fill(context.Background(), r, testCell, testZooms); err != nil {
		t.Fatalf("filling cell %s: %v", testCell, err)
	}

	before := countFiles(t, st.Root())
	beforeBytes, err := st.Bytes()
	if err != nil {
		t.Fatalf("measuring the store: %v", err)
	}

	renderer, err := render.New(src, render.Options{
		Style:       render.BasemapStyle(),
		Attribution: src.Manifest().Attribution,
	})
	if err != nil {
		t.Fatalf("building a renderer over the store: %v", err)
	}
	b := cellBounds(t, testCell, st.CellZoom())
	view := render.View{Bounds: render.Bounds{West: b.West, South: b.South, East: b.East, North: b.North}}

	var zooms []uint8
	for _, px := range []int{256, 1024} {
		view.Width, view.Height = px, px
		res, err := renderer.Render(context.Background(), view)
		if err != nil {
			t.Fatalf("rendering at %dx%d from the store: %v", px, px, err)
		}
		if res.Covered != 1 {
			t.Errorf("rendering at %dx%d covered %.3f of the view from a store holding the whole cell", px, px, res.Covered)
		}
		zooms = append(zooms, res.Zoom)
	}
	if zooms[0] == zooms[1] {
		t.Fatalf("both renders resolved to zoom %d; this test needs two resolutions to ask for different tiles", zooms[0])
	}

	after := countFiles(t, st.Root())
	afterBytes, err := st.Bytes()
	if err != nil {
		t.Fatalf("measuring the store: %v", err)
	}
	if strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("the store's files changed across two renders at different resolutions:\nbefore %v\nafter  %v", before, after)
	}
	if afterBytes != beforeBytes {
		t.Errorf("the store measured %d bytes before the renders and %d after", beforeBytes, afterBytes)
	}
}

// ---------------------------------------------------------------------------
// Eviction

// fillCells puts several cells of one source in a store, in the order given,
// so that the eviction clock runs in that order too.
func fillCells(t *testing.T, cells ...slice.Cell) (*slice.Store, *slice.Source) {
	t.Helper()
	st := newStore(t, slice.Config{})
	zooms := slice.ZoomRange{Min: st.CellZoom(), Max: st.CellZoom() + 1}
	tiles := map[slice.TileRef][]byte{}
	for _, c := range cells {
		for ref, data := range pyramid(t, c, st.CellZoom(), zooms) {
			tiles[ref] = data
		}
	}
	r := archive(t, tiles)
	src := addSource(t, st, r, "archive.pmtiles")
	for _, c := range cells {
		if _, err := src.Fill(context.Background(), r, c, zooms); err != nil {
			t.Fatalf("filling cell %s: %v", c, err)
		}
	}
	return st, src
}

// TestEvict_RemovesWholeCellsAndNeverTheOverviewOrTheManifest.
//
// The overview is twelve files per source and it is what makes a partially
// evicted area degrade into a coarser map instead of vanishing. Evicting it to
// save twelve files would turn every eviction into a hole.
func TestEvict_RemovesWholeCellsAndNeverTheOverviewOrTheManifest(t *testing.T) {
	st, src := fillCells(t, testCell, slice.Cell{X: testCell.X + 1, Y: testCell.Y})
	if err := fillAncestors(t, st, src); err != nil {
		t.Fatalf("filling the overview: %v", err)
	}

	ev, err := st.Evict(0)
	if err != nil {
		t.Fatalf("evicting: %v", err)
	}
	if len(ev.Cells) != 2 {
		t.Errorf("eviction removed %d cells, want both", len(ev.Cells))
	}
	if ev.Bytes() <= 0 {
		t.Errorf("eviction reports freeing %d bytes after removing two cells", ev.Bytes())
	}

	cells, err := src.Cells()
	if err != nil {
		t.Fatalf("listing cells: %v", err)
	}
	if len(cells) != 0 {
		t.Errorf("the source still holds cells %v after evicting to a budget of zero", cells)
	}

	// Everything that is left is a manifest or an overview tile, and the
	// overview is all still there.
	for _, f := range countFiles(t, st.Root()) {
		if f == "store.json" || strings.HasSuffix(f, "/manifest.json") {
			continue
		}
		if !strings.Contains(f, "/overview/") {
			t.Errorf("eviction left %s, which is neither a manifest nor an overview tile", f)
		}
	}
	refs, err := slice.AncestorTiles(testCell, st.CellZoom(), slice.ZoomRange{Min: 0, Max: st.CellZoom() - 1})
	if err != nil {
		t.Fatalf("listing the ancestors: %v", err)
	}
	if len(refs) != int(st.CellZoom()) {
		t.Fatalf("a cell at zoom %d has %d ancestors, want %d", st.CellZoom(), len(refs), st.CellZoom())
	}
	for _, ref := range refs {
		if _, ok, err := src.Tile(ref.Z, ref.X, ref.Y); err != nil || !ok {
			t.Errorf("overview tile %s reads back ok=%v (err %v) after an eviction", ref, ok, err)
		}
	}
}

// TestEvict_TakesTheLeastRecentlyUsedCellFirst.
//
// The clock is moved by Hold, once per render, and the three holds below run
// in a known order. The budget is one byte under the store's size, so exactly
// one cell has to go and it must be the one used longest ago.
func TestEvict_TakesTheLeastRecentlyUsedCellFirst(t *testing.T) {
	oldest := testCell
	middle := slice.Cell{X: testCell.X + 1, Y: testCell.Y}
	newest := slice.Cell{X: testCell.X + 2, Y: testCell.Y}
	st, src := fillCells(t, newest, middle, oldest)

	for _, c := range []slice.Cell{oldest, middle, newest} {
		h := src.Hold([]slice.Cell{c})
		if err := h.TouchError(); err != nil {
			t.Fatalf("holding cell %s: %v", c, err)
		}
		h.Release()
	}

	total, err := st.Bytes()
	if err != nil {
		t.Fatalf("measuring the store: %v", err)
	}
	ev, err := st.Evict(total - 1)
	if err != nil {
		t.Fatalf("evicting: %v", err)
	}
	if len(ev.Cells) != 1 {
		t.Fatalf("eviction removed %d cells to free one byte, want 1: %+v", len(ev.Cells), ev.Cells)
	}
	if ev.Cells[0].Cell != oldest {
		t.Errorf("eviction removed cell %s; the least recently used one is %s", ev.Cells[0].Cell, oldest)
	}
	left, err := src.Cells()
	if err != nil {
		t.Fatalf("listing cells: %v", err)
	}
	if len(left) != 2 {
		t.Errorf("the source holds %d cells after one eviction, want 2", len(left))
	}
}

// TestEvict_SkipsACellARenderIsHolding.
//
// Evicting a cell a running render is reading produces a hole in the map that
// appears on some frames and not others: not reproducible from the activity,
// moving when the budget moves, and looking like a rasterizer fault. The
// budget here is zero, so every cell is a candidate and the only thing saving
// the held one is the hold.
func TestEvict_SkipsACellARenderIsHolding(t *testing.T) {
	held := testCell
	other := slice.Cell{X: testCell.X + 1, Y: testCell.Y}
	st, src := fillCells(t, held, other)

	h := src.Hold([]slice.Cell{held})
	ev, err := st.Evict(0)
	if err != nil {
		t.Fatalf("evicting while a render holds a cell: %v", err)
	}
	if ev.Held != 1 {
		t.Errorf("eviction reports %d held cells, want 1", ev.Held)
	}
	if len(ev.Cells) != 1 || ev.Cells[0].Cell != other {
		t.Errorf("eviction removed %+v; it should have taken only %s", ev.Cells, other)
	}
	if _, complete, err := src.Cell(held); err != nil || !complete {
		t.Errorf("the held cell %s is complete=%v (err %v) after an eviction", held, complete, err)
	}

	// Once the render is done the cell is an ordinary candidate again.
	h.Release()
	h.Release() // idempotent; a deferred release beside an explicit one is ordinary
	ev, err = st.Evict(0)
	if err != nil {
		t.Fatalf("evicting after the render finished: %v", err)
	}
	if len(ev.Cells) != 1 || ev.Cells[0].Cell != held {
		t.Errorf("after the hold was released the eviction removed %+v, want %s", ev.Cells, held)
	}
	if ev.Held != 0 {
		t.Errorf("eviction still reports %d held cells after every hold was released", ev.Held)
	}
}

// TestEvict_AStoreUnderItsBudgetIsLeftAlone. Nothing is ever removed except to
// make room, because the data was deliberately acquired and there is no
// expiry on it -- expiring a slice breaks a render on a plane, which is the
// case it exists for.
func TestEvict_AStoreUnderItsBudgetIsLeftAlone(t *testing.T) {
	st, src := fillCells(t, testCell)
	before := countFiles(t, st.Root())

	ev, err := st.Evict(slice.DefaultBudget)
	if err != nil {
		t.Fatalf("evicting: %v", err)
	}
	if len(ev.Cells) != 0 || ev.Bytes() != 0 {
		t.Errorf("eviction removed %d cells and %d bytes from a store well under budget", len(ev.Cells), ev.Bytes())
	}
	if ev.Before != ev.After {
		t.Errorf("eviction reports %d bytes before and %d after having removed nothing", ev.Before, ev.After)
	}
	if got := countFiles(t, st.Root()); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("the store's files changed: before %v, after %v", before, got)
	}
	if _, complete, err := src.Cell(testCell); err != nil || !complete {
		t.Errorf("cell %s is complete=%v (err %v) after an eviction that removed nothing", testCell, complete, err)
	}
}

// ---------------------------------------------------------------------------
// A global shallow slice

// TestCoverage_AGlobalShallowSliceIsHeldEntirelyInTheOverview.
//
// A flight does not fit the cell model and does not need to: every tile on
// earth from zoom 0 to 3 is 4^0 + 4^1 + 4^2 + 4^3 = 85 tiles, all of them
// shallower than the cell zoom, so they are overview and are never evicted.
// The cell grid is the right structure for a run and pointless for this, which
// is why the depth is an input rather than a constant.
func TestCoverage_AGlobalShallowSliceIsHeldEntirelyInTheOverview(t *testing.T) {
	const worldMax = 3
	st := newStore(t, slice.Config{})
	refs, err := slice.WorldTiles(slice.ZoomRange{Min: 0, Max: worldMax})
	if err != nil {
		t.Fatalf("listing the world: %v", err)
	}
	if len(refs) != 85 {
		t.Fatalf("zooms 0 to %d are %d tiles, want 85", worldMax, len(refs))
	}
	tiles := make(map[slice.TileRef][]byte, len(refs))
	for _, r := range refs {
		tiles[r] = []byte("world " + r.String())
	}
	a := archive(t, tiles)
	src := addSource(t, st, a, "planet.pmtiles")
	f, err := src.FillOverview(context.Background(), a, refs)
	if err != nil {
		t.Fatalf("filling the world: %v", err)
	}
	if f.Written != 85 {
		t.Errorf("the fill wrote %d tiles, want 85", f.Written)
	}
	if got := src.Manifest().Zoom; got != (slice.ZoomRange{Min: 0, Max: worldMax}) {
		t.Errorf("the manifest records zooms %s, want 0-%d", got, worldMax)
	}

	cov, err := src.Coverage(cellBounds(t, testCell, st.CellZoom()))
	if err != nil {
		t.Fatalf("measuring coverage: %v", err)
	}
	// The cell grid honestly reports nothing at street level, because there is
	// nothing: the answer this slice has is in the overview beside it.
	if cov.Cells != 1 || cov.Complete != 0 || len(cov.Missing) != 1 {
		t.Errorf("coverage counts %d cells, %d complete, %d missing; a global shallow slice holds no cell at all", cov.Cells, cov.Complete, len(cov.Missing))
	}
	if cov.Fraction() != 0 {
		t.Errorf("coverage fraction is %v for a slice with no cells", cov.Fraction())
	}
	if cov.OverviewWanted != worldMax+1 || cov.OverviewHeld != worldMax+1 {
		t.Errorf("coverage says %d of %d overview tiles; a global slice from zoom 0 to %d holds one per zoom above any cell", cov.OverviewHeld, cov.OverviewWanted, worldMax)
	}

	before := countFiles(t, st.Root())
	ev, err := st.Evict(0)
	if err != nil {
		t.Fatalf("evicting: %v", err)
	}
	if len(ev.Cells) != 0 {
		t.Errorf("eviction removed %d cells from a store that has none", len(ev.Cells))
	}
	if got := countFiles(t, st.Root()); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("evicting to a budget of zero removed overview tiles: before %v, after %v", before, got)
	}
}

// ---------------------------------------------------------------------------
// Sources

// TestAddSource_RefusesWhatItCouldNotServeLater. Every one of these fails at
// the moment the source is described, which is before anything is downloaded.
func TestAddSource_RefusesWhatItCouldNotServeLater(t *testing.T) {
	good := slice.SourceDesc{
		Source:          "archive.pmtiles",
		TileCompression: slice.CompressionGzip,
		SourceZoom:      slice.ZoomRange{Min: 0, Max: 15},
	}
	for _, tc := range []struct {
		name string
		desc func(slice.SourceDesc) slice.SourceDesc
	}{
		{
			name: "no name, so the store could not say what it holds",
			desc: func(d slice.SourceDesc) slice.SourceDesc { d.Source = ""; return d },
		},
		{
			name: "no compression, and tiles are stored exactly as fetched",
			desc: func(d slice.SourceDesc) slice.SourceDesc { d.TileCompression = ""; return d },
		},
		{
			name: "a compression this store cannot decode",
			desc: func(d slice.SourceDesc) slice.SourceDesc { d.TileCompression = "brotli"; return d },
		},
		{
			name: "a zoom range that runs backwards",
			desc: func(d slice.SourceDesc) slice.SourceDesc {
				d.SourceZoom = slice.ZoomRange{Min: 12, Max: 4}
				return d
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t, slice.Config{})
			if _, err := st.AddSource(tc.desc(good)); err == nil {
				t.Fatal("the source was accepted")
			}
			if got, err := st.Sources(); err != nil || len(got) != 0 {
				t.Errorf("the store lists %v (err %v) after a refused source", got, err)
			}
		})
	}
}

// TestAddSource_TheSameSourceTwiceIsOneDirectory. A source is identified by
// where its bytes came from, so describing it again is opening it, not
// duplicating it -- which is what makes a second fetch over the same archive
// add cells rather than a second copy of everything.
func TestAddSource_TheSameSourceTwiceIsOneDirectory(t *testing.T) {
	st, src, _ := filled(t)
	again, err := st.AddSource(slice.SourceDesc{
		Source:          src.Manifest().Source,
		Build:           src.Manifest().Build,
		Attribution:     "a newer credit",
		TileCompression: src.Manifest().TileCompression,
		SourceZoom:      src.Manifest().SourceZoom,
	})
	if err != nil {
		t.Fatalf("adding the same source again: %v", err)
	}
	if again.ID() != src.ID() {
		t.Errorf("the same source got IDs %s and %s", src.ID(), again.ID())
	}
	if again != src {
		t.Error("the same source in one store gave two Source values, which would be two manifests over one directory")
	}
	if got := again.Manifest().Zoom; got != testZooms {
		t.Errorf("re-adding the source reset its held zoom range to %s, want %s", got, testZooms)
	}
	if got := again.Manifest().Attribution; got != "a newer credit" {
		t.Errorf("re-adding the source left the attribution at %q; the newer reading of a fact about the archive is the better one", got)
	}
	sources, err := st.Sources()
	if err != nil || len(sources) != 1 {
		t.Errorf("the store lists %d sources (err %v), want 1", len(sources), err)
	}
}

// TestSource_IsWhatTheRendererAndTheArchiveReaderAlreadyExpect.
//
// Both of these are compile-time claims and both are load bearing. The store
// satisfies the renderer's TileSource with no adapter, so the renderer reads
// from a slice without a line of it changing; and *pmtiles.Reader satisfies
// this package's Archive with no adapter, so filling a store from an archive
// needs nothing in between. The shapes came from the question rather than from
// each other, which is why this is worth checking rather than assuming.
func TestSource_IsWhatTheRendererAndTheArchiveReaderAlreadyExpect(t *testing.T) {
	var _ render.TileSource = (*slice.Source)(nil)
	var _ slice.Archive = (*pmtiles.Reader)(nil)
}

// ---------------------------------------------------------------------------
// The grid

// TestCellsFor_RoundsOutwardAndStopsAtTheEdgeItLandsOn.
//
// The expected counts are worked out from the grid rather than from the code:
// a rectangle inside one cell touches one, a rectangle spanning two cells in
// each direction touches four, and a rectangle whose eastern edge lies exactly
// on a cell boundary touches only the cells west of it, because the cell
// beyond covers none of it.
func TestCellsFor_RoundsOutwardAndStopsAtTheEdgeItLandsOn(t *testing.T) {
	st := newStore(t, slice.Config{})
	z := st.CellZoom()
	west, south, east, north, err := mercator.TileBounds(z, testCell.X, testCell.Y)
	if err != nil {
		t.Fatalf("bounds of cell %s: %v", testCell, err)
	}
	w, h := east-west, north-south

	for _, tc := range []struct {
		name string
		b    slice.Bounds
		want int
	}{
		{
			name: "wholly inside one cell",
			b:    slice.Bounds{West: west + w/4, South: south + h/4, East: east - w/4, North: north - h/4},
			want: 1,
		},
		{
			name: "exactly one cell, edges on the boundaries",
			b:    slice.Bounds{West: west, South: south, East: east, North: north},
			want: 1,
		},
		{
			name: "spanning one boundary each way, so a two by two block",
			b:    slice.Bounds{West: west + w/2, South: south - h/2, East: east + w/2, North: north - h/2},
			want: 4,
		},
		{
			name: "a hair outside every edge, so a three by three block",
			b:    slice.Bounds{West: west - w/100, South: south - h/100, East: east + w/100, North: north + h/100},
			want: 9,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cells, err := st.CellsFor(tc.b)
			if err != nil {
				t.Fatalf("cells for %+v: %v", tc.b, err)
			}
			if len(cells) != tc.want {
				t.Errorf("got %d cells %v, want %d", len(cells), cells, tc.want)
			}
		})
	}
}

// TestPyramidTiles_IsEveryDescendantOfTheCellShallowestFirst.
//
// The order is checked as well as the contents because the fetch planner is a
// separate step that has to agree with the store about what a cell consists
// of, down to the order; two implementations of this list is how a fetch comes
// to write tiles the store never looks for.
func TestPyramidTiles_IsEveryDescendantOfTheCellShallowestFirst(t *testing.T) {
	const cellZoom = 12
	c := slice.Cell{X: 5, Y: 9}
	refs, err := slice.PyramidTiles(c, cellZoom, slice.ZoomRange{Min: cellZoom, Max: cellZoom + 2})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(refs) != 21 {
		t.Fatalf("a three-level cell is 1 + 4 + 16 = 21 tiles, got %d", len(refs))
	}
	if refs[0] != (slice.TileRef{Z: cellZoom, X: c.X, Y: c.Y}) {
		t.Errorf("the first tile is %s, want the cell itself", refs[0])
	}
	for i, r := range refs {
		if i > 0 && r.Z < refs[i-1].Z {
			t.Fatalf("tile %d is at zoom %d after zoom %d; the list runs shallowest first", i, r.Z, refs[i-1].Z)
		}
		shift := r.Z - cellZoom
		if r.X>>shift != c.X || r.Y>>shift != c.Y {
			t.Errorf("tile %s is not inside cell %s", r, c)
		}
	}

	// A range starting above the cell zoom yields only what belongs to the
	// cell; the shallower half is the shared overview.
	refs, err = slice.PyramidTiles(c, cellZoom, slice.ZoomRange{Min: 0, Max: cellZoom})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(refs) != 1 || refs[0].Z != cellZoom {
		t.Errorf("a range from zoom 0 gave %v; only the cell's own zoom and deeper belong to it", refs)
	}
}

// TestAncestorTiles_IsOneTilePerZoomAboveTheCell. Twelve files for a default
// store, shared by every cell beneath them, which is what makes keeping them
// out of the cells worth the separate directory.
func TestAncestorTiles_IsOneTilePerZoomAboveTheCell(t *testing.T) {
	const cellZoom = 12
	c := slice.Cell{X: 2048, Y: 1362}
	refs, err := slice.AncestorTiles(c, cellZoom, slice.ZoomRange{Min: 0, Max: 15})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(refs) != cellZoom {
		t.Fatalf("a cell at zoom %d has %d ancestors, want %d", cellZoom, len(refs), cellZoom)
	}
	for i, r := range refs {
		if int(r.Z) != i {
			t.Errorf("ancestor %d is at zoom %d", i, r.Z)
		}
		shift := cellZoom - r.Z
		if c.X>>shift != r.X || c.Y>>shift != r.Y {
			t.Errorf("tile %s is not the ancestor of cell %s at zoom %d", r, c, r.Z)
		}
	}
}
