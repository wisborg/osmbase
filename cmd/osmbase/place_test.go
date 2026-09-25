package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
)

// nePlace is one Natural Earth feature: properties, and parts as squares of
// [lat, lon, side].
type nePlace struct {
	props map[string]string
	parts [][3]float64
}

func nePlaces(t *testing.T, ps ...nePlace) []byte {
	t.Helper()
	type feature struct {
		Properties map[string]string `json:"properties"`
		Geometry   struct {
			Type        string          `json:"type"`
			Coordinates [][][][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	doc := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection"}
	for _, p := range ps {
		var f feature
		f.Properties = p.props
		f.Geometry.Type = "MultiPolygon"
		for _, q := range p.parts {
			la, lo, sd := q[0], q[1], q[2]
			f.Geometry.Coordinates = append(f.Geometry.Coordinates, [][][]float64{{
				{lo, la}, {lo + sd, la}, {lo + sd, la + sd}, {lo, la + sd}, {lo, la}}})
		}
		doc.Features = append(doc.Features, f)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// placeStore is a store holding the world at zoom 0 and Natural Earth
// outlines carrying the ambiguities the real files have.
func placeStore(t *testing.T) string {
	t.Helper()
	store := filepath.Join(t.TempDir(), "store")
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	if r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
		t.Fatalf("fetch: exit %d\n%s", r.code, r.stderr)
	}
	dir := boundary.Dir(store)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[boundary.Layer][]byte{
		boundary.Countries: nePlaces(t,
			nePlace{map[string]string{"NAME": "Luxembourg", "ISO_A2": "LU"}, [][3]float64{{49.4, 5.7, 0.7}}},
			nePlace{map[string]string{"NAME": "Atlantis", "ISO_A2": "AT"}, [][3]float64{{10, 10, 20}, {12, 31, 2}, {-50, -150, 3}}},
		),
		boundary.Regions: nePlaces(t,
			nePlace{map[string]string{"name": "Luxembourg", "admin": "Luxembourg", "iso_a2": "LU", "type_en": "District"}, [][3]float64{{49.5, 5.8, 0.4}}},
			nePlace{map[string]string{"name": "Luxembourg", "admin": "Belgium", "iso_a2": "BE", "type_en": "Province"}, [][3]float64{{49.5, 5.0, 0.6}}},
			nePlace{map[string]string{"name": "Newcastle upon Tyne", "admin": "United Kingdom", "iso_a2": "GB", "type_en": "Metropolitan Borough"}, [][3]float64{{54.9, -1.8, 0.2}}},
		),
		boundary.Waters: nePlaces(t, nePlace{map[string]string{"name": "Sea"}, [][3]float64{{0, 0, 1}}}),
	}
	for layer, body := range files {
		if err := os.WriteFile(filepath.Join(dir, boundary.File(boundary.DefaultDetail, layer)), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestRenderPlaceDrawsTheOnePlaceANameMeans(t *testing.T) {
	store := placeStore(t)
	out := filepath.Join(t.TempDir(), "atlantis.png")
	r := runCLI(t, "render", "--store", store, "--place", "Atlantis", "--width", "512", "--height", "512", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	line, ok := lineContaining(r.stdout, "fitted")
	if !ok || !strings.Contains(line, "Atlantis (country)") || !strings.Contains(line, "at zoom") {
		t.Errorf("the report does not say what was drawn:\n%s", r.stdout)
	}
	// The part an ocean away is left out, and the report says so -- a map
	// of a country that silently dropped some of it would claim otherwise.
	if !strings.Contains(line, "2 of its 3 parts") {
		t.Errorf("the report does not say a part was left out:\n%s", line)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("no image was written: %v", err)
	}
}

// Several places is a refusal that lists them all and says how to choose --
// and the suggestion it makes has to work.
func TestRenderPlaceRefusesAnAmbiguousNameAndSaysHowToChoose(t *testing.T) {
	store := placeStore(t)
	out := filepath.Join(t.TempDir(), "lux.png")
	r := runCLI(t, "render", "--store", store, "--place", "Luxembourg", "--out", out)
	if r.code != 2 {
		t.Fatalf("exit %d, want 2\n%s", r.code, r.stderr)
	}
	for _, want := range []string{
		"is 3 places", "Luxembourg (country)", "Luxembourg (district, Luxembourg)", "Luxembourg (province, Belgium)",
		`--place "Luxembourg, Luxembourg"`, "--place-level country",
	} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, r.stderr)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("an image was written for a name that was refused")
	}

	for _, args := range [][]string{
		{"--place", "Luxembourg, Luxembourg"},
		{"--place", "Luxembourg", "--place-level", "country"},
		{"--place", "Luxembourg, BE"},
	} {
		r := runCLI(t, append([]string{"render", "--store", store, "--out", out, "--width", "256", "--height", "256"}, args...)...)
		if r.code != 0 {
			t.Errorf("%v: exit %d\n%s", args, r.code, r.stderr)
		}
	}
}

// A name that only begins a place's name is offered, and nothing is drawn.
func TestRenderPlaceOffersAPartialMatchAndDrawsNothing(t *testing.T) {
	store := placeStore(t)
	out := filepath.Join(t.TempDir(), "nc.png")
	r := runCLI(t, "render", "--store", store, "--place", "Newcastle", "--out", out)
	if r.code != 2 || !strings.Contains(r.stderr, "did you mean") || !strings.Contains(r.stderr, "Newcastle upon Tyne") {
		t.Errorf("exit %d, want a refusal offering Newcastle upon Tyne:\n%s", r.code, r.stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("an image was written for a name that matched nothing exactly")
	}
}

func TestRenderPlaceRefusals(t *testing.T) {
	store := placeStore(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--place", "Nowhere At All"}, "is named"},
		{[]string{"--place", "Atlantis", "--bbox", "0,0,1,1"}, "give one"},
		{[]string{"--place", "Atlantis", "--lat", "1", "--lon", "1"}, "give one"},
		{[]string{"--place-level", "country", "--lat", "1", "--lon", "1"}, "--place-level narrows --place"},
		{[]string{"--place", "Atlantis", "--place-level", "city"}, "--place-level"},
	} {
		r := runCLI(t, append([]string{"render", "--store", store, "--out", filepath.Join(t.TempDir(), "x.png")}, tc.args...)...)
		if r.code != 2 || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%v: exit %d, want 2 saying %q:\n%s", tc.args, r.code, tc.want, r.stderr)
		}
	}
}

// A store with no outlines says which command fills them, rather than that
// the name is unknown.
func TestRenderPlaceWithNoOutlinesSaysWhereToGetThem(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	if r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", store, "--yes"); r.code != 0 {
		t.Fatalf("fetch: %s", r.stderr)
	}
	r := runCLI(t, "render", "--store", store, "--place", "Atlantis", "--out", filepath.Join(t.TempDir(), "x.png"))
	if r.code == 0 || !strings.Contains(r.stderr, "osmbase boundaries") {
		t.Errorf("exit %d, want the boundaries command named:\n%s", r.code, r.stderr)
	}
}

// fetch takes the same flag and fetches the place's extent -- the main part,
// not the rectangle around every part.
func TestFetchPlacePlansThePlacesExtent(t *testing.T) {
	store := placeStore(t)
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	// --max-zoom, because an area this size is otherwise in the planner's
	// "whole world at flight detail" band -- the same as --bbox would be.
	r := runCLI(t, "fetch", archive, "--place", "Atlantis", "--max-zoom", "0", "--store", store, "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if line, ok := lineContaining(r.stdout, "place"); !ok || !strings.Contains(line, "Atlantis (country)") {
		t.Errorf("the plan does not name the place:\n%s", r.stdout)
	}
	if line, ok := lineContaining(r.stdout, "area"); !ok || !strings.Contains(line, "west 10.0000, south 10.0000, east 33.0000, north 30.0000") {
		t.Errorf("the area is not the main part and its neighbour:\n%s", r.stdout)
	}
}
