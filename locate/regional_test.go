package locate_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/locate"
)

// A store holding boundaries for one country and tiles for the world, which
// is what a user who fetched a map and then built Sydney's suburbs has.
//
// Through the real boundary source rather than a fake, because the defect
// was in how the two halves fit: the source covered locality everywhere
// because one file covered it somewhere, AtEach took a covered level as
// answered by containment alone, and a coordinate in Horsens lost "near
// Horsens" for nothing. Each half passed its own tests.
func TestARegionalBoundaryFileLeavesTheRestOfTheWorldToTheTiles(t *testing.T) {
	const horsensLat, horsensLon = 55.8623, 9.8451
	src := withPlaces(t, 10, place{lat: horsensLat + 0.01, lon: horsensLon, kind: "locality", name: "Horsens"})

	root := t.TempDir()
	if err := os.MkdirAll(boundary.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := boundary.DerivedFile("sydney")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(boundary.Dir(root), name))
	if err != nil {
		t.Fatal(err)
	}
	shire := boundary.NewArea("Hornsby Shire", "6", []boundary.Polygon{{Outer: boundary.Ring{
		{Lat: -34, Lon: 151}, {Lat: -34, Lon: 152}, {Lat: -33, Lon: 152}, {Lat: -33, Lon: 151}}}})
	if err := boundary.WriteDerived(f, boundary.NewSet(boundary.Provenance{Source: "x"}, []boundary.Area{shire})); err != nil {
		t.Fatal(err)
	}
	f.Close()

	places, err := locate.AtEach(context.Background(), src,
		[]locate.Coord{{Lat: horsensLat, Lon: horsensLon}, {Lat: -33.5, Lon: 151.5}},
		locate.Options{Levels: []locate.Level{locate.Locality}, Boundaries: boundary.Open(root, "")})
	if err != nil {
		t.Fatalf("AtEach: %v", err)
	}

	if m, ok := places[0].Match(locate.Locality); !ok || m.Name != "Horsens" || m.Source != locate.Near {
		t.Errorf("Horsens got %+v, want the tiles' nearest locality: the Sydney file knows nothing about it", places[0].Matches)
	}
	if m, ok := places[1].Match(locate.Locality); !ok || m.Name != "Hornsby Shire" || m.Source != locate.Contained {
		t.Errorf("Hornsby got %+v, want the contained council area", places[1].Matches)
	}
}
