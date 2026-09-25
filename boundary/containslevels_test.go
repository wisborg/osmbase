package boundary

import (
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// ContainsLevels is an optimisation, so its whole contract is to agree: for
// every level and every point, what Contains says. Checked over a grid across
// stores holding Natural Earth and derived files, both country sources, and
// points inside, outside and between them -- the Inside, Outside and NoData
// cases all occur.
func TestContainsLevelsAgreesWithContains(t *testing.T) {
	levels := []locate.Level{locate.Country, locate.Region, locate.Water, locate.Locality, locate.Macrohood, locate.Neighbourhood, locate.Street}
	stores := map[string]*Source{
		"derived and Natural Earth": derivedFindStore(t),
		"Denmark":                   denmarkStore(t),
	}
	osm := osmCountryStore(t)
	stores["country from OSM"] = OpenWith(osm, Options{Country: CountryOSM})

	seen := map[locate.Containment]bool{}
	for name, src := range stores {
		for lat := -60.0; lat <= 60; lat += 0.37 {
			for lon := -10.0; lon <= 160; lon += 0.73 {
				got := src.ContainsLevels(lat, lon, levels)
				for i, l := range levels {
					n, k, c, st := src.Contains(l, lat, lon)
					want := locate.Answer{Name: n, Kind: k, Credit: c, Containment: st}
					seen[st] = true
					if got[i] != want {
						t.Fatalf("%s at %v,%v: %s = %+v, Contains says %+v", name, lat, lon, l, got[i], want)
					}
				}
			}
		}
	}
	for _, c := range []locate.Containment{locate.Inside, locate.Outside, locate.NoData} {
		if !seen[c] {
			t.Errorf("no point was %v, so that case was not compared", c)
		}
	}
}
