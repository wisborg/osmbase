package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// writeDerived puts a derived file in a store and returns the store root.
func writeDerived(t testing.TB, root, region string, p Provenance, areas ...Area) {
	t.Helper()
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := DerivedFile(region)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := WriteDerived(f, NewSet(p, areas)); err != nil {
		t.Fatal(err)
	}
}

// inside is Contains with the answer folded to "an area holds the point".
// Most tests here ask which area answered; the ones about what a source says
// when none does call Contains and look at the state.
func inside(src *Source, l locate.Level, lat, lon float64) (name, kind, credit string, ok bool) {
	name, kind, credit, c := src.Contains(l, lat, lon)
	return name, kind, credit, c == locate.Inside
}

func osmProv() Provenance {
	return Provenance{Source: "x.osm.pbf", Attribution: "© OpenStreetMap contributors, ODbL"}
}

// The rule is the nesting itself, because an admin_level is a number whose
// meaning differs by country. The outermost area containing a point is its
// locality and the innermost is its neighbourhood.
func TestTheContainmentStackIsRankedOntoLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		areas []Area
		at    Coord
		want  map[locate.Level]string
	}{
		{
			name: "one area is a locality and nothing else",
			// A country that stops its hierarchy at the municipality has one
			// name at this range, and inventing two from it would be a claim
			// the data does not make.
			areas: []Area{NewArea("Horsens Kommune", "7", []Polygon{{Outer: square(55, 9, 2)}})},
			at:    Coord{Lat: 55.5, Lon: 9.5},
			want:  map[locate.Level]string{locate.Locality: "Horsens Kommune"},
		},
		{
			name: "two nested areas are a locality and a neighbourhood",
			areas: []Area{
				NewArea("Hornsby Shire", "6", []Polygon{{Outer: square(-34, 151, 2)}}),
				NewArea("Hornsby", "9", []Polygon{{Outer: square(-33.5, 151.5, 0.2)}}),
			},
			at:   Coord{Lat: -33.45, Lon: 151.55},
			want: map[locate.Level]string{locate.Locality: "Hornsby Shire", locate.Neighbourhood: "Hornsby"},
		},
		{
			name: "three nested areas fill the middle",
			areas: []Area{
				NewArea("Outer", "5", []Polygon{{Outer: square(-34, 151, 4)}}),
				NewArea("Middle", "7", []Polygon{{Outer: square(-33.6, 151.4, 1)}}),
				NewArea("Inner", "10", []Polygon{{Outer: square(-33.5, 151.5, 0.2)}}),
			},
			at: Coord{Lat: -33.45, Lon: 151.55},
			want: map[locate.Level]string{
				locate.Locality: "Outer", locate.Macrohood: "Middle", locate.Neighbourhood: "Inner",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeDerived(t, root, "r", osmProv(), tc.areas...)
			src := Open(root, DefaultDetail)

			for _, level := range []locate.Level{locate.Locality, locate.Macrohood, locate.Neighbourhood} {
				name, kind, _, ok := inside(src, level, tc.at.Lat, tc.at.Lon)
				want, wanted := tc.want[level]
				switch {
				case wanted && !ok:
					t.Errorf("%s was not answered, want %q", level, want)
				case !wanted && ok:
					t.Errorf("%s answered %q, want nothing", level, name)
				case wanted && name != want:
					t.Errorf("%s = %q, want %q", level, name, want)
				case wanted && kind == "":
					t.Errorf("%s carries no admin_level, and that is how a reader tells what it got", level)
				}
			}
		})
	}
}

// A store with no derived file must leave these levels to the tiles rather
// than claiming them and answering nothing.
func TestLevelsAreNotClaimedWithoutAFile(t *testing.T) {
	src := Open(t.TempDir(), DefaultDetail)
	for _, level := range []locate.Level{locate.Locality, locate.Macrohood, locate.Neighbourhood} {
		if src.Covers(level) {
			t.Errorf("%s is claimed by a store holding no derived file", level)
		}
	}
}

