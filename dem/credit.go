package dem

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

// Attribution is one elevation source's entry in Mapterhorn's source list,
// attribution.json: who made the data and under what licence.
type Attribution struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	Producer string `json:"producer"`
	License  string `json:"license"`
	Website  string `json:"website"`
}

// ReadAttributions reads a Mapterhorn source list.
func ReadAttributions(r io.Reader) ([]Attribution, error) {
	var out []Attribution
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("dem: reading the list of elevation sources: %w", err)
	}
	return out, nil
}

// CompiledBy is the credit owed to Mapterhorn itself, for assembling the
// sources into tiles, ahead of the sources' own.
const CompiledBy = "Mapterhorn"

// SourcesPage is where Mapterhorn lists every source with its licence and
// the notice it asks for.
const SourcesPage = "mapterhorn.com/attribution"

// ShortCredit is the elevation credit for the image itself, given the
// sources under it: Mapterhorn, and where every source is listed.
//
// It is what Mapterhorn's own map shows -- "© Mapterhorn", linked to that
// list -- with the address written out, since a picture carries no link.
// The full notice, Copernicus's dictated sentence among it, is Credit's,
// and belongs with whoever publishes the picture: in a caption, a video's
// description, a credits page. The GLO-30 licence asks for its notice to
// be given to the public and does not say where; a sentence-long credit
// burnt into every frame was the strictest answer, and cost two lines of
// every map.
//
// No ids is no credit, as for Credit.
func ShortCredit(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return "Elevation: © " + CompiledBy + ", " + SourcesPage
}

// Copernicus GLO-30's licence asks for a sentence of its own on anything
// made from the data -- a hillshade is -- in place of a producer's name, and
// for a disclaimer in any notice covering distribution. Both are quoted from
// the licence, word for word; see docs/elevation.md. The disclaimer is for a
// program's notices and README rather than for the image.
const (
	glo30         = "glo30"
	GLO30Notice   = "produced using Copernicus WorldDEM-30 © DLR e.V. 2010-2014 and © Airbus Defence and Space GmbH 2014-2018 provided under COPERNICUS by the European Union and ESA; all rights reserved"
	GLO30Disclaim = "The organisations in charge of the Copernicus programme by law or by delegation do not incur any liability for any use of the Copernicus WorldDEM-30"
)

