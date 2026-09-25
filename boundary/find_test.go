package boundary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// neFeature is one Natural Earth feature: its properties, and its parts as
// squares of [lat, lon, side].
type neFeature struct {
	props map[string]string
	parts [][3]float64
}

// neDoc builds a Natural Earth style file, with MultiPolygon geometry so a
// feature can have parts.
func neDoc(t *testing.T, fs ...neFeature) string {
	t.Helper()
	type feature struct {
		Properties map[string]string `json:"properties"`
		Geometry   struct {
			Type        string          `json:"type"`
			Coordinates [][][][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	var doc struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}
	doc.Type = "FeatureCollection"
	for _, f := range fs {
		var ft feature
		ft.Properties = f.props
		ft.Geometry.Type = "MultiPolygon"
		for _, p := range f.parts {
			la, lo, sd := p[0], p[1], p[2]
			ft.Geometry.Coordinates = append(ft.Geometry.Coordinates, [][][]float64{{
				{lo, la}, {lo + sd, la}, {lo + sd, la + sd}, {lo, la + sd}, {lo, la},
			}})
		}
		doc.Features = append(doc.Features, ft)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// findStore holds the ambiguities measured against the real 10m files:
// Luxembourg is a country, a district of it and a province of Belgium;
// Georgia is a country and a US state; Newcastle is only Newcastle upon Tyne.
func findStore(t *testing.T) *Source {
	t.Helper()
	root := t.TempDir()
	countries := neDoc(t,
		neFeature{map[string]string{"NAME": "France", "ISO_A2": "-99", "ISO_A2_EH": "FR", "ISO_A3": "-99", "ADM0_A3": "FRA"}, [][3]float64{{42, -5, 9}}},
		neFeature{map[string]string{"NAME": "Luxembourg", "NAME_EN": "Luxembourg", "ISO_A2": "LU"}, [][3]float64{{49.4, 5.7, 0.7}}},
		neFeature{map[string]string{"NAME": "Georgia", "ISO_A2": "GE"}, [][3]float64{{41, 40, 3}}},
		neFeature{map[string]string{"NAME": "Denmark", "NAME_EN": "Denmark", "ISO_A2": "DK"}, [][3]float64{{54.8, 8, 3}}},
		neFeature{map[string]string{"NAME": "Nowhere", "ISO_A2": "-99"}, [][3]float64{{0, 0, 1}}},
	)
	regions := neDoc(t,
		neFeature{map[string]string{"name": "Luxembourg", "admin": "Luxembourg", "iso_a2": "LU", "type_en": "District"}, [][3]float64{{49.5, 5.8, 0.4}}},
		neFeature{map[string]string{"name": "Luxembourg", "admin": "Belgium", "iso_a2": "BE", "type_en": "Province"}, [][3]float64{{49.5, 5.0, 0.6}}},
		neFeature{map[string]string{"name": "Georgia", "admin": "United States of America", "iso_a2": "US", "type_en": "State"}, [][3]float64{{30, -85, 5}}},
		neFeature{map[string]string{"name": "Newcastle upon Tyne", "admin": "United Kingdom", "iso_a2": "GB", "type_en": "Metropolitan Borough"}, [][3]float64{{54.9, -1.8, 0.2}}},
		neFeature{map[string]string{"name": "Newcastleton", "admin": "United Kingdom", "iso_a2": "GB", "type_en": "Village"}, [][3]float64{{55.1, -2.8, 0.1}}},
		// As in the real file: the capital region's English name is its
		// country's.
		neFeature{map[string]string{"name": "Hovedstaden", "name_en": "Denmark", "admin": "Denmark", "iso_a2": "DK", "type_en": "Region"}, [][3]float64{{55.6, 11.8, 0.5}}},
	)
	writeNaturalEarth(t, root, DefaultDetail, countries, regions, neSquare("Sea", 0, 0, 1))
	return Open(root, DefaultDetail)
}

func described(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Describe())
	}
	return out
}

// Every place the name could mean comes back, named well enough to choose
// between -- and none is chosen.
func TestFindReturnsEveryPlaceANameCouldMean(t *testing.T) {
	exact, near := findStore(t).Find("Luxembourg")
	got := strings.Join(described(exact), "; ")
	want := "Luxembourg (country); Luxembourg (district, Luxembourg); Luxembourg (province, Belgium)"
	if got != want {
		t.Errorf("Find(Luxembourg) = %q, want %q", got, want)
	}
	if near != nil {
		t.Errorf("near matches came back beside exact ones: %v", described(near))
	}
}

func TestFindIsNarrowedByWhatItIsIn(t *testing.T) {
	src := findStore(t)
	for _, q := range []string{"Luxembourg, Belgium", "luxembourg, belgium", "Luxembourg, BE", "Georgia, United States of America", "Georgia, US"} {
		exact, _ := src.Find(q)
		if len(exact) != 1 || exact[0].Level != locate.Region {
			t.Errorf("Find(%q) = %v, want the one region", q, described(exact))
		}
	}
	// A qualifier nothing is in finds nothing, rather than being ignored.
	if exact, near := src.Find("Luxembourg, France"); exact != nil || near != nil {
		t.Errorf("Find(Luxembourg, France) = %v %v, want nothing", described(exact), described(near))
	}
}

func TestFindIsNarrowedByLevel(t *testing.T) {
	src := findStore(t)
	if exact, _ := src.Find("Georgia", locate.Country); len(exact) != 1 || exact[0].Level != locate.Country {
		t.Errorf("Find(Georgia, country) = %v, want the country", described(exact))
	}
	if exact, _ := src.Find("Georgia", locate.Region); len(exact) != 1 || exact[0].In != "United States of America" {
		t.Errorf("Find(Georgia, region) = %v, want the US state", described(exact))
	}
}

// Case does not matter, the ISO code is a name, and Natural Earth's -99 for
// "no code" is not one.
func TestFindMatchesAnyNameOrCode(t *testing.T) {
	src := findStore(t)
	// Only the country: the capital region's English name is "Denmark" in
	// the real file, and a region is never named after its own country.
	for _, q := range []string{"denmark", "DENMARK", "DK", "dk"} {
		if exact, _ := src.Find(q); len(exact) != 1 || exact[0].Name != "Denmark" {
			t.Errorf("Find(%q) = %v, want Denmark", q, described(exact))
		}
	}
	// France's plain ISO fields are -99 in the real file; the _EH and
	// ADM0 codes are what name it.
	for _, q := range []string{"FR", "FRA"} {
		if exact, _ := src.Find(q); len(exact) != 1 || exact[0].Name != "France" {
			t.Errorf("Find(%q) = %v, want France", q, described(exact))
		}
	}
	if exact, near := src.Find("-99"); exact != nil || near != nil {
		t.Errorf("Find(-99) = %v %v; Natural Earth's missing-code marker matched as a code", described(exact), described(near))
	}
}

// A name that only begins a place's name is offered, never taken: the
// Newcastle a person means may be one this data does not hold at all, and
// taking Newcastle upon Tyne for it would draw England for somebody in
// Australia. And it has to begin a WORD -- Newcastleton is a different place.
func TestFindOffersAPartialMatchWithoutTakingIt(t *testing.T) {
	src := findStore(t)
	exact, near := src.Find("Newcastle")
	if exact != nil {
		t.Errorf("a partial match came back as exact: %v", described(exact))
	}
	if got := described(near); len(got) != 1 || got[0] != "Newcastle upon Tyne (metropolitan borough, United Kingdom)" {
		t.Errorf("near = %v, want Newcastle upon Tyne alone", got)
	}
	if exact, _ := src.Find("Newcastle upon Tyne"); len(exact) != 1 {
		t.Errorf("the full name is not an exact match: %v", described(exact))
	}
}

// The extent is the main part and what lies near it, not the rectangle around
// every part: France's includes Corsica and not Guiana.
func TestTheExtentLeavesOutPartsAnOceanAway(t *testing.T) {
	a := NewArea("France", "country", []Polygon{
		{Outer: square(42, -5, 9)},    // the mainland, the largest part
		{Outer: square(41.3, 8.5, 1)}, // Corsica, near it
		{Outer: square(2, -54, 3)},    // Guiana, an ocean away
		{Outer: square(-21, 55, 0.5)}, // Réunion
	})
	e, shown := mainExtent(a)
	if shown != 2 {
		t.Errorf("showed %d parts, want the mainland and Corsica", shown)
	}
	want := Extent{West: -5, South: 41.3, East: 9.5, North: 51}
	if e != want {
		t.Errorf("extent = %+v, want %+v", e, want)
	}
}

// Parts are taken in one step at a time, so a chain of islands each near the
// last comes in whole even where the far end is not near the main part:
// Bornholm is not within reach of Jutland, but it is of Jutland and Zealand.
func TestTheExtentGathersAChainOfPartsOneStepAtATime(t *testing.T) {
	a := NewArea("Denmark", "country", []Polygon{
		{Outer: square(54.8, 8, 3)},       // Jutland, the largest
		{Outer: square(55, 11, 1.6)},      // Zealand
		{Outer: square(54.95, 14.7, 0.4)}, // Bornholm
	})
	if _, direct := mainExtent(NewArea("x", "", []Polygon{{Outer: square(54.8, 8, 3)}, {Outer: square(54.95, 14.7, 0.4)}})); direct != 1 {
		t.Fatalf("precondition: Bornholm is within reach of Jutland alone, so this proves nothing")
	}
	if _, shown := mainExtent(a); shown != 3 {
		t.Errorf("showed %d of 3 parts, want the chain whole", shown)
	}
}

func TestACandidateCarriesItsExtentAndPartCount(t *testing.T) {
	exact, _ := findStore(t).Find("Denmark")
	if len(exact) != 1 {
		t.Fatalf("Find(Denmark) = %v", described(exact))
	}
	c := exact[0]
	if c.Parts != 1 || c.Shown != 1 {
		t.Errorf("parts %d shown %d, want 1 and 1", c.Parts, c.Shown)
	}
	if (c.Extent != Extent{West: 8, South: 54.8, East: 11, North: 57.8}) {
		t.Errorf("extent = %+v", c.Extent)
	}
}
