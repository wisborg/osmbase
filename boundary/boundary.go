// Package boundary answers which named area contains a coordinate.
//
// It exists because the vector tiles cannot. Every named feature in those
// tiles is a point or a line -- the boundaries layer carries an admin level
// and no name, and landuse polygons carry a kind and no name -- so a lookup
// against tiles alone can only report the nearest label anchor, which is a
// different claim from containment. See docs/locate.md.
//
// The data is Natural Earth, which is public domain: no attribution is
// required, nothing propagates to a consumer's output, and there is no NOTICE
// entry to keep. That is why it is the first boundary source rather than the
// best one. It reaches country and region and stops there; suburb needs
// OpenStreetMap's administrative relations, which is a later source behind the
// same interface.
package boundary

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Area is one named region with the geometry to test a point against.
type Area struct {
	Name string
	// Kind is the producer's own description -- "country", "state" -- passed
	// through rather than normalised.
	Kind string

	// polygons are the parts of the area, each with its own box.
	polygons []polygon

	// names is what a search by name can match, for the areas that came
	// from a file which says -- Natural Earth's. Nil for a derived area.
	names *placeNames
}

// polygon is one part of an area: the rings in GeoJSON order -- the first is
// the outline and any that follow are holes -- with the box that bounds it.
//
// A box PER PART rather than one for the whole area, and the reason is the
// antimeridian. Natural Earth splits a country that crosses it into separate
// parts either side, so Russia, Fiji and Antarctica each have parts at both
// -180 and +180 and an area-wide box spans the entire world. That box rejects
// nothing, so every lookup anywhere on earth walked all 214 of Russia's
// polygons. Per part, each box is small and the prefilter works for exactly
// the countries it previously gave up on.
type polygon struct {
	rings []Ring
	box   box
}

// box is a bounding rectangle in degrees.
//
// One type, because this package had four spellings of the same rule: the
// fields on polygon, the accumulation in newArea, the prefilter written
// inline in contains, and the degrees-squared product in boxArea. Two of
// them already disagreed about whether a polygon's box covers its holes as
// well as its outline -- which happened to be harmless, since a hole is
// inside its outline, and was nobody's decision.
type box struct{ west, south, east, north float64 }

// boxOf is the bounding rectangle of a ring.
func boxOf(r Ring) box {
	b := box{west: math.Inf(1), south: math.Inf(1), east: math.Inf(-1), north: math.Inf(-1)}
	return b.extend(r)
}

func (b box) extend(r Ring) box {
	for _, c := range r {
		b.west, b.east = math.Min(b.west, c.Lon), math.Max(b.east, c.Lon)
		b.south, b.north = math.Min(b.south, c.Lat), math.Max(b.north, c.Lat)
	}
	return b
}

// holds reports whether a coordinate is inside the rectangle. It is the
// prefilter that makes a lookup cheap: most of the work of one is NOT doing
// the point-in-polygon test, because a coordinate is outside all but one of
// two hundred countries.
func (b box) holds(lat, lon float64) bool {
	return lon >= b.west && lon <= b.east && lat >= b.south && lat <= b.north
}

// area is the rectangle's extent in degrees squared. A comparison between
// two boxes only, never a real area -- a degree of longitude is not a degree
// of latitude outside the equator.
func (b box) area() float64 { return (b.east - b.west) * (b.north - b.south) }

// Coord is a vertex, LATITUDE FIRST.
//
// The order matters more than it looks. This module carries a coordinate
// type per package -- this one, locate.Coord, osm.Point -- and a
// transposition between any two of them compiles, passes a positional
// literal, and puts a Danish municipality in the Indian Ocean. They all read
// latitude first, and this one used to read longitude first and be
// unexported besides. Collapsing the internal vertex and the exported one
// into a single type is what stops a conversion between them existing at
// all, which is one fewer place for the transposition to happen.
type Coord struct{ Lat, Lon float64 }

// Ring is a closed ring, with the first point NOT repeated at the end. That
// is what Area.contains assumes: it walks the edges with a wraparound rather
// than looking for a repeated first vertex.
type Ring []Coord

// Polygon is one part of an area: an outline and the holes in it.
type Polygon struct {
	Outer Ring
	Holes []Ring
}

// Set is the areas of one level, searchable by coordinate.
type Set struct {
	areas []Area

	// prov is what the file said about itself, empty for a set read from
	// GeoJSON or built in memory. See Provenance.
	prov Provenance
}

// Provenance returns what the set's source said about itself. The zero value
// means it said nothing, which is what a GeoJSON source does.
func (s *Set) Provenance() Provenance { return s.prov }

// Len is how many areas the set holds.
func (s *Set) Len() int { return len(s.areas) }

