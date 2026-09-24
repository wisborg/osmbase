package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// mixedStore is a store holding a map of the area around Horsens and a
// derived boundary file for somewhere else entirely -- what a user has after
// fetching a map at home and building suburbs for a trip. Both halves are
// built locally; nothing here reaches a network.
//
// It returns the store and a coordinate beside the one named place in the
// map, as the strings --lat and --lon take.
func mixedStore(t *testing.T) (store, lat, lon string) {
	t.Helper()
	const z = 10
	x, y, err := mercator.TileAt(z, 9.8451, 55.8623)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := mercator.NewTileTransform(z, x, y, mvt.DefaultExtent)
	if err != nil {
		t.Fatal(err)
	}
	// The place at the middle of the tile, and the question a little way
	// from it, both in the tile's own coordinates so neither is a projection
	// worked out by hand.
	qLon, qLat := tr.LonLat(2000, 2000)
	lat = strconv.FormatFloat(qLat, 'f', 6, 64)
	lon = strconv.FormatFloat(qLon, 'f', 6, 64)
	spec := osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "places", Extent: mvt.DefaultExtent,
		Features: []osmbasetest.FeatureSpec{{
			Type: mvt.GeomPoint,
			Tags: []osmbasetest.Tag{
				{Key: "name", Value: mvt.StringValue("Horsens")},
				{Key: "kind", Value: mvt.StringValue("locality")},
			},
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 2048, Y: 2048}}},
		}},
	}}}
	archive := fixtureArchive(t, z, x, y, spec)

	store = filepath.Join(t.TempDir(), "store")
	r := runCLI(t, "fetch", archive, "--lat", lat, "--lon", lon, "--radius", "1",
		"--max-zoom", "10", "--store", store, "--yes")
	if r.code != 0 {
		t.Fatalf("fetch: exit %d\n%s", r.code, r.stderr)
	}

	dir := boundary.Dir(store)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, _ := boundary.DerivedFile("sydney")
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	shire := boundary.NewArea("Hornsby Shire", "6", []boundary.Polygon{{Outer: boundary.Ring{
		{Lat: -34, Lon: 151}, {Lat: -34, Lon: 152}, {Lat: -33, Lon: 152}, {Lat: -33, Lon: 151}}}})
	if err := boundary.WriteDerived(f, boundary.NewSet(boundary.Provenance{Source: "x"}, []boundary.Area{shire})); err != nil {
		t.Fatal(err)
	}
	return store, lat, lon
}

// A level the boundaries cover no longer means the tiles go unopened. The
// Sydney file covers locality, so no level NEEDS the map -- and before, the
// command therefore never opened it, and Horsens was answered with nothing.
func TestTheMapIsOpenedForPlacesTheBoundariesDoNotKnow(t *testing.T) {
	store, lat, lon := mixedStore(t)

	out, err := locateSaying(t, "--store", store, "--lat", lat, "--lon", lon, "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	line, ok := lineContaining(out, "Horsens")
	if !ok || !strings.Contains(line, "near") {
		t.Errorf("Horsens was not answered from the map:\n%s", out)
	}
}

// The map's credit is owed for names taken from the map. Opening it as a
// fallback does not make it owed for a lookup the boundaries answered
// entirely.
func TestTheMapIsCreditedOnlyWhenItNamedSomething(t *testing.T) {
	store, lat, lon := mixedStore(t)
	// The fixture archive carries no credit of its own, and a test looking
	// for the absence of one it never had passes on either side of the fix.
	const credit = "Map Credit For This Test"
	setMapCredit(t, store, credit)

	near, err := locateSaying(t, "--store", store, "--lat", lat, "--lon", lon, "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(near, credit) {
		t.Fatalf("the answer taken from the map printed no credit:\n%s", near)
	}
	contained, err := locateSaying(t, "--store", store, "--lat", "-33.5", "--lon", "151.5", "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(contained, "Hornsby Shire") {
		t.Fatalf("the contained answer is missing:\n%s", contained)
	}
	if strings.Contains(contained, credit) {
		t.Errorf("the map is credited for an answer it did not give:\n%s", contained)
	}
}

// setMapCredit rewrites the credit in the store's one manifest, which is
// where the fill put whatever the archive's metadata said.
func setMapCredit(t *testing.T, store, credit string) {
	t.Helper()
	manifests, err := filepath.Glob(filepath.Join(store, "*", "manifest.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatalf("want one manifest in the store, got %v (%v)", manifests, err)
	}
	var m map[string]any
	body, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	m["attribution"] = credit
	if body, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifests[0], body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The map's credit is printed to the terminal like a name is, and comes out
// of a manifest in the store like a name comes out of a boundary file -- so
// it is filtered the same way. Planted by rewriting the manifest, because the
// fill writes whatever the archive's metadata said.
func TestAPlantedMapCreditCannotForgeOutput(t *testing.T) {
	store, lat, lon := mixedStore(t)
	setMapCredit(t, store, "\x1b[2JCredit\n  country        near Atlantis (0)")

	out, err := locateSaying(t, "--store", store, "--lat", lat, "--lon", lon, "--levels", "locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(out, "Credit") {
		t.Fatalf("the planted credit was not printed at all, so this proves nothing:\n%q", out)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\n  country") {
		t.Errorf("the map's credit reached the terminal unfiltered:\n%q", out)
	}
}
