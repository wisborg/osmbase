package fetch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// writeArchive builds a one-tile PMTiles archive on disk and returns its path.
func writeArchive(t *testing.T, name, attribution string, tileType pmtiles.TileType, minZoom, maxZoom uint8) string {
	t.Helper()
	tile, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "earth",
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
				Exterior: mvt.Ring{{X: 0, Y: 0}, {X: 4096, Y: 0}, {X: 4096, Y: 4096}, {X: 0, Y: 4096}},
			}}},
		}},
	}}})
	if err != nil {
		t.Fatalf("building the fixture tile: %v", err)
	}
	meta := []byte(`{}`)
	if attribution != "" {
		meta = []byte(`{"attribution":` + quote(attribution) + `}`)
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:    []osmbasetest.ArchiveTile{{ID: 0, Data: tile}},
		Metadata: meta,
		TileType: tileType,
		MinZoom:  minZoom, MaxZoom: maxZoom,
	})
	if err != nil {
		t.Fatalf("building the fixture archive: %v", err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatalf("writing the fixture archive: %v", err)
	}
	return path
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// TestOpen_TellsAPathFromAURLAndRefusesNeither covers the dispatch every
// consumer used to spell for itself.
func TestOpen_TellsAPathFromAURLAndRefusesNeither(t *testing.T) {
	path := writeArchive(t, "map.pmtiles", "© OpenStreetMap", pmtiles.TileTypeMVT, 0, 5)

	t.Run("a path opens the file", func(t *testing.T) {
		a, err := fetch.Open(path, fetch.Options{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer a.Close()
		if a.Remote() {
			t.Error("a local file was reported as remote, so a caller would put a privacy prompt in front of reading the user's own disk")
		}
		if n, ok := a.Size(); !ok || n <= 0 {
			t.Errorf("Size() = %d, %v, want the file's own size", n, ok)
		}
		if _, _, ok := a.Traffic(); ok {
			t.Error("a local archive reported network traffic; \"0 requests\" invites a reader to think the number means something")
		}
	})

	t.Run("a scheme that is not http is refused by name", func(t *testing.T) {
		_, err := fetch.Open("ftp://example.test/map.pmtiles", fetch.Options{})
		if err == nil {
			t.Fatal("an ftp URL was accepted")
		}
		if !strings.Contains(err.Error(), "ftp") {
			t.Errorf("the refusal does not name the scheme: %v", err)
		}
	})

	t.Run("a missing file says what a source is", func(t *testing.T) {
		_, err := fetch.Open(filepath.Join(t.TempDir(), "absent.pmtiles"), fetch.Options{})
		if err == nil {
			t.Fatal("a path to nothing was accepted")
		}
		if !strings.Contains(err.Error(), ".pmtiles") {
			t.Errorf("the refusal does not say what to pass instead: %v", err)
		}
	})

	t.Run("a directory is not an archive", func(t *testing.T) {
		if _, err := fetch.Open(t.TempDir(), fetch.Options{}); err == nil {
			t.Fatal("a directory was accepted as an archive")
		}
	})
}

// TestOpen_ShipsNoDefaultArchiveOfItsOwn is the architecture's rule about
// whose host a consumer's traffic lands on, held in code rather than prose.
//
// A library that defaulted to somebody's bucket would put every consumer's
// requests on a host the consumer never chose, and would do it to the
// consumer who wrote the least code. A command may have a default -- it has a
// user in front of it, help text to explain the cost, and a flag to avoid it
// -- and passes it in.
func TestOpen_ShipsNoDefaultArchiveOfItsOwn(t *testing.T) {
	_, err := fetch.Open("", fetch.Options{})
	if err == nil {
		t.Fatal("Open with no source and no default opened something; a library must not choose a host for its consumer")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("the refusal does not explain what is missing: %v", err)
	}
}

// TestOpen_RefusesRasterTilesBeforeTheDecoderGetsThem is about the quality of
// a failure rather than whether one happens.
//
// A protobuf decoder handed a PNG does not announce that it has been handed a
// PNG. It reports something about an undefined field number, several layers
// below the command the user ran, and leaves them looking at the wrong thing
// entirely.
func TestOpen_RefusesRasterTilesBeforeTheDecoderGetsThem(t *testing.T) {
	path := writeArchive(t, "raster.pmtiles", "© Somebody", pmtiles.TileTypePNG, 0, 5)

	if a, err := fetch.Open(path, fetch.Options{}); err != nil {
		t.Errorf("a raster archive was refused without being asked for vector tiles: %v", err)
	} else {
		a.Close()
	}

	_, err := fetch.Open(path, fetch.Options{RequireVectorTiles: true})
	if err == nil {
		t.Fatal("a png archive was accepted for vector tile reading")
	}
	for _, want := range []string{"png", "mvt"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("the refusal does not mention %q, so it does not say what is wrong: %v", want, err)
		}
	}
}

// TestAttribution_SaysWhenThereIsNoneRatherThanReturningEmpty keeps the
// caller's policy decision reachable.
//
// The right response to an uncreditable archive differs by consumer: a render
// that has to discharge an attribution obligation should refuse, and a tool
// reporting on an archive should say so and carry on. Both need to be able to
// TELL, which an empty string does not let them do reliably -- it is also
// what a caller gets from a field it forgot to read.
func TestAttribution_SaysWhenThereIsNoneRatherThanReturningEmpty(t *testing.T) {
	const credit = `<a href="https://example.test/">&copy; OpenStreetMap</a>`
	withCredit := writeArchive(t, "credited.pmtiles", credit, pmtiles.TileTypeMVT, 0, 5)
	without := writeArchive(t, "bare.pmtiles", "", pmtiles.TileTypeMVT, 0, 5)

	a, err := fetch.Open(withCredit, fetch.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()
	got, err := a.Attribution()
	if err != nil {
		t.Fatalf("Attribution: %v", err)
	}
	// RAW, markup and all: a store's manifest records what the source said,
	// so that two programs' stores hold the same bytes. The conversion to
	// readable text belongs at the point of display.
	if got != credit {
		t.Errorf("Attribution() = %q, want the archive's own bytes %q", got, credit)
	}

	b, err := fetch.Open(without, fetch.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()
	if _, err := b.Attribution(); !errors.Is(err, fetch.ErrNoAttribution) {
		t.Errorf("Attribution() of an uncredited archive returned %v, want one matching ErrNoAttribution", err)
	}
}

// TestSourceID_IsTheStoreKeyTwoProgramsHaveToAgreeOn is the property that
// makes a shared store possible, and the reason this package exists at all.
//
// The default store root is the same path for every program built on this
// library. If two of them file the same archive under different IDs, the same
// ground lands on disk twice under two names, and a store that then holds two
// archives is one every consumer has to disambiguate. Agreement held while
// there were two implementations of this, by coincidence rather than by
// construction; it would not have survived a third.
func TestSourceID_IsTheStoreKeyTwoProgramsHaveToAgreeOn(t *testing.T) {
	path := writeArchive(t, "map.pmtiles", "© OpenStreetMap", pmtiles.TileTypeMVT, 0, 5)
	a, err := fetch.Open(path, fetch.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()

	if got, want := a.SourceID(), slice.SourceID(a.Name(), ""); got != want {
		t.Errorf("SourceID() = %s, want slice.SourceID(name, \"\") = %s: a consumer computing it the documented way would disagree with this one", got, want)
	}

	// And the store agrees, which is the half that actually matters: the
	// directory AddTo creates has to be the one SourceID names.
	root := filepath.Join(t.TempDir(), "store")
	st, err := slice.Create(root, slice.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := a.AddTo(st, "© OpenStreetMap"); err != nil {
		t.Fatalf("AddTo: %v", err)
	}
	sources, err := st.Sources()
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 1 || sources[0].ID != a.SourceID() {
		t.Errorf("the store filed it under %v, want the single ID %s", sources, a.SourceID())
	}
}

// writeDeepArchive builds an archive holding enough tiles, over enough zoom
// levels, for a plan against it to be non-trivial. The one-tile fixture above
// cannot show a planning difference: every plan over it is one tile.
func writeDeepArchive(t *testing.T, maxZoom uint8) string {
	t.Helper()
	tile, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "earth",
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPolygon,
			Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
				Exterior: mvt.Ring{{X: 0, Y: 0}, {X: 4096, Y: 0}, {X: 4096, Y: 4096}, {X: 0, Y: 4096}},
			}}},
		}},
	}}})
	if err != nil {
		t.Fatalf("building the fixture tile: %v", err)
	}
	tiles := make([]osmbasetest.ArchiveTile, 0, 400)
	for id := uint64(0); id < 400; id++ {
		tiles = append(tiles, osmbasetest.ArchiveTile{ID: id, Data: tile})
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:    tiles,
		Metadata: []byte(`{"attribution":"\u00a9 OpenStreetMap"}`),
		TileType: pmtiles.TileTypeMVT,
		MinZoom:  0, MaxZoom: maxZoom,
	})
	if err != nil {
		t.Fatalf("building the fixture archive: %v", err)
	}
	path := filepath.Join(t.TempDir(), "deep.pmtiles")
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatalf("writing the fixture archive: %v", err)
	}
	return path
}

