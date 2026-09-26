package osm

import (
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
)

// A city's or a town's extent -- boundary=place with place=city or town --
// is kept beside the administrative boundaries, whatever --levels asks for,
// since it is no administrative level. Its kind is the place. Any other
// place boundary is not a city and is not kept.
func TestACitysExtentIsKept(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.7, 9.6).Node(3, 55.8, 9.6).Node(4, 55.8, 9.5).
		Way(10, []int64{1, 2, 3, 4, 1})
	members := []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}}
	e.Relation(100, members, "boundary", "place", "place", "city", "name", "Big City")
	e.Relation(101, members, "boundary", "place", "place", "town", "name", "Small Town")
	e.Relation(102, members, "boundary", "place", "place", "region", "name", "The Middle")
	e.Relation(103, members, "boundary", "place", "place", "suburb", "name", "A Suburb")
	e.Relation(104, members, "boundary", "administrative", "admin_level", "8", "name", "A Council")
	e.Relation(105, members, "type", "multipolygon", "place", "city", "name", "Not A Boundary")

	got, err := Read(from(e.Bytes()), Options{Levels: []int{10}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	places := map[string]string{}
	for _, b := range got {
		places[b.Name] = b.Place
	}
	if len(got) != 2 || places["Big City"] != "city" || places["Small Town"] != "town" {
		t.Errorf("kept %v; want the city and the town only, with their places", places)
	}

	areas, _, err := Areas(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range areas {
		if want := places[a.Name]; a.Kind != want {
			t.Errorf("%s has kind %q, want %q", a.Name, a.Kind, want)
		}
	}
}