// Credit is the full notice a map owes for elevation from the sources named by ids,
// with attrs saying who each is: Mapterhorn, then each source's producer and
// licence in the order given. A source missing from attrs is credited by its
// id, which is less than it is owed and more than nothing -- and is what
// says the list is out of date. Sources credited in the same words are
// credited once. No ids is no credit.
func Credit(ids []string, attrs []Attribution) string {
	if len(ids) == 0 {
		return ""
	}
	parts := []string{"Elevation: " + CompiledBy}
	for _, id := range ids {
		if id == glo30 {
			parts = append(parts, GLO30Notice)
			continue
		}
		i := slices.IndexFunc(attrs, func(a Attribution) bool { return a.Source == id })
		if i < 0 {
			parts = append(parts, id)
			continue
		}
		a := attrs[i]
		s := cleanCredit(a.Producer)
		if s == "" {
			s = cleanCredit(a.Name)
		}
		if l := cleanCredit(a.License); l != "" {
			s += " (" + l + ")"
		}
		// Several datasets of one producer under one licence -- a national
		// survey's, region by region -- are credited once: the same words
		// again say nothing more, and a long flight's notice named the
		// U.S. Geological Survey seventeen times.
		if !slices.Contains(parts, s) {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

func cleanCredit(s string) string { return strings.Join(strings.Fields(s), " ") }

// CoverageLayer and CoverageTag are where Mapterhorn's coverage tiles name a
// polygon's source.
const (
	CoverageLayer = "coverage"
	CoverageTag   = "source"
)

// SourcesIn is the elevation sources whose coverage polygons reach into the
// rectangle west, south, east, north (degrees), read from coverage tiles at
// zoom z or the nearest shallower zoom held. The ids come back sorted, finest
// first as the coverage lists them, so the same view gives the same credit.
//
// A polygon reaches the rectangle when one of its edges crosses it or the
// rectangle is inside it, holes and all. A bounding box would be simpler and
// is wrong in the case that matters: the worldwide fallback is one polygon
// with a hole wherever finer data exists, so its box covers every view on
// earth, and every map would carry Copernicus's sentence-long notice for
// data it did not draw.
func SourcesIn(coverage TileSource, z uint8, west, south, east, north float64) ([]string, error) {
	x0, y0, err := mercator.TileAt(z, west, north)
	if err != nil {
		return nil, err
	}
	x1, y1, err := mercator.TileAt(z, east, south)
	if err != nil {
		return nil, err
	}
	vx0, vy0 := mercator.Project(west, north)
	vx1, vy1 := mercator.Project(east, south)

	seen := map[string]bool{}
	read := map[[3]uint32]bool{}
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			// Walk up for a tile the store lacks; an ancestor stands in for
			// several of the view's tiles and is read once.
			ref := [3]uint32{uint32(z), tx, ty}
			var data []byte
			for {
				d, ok, err := coverage.Tile(uint8(ref[0]), ref[1], ref[2])
				if err != nil {
					return nil, fmt.Errorf("dem: reading coverage tile %d/%d/%d: %w", ref[0], ref[1], ref[2], err)
				}
				if ok {
					data = d
					break
				}
				if ref[0] == 0 {
					break
				}
				ref = [3]uint32{ref[0] - 1, ref[1] >> 1, ref[2] >> 1}
			}
			if data == nil || read[ref] {
				continue
			}
			read[ref] = true
			t, err := mvt.Decode(data)
			if err != nil {
				return nil, fmt.Errorf("dem: coverage tile %d/%d/%d: %w", ref[0], ref[1], ref[2], err)
			}
			l, ok := t.Layer(CoverageLayer)
			if !ok {
				continue
			}
			n := math.Exp2(float64(ref[0]))
			scale := 1 / (n * float64(l.Extent))
			ox, oy := float64(ref[1])/n, float64(ref[2])/n
			for _, f := range l.Features {
				v, ok := f.Tag(CoverageTag)
				if !ok || v.Kind != mvt.ValueString || seen[v.Str] {
					continue
				}
				// The view in this tile's own units.
				r := rect{(vx0 - ox) / scale, (vy0 - oy) / scale, (vx1 - ox) / scale, (vy1 - oy) / scale}
				for _, p := range f.Geometry.Polygons {
					if r.meets(p) {
						seen[v.Str] = true
						break
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return SortSources(out), nil
}

// SortSources puts source ids in the order a credit names them, in place,
// and returns them: by id, with glo30 -- the worldwide fallback -- last, so
// the credit leads with the data that actually shaped the view where there
// is finer. SourcesIn's order; for a caller putting the sources of several
// views together into one credit.
func SortSources(ids []string) []string {
	slices.SortFunc(ids, func(a, b string) int {
		switch {
		case a == glo30 && b != glo30:
			return 1
		case b == glo30 && a != glo30:
			return -1
		}
		return strings.Compare(a, b)
	})
	return ids
}

// rect is an axis-aligned rectangle, x0 <= x1 and y0 <= y1.
type rect struct{ x0, y0, x1, y1 float64 }

// meets reports whether the polygon and the rectangle share any area: an
// edge of the polygon, its holes' included, crosses the rectangle, or the
// rectangle lies inside the polygon. An edge of a hole crossing it is
// enough, since the polygon is on one side of that edge.
func (r rect) meets(p mvt.Polygon) bool {
	rings := append([]mvt.Ring{p.Exterior}, p.Holes...)
	for _, ring := range rings {
		for i := range ring {
			a, b := ring[i], ring[(i+1)%len(ring)]
			if r.crossedBy(float64(a.X), float64(a.Y), float64(b.X), float64(b.Y)) {
				return true
			}
		}
	}
	// No edge crosses it, so the rectangle is wholly inside or wholly
	// outside; one corner says which.
	inside := false
	for _, ring := range rings {
		if contains(ring, r.x0, r.y0) {
			inside = !inside
		}
	}
	return inside
}

// crossedBy reports whether the segment from (ax, ay) to (bx, by) touches
// the rectangle: Liang-Barsky clipping, kept to its yes or no.
func (r rect) crossedBy(ax, ay, bx, by float64) bool {
	t0, t1 := 0.0, 1.0
	dx, dy := bx-ax, by-ay
	for _, e := range [4][2]float64{{-dx, ax - r.x0}, {dx, r.x1 - ax}, {-dy, ay - r.y0}, {dy, r.y1 - ay}} {
		p, q := e[0], e[1]
		if p == 0 {
			if q < 0 {
				return false
			}
			continue
		}
		t := q / p
		if p < 0 {
			t0 = math.Max(t0, t)
		} else {
			t1 = math.Min(t1, t)
		}
		if t0 > t1 {
			return false
		}
	}
	return true
}

// contains reports whether the point is inside the ring, by the even-odd
// rule.
func contains(ring mvt.Ring, x, y float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		xi, yi := float64(ring[i].X), float64(ring[i].Y)
		xj, yj := float64(ring[j].X), float64(ring[j].Y)
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}