// TestPlan_TakesTheSourceZoomFromTheArchiveNotTheCaller closes the gap that
// made this worth centralising.
//
// Every caller used to copy the archive's zoom range out of its header into
// the request. That is not a choice about the fetch, it is a fact about the
// archive, and both ways of getting it wrong are bad in a different way --
// which the two wrong values below were chosen to show, and which were
// confirmed by removing the line this test is about:
//
//   - Overstating the depth (9-14 for an archive holding 0-8) fails, loudly
//     but from three layers down, with a message about cell zoom arithmetic
//     that says nothing about the archive.
//   - Understating it (0-2) does not fail at all. The plan comes back
//     shallower -- three tiles instead of five, at zooms the map will look
//     blurry at -- and nothing anywhere says the archive had more to give.
//
// The assertion is that all three requests produce the SAME plan, because the
// field is overwritten from the header before the planner sees it. A caller
// cannot get this wrong any more, which is the point: it was never theirs to
// get right.
func TestPlan_TakesTheSourceZoomFromTheArchiveNotTheCaller(t *testing.T) {
	const archiveMaxZoom = 8
	path := writeDeepArchive(t, archiveMaxZoom)
	a, err := fetch.Open(path, fetch.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()

	plan := func(sz slice.ZoomRange) *acquire.Plan {
		t.Helper()
		p, err := a.Plan(context.Background(), nil, acquire.Request{
			Bounds:     slice.Bounds{West: 0, South: 0, East: 1, North: 1},
			MaxZoom:    acquire.AutoZoom,
			CellZoom:   6,
			SourceZoom: sz,
		})
		if err != nil {
			t.Fatalf("Plan with SourceZoom %v: %v", sz, err)
		}
		return p
	}

	right := plan(slice.ZoomRange{Min: 0, Max: archiveMaxZoom})
	if right.Tiles == 0 {
		t.Fatal("precondition: the correct request planned no tiles, so a wrong one cannot differ from it")
	}
	for _, wrong := range []slice.ZoomRange{
		{Min: 9, Max: 14}, // deeper than the archive goes
		{Min: 0, Max: 2},  // shallower than the archive goes
	} {
		got := plan(wrong)
		if got.Zoom != right.Zoom || got.Tiles != right.Tiles {
			t.Errorf("SourceZoom %v produced zoom %v over %d tiles; the archive's own %v gives zoom %v over %d, and the caller's value should not have been consulted",
				wrong, got.Zoom, got.Tiles, right.Zoom, right.Zoom, right.Tiles)
		}
	}
}

// TestOpen_MarksABadSchemeAsTheUsersTypingRatherThanAFailure keeps a
// distinction a caller cannot otherwise make.
//
// A command exits 2 when it was used wrongly and 1 when something went wrong,
// and those are different situations for whoever is reading the exit code --
// a script, or a person deciding whether to check their connection or their
// command line. Open cannot make that choice, since it does not know it is
// inside a command, but it does know which of its refusals is certainly about
// what was typed. A caller matching on the message prose instead would break
// the first time the wording improved.
func TestOpen_MarksABadSchemeAsTheUsersTypingRatherThanAFailure(t *testing.T) {
	_, err := fetch.Open("s3://bucket/planet.pmtiles", fetch.Options{})
	if !errors.Is(err, fetch.ErrUnsupportedScheme) {
		t.Fatalf("Open returned %v, want an error matching ErrUnsupportedScheme", err)
	}
	// The sentinel must not swallow the detail: the message still has to name
	// what was wrong and what to pass instead.
	for _, want := range []string{"s3", ".pmtiles"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}

	// A path that simply is not there is the other case, and must NOT be
	// reported as a usage error: the command line was fine, the file was not.
	_, err = fetch.Open(filepath.Join(t.TempDir(), "absent.pmtiles"), fetch.Options{})
	if errors.Is(err, fetch.ErrUnsupportedScheme) {
		t.Errorf("a missing file was reported as a bad scheme: %v", err)
	}
}
