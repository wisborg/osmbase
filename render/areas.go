package render

import (
	"context"
	"fmt"
	"math"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

// Areas are how large the areas a style stacks are, each measured as a
// whole over a fixed region: what Options.Areas ranks overlapping areas by,
// so that every view of the region stacks them alike. See stack.go.
type Areas struct {
	// byID is each area's size in Web Mercator's world units squared, by
	// feature id. An area without an id cannot be told apart from tile to
	// tile, so it is not measured, and every view ranks it by what it
	// holds of it.
	byID map[uint64]float64
}

// Len is how many areas were measured.
func (a *Areas) Len() int {
	if a == nil {
		return 0
	}
	return len(a.byID)
}

// of is a's measured size, when there is one.
func (a *Areas) of(s *stackArea) (float64, bool) {
	if a == nil || !s.hasID {
		return 0, false
	}
	w, ok := a.byID[s.key]
	return w, ok
}

// MeasureAreas measures every area o's style stacks within b, from src's
// tiles at zoom z, as Options.Areas wants them.
//
// Each tile's piece of an area is clipped to the tile's own square and the
// pieces are added up by feature id, so an area is measured whole however
// many tiles it spans -- as far as b reaches: an area running beyond b is
// measured as much of it as b holds, which is enough for every view inside
// b to agree. z is usually the deepest zoom the views will be drawn at,
// where the areas are least generalised; a tile src lacks is measured from
// its nearest ancestor, as a render would draw it.
func MeasureAreas(ctx context.Context, src TileSource, o Options, b Bounds, z uint8) (*Areas, error) {
	r, err := New(src, o)
	if err != nil {
		return nil, err
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	if z > mercator.MaxZoom {
		return nil, fmt.Errorf("render: measuring areas at zoom %d, deeper than %d", z, mercator.MaxZoom)
	}
	// A view of b drawn at exactly zoom z, never rasterized: it is only the
	// frame the pieces are clipped and measured in.
	x0, y0 := mercator.Project(b.West, b.North)
	x1, y1 := mercator.Project(b.East, b.South)
	world := tileSize * math.Exp2(float64(z))
	p, err := resolve(View{Bounds: b, Width: max(1, int(math.Ceil((x1-x0)*world))), Height: max(1, int(math.Ceil((y1-y0)*world)))})
	if err != nil {
		return nil, err
	}
	p.tileZoom = z

	// The stacked rules, by layer: a feature is measured when a rule that
	// could stack it selects it.
	stacked := map[string][]*Rule{}
	for i := range r.style.Rules {
		if rule := &r.style.Rules[i]; stackable(rule) {
			stacked[rule.Layer] = append(stacked[rule.Layer], rule)
		}
	}

	out := &Areas{byID: map[uint64]float64{}}
	d := drawer{p: p}
	decoded := map[tileRef]*mvt.Tile{}
	tx0, ty0, tx1, ty1 := p.tileRange(z)
	for ty := ty0; ty <= ty1; ty++ {
		for tx := tx0; tx <= tx1; tx++ {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("render: measuring areas at zoom %d: %w", z, err)
			}
			want := tileRef{z: z, x: tx, y: ty}
			ref, data, ok, err := r.walkUp(want)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			tile, seen := decoded[ref]
			if !seen {
				t, err := mvt.Decode(data)
				if err != nil {
					return nil, fmt.Errorf("render: decoding tile %s: %w", ref, err)
				}
				tile, decoded[ref] = &t, &t
			}
			// The wanted tile's own square, also for an ancestor's geometry
			// drawn in its place: the ancestor's pieces reach over its
			// neighbours' squares too, and are counted there by them.
			square := p.tileBox(want.z, want.x, want.y)
			for name, rules := range stacked {
				layer, ok := tile.Layer(name)
				if !ok {
					continue
				}
				extent := layer.Extent
				if extent == 0 {
					extent = mvt.DefaultExtent
				}
				tr := p.tileTransform(ref.z, ref.x, ref.y, extent)
				for fi := range layer.Features {
					f := &layer.Features[fi]
					if f.Type != mvt.GeomPolygon || !f.HasID {
						continue
					}
					selected := false
					for _, rule := range rules {
						if rule.matches(f) {
							selected = true
							break
						}
					}
					if !selected {
						continue
					}
					_, area := d.clipPiece(f.Geometry.Polygons, tr, square)
					out.byID[f.ID] += area / (p.scale * p.scale)
				}
			}
		}
	}
	return out, nil
}
