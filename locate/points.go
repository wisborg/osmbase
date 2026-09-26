package locate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

// Tile names one tile.
type Tile struct {
	Z    uint8
	X, Y uint32
}

// PlacePoint is a place the tiles mark with a point: a city, a town, a
// village. It has a position and no outline.
type PlacePoint struct {
	// Names are the feature's own name first, then its English one when
	// that differs -- "København", then "Copenhagen" -- since a person may
	// type either.
	Names []string
	// Kind is the schema's kind_detail: city, town, village, hamlet.
	Kind     string
	Lat, Lon float64
}

// LocalityZoom is the zoom the tiles are read at for localities, which is
// where Locality's own lookups read them.
func LocalityZoom() uint8 {
	z, _ := Locality.Zoom()
	return z
}

// PlacePoints reads the localities marked in the given tiles.
//
// For a search by name, which is the opposite question from At's: not which
// place is near a coordinate, but where a named place is. Tiles at other
// zooms than LocalityZoom are read too but hold a different selection of
// places, so a caller wanting localities passes LocalityZoom's tiles.
//
// A place near a tile's edge is carried in the buffer of its neighbours as
// well, so the same town arrives several times; one name within a kilometre
// of a place already found is that place again, and is kept once.
func PlacePoints(src TileSource, tiles []Tile) ([]PlacePoint, error) {
	spec, _ := tileSpec(Locality)
	var out []PlacePoint
	for _, t := range tiles {
		feats, err := readLayer(src, tileRef{z: t.Z, x: t.X, y: t.Y}, spec.layer)
		if err != nil {
			return nil, err
		}
		if len(feats) == 0 {
			continue
		}
		tr, err := mercator.NewTileTransform(t.Z, t.X, t.Y, mvt.DefaultExtent)
		if err != nil {
			return nil, fmt.Errorf("locate: tile %d/%d/%d: %w", t.Z, t.X, t.Y, err)
		}
		for i := range feats {
			f := &feats[i]
			if !kindMatches(f, spec.kinds) || len(f.Geometry.Points) == 0 {
				continue
			}
			name, ok := preferredName(f, "")
			if !ok {
				continue
			}
			p := PlacePoint{Names: []string{name}}
			if en, ok := textTag(f, "name:en"); ok && en != "" && !strings.EqualFold(en, name) {
				p.Names = append(p.Names, en)
			}
			p.Kind, _ = textTag(f, "kind_detail")
			pt := f.Geometry.Points[0]
			p.Lon, p.Lat = tr.LonLat(pt.X, pt.Y)
			if slices.ContainsFunc(out, func(q PlacePoint) bool {
				return q.Names[0] == name && haversineM(q.Lat, q.Lon, p.Lat, p.Lon) < 1000
			}) {
				continue
			}
			out = append(out, p)
		}
	}
	return out, nil
}
