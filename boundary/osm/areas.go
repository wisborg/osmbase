package osm

import (
	"fmt"
	"slices"
	"strconv"
	"time"

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
	// Complete is the number of boundaries that produced a usable outline
	// and had nothing left over; Partial produced one and had gaps as well;
	// Unclosed produced none.
	//
	// Unclosed counts two different things and part 7 should say so: a
	// boundary whose ways never closed, and one whose only outlines closed
	// and then had to be left out for crossing the antimeridian. Both mean
	// "no geometry to test a point against", which is what the caller acts
	// on; WrappedRings is how the second is told apart.
	//
	// The three always sum to the number of boundaries handed in.
	Complete, Partial, Unclosed int

	// Outlines and Holes are the rings kept: the areas enclosed, and the
	// holes actually cut out of one of them.
	Outlines, Holes int

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

		outers := keep(rings.Outer, &rep.WrappedRings)
		holes := keep(rings.Inner, &rep.WrappedRings)

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

		polys, orphans, err := boundary.Polygons(outers, holes)
		if err != nil {
			return nil, rep, fmt.Errorf("osm: pairing the holes of %q: %w", b.Name, err)
		}
		rep.Outlines += len(outers)
		// Counted from what was attached rather than by subtracting what was
		// not. The subtraction was right only while nothing else could drop
		// a hole, which is a thing to notice rather than a thing to rely on.
		for _, p := range polys {
			rep.Holes += len(p.Holes)
		}
		rep.OrphanHoles += len(orphans)

		areas = append(areas, boundary.NewArea(b.Name, strconv.Itoa(b.AdminLevel), polys))
	}
	return areas, rep, nil
}

// keep converts the rings a boundary can be tested against, counting the
// ones that wrap the antimeridian and leaving them out. See boundary.Wraps:
// this pipeline cannot reason about a wrapped ring, so it says so rather
// than handing it to a test that cannot see the problem.
func keep(rs []Ring, wrapped *int) []boundary.Ring {
	out := make([]boundary.Ring, 0, len(rs))
	for _, r := range rs {
		b := toRing(r)
		if boundary.Wraps(b) {
			*wrapped++
			continue
		}
		out = append(out, b)
	}
	return out
}

func toRing(r Ring) boundary.Ring {
	out := make(boundary.Ring, len(r))
	for i, p := range r {
		out[i] = boundary.Coord{Lat: p.Lat, Lon: p.Lon}
	}
	return out
}

// Attribution is the credit the ODbL requires of anything built from
// OpenStreetMap.
//
// A constant here, and the only spelling of it, because the derived file
// carries it as DATA and the obligation is real: a derived boundary file is
// a Derivative Database rather than a Produced Work, so share-alike attaches
// to the file and whoever receives it must receive it under the ODbL. A
// caller assembling its own credit string is a caller that can get it wrong
// or leave it out.
const Attribution = "© OpenStreetMap contributors, ODbL"

// Provenance describes a file built from an OSM extract, with the credit
// already filled in.
func Provenance(source string, levels []int) boundary.Provenance {
	return boundary.Provenance{
		Source:      source,
		Attribution: Attribution,
		Created:     time.Now().UTC(),
		Levels:      slices.Clone(levels),
	}
}
