package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
)

// countryStore is Natural Earth's Atlantis, a square of 4 at the origin, with
// a derived file whose national border is a square of 5: the same country
// with a strip of territorial water around it.
func countryStore(t *testing.T, areas ...boundary.Area) string {
	t.Helper()
	root := naturalEarthStore(t)
	name, _ := boundary.DerivedFile("atlantis")
	f, err := os.Create(filepath.Join(boundary.Dir(root), name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prov := boundary.Provenance{Source: "x", Attribution: "© OpenStreetMap contributors, ODbL"}
	if err := boundary.WriteDerived(f, boundary.NewSet(prov, areas)); err != nil {
		t.Fatal(err)
	}
	return root
}

func square5() boundary.Ring {
	return boundary.Ring{{Lat: -0.5, Lon: -0.5}, {Lat: -0.5, Lon: 4.5}, {Lat: 4.5, Lon: 4.5}, {Lat: 4.5, Lon: -0.5}}
}

// The flag end to end, in the strip where the two sources disagree: at sea by
// default, in the country with osm -- and credited to whichever answered.
func TestCountryFromChoosesWhoAnswersTheCountry(t *testing.T) {
	root := countryStore(t, boundary.NewArea("OSM Atlantis", "2", []boundary.Polygon{{Outer: square5()}}))

	type report struct {
		Places []struct {
			Matches []struct {
				Level, Name, Kind, Source, Attribution string
			}
		}
	}
	country := func(args ...string) (name, kind, credit string, found bool) {
		t.Helper()
		out, err := locateSaying(t, append([]string{"--store", root, "--lat", "4.2", "--lon", "4.2",
			"--levels", "country", "--format", "json"}, args...)...)
		if err != nil {
			t.Fatalf("locate %v: %v", args, err)
		}
		var r report
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("parsing: %v\n%s", err, out)
		}
		for _, m := range r.Places[0].Matches {
			if m.Level == "country" {
				return m.Name, m.Kind, m.Attribution, true
			}
		}
		return "", "", "", false
	}

	if name, _, _, found := country(); found {
		t.Errorf("by default a point off the coast is in %q, want at sea", name)
	}
	if name, _, _, found := country("--country-from", "natural-earth"); found {
		t.Errorf("with natural-earth a point off the coast is in %q, want at sea", name)
	}
	name, kind, credit, found := country("--country-from", "osm")
	if !found || name != "OSM Atlantis" || kind != "2" {
		t.Errorf("with osm got (%q, %q, %v), want OSM Atlantis at admin_level 2", name, kind, found)
	}
	if !strings.Contains(credit, "OpenStreetMap") {
		t.Errorf("with osm the country is credited to %q, want OpenStreetMap", credit)
	}
}

// Asked for and impossible is an error, not every country quietly taken
// from Natural Earth: a store of suburbs holds no national border.
func TestCountryFromOSMWithNoNationalBorderSaysSo(t *testing.T) {
	root := countryStore(t, boundary.NewArea("Suburb", "9", []boundary.Polygon{{Outer: square5()}}))
	_, err := locateSaying(t, "--store", root, "--lat", "1", "--lon", "1", "--country-from", "osm")
	if err == nil {
		t.Fatal("--country-from osm was answered from a store with no national border")
	}
	if !strings.Contains(err.Error(), "national border") {
		t.Errorf("error %q does not say what is missing", err)
	}

	// And a store with no boundaries at all.
	_, err = locateSaying(t, "--store", t.TempDir(), "--lat", "1", "--lon", "1", "--country-from", "osm")
	if err == nil || !strings.Contains(err.Error(), "national border") {
		t.Errorf("an empty store: %v, want the missing national border named", err)
	}
}

func TestCountryFromRefusesAnUnknownSource(t *testing.T) {
	_, err := locateSaying(t, "--store", t.TempDir(), "--lat", "1", "--lon", "1", "--country-from", "openstreetmap")
	if err == nil || !strings.Contains(err.Error(), "--country-from") {
		t.Errorf("got %v, want the flag refused by name", err)
	}
}
