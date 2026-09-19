package locate

import (
	"math"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

// earthCircumferenceM is the equatorial circumference, which is the width of
// the Web Mercator square in metres.
const earthCircumferenceM = 40_075_016.686

// earthRadiusM is the mean radius, for the great-circle distance below.
const earthRadiusM = 6_371_008.8

// textTag reads a string attribute, reporting whether it was there AND was a
// string.
//
// A value of another kind is treated as absent rather than stringified: a
// schema encoding a name as a number is a schema this was not written for, and
// papering over it would produce labels like "42".
func textTag(f *mvt.Feature, key string) (string, bool) {
	v, ok := f.Tags[key]
	if !ok {
		return "", false
	}
	s, ok := v.Text()
	return s, ok
}

// kindMatches reports whether a feature is one of the kinds a level wants.
// An empty list takes every kind.
func kindMatches(f *mvt.Feature, kinds []string) bool {
	if len(kinds) == 0 {
		return true
	}
	kind, ok := textTag(f, "kind")
	if !ok {
		return false
	}
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// preferredName reads a feature's name, preferring a translation.
//
// The schema carries name:da, name:ja and dozens more, so asking for a language
// costs one extra map lookup. Falling back to the untranslated name rather than
// declining is deliberate: the local spelling is a correct answer, and a
// consumer that asked for Danish would rather be told "Horsens" than nothing.
//
// An empty name is treated as no name. A feature with a name of "" would
// otherwise win a nearest-match contest and report a place called nothing.
func preferredName(f *mvt.Feature, lang string) (string, bool) {
	if lang != "" {
		if s, ok := textTag(f, "name:"+lang); ok && s != "" {
			return s, true
		}
	}
	s, ok := textTag(f, "name")
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// distanceTo is how far a feature is from a coordinate, in metres.
//
// Two methods, because the two geometries ask different questions at different
// scales. A point feature is answered by the great-circle distance, which is
// accurate at any separation -- country labels are thousands of kilometres
// apart. A line is answered on a local tangent plane, because the distance to a
// LINE is the distance to its nearest segment and segment arithmetic on a
// sphere is a great deal of work for no benefit here: the only line level is
// the street, capped at a couple of hundred metres, where a tangent plane is
// accurate to well under a metre.
func distanceTo(f *mvt.Feature, line bool, at Coord, zoom uint8, ref tileRef) (float64, bool) {
	tr, err := mercator.NewTileTransform(zoom, ref.x, ref.y, mvt.DefaultExtent)
	if err != nil {
		return 0, false
	}
	if line {
		return distanceToLines(f.Geometry.Lines, tr, at)
	}
	best := math.Inf(1)
	for _, p := range f.Geometry.Points {
		lon, lat := tr.LonLat(p.X, p.Y)
		if d := haversineM(at.Lat, at.Lon, lat, lon); d < best {
			best = d
		}
	}
	return best, !math.IsInf(best, 1)
}

// distanceToLines is the distance to the nearest point on any segment.
func distanceToLines(lines [][]mvt.Point, tr mercator.TileTransform, at Coord) (float64, bool) {
	// Metres per degree at this latitude, which is what makes the plane local:
	// a degree of longitude shortens toward the poles and a degree of latitude
	// does not.
	mPerLat := earthCircumferenceM / 360
	mPerLon := mPerLat * math.Cos(at.Lat*math.Pi/180)

	best := math.Inf(1)
	for _, ln := range lines {
		for i := 1; i < len(ln); i++ {
			alon, alat := tr.LonLat(ln[i-1].X, ln[i-1].Y)
			blon, blat := tr.LonLat(ln[i].X, ln[i].Y)
			d := pointToSegmentM(
				(at.Lon-alon)*mPerLon, (at.Lat-alat)*mPerLat,
				(blon-alon)*mPerLon, (blat-alat)*mPerLat,
			)
			if d < best {
				best = d
			}
		}
		// A one-point "line" has no segment. Measured as a point rather than
		// skipped, so a degenerate feature still answers.
		if len(ln) == 1 {
			lon, lat := tr.LonLat(ln[0].X, ln[0].Y)
			if d := haversineM(at.Lat, at.Lon, lat, lon); d < best {
				best = d
			}
		}
	}
	return best, !math.IsInf(best, 1)
}

// pointToSegmentM is the distance from the origin to the segment from the
// origin-relative point (0,0)+p to (0,0)+p+v... stated plainly: the point is at
// (px, py) relative to the segment's start, and the segment runs along (vx, vy).
func pointToSegmentM(px, py, vx, vy float64) float64 {
	len2 := vx*vx + vy*vy
	if len2 == 0 {
		return math.Hypot(px, py)
	}
	// Projection of the point onto the segment, clamped to its ends so that a
	// point beyond either end measures to that end rather than to the
	// infinite line it lies on.
	t := (px*vx + py*vy) / len2
	t = math.Min(math.Max(t, 0), 1)
	return math.Hypot(px-t*vx, py-t*vy)
}

// haversineM is the great-circle distance between two coordinates in metres.
func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(a)))
}
