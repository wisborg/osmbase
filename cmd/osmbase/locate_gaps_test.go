package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
)

// neSquareDoc is a Natural Earth style GeoJSON document holding one square.
func neSquareDoc(name string, lat, lon, side float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	c := func(la, lo float64) string { return "[" + f(lo) + "," + f(la) + "]" }
	return `{"type":"FeatureCollection","features":[{"properties":{"NAME":"` + name +
		`"},"geometry":{"type":"Polygon","coordinates":[[` +
		c(lat, lon) + `,` + c(lat, lon+side) + `,` + c(lat+side, lon+side) + `,` +
		c(lat+side, lon) + `,` + c(lat, lon) + `]]}}]}`
}

// naturalEarthStore is a store holding the three Natural Earth files and no
// tiles -- what "osmbase boundaries" leaves behind before any "osmbase fetch".
func naturalEarthStore(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		layer boundary.Layer
		body  string
	}{
		{boundary.Countries, neSquareDoc("Atlantis", 0, 0, 4)},
		{boundary.Regions, neSquareDoc("North Province", 0, 0, 3)},
		{boundary.Waters, neSquareDoc("Bay of Nowhere", 0, 0, 2)},
	} {
		name := boundary.File(boundary.DefaultDetail, f.layer)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestAContainedAnswerPrintsItsSourceAndNoDistance pins the text output for
// the answers this change made possible.
//
// The tests written with the change read the text output with
// strings.Contains on a name, so the line's shape is unasserted: printing
// "0 m away" under a contained answer passes them all. Measured: removing the
// guard that suppresses the distance line leaves cmd/osmbase green.
//
// That guard is not cosmetic. Its own comment says why -- a distance under a
// containment answer invites a reader to think a measurement was taken and
// came back as nothing, when the question does not apply -- and this command
// prints a distance on every other line precisely so the reader can tell the
// two claims apart.
func TestAContainedAnswerPrintsItsSourceAndNoDistance(t *testing.T) {
	store := derivedStore(t)
	out, err := locateSaying(t, "--store", store, "--lat", "-33.67", "--lon", "151.08",
		"--levels", "locality,neighbourhood")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}

	// The level, the word "contained", the name, and the admin_level the file
	// carried -- in that order, which is the order every other answer in this
	// command is read in.
	for _, want := range []string{
		"contained Hornsby Shire (6)",
		"contained Hornsby (9)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "away") {
		t.Errorf("a distance was printed under a contained answer:\n%s", out)
	}
	// And the coordinate heading is still there, so a format that dropped
	// everything would not pass by printing nothing.
	if !strings.Contains(out, "-33.67") {
		t.Errorf("the coordinate heading is missing:\n%s", out)
	}
}

// TestTheLevelsNeedingTilesAreNamedAndCountedCorrectly covers tileLevels,
// joinLevelNames and plural together.
//
// The only test over this path asks for one level, so the singular arm is the
// only one ever taken and joinLevelNames is only ever called with one name.
// Measured: swapping plural's two arms passes cmd/osmbase in full. The
// message exists so a user can act on it -- "ask for fewer levels" is only
// useful if the levels are named -- and a message that reads "street need map
// tiles" is the kind of thing that gets read as a bug in the tool.
//
// A Natural Earth store is the fixture rather than a derived one, because
// with derived boundaries present the only level left needing tiles is
// street: every other level is covered, and the plural arm is unreachable.
// Without them, the three levels below region need tiles too.
func TestTheLevelsNeedingTilesAreNamedAndCountedCorrectly(t *testing.T) {
	store := naturalEarthStore(t)
	if _, err := os.Stat(filepath.Join(store, "store.json")); err == nil {
		t.Fatal("the fixture has a tile store, so this proves nothing")
	}

	for _, tc := range []struct {
		name   string
		levels string
		want   []string
		reject string
	}{
		{
			name:   "one level is singular and names itself",
			levels: "street",
			want:   []string{"street needs map tiles"},
			reject: "street need map tiles",
		},
		{
			name:   "several levels are plural and all of them are named",
			levels: "locality,macrohood,neighbourhood",
			want:   []string{"locality, macrohood, neighbourhood need map tiles"},
			reject: "needs map tiles",
		},
		{
			name:   "the levels the boundaries cover are not in the message",
			levels: "country,street",
			want:   []string{"street needs map tiles"},
			reject: "country",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := locateSaying(t, "--store", store, "--lat", "0.5", "--lon", "0.5",
				"--levels", tc.levels)
			if err == nil {
				t.Fatal("a level needing tiles was answered by a store with none")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not carry %q", err, want)
				}
			}
			if strings.Contains(err.Error(), tc.reject) {
				t.Errorf("error %q carries %q, which it should not", err, tc.reject)
			}
			// And it says where to look, because the user may have several
			// stores and the default one is not printed anywhere else.
			if !strings.Contains(err.Error(), store) {
				t.Errorf("error %q does not name the store", err)
			}
		})
	}
}

// TestBothObligationsAreNamedWhenBothKindsOfFileAnswered covers
// containedCredits across two sources in one lookup.
//
// Every test over this path has one boundary file kind, so the credit list is
// never longer than one and a function that returned the first credit it
// found passes. A complete store -- "osmbase boundaries" and "osmbase
// boundaries --osm" both run -- answers country from Natural Earth and
// locality from an OpenStreetMap file in the SAME lookup, and the two
// obligations are different: one is public domain and owes nothing, the other
// is ODbL and owes the credit. Printing only the first is a licence failure
// if the first is the public domain one.
func TestBothObligationsAreNamedWhenBothKindsOfFileAnswered(t *testing.T) {
	store := naturalEarthStore(t)

	// A derived file inside the Natural Earth country, so one point is
	// contained by both.
	dir := boundary.Dir(store)
	name, err := boundary.DerivedFile("atlantis")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	area := boundary.NewArea("Council Area", "6", []boundary.Polygon{{
		Outer: boundary.Ring{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}, {Lat: 1, Lon: 1}, {Lat: 1, Lon: 0}},
	}})
	prov := boundary.Provenance{Source: "atlantis.osm.pbf", Attribution: "© OpenStreetMap contributors, ODbL"}
	if err := boundary.WriteDerived(f, boundary.NewSet(prov, []boundary.Area{area})); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := locateSaying(t, "--store", store, "--lat", "0.5", "--lon", "0.5",
		"--levels", "country,locality")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !strings.Contains(out, "Atlantis") || !strings.Contains(out, "Council Area") {
		t.Fatalf("both files should have answered:\n%s", out)
	}

	line, ok := lineContaining(out, "Contained answers are from")
	if !ok {
		t.Fatalf("no contained credit line:\n%s", out)
	}
	for _, want := range []string{"Natural Earth", "OpenStreetMap"} {
		if !strings.Contains(line, want) {
			t.Errorf("the credit line %q does not name %s", line, want)
		}
	}
	// No tiles were read, so there is no archive to credit and the line above
	// this one must not be invented.
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("an empty credit line was printed:\n%q", out)
	}
}

func lineContaining(s, sub string) (string, bool) {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return line, true
		}
	}
	return "", false
}
