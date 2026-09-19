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
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

// Area is one named region with the geometry to test a point against.
type Area struct {
	Name string
	// Kind is the producer's own description -- "country", "state" -- passed
	// through rather than normalised.
	Kind string

	// polygons are the rings in GeoJSON order: the first is the outline and
	// any that follow are holes.
	polygons [][][]point

	// west, south, east, north bound every polygon, so a point outside them
	// is rejected without walking a single ring. Most of the work of a lookup
	// is NOT doing the point-in-polygon test: a coordinate is outside all but
	// one of two hundred countries.
	west, south, east, north float64
}

type point struct{ lon, lat float64 }

// Set is the areas of one level, searchable by coordinate.
type Set struct {
	areas []Area
}

// Len is how many areas the set holds.
func (s *Set) Len() int { return len(s.areas) }

// At returns the area containing a coordinate, and whether one did.
//
// The first containing area wins. Natural Earth's country and state layers do
// not overlap, so "first" and "only" are the same thing here -- and a source
// whose areas DID overlap would be one this could not answer honestly anyway,
// since there would be no basis for preferring one.
func (s *Set) At(lat, lon float64) (Area, bool) {
	for i := range s.areas {
		a := &s.areas[i]
		if lon < a.west || lon > a.east || lat < a.south || lat > a.north {
			continue
		}
		if a.contains(lat, lon) {
			return *a, true
		}
	}
	return Area{}, false
}

// contains is the point-in-polygon test, holes included.
//
// Ray casting: a point is inside a ring when a ray from it crosses the ring an
// odd number of times. Inside the outline and inside a hole is outside the
// area, which is what the inner loop subtracts.
func (a *Area) contains(lat, lon float64) bool {
	for _, poly := range a.polygons {
		if len(poly) == 0 || !inRing(poly[0], lat, lon) {
			continue
		}
		inHole := false
		for _, hole := range poly[1:] {
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

// inRing reports whether a point is inside a closed ring.
//
// The half-open comparison on latitude -- one end inclusive, the other not --
// is what stops a vertex being counted twice when the ray passes exactly
// through it. Without it a point due east of a vertex is reported inside or
// outside depending on which way the two adjoining edges happen to run, which
// is a coin toss that lands differently for different points along the same
// border.
func inRing(ring []point, lat, lon float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		pi, pj := ring[i], ring[j]
		if (pi.lat > lat) == (pj.lat > lat) {
			continue
		}
		x := (pj.lon-pi.lon)*(lat-pi.lat)/(pj.lat-pi.lat) + pi.lon
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
// Natural Earth carries several. NAME is the common short form -- "Denmark",
// "New South Wales" -- and is what a reader wants; the others are fallbacks
// for the handful of records that lack it.
var nameKeys = []string{"NAME_EN", "NAME", "NAME_LONG", "ADMIN", "name"}

// Read parses a Natural Earth GeoJSON file into a searchable set.
func Read(r io.Reader, kind string) (*Set, error) {
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
		set.areas = append(set.areas, newArea(name, kind, polys))
	}
	if len(set.areas) == 0 {
		return nil, fmt.Errorf("boundary: the %s file holds no named areas; it is not the file this expects", kind)
	}
	return set, nil
}

func firstName(props map[string]json.RawMessage) string {
	for _, k := range nameKeys {
		raw, ok := props[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// readGeometry decodes the two shapes these files use into one form.
func readGeometry(kind string, raw json.RawMessage) ([][][]point, error) {
	switch kind {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(raw, &rings); err != nil {
			return nil, fmt.Errorf("decoding a polygon: %w", err)
		}
		return [][][]point{toRings(rings)}, nil
	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(raw, &polys); err != nil {
			return nil, fmt.Errorf("decoding a multipolygon: %w", err)
		}
		out := make([][][]point, 0, len(polys))
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

func toRings(rings [][][2]float64) [][]point {
	out := make([][]point, 0, len(rings))
	for _, ring := range rings {
		pts := make([]point, len(ring))
		for i, c := range ring {
			pts[i] = point{lon: c[0], lat: c[1]}
		}
		out = append(out, pts)
	}
	return out
}

// newArea computes the bounding box once, at load, because it is what makes a
// lookup cheap and it never changes.
func newArea(name, kind string, polys [][][]point) Area {
	a := Area{
		Name: name, Kind: kind, polygons: polys,
		west: math.Inf(1), south: math.Inf(1),
		east: math.Inf(-1), north: math.Inf(-1),
	}
	for _, poly := range polys {
		for _, ring := range poly {
			for _, p := range ring {
				a.west, a.east = math.Min(a.west, p.lon), math.Max(a.east, p.lon)
				a.south, a.north = math.Min(a.south, p.lat), math.Max(a.north, p.lat)
			}
		}
	}
	return a
}
