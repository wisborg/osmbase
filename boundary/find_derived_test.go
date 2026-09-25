package boundary

import (
	"strings"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// derivedFindStore holds Natural Earth's Australia and New South Wales, and
// the United Kingdom with Newcastle upon Tyne as a region -- as the real 10m
// files do -- beside two derived files: one of New South Wales, with a
// Newcastle suburb inside the City of Newcastle and the state's own border at
// level 4, and one of England with a Newcastle at level 8.
func derivedFindStore(t *testing.T) *Source {
	t.Helper()
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail,
		neDoc(t,
			neFeature{map[string]string{"NAME": "Australia", "ISO_A2": "AU"}, [][3]float64{{-40, 140, 20}}},
			neFeature{map[string]string{"NAME": "United Kingdom", "ISO_A2": "GB"}, [][3]float64{{50, -6, 8}}},
		),
		neDoc(t,
			neFeature{map[string]string{"name": "New South Wales", "admin": "Australia", "iso_a2": "AU", "type_en": "State"}, [][3]float64{{-37, 141, 13}}},
			neFeature{map[string]string{"name": "Newcastle upon Tyne", "admin": "United Kingdom", "iso_a2": "GB", "type_en": "Metropolitan Borough"}, [][3]float64{{54.9, -1.8, 0.3}}},
		),
		neSquare("Sea", 0, 0, 1))
	writeDerived(t, root, "nsw", osmProv(),
		NewArea("New South Wales", "4", []Polygon{{Outer: square(-37, 141, 13)}}),
		NewArea("City of Newcastle", "6", []Polygon{{Outer: square(-33.0, 151.6, 0.3)}}),
		NewArea("Newcastle", "9", []Polygon{{Outer: square(-32.95, 151.7, 0.1)}}),
		NewArea("Hornsby Shire", "6", []Polygon{{Outer: square(-33.8, 150.9, 0.4)}}),
		NewArea("Hornsby", "9", []Polygon{{Outer: square(-33.7, 151.0, 0.1)}}),
	)
	writeDerived(t, root, "england", osmProv(),
		NewArea("Newcastle", "8", []Polygon{{Outer: square(54.95, -1.7, 0.1)}}),
	)
	return Open(root, DefaultDetail)
}

// A derived area is found by its name and placed by what holds it: the
// nearest wider derived area, then Natural Earth's region and country. That
// is what tells the two Newcastles apart in a list.
func TestFindListsDerivedAreasWithWhatTheyAreIn(t *testing.T) {
	exact, _ := derivedFindStore(t).Find("Newcastle")
	got := strings.Join(described(exact), "; ")
	for _, want := range []string{
		"Newcastle (admin level 9, City of Newcastle, New South Wales, Australia)",
		"Newcastle (admin level 8, Newcastle upon Tyne, United Kingdom)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Find(Newcastle) = %q, want it to include %q", got, want)
		}
	}
	if len(exact) != 2 {
		t.Errorf("Find(Newcastle) = %d candidates, want the two derived Newcastles", len(exact))
	}
}

// Any of the names a derived area is in qualifies it.
func TestFindNarrowsADerivedAreaByWhatHoldsIt(t *testing.T) {
	src := derivedFindStore(t)
	for _, q := range []string{
		"Newcastle, Australia", "Newcastle, AU", "Newcastle, New South Wales", "Newcastle, City of Newcastle",
	} {
		exact, _ := src.Find(q)
		if len(exact) != 1 || !strings.Contains(exact[0].In, "Australia") {
			t.Errorf("Find(%q) = %v, want the Australian one", q, described(exact))
		}
	}
	if exact, _ := src.Find("Newcastle, United Kingdom"); len(exact) != 1 || exact[0].Type != "admin level 8" {
		t.Errorf("Find(Newcastle, United Kingdom) = %v, want the English one", described(exact))
	}
}