// Containing returns every area holding the coordinate, largest first.
//
// At answers "which one", which is the question for a set of countries,
// because they do not overlap. A set of administrative areas does overlap --
// deliberately, because it IS a hierarchy: a point in a suburb is also in the
// council area around it. Which of those is a "locality" and which a
// "neighbourhood" cannot be read off an admin_level, because the levels mean
// different things in different countries, so the caller needs the whole
// stack and the order it nests in.
func (s *Set) Containing(lat, lon float64) []Area {
	var out []Area
	for i := range s.areas {
		if s.areas[i].contains(lat, lon) {
			out = append(out, s.areas[i])
		}
	}
	sortOutermostFirst(out)
	return out
}

// sortOutermostFirst orders areas widest to narrowest.
func sortOutermostFirst(areas []Area) {
	// Stable, so two areas the comparison cannot tell apart keep the order
	// the file gave them rather than an arbitrary one.
	slices.SortStableFunc(areas, outermostFirst)
}

// outermostFirst compares two areas that both hold a point, wider first.
//
// One function because the rule is one rule: Source.Contains sorts the
// merged stack of several files again, and a comparator written twice is a
// comparator that can be refined once -- which this one has been.
//
// Two areas of the SAME extent are ordered by admin_level, lower first. That
// happens: a suburb that is the whole of its council area has the council
// area's outline, and before this the file's order decided which was the
// locality and which the neighbourhood. Lower-is-wider is OpenStreetMap's own
// convention in every country, unlike what any one number MEANS, so this is
// not the per-country table the ranking refuses to be. A kind that is not a
// number -- Natural Earth's "country", "state" -- leaves the tie to the file.
func outermostFirst(a, b Area) int {
	if c := cmp.Compare(b.boxArea(), a.boxArea()); c != 0 {
		return c
	}
	la, errA := strconv.Atoi(a.Kind)
	lb, errB := strconv.Atoi(b.Kind)
	if errA != nil || errB != nil {
		return 0
	}
	return cmp.Compare(la, lb)
}

// At returns the area containing a coordinate, and whether one did.
//
// The SMALLEST containing area wins, not the first. The admin layers do not
// overlap, so for them the two are the same thing -- but the marine layer
// nests, and heavily: the Tasman Sea is inside the South Pacific, which is
// inside nothing but sits earlier in the file. Taking the first reported a
// trans-Tasman flight as being over the Pacific Ocean, which is true and is
// not the answer anybody wanted.
//
// Smallest is measured by bounding-box area rather than by true area, which
// is cheap, already computed, and enough to order things that genuinely
// contain one another. It would be the wrong tool for ranking areas that
// merely overlap; nothing here does.
func (s *Set) At(lat, lon float64) (Area, bool) {
	best := -1
	bestSize := math.Inf(1)
	for i := range s.areas {
		if !s.areas[i].contains(lat, lon) {
			continue
		}
		if size := s.areas[i].boxArea(); size < bestSize {
			best, bestSize = i, size
		}
	}
	if best < 0 {
		return Area{}, false
	}
	return s.areas[best], true
}

// boxArea is the extent of the rectangle around all of an area's parts, for
// ordering areas that contain one another. Degrees squared: not a real area,
// and never compared against anything but another of these.
//
// The rectangle around the UNION of the parts, not the sum of each part's
// rectangle, and the difference decides rankings. Containment is what this
// orders by, and only the union is guaranteed to shrink with it: an area
// inside another has its whole outline inside the other's, so its rectangle
// is too. A sum has no such property -- a suburb in two parts whose
// rectangles overlap scored larger than the council area around it, and came
// back as the locality with its own council as the neighbourhood.
//
// An area with parts either side of the antimeridian -- Natural Earth splits
// such areas there -- gets a union as wide as the world. That makes it rank
// wider than its true size, but never wider than something that contains
// it: that area has parts either side of the seam too, so its union is just
// as wide and at least as tall.
//
// Parts with no extent are skipped. A polygon with no rings keeps the empty
// box its accumulation started from -- west +Inf, east -Inf -- and one of
// those beside a real polygon made the WHOLE area infinite: larger than the
// world, so it won every outermost-first ranking and lost to nothing in a
// smallest-first one. It is the same shape as the wrapped-coordinate case
// the derived format already refuses, with +Inf instead of a large number.
func (a *Area) boxArea() float64 {
	u, ok := a.extent()
	if !ok {
		return 0
	}
	return u.area()
}

// extent is the rectangle around every part of an area that has one, and
// false for an area none of whose parts do.
func (a *Area) extent() (box, bool) {
	u := boxOf(nil)
	for _, p := range a.polygons {
		if b := p.box.area(); math.IsInf(b, 0) || math.IsNaN(b) {
			continue
		}
		u.west, u.east = math.Min(u.west, p.box.west), math.Max(u.east, p.box.east)
		u.south, u.north = math.Min(u.south, p.box.south), math.Max(u.north, p.box.north)
	}
	return u, !math.IsInf(u.west, 0)
}

