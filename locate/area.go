package locate

import (
	"fmt"
	"math"
	"sort"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

// tileCache decodes each tile once for the length of one lookup.
type tileCache struct {
	src   TileSource
	tiles map[tileRef]*mvt.Tile
}

func (c *tileCache) get(ref tileRef) (*mvt.Tile, error) {
	if t, ok := c.tiles[ref]; ok {
		return t, nil
	}
	raw, ok, err := c.src.Tile(ref.z, ref.x, ref.y)
	if err != nil {
		return nil, fmt.Errorf("locate: reading tile %d/%d/%d: %w", ref.z, ref.x, ref.y, err)
	}
	var t *mvt.Tile
	if ok {
		d, err := mvt.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("locate: decoding tile %d/%d/%d: %w", ref.z, ref.x, ref.y, err)
		}
		t = &d
	}
	c.tiles[ref] = t
	return t, nil
}

// areasAt answers the Area level for the points idx: the smallest named area
// polygon in the tiles that holds each one.
//
// The schema does not name its landuse polygons. The name is on a label point
// in the pois layer that carries the SAME feature identifier as the polygon --
// both are the one OpenStreetMap object, drawn twice -- so the polygon says
// where the park is and the label says what it is called. The label is looked
// for in the polygon's own tile and then its eight neighbours, since a large
// park's label is where the park's middle is, and that may be a tile away
// from the part of it a course runs through.
//
// The smallest holding polygon wins: a golf course inside a park is the golf
// course. A polygon with no label to be found is passed over for the next
// smallest, rather than answered with no name.
func areasAt(src TileSource, spec levelSpec, idx []int, pts []Coord, opts Options, out []Place) error {
	cache := &tileCache{src: src, tiles: map[tileRef]*mvt.Tile{}}
	byTile := map[tileRef][]int{}
	for _, i := range idx {
		x, y, err := mercator.TileAt(spec.zoom, pts[i].Lon, pts[i].Lat)
		if err != nil {
			continue
		}
		ref := tileRef{spec.zoom, x, y}
		byTile[ref] = append(byTile[ref], i)
	}
	labels := map[uint64]string{}
	for ref, points := range byTile {
		t, err := cache.get(ref)
		if err != nil {
			return err
		}
		if t == nil {
			continue
		}
		l, ok := t.Layer(spec.layer)
		if !ok {
			continue
		}
		n := float64(uint32(1) << ref.z)
		for _, i := range points {
			px, py := mercator.Project(pts[i].Lon, pts[i].Lat)
			x := (px*n - float64(ref.x)) * float64(l.Extent)
			y := (py*n - float64(ref.y)) * float64(l.Extent)

			type candidate struct {
				f    *mvt.Feature
				size float64
			}
			var cands []candidate
			for k := range l.Features {
				f := &l.Features[k]
				if f.Type != mvt.GeomPolygon || !f.HasID || !kindMatches(f, spec.kinds) {
					continue
				}
				if size, in := holds(f.Geometry.Polygons, x, y); in {
					cands = append(cands, candidate{f, size})
				}
			}
			sort.SliceStable(cands, func(a, b int) bool { return cands[a].size < cands[b].size })
			for _, c := range cands {
				name, ok := labels[c.f.ID]
				if !ok {
					if name, err = labelFor(cache, ref, c.f.ID, opts.Language); err != nil {
						return err
					}
					labels[c.f.ID] = name
				}
				if name == "" {
					continue
				}
				kind, _ := textTag(c.f, "kind")
				out[i].setMatch(Match{Level: spec.level, Name: name, Kind: kind, Source: Within})
				break
			}
		}
	}
	return nil
}

// labelFor is the name on the pois label sharing a polygon's identifier, in
// its tile or a neighbour, or "" when there is none.
func labelFor(cache *tileCache, ref tileRef, id uint64, lang string) (string, error) {
	n := int64(1) << ref.z
	for _, d := range [][2]int64{{0, 0}, {-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
		ny := int64(ref.y) + d[1]
		if ny < 0 || ny >= n {
			continue
		}
		nb := tileRef{ref.z, uint32((int64(ref.x) + d[0] + n) % n), uint32(ny)}
		t, err := cache.get(nb)
		if err != nil {
			return "", err
		}
		if t == nil {
			continue
		}
		l, ok := t.Layer("pois")
		if !ok {
			continue
		}
		for k := range l.Features {
			f := &l.Features[k]
			if f.HasID && f.ID == id {
				if name, ok := preferredName(f, lang); ok {
					return name, nil
				}
			}
		}
	}
	return "", nil
}

// holds reports whether any of polygons holds the point, and the size of the
// one that does, in square tile units.
func holds(polygons []mvt.Polygon, x, y float64) (float64, bool) {
	for _, p := range polygons {
		if !inRing(p.Exterior, x, y) {
			continue
		}
		inHole := false
		for _, h := range p.Holes {
			if inRing(h, x, y) {
				inHole = true
				break
			}
		}
		if !inHole {
			return ringArea(p.Exterior), true
		}
	}
	return 0, false
}

// inRing is the even-odd test: a ray from the point crosses the ring's edges
// an odd number of times exactly when the point is inside.
func inRing(r mvt.Ring, x, y float64) bool {
	in := false
	for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
		ax, ay := float64(r[i].X), float64(r[i].Y)
		bx, by := float64(r[j].X), float64(r[j].Y)
		if (ay > y) != (by > y) && x < (bx-ax)*(y-ay)/(by-ay)+ax {
			in = !in
		}
	}
	return in
}

func ringArea(r mvt.Ring) float64 {
	var a float64
	for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
		a += float64(r[j].X)*float64(r[i].Y) - float64(r[i].X)*float64(r[j].Y)
	}
	return math.Abs(a) / 2
}