// The level filter: local areas are Locality, and asking for a country or a
// region leaves them out.
func TestFindByLevelSeparatesDerivedAreas(t *testing.T) {
	src := derivedFindStore(t)
	if exact, _ := src.Find("Newcastle", locate.Locality); len(exact) != 2 {
		t.Errorf("Find(Newcastle, local) = %v, want both derived Newcastles", described(exact))
	}
	if exact, near := src.Find("Newcastle", locate.Region); exact != nil || len(near) != 1 {
		t.Errorf("Find(Newcastle, region) = %v %v, want only Newcastle upon Tyne offered", described(exact), described(near))
	}
}

// A derived file's state border is not a second New South Wales: Natural
// Earth answers states, and the derived file is searched below them.
func TestADerivedStateBorderIsNotASecondCandidate(t *testing.T) {
	exact, _ := derivedFindStore(t).Find("New South Wales")
	if len(exact) != 1 || exact[0].Level != locate.Region {
		t.Errorf("Find(New South Wales) = %v, want only Natural Earth's", described(exact))
	}
}

// An exact derived name wins over a partial one, as it does for Natural
// Earth: "Hornsby" is the suburb, and Hornsby Shire is a different place a
// person would name in full.
func TestFindPrefersAnExactDerivedName(t *testing.T) {
	exact, _ := derivedFindStore(t).Find("Hornsby")
	if len(exact) != 1 || exact[0].Name != "Hornsby" {
		t.Errorf("Find(Hornsby) = %v, want the suburb alone", described(exact))
	}
	if exact, _ := derivedFindStore(t).Find("Hornsby Shire"); len(exact) != 1 {
		t.Errorf("Find(Hornsby Shire) = %v", described(exact))
	}
}

// The same boundary in two overlapping extracts is one place.
func TestADerivedAreaInTwoFilesIsFoundOnce(t *testing.T) {
	root := t.TempDir()
	a := NewArea("Hornsby", "9", []Polygon{{Outer: square(-33.7, 151.0, 0.1)}})
	writeDerived(t, root, "sydney", osmProv(), a)
	writeDerived(t, root, "nsw", osmProv(), a)
	if exact, _ := Open(root, DefaultDetail).Find("Hornsby"); len(exact) != 1 {
		t.Errorf("Find(Hornsby) = %v, want one", described(exact))
	}
}

// What an area is in is asked at a point INSIDE it. An L-shaped area has the
// middle of its box outside it -- over the neighbour, here -- and asking there
// would place it in the wrong council.
func TestInteriorPointIsInsideAnLShapedArea(t *testing.T) {
	l := NewArea("L", "9", []Polygon{{Outer: Ring{
		{Lat: 0, Lon: 0}, {Lat: 0, Lon: 3}, {Lat: 1, Lon: 3}, {Lat: 1, Lon: 1}, {Lat: 3, Lon: 1}, {Lat: 3, Lon: 0},
	}}})
	if l.contains(1.5, 1.5) {
		t.Fatal("precondition: the middle of the box is inside the L, so this proves nothing")
	}
	lat, lon, ok := interiorPoint(l)
	if !ok || !l.contains(lat, lon) {
		t.Errorf("interior point %v,%v (%v) is not inside the area", lat, lon, ok)
	}
}