func TestLevelsAreClaimedWhenAFileIsThere(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "r", osmProv(), NewArea("A", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	src := Open(root, DefaultDetail)
	for _, level := range []locate.Level{locate.Locality, locate.Macrohood, locate.Neighbourhood} {
		if !src.Covers(level) {
			t.Errorf("%s is not claimed although a derived file is there", level)
		}
	}
	// And the levels that are not this file's business are untouched.
	if src.Covers(locate.Street) {
		t.Error("street is claimed, and no boundary file holds a line")
	}
}

// Which credit a contained answer carries is no longer a constant: Natural
// Earth is public domain and says so, a derived file says whatever its own
// provenance says.
func TestTheCreditFollowsTheSourceThatAnswered(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "r", osmProv(), NewArea("A", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	src := Open(root, DefaultDetail)

	_, _, got, ok := inside(src, locate.Locality, 0.5, 0.5)
	if !ok {
		t.Fatal("the derived area was not found")
	}
	if !strings.Contains(got, "OpenStreetMap") || !strings.Contains(got, "ODbL") {
		t.Errorf("locality credit = %q, want the OpenStreetMap credit", got)
	}
	// A level nothing here answers carries nothing, because there is no
	// answer to attach it to.
	if _, _, c, ok := inside(src, locate.Street, 0.5, 0.5); ok || c != "" {
		t.Errorf("street answered with credit %q", c)
	}
}

// A store may hold several regions, and nothing tells a lookup which one a
// coordinate is in -- that is the question.
func TestSeveralRegionsAreAllConsulted(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "north", osmProv(), NewArea("North", "7", []Polygon{{Outer: square(55, 9, 1)}}))
	writeDerived(t, root, "south", osmProv(), NewArea("South", "7", []Polygon{{Outer: square(-34, 151, 1)}}))
	src := Open(root, DefaultDetail)

	for _, tc := range []struct {
		lat, lon float64
		want     string
	}{
		{55.5, 9.5, "North"},
		{-33.5, 151.5, "South"},
		{0, 0, ""},
	} {
		name, _, _, ok := inside(src, locate.Locality, tc.lat, tc.lon)
		if tc.want == "" && ok {
			t.Errorf("a point in neither region answered %q", name)
		}
		if tc.want != "" && name != tc.want {
			t.Errorf("at %v,%v got %q, want %q", tc.lat, tc.lon, name, tc.want)
		}
	}
}

// A store built by "osmbase boundaries --osm" and nothing else holds no
// Natural Earth outlines. Reporting it as having no boundaries at all made
// the derived file dead: nothing opened a source, so nothing read it.
func TestAStoreWithOnlyADerivedFileIsAvailable(t *testing.T) {
	root := t.TempDir()
	if Available(root, DefaultDetail) {
		t.Fatal("an empty store reports boundaries")
	}
	writeDerived(t, root, "r", osmProv(), NewArea("A", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	if !Available(root, DefaultDetail) {
		t.Error("a store holding a derived file reports no boundaries")
	}
	// And it still does not claim the levels it has no data for.
	src := Open(root, DefaultDetail)
	if _, _, _, ok := inside(src, locate.Country, 0.5, 0.5); ok {
		t.Error("a country was answered from a store with no country outlines")
	}
}

// A derived file that will not parse must not take the level away from the
// tiles for every coordinate in a route, nor be re-read per point.
func TestABrokenDerivedFileIsNotFatal(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, _ := DerivedFile("broken")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not a derived file"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := Open(root, DefaultDetail)
	if src.Covers(locate.Locality) {
		t.Error("a level is claimed on the strength of a file that does not load")
	}
	if _, _, _, ok := inside(src, locate.Locality, 0, 0); ok {
		t.Error("a broken file answered a lookup")
	}
}

// Containing's order is its contract: a caller ranking a hierarchy needs to
// know which end is the outside. Tested here rather than only through
// Source.Contains, which sorts the merged stack again and so cannot see this.
func TestContainingReturnsTheStackOutermostFirst(t *testing.T) {
	set := NewSet(Provenance{}, []Area{
		// Deliberately not in size order in the file.
		NewArea("Middle", "7", []Polygon{{Outer: square(-33.9, 151.1, 1)}}),
		NewArea("Outer", "5", []Polygon{{Outer: square(-34, 151, 3)}}),
		NewArea("Inner", "10", []Polygon{{Outer: square(-33.8, 151.2, 0.2)}}),
		NewArea("Elsewhere", "7", []Polygon{{Outer: square(10, 10, 1)}}),
	})

	got := set.Containing(-33.75, 151.25)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if len(names) != 3 {
		t.Fatalf("Containing = %v, want the three areas holding the point", names)
	}
	if names[0] != "Outer" || names[len(names)-1] != "Inner" {
		t.Errorf("Containing = %v, want outermost first and innermost last", names)
	}
}
