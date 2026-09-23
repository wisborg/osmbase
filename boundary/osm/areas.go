package osm

import (
	"fmt"
	"math"
	"strconv"

	"github.com/wisborg/osmbase/boundary"
)

// Report says what became of the boundaries an extract held.
//
// Returned alongside the areas rather than logged, because every number in it
// is a fact about the extract rather than about the code, and the command
// that builds the file is the thing that should say so. A region whose
// outlines mostly did not close is one whose extract was cut through them,
// and the person who chose the extract is the one who can act on it.
type Report struct {
	// Complete is the number of boundaries that produced at least one ring
	// and had nothing left over; Partial produced rings and gaps; Unclosed
	// produced no ring at all.
	Complete, Partial, Unclosed int

	// Rings and Holes are the outlines and the holes in them.
	Rings, Holes int

	// OrphanHoles is holes that lie inside no outline, which happens when
	// the outline was cut off by the edge of the extract.
	OrphanHoles int

	// WrappedRings is rings that appear to cross the antimeridian. See
	// Areas: this pipeline cannot draw them correctly and says so.
	WrappedRings int
}

// Areas turns assembled boundaries into areas ready to write.
//
// Kind is the admin_level as a decimal string rather than a word, and that is
// deliberate: the level's MEANING differs by country. Seven is the
// municipality in Denmark and nine is the suburb in Australia, so naming them
// here would be asserting one country's scheme over every other. Mapping a
// level to the level a caller asked for belongs where the country is known.
//
// # The antimeridian
//
// boundary.inRing treats longitude as linear and says so, with the note that
// a source which does not pre-split a ring crossing the seam "would be wrong
// in a way this cannot detect, so a new BoundarySource has to be checked for
// it". This is that new source, so it is checked here: a ring with a step of
// more than 180 degrees between consecutive vertices has wrapped, and those
// are counted and reported rather than quietly handed to a test that cannot
// see them.
func Areas(bs []Boundary) ([]boundary.Area, Report, error) {
	var rep Report
	areas := make([]boundary.Area, 0, len(bs))

	for _, b := range bs {
		rings, err := Assemble(b.Ways)
		if err != nil {
			return nil, rep, fmt.Errorf("osm: assembling %q: %w", b.Name, err)
		}

		outers := make([]boundary.Ring, 0, len(rings.Outer))
		for _, r := range rings.Outer {
			if wraps(r) {
				rep.WrappedRings++
				continue
			}
			outers = append(outers, toRing(r))
		}
		holes := make([]boundary.Ring, 0, len(rings.Inner))
		for _, r := range rings.Inner {
			if wraps(r) {
				rep.WrappedRings++
				continue
			}
			holes = append(holes, toRing(r))
		}

		// Classified after the unusable rings are filtered out, not before:
		// a boundary whose only outline wrapped the seam has no outline, and
		// counting it complete and taking the count back afterwards is a way
		// to get the bookkeeping wrong once and never notice.
		switch {
		case len(outers) == 0:
			// No outline means nothing to test a point against. Dropped
			// rather than written as a nameless hole in the map; the count
			// is how it is reported.
			rep.Unclosed++
			continue
		case len(rings.Open) == 0:
			rep.Complete++
		default:
			rep.Partial++
		}

		polys, orphans := boundary.Polygons(outers, holes)
		rep.Rings += len(outers)
		rep.Holes += len(holes) - len(orphans)
		rep.OrphanHoles += len(orphans)

		areas = append(areas, boundary.NewArea(b.Name, strconv.Itoa(b.AdminLevel), polys))
	}
	return areas, rep, nil
}

func toRing(r Ring) boundary.Ring {
	out := make(boundary.Ring, len(r))
	for i, p := range r {
		out[i] = boundary.Coord{Lat: p.Lat, Lon: p.Lon}
	}
	return out
}

// wraps reports whether a ring appears to cross the antimeridian.
//
// A step of more than half the world between consecutive vertices is not a
// step: boundary vertices are metres apart, so it is the seam. The ring would
// need unwrapping to be tested against, and this pipeline does not do that --
// counting it is how the gap stays visible instead of becoming a boundary
// that quietly contains the wrong half of the planet.
func wraps(r Ring) bool {
	for i := range r {
		j := (i + 1) % len(r)
		if math.Abs(r[j].Lon-r[i].Lon) > 180 {
			return true
		}
	}
	return false
}
