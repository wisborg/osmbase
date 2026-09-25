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

// deepArchive is a local archive holding every tile from zoom 0 to maxZoom,
// each the world fixture, crediting whoever it is told to.
func deepArchive(t *testing.T, maxZoom uint8, credit string) string {
	t.Helper()
	data, err := osmbasetest.BuildTile(worldTile())
	if err != nil {
		t.Fatal(err)
	}
	var tiles []osmbasetest.ArchiveTile
	for z := uint8(0); z <= maxZoom; z++ {
		n := uint32(1) << z
		for y := uint32(0); y < n; y++ {
			for x := uint32(0); x < n; x++ {
				id, err := pmtiles.ZxyToID(z, x, y)
				if err != nil {
					t.Fatal(err)
				}
				tiles = append(tiles, osmbasetest.ArchiveTile{ID: id, Data: data})
			}
		}
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:    tiles,
		Metadata: []byte(`{"attribution":` + quoteJSON(credit) + `}`),
		TileType: pmtiles.TileTypeMVT, MinZoom: 0, MaxZoom: maxZoom,
		MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "deep.pmtiles")
	if err := os.WriteFile(path, built.Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quoteJSON(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// shallowStore is a store filled from a zoom-2 archive, but only to zoom 0.
func shallowStore(t *testing.T) string {
	t.Helper()
	store := filepath.Join(t.TempDir(), "store")
	if r := runCLI(t, "fetch", deepArchive(t, 2, "Deep Credit"), "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
		t.Fatalf("fetch: exit %d\n%s", r.code, r.stderr)
	}
	return store
}

func renderZoom2(t *testing.T, store string, extra ...string) result {
	t.Helper()
	out := filepath.Join(t.TempDir(), "map.png")
	r := runCLI(t, append([]string{"render", "--store", store, "--lat", "20", "--lon", "20", "--zoom", "2",
		"--width", "256", "--height", "256", "--out", out}, extra...)...)
	if r.code != 0 {
		t.Fatalf("render %v: exit %d\n%s", extra, r.code, r.stderr)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("render %v wrote no image", extra)
	}
	return r
}

// heldAtZoom2 is how much of the test view the store holds.
func heldAtZoom2(t *testing.T, store string) (int, int) {
	t.Helper()
	st, err := slice.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := st.Sources()
	src, err := st.Source(ms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := viewAround(2, 20, 20, 256, 256)
	if err != nil {
		t.Fatal(err)
	}
	held, wanted, err := src.HeldAt(slice.Bounds{West: v.Bounds.West, South: v.Bounds.South, East: v.Bounds.East, North: v.Bounds.North}, 2)
	if err != nil {
		t.Fatal(err)
	}
	return held, wanted
}

// Nobody at a terminal: the offer is made, nothing is read from stdin --
// which may be a pipe that never closes -- nothing is fetched, and the map is
// still drawn, from what the store holds.
func TestRenderOffersButDoesNotWaitWhenNobodyCanAnswer(t *testing.T) {
	saved := stdinAnswerable
	stdinAnswerable = func() bool { return false }
	t.Cleanup(func() { stdinAnswerable = saved })
	store := shallowStore(t)
	r := renderZoom2(t, store)
	for _, want := range []string{"needs", "at zoom 2", "holds 0 of them", "nothing is attached to answer", "--yes"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the notice does not say %q:\n%s", want, r.stderr)
		}
	}
	if held, _ := heldAtZoom2(t, store); held != 0 {
		t.Errorf("%d tiles were fetched without anybody agreeing", held)
	}
}

// --yes answers in advance: the view is fetched, then drawn at its own zoom.
func TestRenderYesFetchesWhatTheViewLacks(t *testing.T) {
	store := shallowStore(t)
	r := renderZoom2(t, store, "--yes")
	if !strings.Contains(r.stderr, "fetched") {
		t.Errorf("nothing says a fetch happened:\n%s", r.stderr)
	}
	if held, wanted := heldAtZoom2(t, store); held != wanted {
		t.Errorf("after --yes the store holds %d of %d tiles", held, wanted)
	}
	if line, _ := lineContaining(r.stdout, "overzoomed"); !strings.Contains(line, "0.0%") {
		t.Errorf("the render was still overzoomed after the fetch: %s", line)
	}
	// And the next render has nothing to offer.
	if again := renderZoom2(t, store); strings.Contains(again.stderr, "needs") {
		t.Errorf("a view the store now holds was offered again:\n%s", again.stderr)
	}
}

// At a terminal the question is asked, and only a yes is a yes.
func TestRenderAsksAtATerminal(t *testing.T) {
	saved := stdinAnswerable
	stdinAnswerable = func() bool { return true }
	t.Cleanup(func() { stdinAnswerable = saved })

	for _, tc := range []struct {
		answer  string
		fetched bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false}, // the stream ended
	} {
		store := shallowStore(t)
		stdinSaying(t, tc.answer)
		r := renderZoom2(t, store)
		if !strings.Contains(r.stderr, "Fetch it now? [y/N]") {
			t.Errorf("%q: the question was not asked:\n%s", tc.answer, r.stderr)
		}
		if held, wanted := heldAtZoom2(t, store); (held == wanted) != tc.fetched {
			t.Errorf("%q: store holds %d of %d, fetched should be %v", tc.answer, held, wanted, tc.fetched)
		}
	}
}

// With no store at all, the offer would be from the default archive over the
// network -- so declining it must not have touched the network, and the old
// refusal stands. runCLI fails the test on any network notice.
func TestRenderWithNoStoreOffersAndDeclinesWithoutTheNetwork(t *testing.T) {
	out := filepath.Join(t.TempDir(), "map.png")
	r := runCLI(t, "render", "--store", filepath.Join(t.TempDir(), "none"), "--lat", "1", "--lon", "1", "--out", out)
	if r.code == 0 {
		t.Fatal("a render from no store at all succeeded")
	}
	if !strings.Contains(r.stderr, "holds no map yet") || !strings.Contains(r.stderr, "data.source.coop") {
		t.Errorf("the offer does not say what it would contact:\n%s", r.stderr)
	}
}

// A store holding two archives credits the one drawn from. It credited the
// first one listed, whichever --archive chose.
func TestRenderFromStoreCreditsTheArchiveItDrewFrom(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	for _, credit := range []string{"First Credit", "Second Credit"} {
		if r := runCLI(t, "fetch", deepArchive(t, 0, credit), "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
			t.Fatalf("fetch: %s", r.stderr)
		}
	}
	st, _ := slice.Open(store)
	ms, _ := st.Sources()
	for _, m := range ms {
		out := filepath.Join(t.TempDir(), "map.png")
		r := runCLI(t, "render", "--store", store, "--archive", m.ID, "--lat", "1", "--lon", "1", "--zoom", "0",
			"--width", "128", "--height", "128", "--out", out)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		if line, _ := lineContaining(r.stdout, "credit"); !strings.Contains(line, m.Attribution) {
			t.Errorf("drew from %s and credited %q", m.Attribution, line)
		}
	}
}

// A render deeper than the archive goes asks about the archive's deepest
// zoom, not its own: past that, overzoom is the answer and there is nothing
// to fetch. Asked at its own zoom, the store could never hold it, and every
// render would offer the same download for ever.
func TestRenderDeeperThanTheArchiveOffersOnce(t *testing.T) {
	store := shallowStore(t)
	out := filepath.Join(t.TempDir(), "map.png")
	args := []string{"render", "--store", store, "--lat", "20", "--lon", "20", "--zoom", "4",
		"--width", "256", "--height", "256", "--out", out}
	if r := runCLI(t, append(args, "--yes")...); r.code != 0 || !strings.Contains(r.stderr, "at zoom 2") {
		t.Fatalf("exit %d; the offer should be for zoom 2, the archive's deepest:\n%s", r.code, r.stderr)
	}
	if r := runCLI(t, args...); strings.Contains(r.stderr, "needs") {
		t.Errorf("offered again after the archive's deepest zoom was fetched:\n%s", r.stderr)
	}
}
