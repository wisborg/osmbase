package boundary

import (
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// The City level is the smallest city or town extent holding a point. A
// city's extent is no locality: the suburbs inside it rank as they did.
// Outside every city, the level is Outside where the file knows the ground
// and NoData where it does not. And it is covered only where a file holds a
// city.
func TestTheCityLevelIsTheCitysExtent(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "r", osmProv(),
		// The city is wider than the council inside it, so ranked into the
		// levels below it would be every suburb's locality.
		NewArea("Greater Place", "city", []Polygon{{Outer: square(-34, 150, 4)}}),
		NewArea("Inner Town", "town", []Polygon{{Outer: square(-33.2, 150.2, 0.5)}}),
		NewArea("The Council", "6", []Polygon{{Outer: square(-34, 150, 3)}}),
		NewArea("A Suburb", "9", []Polygon{{Outer: square(-33.5, 151.5, 0.2)}}),
		NewArea("Far Council", "6", []Polygon{{Outer: square(-20, 130, 1)}}),
	)
	src := Open(root, DefaultDetail)
	if !src.Covers(locate.City) {
		t.Fatal("a file holding a city does not cover the city level")
	}
	for _, tc := range []struct {
		lat, lon float64
		want     string
		c        locate.Containment
	}{
		{-33.45, 151.55, "Greater Place", locate.Inside},
		{-33.0, 150.4, "Inner Town", locate.Inside},
		{-19.5, 130.5, "", locate.Outside}, // in a council, in no city
		{10, 10, "", locate.NoData},
	} {
		name, kind, credit, c := src.Contains(locate.City, tc.lat, tc.lon)
		if name != tc.want || c != tc.c || (c == locate.Inside && (kind == "" || credit == "")) {
			t.Errorf("%v,%v: %q %q %q %v; want %q %v", tc.lat, tc.lon, name, kind, credit, c, tc.want, tc.c)
		}
	}
	// The suburb's locality is the council, not the city.
	if name, _, _, ok := inside(src, locate.Locality, -33.45, 151.55); !ok || name != "The Council" {
		t.Errorf("locality %q; a city's extent was ranked into the levels below", name)
	}
	got := src.ContainsLevels(-33.45, 151.55, []locate.Level{locate.City, locate.Neighbourhood})
	if got[0].Name != "Greater Place" || got[1].Name != "A Suburb" {
		t.Errorf("ContainsLevels %+v", got)
	}

	plain := t.TempDir()
	writeDerived(t, plain, "r", osmProv(), NewArea("The Council", "6", []Polygon{{Outer: square(-34, 150, 3)}}))
	if Open(plain, DefaultDetail).Covers(locate.City) {
		t.Error("a file with no city covers the city level")
	}
}

// A city's extent is found by name as a city, not as a locality.
func TestACityIsFoundAsACity(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "r", osmProv(), NewArea("Greater Place", "city", []Polygon{{Outer: square(-34, 150, 4)}}))
	exact, _ := Open(root, DefaultDetail).Find("Greater Place")
	if len(exact) != 1 || exact[0].Level != locate.City {
		t.Errorf("found %+v", exact)
	}
}
