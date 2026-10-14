package boundary_test

import (
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
)

// An area read from Natural Earth carries its country's code, its Wikidata
// id and its rank, as a country's file and a state's file spell them; and
// says whether it holds a point.
func TestNaturalEarthFieldsAreRead(t *testing.T) {
	const doc = `{"type":"FeatureCollection","features":[
	 {"type":"Feature","properties":{"NAME":"Testland","ADM0_A3":"TST","WIKIDATAID":"Q1","scalerank":1,"LABEL_X":4.5,"LABEL_Y":5.5},
	  "geometry":{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}},
	 {"type":"Feature","properties":{"name":"North Test","adm0_a3":"TST","wikidataid":"Q2","scalerank":2.0},
	  "geometry":{"type":"Polygon","coordinates":[[[0,5],[10,5],[10,10],[0,10],[0,5]]]}},
	 {"type":"Feature","properties":{"NAME":"Plain"},
	  "geometry":{"type":"Polygon","coordinates":[[[20,0],[30,0],[30,10],[20,10],[20,0]]]}}]}`
	set, err := boundary.Read(strings.NewReader(doc), "country", false)
	if err != nil {
		t.Fatal(err)
	}
	areas := set.Areas()
	if len(areas) != 3 {
		t.Fatalf("read %d areas, want 3", len(areas))
	}
	for i, want := range []struct {
		country, wikidata string
		rank              int
	}{{"TST", "Q1", 1}, {"TST", "Q2", 2}, {"", "", 0}} {
		a := areas[i]
		if a.Country != want.country || a.Wikidata != want.wikidata || a.Rank != want.rank {
			t.Errorf("%s: country %q, wikidata %q, rank %d; want %q, %q, %d", a.Name, a.Country, a.Wikidata, a.Rank, want.country, want.wikidata, want.rank)
		}
	}
	if a := areas[0]; !a.HasLabel || a.Label != (boundary.Coord{Lat: 5.5, Lon: 4.5}) {
		t.Errorf("Testland's label point is %v (given %v), want 5.5, 4.5", a.Label, a.HasLabel)
	}
	if areas[2].HasLabel {
		t.Error("Plain has a label point it was not given")
	}
	if !areas[1].Contains(7, 5) || areas[1].Contains(3, 5) {
		t.Error("North Test does not hold exactly its own half")
	}
}