// contains is the point-in-polygon test, holes included.
//
// Ray casting: a point is inside a ring when a ray from it crosses the ring an
// odd number of times. Inside the outline and inside a hole is outside the
// area, which is what the inner loop subtracts.
func (a *Area) contains(lat, lon float64) bool {
	for _, poly := range a.polygons {
		// The box first; see box.holds.
		if !poly.box.holds(lat, lon) {
			continue
		}
		if len(poly.rings) == 0 || !inRing(poly.rings[0], lat, lon) {
			continue
		}
		inHole := false
		for _, hole := range poly.rings[1:] {
			if inRing(hole, lat, lon) {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}

// Wraps reports whether a ring crosses the antimeridian.
//
// It lives here, next to inRing, because inRing is what makes it necessary:
// that function treats longitude as linear and says so, adding that a source
// which does not pre-split a ring at the seam "would be wrong in a way this
// cannot detect, so a new BoundarySource has to be checked for it". Leaving
// the check in whichever source happened to need it first means the next one
// rediscovers the obligation from a comment, and writes it again.
//
// A step of more than half the world between consecutive vertices is not a
// step: boundary vertices are metres apart, and the measured maximum on the
// real Natural Earth file is half a degree. The closing edge is included,
// because inRing walks it too -- a Ring does not repeat its first vertex.
//
// What a caller should do with a wrapped ring is its own decision, and the
// two directions are not symmetric: dropping a wrapped OUTLINE loses an
// answer, while dropping a wrapped HOLE fills the hole in and makes
// containment say yes where it should say no.
func Wraps(r Ring) bool {
	for i := range r {
		j := (i + 1) % len(r)
		if math.Abs(r[j].Lon-r[i].Lon) > 180 {
			return true
		}
	}
	return false
}

// inRing reports whether a point is inside a closed ring.
//
// Ray casting eastward: a point is inside when the ray crosses the ring an odd
// number of times.
//
// # Longitude is treated as linear, and that is a dependency on the data
//
// There is no antimeridian handling here, and there does not need to be for
// Natural Earth: it splits a country crossing the seam into separate parts
// either side. Measured on the real 10m country file, the largest longitude
// step between consecutive vertices is 0.49 degrees for Russia and 0.10 for
// Fiji -- no ring crosses ±180. A source that did NOT pre-split would need
// unwrapping here, and would be wrong in a way this cannot detect, so a new
// BoundarySource has to be checked for it.
//
// # Two things that look like bugs and are not
//
// The half-open latitude comparison and the direction of the longitude test
// are both conventions rather than correctness conditions: reversing either
// gives identical answers for every point, because ray-crossing parity is
// direction-independent for a closed ring, and a vertex exactly on the query
// latitude resolves to the same crossing longitude whichever of its two edges
// is credited with it. Both were checked over a dense grid on a non-convex
// ring. Neither is worth a test, because no test can distinguish them.
func inRing(ring Ring, lat, lon float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		pi, pj := ring[i], ring[j]
		if (pi.Lat > lat) == (pj.Lat > lat) {
			continue
		}
		x := (pj.Lon-pi.Lon)*(lat-pi.Lat)/(pj.Lat-pi.Lat) + pi.Lon
		if lon < x {
			in = !in
		}
	}
	return in
}

// geoJSON is the subset of the format these files use.
//
// Decoded into named fields rather than into any-typed maps, so that a file
// whose shape changed fails at the decode with a type error rather than
// producing an empty set that looks like a coordinate in the ocean.
type geoJSON struct {
	Features []struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Geometry   struct {
			Type string `json:"type"`
			// Coordinates is [][][2]float64 for a Polygon and one level
			// deeper for a MultiPolygon, so it is decoded late -- see
			// readGeometry.
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

// nameKeys are the properties a name is read from, in order of preference.
//
// Natural Earth carries several at once and they differ. The admin files use
// UPPERCASE keys and the marine file lowercase ones, so the two halves of this
// list never compete -- which is what lets them be ordered by opposite rules.
//
// Admin: NAME_EN before NAME, because NAME is sometimes the local-script
// spelling and this package has no language parameter for a caller to ask
// with, so the English form is the one that can be relied on to render.
//
// Marine: name before name_en, because there the English form is the LESS
// specific one -- name is "South Pacific Ocean" and name_en is "Pacific
// Ocean", for 71 of 306 features. A flight wants the specific one.
var nameKeys = []string{"NAME_EN", "NAME", "NAME_LONG", "ADMIN", "name", "name_en"}

// kindKeys are the properties a feature's own kind is read from.
//
// The admin files carry none and take the kind passed to Read -- every
// feature in a country file is a country. The marine file does carry one, in
// featurecla, and its values are worth keeping: "strait" and "ocean" are
// different enough that flattening both to "water" would throw away the part
// a reader finds informative.
var kindKeys = []string{"featurecla", "type_en"}

// Read parses a Natural Earth GeoJSON file into a searchable set.
//
// fromFeature says whether a feature's own class is worth reading. The marine
// file's is -- "ocean", "strait", "bay" are the words a reader wants -- and
// the admin files' is not: theirs says "Admin-0 country", which is Natural
// Earth's internal vocabulary and reads as jargon in an answer. So the admin
// files take the kind passed here, which is true of every feature in them
// anyway.
func Read(r io.Reader, kind string, fromFeature bool) (*Set, error) {
	var doc geoJSON
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("boundary: reading the %s outlines: %w", kind, err)
	}
	set := &Set{}
	for i := range doc.Features {
		f := &doc.Features[i]
		name := firstName(f.Properties)
		if name == "" {
			// A shape with no name cannot answer the question this package
			// exists for. Skipped rather than kept as an empty answer.
			continue
		}
		polys, err := readGeometry(f.Geometry.Type, f.Geometry.Coordinates)
		if err != nil {
			return nil, fmt.Errorf("boundary: %s %q: %w", kind, name, err)
		}
		if len(polys) == 0 {
			continue
		}
		k := kind
		if fromFeature {
			k = featureKind(f.Properties, kind)
		}
		a := newArea(name, k, polys)
		a.names = namesOf(f.Properties, name)
		set.areas = append(set.areas, a)
	}
	if len(set.areas) == 0 {
		return nil, fmt.Errorf("boundary: the %s file holds no named areas; it is not the file this expects", kind)
	}
	return set, nil
}

// featureKind reads a feature's own kind, falling back to the file's.
func featureKind(props map[string]json.RawMessage, fallback string) string {
	for _, k := range kindKeys {
		raw, ok := props[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return fallback
}

// firstName reads a feature's name, preferring the first key that has one --
// except that an ALL-CAPS name gives way to a later key that is not.
//
// The exception is for the marine file, where Natural Earth shouts the
// largest features: "INDIAN OCEAN" and "SOUTHERN OCEAN" are the cartographic
// convention for an ocean label on a map, and are not how a sentence should
// name them. Where a cased alternative exists it says the same thing --
// "Indian Ocean" -- so preferring it loses nothing. Where none does, the
// shouted name is still the name and is returned rather than dropped.
//
// Only a name that is entirely upper case defers, so an ordinary name
// carrying capitals is unaffected.
func firstName(props map[string]json.RawMessage) string {
	var shouted string
	for _, k := range nameKeys {
		raw, ok := props[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || strings.TrimSpace(s) == "" {
			continue
		}
		if isShouted(s) {
			if shouted == "" {
				shouted = s
			}
			continue
		}
		return s
	}
	return shouted
}

// isShouted reports whether a string has letters and none of them are lower
// case.
func isShouted(s string) bool {
	letters := false
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsLetter(r) {
			letters = true
		}
	}
	return letters
}

// readGeometry decodes the two shapes these files use into one form.
func readGeometry(kind string, raw json.RawMessage) ([][]Ring, error) {
	switch kind {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(raw, &rings); err != nil {
			return nil, fmt.Errorf("decoding a polygon: %w", err)
		}
		return [][]Ring{toRings(rings)}, nil
	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(raw, &polys); err != nil {
			return nil, fmt.Errorf("decoding a multipolygon: %w", err)
		}
		out := make([][]Ring, 0, len(polys))
		for _, rings := range polys {
			out = append(out, toRings(rings))
		}
		return out, nil
	default:
		// Not an error. A file may legitimately carry a point or a line for
		// something with no area, and this package has nothing to say about
		// those rather than being unable to read the file.
		return nil, nil
	}
}

func toRings(rings [][][2]float64) []Ring {
	out := make([]Ring, 0, len(rings))
	for _, ring := range rings {
		pts := make(Ring, len(ring))
		for i, c := range ring {
			pts[i] = Coord{Lat: c[1], Lon: c[0]}
		}
		out = append(out, pts)
	}
	return out
}

// newArea computes the bounding box once, at load, because it is what makes a
// lookup cheap and it never changes.
func newArea(name, kind string, polys [][]Ring) Area {
	a := Area{Name: name, Kind: kind}
	for _, rings := range polys {
		// Over the OUTLINE only, not the holes. A hole is inside its
		// outline, so including them cannot widen the box -- but saying so
		// is what makes the two former spellings of this agree on purpose
		// rather than by luck.
		p := polygon{rings: rings}
		if len(rings) > 0 {
			p.box = boxOf(rings[0])
		} else {
			p.box = boxOf(nil)
		}
		a.polygons = append(a.polygons, p)
	}
	return a
}
