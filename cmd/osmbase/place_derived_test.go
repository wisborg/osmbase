package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
)

// withDerived adds a derived boundary file to a store.
func withDerived(t *testing.T, store, region string, areas ...boundary.Area) {
	t.Helper()
	dir := boundary.Dir(store)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := boundary.DerivedFile(region)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prov := boundary.Provenance{Source: "x", Attribution: "© OpenStreetMap contributors, ODbL"}
	if err := boundary.WriteDerived(f, boundary.NewSet(prov, areas)); err != nil {
		t.Fatal(err)
	}
}

func sq(lat, lon, side float64) []boundary.Polygon {
	return []boundary.Polygon{{Outer: boundary.Ring{
		{Lat: lat, Lon: lon}, {Lat: lat, Lon: lon + side}, {Lat: lat + side, Lon: lon + side}, {Lat: lat + side, Lon: lon}}}}
}

// Two Newcastles from two derived files: refused with both listed and placed,
// and the suggested qualifier is one that works when followed.
func TestRenderPlaceFindsDerivedAreasAndSaysWhichIsWhich(t *testing.T) {
	store := placeStore(t)
	withDerived(t, store, "atlantis",
		boundary.NewArea("Newcastle Council", "6", sq(15, 15, 2)),
		boundary.NewArea("Newcastle", "9", sq(15.5, 15.5, 0.5)))
	withDerived(t, store, "england",
		boundary.NewArea("Newcastle", "8", sq(54.95, -1.75, 0.1)))

	out := filepath.Join(t.TempDir(), "nc.png")
	r := runCLI(t, "render", "--store", store, "--place", "Newcastle", "--out", out)
	if r.code != 2 {
		t.Fatalf("exit %d, want 2\n%s", r.code, r.stderr)
	}
	for _, want := range []string{
		"Newcastle (admin level 9, Newcastle Council, Atlantis)",
		"Newcastle (admin level 8, Newcastle upon Tyne, United Kingdom)",
	} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the refusal does not list %q:\n%s", want, r.stderr)
		}
	}
	hint := regexp.MustCompile(`--place "([^"]+)"`).FindStringSubmatch(r.stderr)
	if hint == nil {
		t.Fatalf("no qualifier was suggested:\n%s", r.stderr)
	}
	if r := runCLI(t, "render", "--store", store, "--place", hint[1], "--width", "256", "--height", "256", "--out", out); r.code != 0 {
		t.Errorf("the suggestion %q does not work: exit %d\n%s", hint[1], r.code, r.stderr)
	}
	for _, q := range []string{"Newcastle, Atlantis", "Newcastle, United Kingdom", "Newcastle, GB"} {
		if r := runCLI(t, "render", "--store", store, "--place", q, "--width", "256", "--height", "256", "--out", out); r.code != 0 {
			t.Errorf("%q: exit %d\n%s", q, r.code, r.stderr)
		}
	}
}

// --place-level local asks the derived files only.
func TestRenderPlaceLevelLocal(t *testing.T) {
	store := placeStore(t)
	withDerived(t, store, "atlantis", boundary.NewArea("Luxembourg Street", "10", sq(15, 15, 0.1)),
		boundary.NewArea("Harbour", "9", sq(16, 16, 0.2)))
	out := filepath.Join(t.TempDir(), "x.png")
	r := runCLI(t, "render", "--store", store, "--place", "Harbour", "--place-level", "local", "--width", "256", "--height", "256", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if line, _ := lineContaining(r.stdout, "fitted"); !strings.Contains(line, "Harbour (admin level 9, Atlantis)") {
		t.Errorf("fitted line: %q", line)
	}
	// And local leaves Natural Earth's places out: Luxembourg is three
	// places there and none of them here.
	r = runCLI(t, "render", "--store", store, "--place", "Luxembourg", "--place-level", "local", "--out", out)
	if r.code != 2 || strings.Contains(r.stderr, "(country)") {
		t.Errorf("--place-level local reached Natural Earth:\n%s", r.stderr)
	}
}

// A store holding only a derived file -- "boundaries --osm" and nothing else
// -- can still be searched.
func TestRenderPlaceFromAStoreOfDerivedFilesOnly(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	if r := runCLI(t, "fetch", fixtureArchive(t, 0, 0, 0, worldTile()), "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
		t.Fatalf("fetch: %s", r.stderr)
	}
	withDerived(t, store, "somewhere", boundary.NewArea("Hornsby", "9", sq(10, 10, 5)))
	out := filepath.Join(t.TempDir(), "x.png")
	r := runCLI(t, "render", "--store", store, "--place", "Hornsby", "--width", "256", "--height", "256", "--out", out)
	if r.code != 0 {
		t.Errorf("exit %d\n%s", r.code, r.stderr)
	}
}
