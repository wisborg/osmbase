package boundary

import (
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// osmCountryStore is Natural Earth's Atlantis, a square of 4 at the origin,
// beside a derived file whose national border is a square of 5 -- the same
// country with a strip of territorial water around it, which is how the two
// sources differ -- and a kommune inside both.
func osmCountryStore(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail, neSquare("Atlantis", 0, 0, 4), "", "")
	writeDerived(t, root, "atlantis", osmProv(),
		NewArea("OSM Atlantis", "2", []Polygon{{Outer: square(-0.5, -0.5, 5)}}),
		NewArea("Kommune", "7", []Polygon{{Outer: square(1, 1, 1)}}))
	return root
}

// The same store answers the country differently under each setting, which
// is the whole of the option. Each row is asked of both, so a source that
// ignored the setting fails one column or the other.
func TestTheCountryComesFromTheChosenSource(t *testing.T) {
	root := osmCountryStore(t)
	byDefault := Open(root, DefaultDetail)
	fromOSM := OpenWith(root, Options{Country: CountryOSM})

	type answer struct {
		name, kind, credit string
		c                  locate.Containment
	}
	ne := func(name string) answer { return answer{name, "country", NaturalEarthCredit, locate.Inside} }
	osm := func(name string) answer { return answer{name, "2", osmProv().Attribution, locate.Inside} }
	atSea := answer{c: locate.Outside}

	for _, tc := range []struct {
		name         string
		lat, lon     float64
		def, withOSM answer
	}{
		{"on land", 1.5, 1.5, ne("Atlantis"), osm("OSM Atlantis")},
		// Inside OSM's border and outside Natural Earth's coast: the
		// territorial-waters strip the default exists for.
		{"in territorial waters", 4.2, 4.2, atSea, osm("OSM Atlantis")},
		// Outside both: the sea, which both agree on. With OSM chosen this
		// is Natural Earth answering, not the derived file.
		{"beyond them", 20, 20, atSea, atSea},
	} {
		for _, side := range []struct {
			label string
			src   *Source
			want  answer
		}{{"default", byDefault, tc.def}, {"osm", fromOSM, tc.withOSM}} {
			name, kind, credit, c := side.src.Contains(locate.Country, tc.lat, tc.lon)
			if got := (answer{name, kind, credit, c}); got != side.want {
				t.Errorf("%s, %s: got %+v, want %+v", tc.name, side.label, got, side.want)
			}
		}
	}
}

// Where no derived file holds the point, Natural Earth answers the country
// under CountryOSM exactly as by default -- a Denmark file says nothing about
// Germany.
func TestWithOSMChosenNaturalEarthAnswersBeyondTheFiles(t *testing.T) {
	root := t.TempDir()
	writeNaturalEarth(t, root, DefaultDetail, neSquare("Elsewhere", 10, 10, 4), "", "")
	writeDerived(t, root, "atlantis", osmProv(),
		NewArea("OSM Atlantis", "2", []Polygon{{Outer: square(0, 0, 4)}}))
	src := OpenWith(root, Options{Country: CountryOSM})

	name, _, credit, c := src.Contains(locate.Country, 11, 11)
	if c != locate.Inside || name != "Elsewhere" || credit != NaturalEarthCredit {
		t.Errorf("got (%q, %q, %v), want Natural Earth's Elsewhere", name, credit, c)
	}
}

// A store of derived files and no Natural Earth outlines covers the country
// only when OSM was asked for and a file holds a border; a point no file
// holds is then NoData, and goes to the tiles rather than being called sea
// by a source that has no sea to speak of.
func TestADerivedOnlyStoreCoversTheCountryOnlyWhenAskedTo(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "atlantis", osmProv(),
		NewArea("OSM Atlantis", "2", []Polygon{{Outer: square(0, 0, 4)}}))

	if Open(root, DefaultDetail).Covers(locate.Country) {
		t.Error("the default covers the country from a store with no Natural Earth outlines")
	}
	src := OpenWith(root, Options{Country: CountryOSM})
	if !src.Covers(locate.Country) {
		t.Fatal("CountryOSM does not cover the country from a file holding a national border")
	}
	if _, _, _, c := src.Contains(locate.Country, 20, 20); c != locate.NoData {
		t.Errorf("a point no file holds is %v, want NoData", c)
	}
}

// A file holding no national border -- built with "--levels 8,9,10" -- gives
// CountryOSM nothing to answer from, and says so rather than letting every
// answer quietly be Natural Earth's.
func TestHasDerivedCountries(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "suburbs", osmProv(),
		NewArea("Suburb", "9", []Polygon{{Outer: square(0, 0, 1)}}))
	src := OpenWith(root, Options{Country: CountryOSM})
	if src.HasDerivedCountries() {
		t.Error("a file of suburbs reports a national border")
	}
	if src.Covers(locate.Country) {
		t.Error("the country is covered from a file with no national border and no Natural Earth")
	}

	writeDerived(t, root, "country", osmProv(),
		NewArea("OSM Atlantis", "2", []Polygon{{Outer: square(0, 0, 4)}}))
	if !OpenWith(root, Options{Country: CountryOSM}).HasDerivedCountries() {
		t.Error("a file holding a national border reports none")
	}
}

// Two national borders holding one point -- two extracts, or a territory
// tagged at level 2 inside its state -- answer with the innermost, as
// Natural Earth's water does.
func TestTheInnermostNationalBorderAnswers(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "a", osmProv(),
		NewArea("Wide", "2", []Polygon{{Outer: square(0, 0, 8)}}),
		NewArea("Narrow", "2", []Polygon{{Outer: square(1, 1, 2)}}))
	src := OpenWith(root, Options{Country: CountryOSM})
	if name, _, _, _ := src.Contains(locate.Country, 2, 2); name != "Narrow" {
		t.Errorf("country = %q, want the innermost border", name)
	}
}

// Choosing OSM for the country does not put the country back into the
// levels below a region: the kommune is still the locality, not the
// national border around it.
func TestChoosingOSMLeavesTheLevelsBelowARegionAlone(t *testing.T) {
	src := OpenWith(osmCountryStore(t), Options{Country: CountryOSM})
	if name, _, _, _ := inside(src, locate.Locality, 1.5, 1.5); name != "Kommune" {
		t.Errorf("locality = %q, want the kommune", name)
	}
}

func TestCountrySourceNamesRoundTrip(t *testing.T) {
	for _, c := range []CountrySource{CountryNaturalEarth, CountryOSM} {
		if got, ok := ParseCountrySource(c.String()); !ok || got != c {
			t.Errorf("ParseCountrySource(%q) = %v, %v", c.String(), got, ok)
		}
	}
	if _, ok := ParseCountrySource("openstreetmap"); ok {
		t.Error("an unknown name was accepted")
	}
}
