package locate_test

import (
	"testing"

	"github.com/wisborg/osmbase/locate"
	"github.com/wisborg/osmbase/mercator"
)

func tilesOf(t *testing.T, src map[[3]uint32][]byte) []locate.Tile {
	t.Helper()
	var out []locate.Tile
	for k := range src {
		out = append(out, locate.Tile{Z: uint8(k[0]), X: k[1], Y: k[2]})
	}
	return out
}

// Localities come back with their names, kind and position; other kinds of
// place do not.
func TestPlacePointsReadsTheLocalitiesInTiles(t *testing.T) {
	z := locate.LocalityZoom()
	src := withPlaces(t, z,
		place{lat: 55.8623, lon: 9.8451, kind: "locality", detail: "city", name: "Horsens"},
		place{lat: 55.6761, lon: 12.5683, kind: "locality", detail: "city", name: "København", names: map[string]string{"en": "Copenhagen"}},
		place{lat: 55.87, lon: 9.86, kind: "neighbourhood", name: "Østbyen"},
	)
	got, err := locate.PlacePoints(src, tilesOf(t, src))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]locate.PlacePoint{}
	for _, p := range got {
		byName[p.Names[0]] = p
	}
	if len(got) != 2 {
		t.Errorf("got %d places, want the two localities: %+v", len(got), got)
	}
	h, ok := byName["Horsens"]
	if !ok || h.Kind != "city" || abs(h.Lat-55.8623) > 0.01 || abs(h.Lon-9.8451) > 0.01 {
		t.Errorf("Horsens = %+v", h)
	}
	if k := byName["København"]; len(k.Names) != 2 || k.Names[1] != "Copenhagen" {
		t.Errorf("København's names = %v, want the English one too", k.Names)
	}
}

// A place near a tile edge is carried in its neighbours' buffers too; it is
// one place.
func TestPlacePointsKeepsAPlaceInTwoTilesOnce(t *testing.T) {
	z := locate.LocalityZoom()
	// Straddling a tile boundary by a hundred metres either side.
	x, _, _ := mercator.TileAt(z, 9.8451, 55.8623)
	w, _, _, _, _ := mercator.TileBounds(z, x, 0)
	src := withPlaces(t, z,
		place{lat: 55.8623, lon: w - 0.001, kind: "locality", name: "Edge"},
		place{lat: 55.8623, lon: w + 0.001, kind: "locality", name: "Edge"},
	)
	if len(src) != 2 {
		t.Fatalf("precondition: the fixture is in %d tiles, want 2", len(src))
	}
	got, _ := locate.PlacePoints(src, tilesOf(t, src))
	if len(got) != 1 {
		t.Errorf("got %d places, want Edge once", len(got))
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