// Natural Earth's coast is generalised, so a point inside a harbour suburb can
// fall outside the country's outline while inside its region's. The region
// records its country, and that is enough to say -- and to qualify by.
func TestADerivedAreaOutsideTheCountryOutlineStillKnowsItsCountry(t *testing.T) {
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail,
		neDoc(t, neFeature{map[string]string{"NAME": "Australia", "ISO_A2": "AU"}, [][3]float64{{-40, 140, 11}}}),
		neDoc(t, neFeature{map[string]string{"name": "New South Wales", "admin": "Australia", "iso_a2": "AU", "type_en": "State"}, [][3]float64{{-37, 141, 13}}}),
		neSquare("Sea", 0, 0, 1))
	// At longitude 151.7, past the country outline's east edge at 151.
	writeDerived(t, root, "nsw", osmProv(), NewArea("Newcastle", "9", []Polygon{{Outer: square(-32.95, 151.7, 0.1)}}))

	src := Open(root, DefaultDetail)
	exact, _ := src.Find("Newcastle")
	if len(exact) != 1 || exact[0].In != "New South Wales, Australia" {
		t.Errorf("Find(Newcastle) = %v, want it in New South Wales, Australia", described(exact))
	}
	for _, q := range []string{"Newcastle, Australia", "Newcastle, AU"} {
		if exact, _ := src.Find(q); len(exact) != 1 {
			t.Errorf("Find(%q) = %v, want the one", q, described(exact))
		}
	}
}

// denmarkStore is Natural Earth's Denmark and Midtjylland beside a derived
// file built at every level, which carries OpenStreetMap's local names:
// Danmark, Region Midtjylland, Horsens Kommune.
func denmarkStore(t *testing.T) *Source {
	t.Helper()
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail,
		neDoc(t, neFeature{map[string]string{"NAME": "Denmark", "ISO_A2": "DK"}, [][3]float64{{54.5, 8, 4}}}),
		neDoc(t, neFeature{map[string]string{"name": "Midtjylland", "admin": "Denmark", "iso_a2": "DK", "type_en": "Region"}, [][3]float64{{55.5, 8, 2.5}}}),
		neSquare("Sea", 0, 0, 1))
	writeDerived(t, root, "denmark", osmProv(),
		NewArea("Danmark", "2", []Polygon{{Outer: square(54.4, 7.9, 4.2)}}),
		NewArea("Region Midtjylland", "4", []Polygon{{Outer: square(55.5, 8, 2.5)}}),
		NewArea("Horsens Kommune", "7", []Polygon{{Outer: square(55.7, 9.6, 0.3)}}),
	)
	return Open(root, DefaultDetail)
}

// The derived files' national and state borders are found by their local
// names, at the level they are: "Danmark" is a country and "Region
// Midtjylland" a region, which Natural Earth's English names would not find.
func TestLocalNamesOfCountriesAndRegionsAreFound(t *testing.T) {
	src := denmarkStore(t)
	for _, tc := range []struct {
		q     string
		level locate.Level
		in    string
	}{
		{"Danmark", locate.Country, ""},
		{"Region Midtjylland", locate.Region, "Denmark"},
	} {
		exact, _ := src.Find(tc.q)
		if len(exact) != 1 || exact[0].Level != tc.level || exact[0].In != tc.in {
			t.Errorf("Find(%q) = %+v, want one at %s in %q", tc.q, exact, tc.level, tc.in)
		}
	}
	if exact, _ := src.Find("Region Midtjylland", locate.Region); len(exact) != 1 {
		t.Errorf("--place-level region did not find the derived region: %v", described(exact))
	}
	if exact, _ := src.Find("Danmark", locate.Locality); len(exact) != 0 {
		t.Errorf("--place-level local found a country: %v", described(exact))
	}
}

// A kommune is placed once in its region: the derived region is OSM's name
// for the region Natural Earth names in English, and listing both read as
// "Region Midtjylland, Midtjylland, Denmark". It still qualifies a search.
func TestADerivedRegionIsNotSaidTwice(t *testing.T) {
	src := denmarkStore(t)
	exact, _ := src.Find("Horsens Kommune")
	if len(exact) != 1 || exact[0].In != "Midtjylland, Denmark" {
		t.Errorf("Find(Horsens Kommune) = %v, want it in Midtjylland, Denmark", described(exact))
	}
	if exact, _ := src.Find("Horsens Kommune, Region Midtjylland"); len(exact) != 1 {
		t.Errorf("the derived region's name does not qualify: %v", described(exact))
	}
}
