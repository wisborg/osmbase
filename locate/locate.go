// Package locate answers "where is this point" from map data on disk.
//
// Country, region, locality, suburb, street -- read from the same vector tiles
// a render draws, so a lookup costs nothing and reaches nobody. See
// docs/locate.md for the design and, more importantly, for what this can and
// cannot honestly say.
//
// # The one thing to understand before using it
//
// These tiles carry no named areas. Every named feature in the schema is a
// POINT or a LINE: boundaries are lines with an admin level and no name, and
// landuse polygons carry a kind and no name. So nothing here can answer "which
// suburb contains this coordinate". It answers "which suburb point is nearest",
// which is a different claim -- a locality point is a label anchor near the
// middle of a town, not the town.
//
// Every Match therefore carries how it was reached and how far away the
// evidence was. A Contained match may be rendered as "Newtown". A Near match
// must be rendered as "near Newtown", or declined. A consumer that ignores the
// distinction will put a confident suburb name on a picture of somebody who was
// in the next suburb.
package locate

import (
	"encoding/json"
	"fmt"
)

// TileSource is where tiles come from, and is the whole of what this package
// asks of a store.
//
// Declared here rather than imported from render for the reason render declares
// its own: the dependency points from the store to its consumers, so a store
// satisfies both without either package knowing about the other. It is the same
// method, so one store value serves both.
type TileSource interface {
	Tile(z uint8, x, y uint32) (data []byte, ok bool, err error)
}

// Level is one rung of the hierarchy, from the widest to the narrowest.
//
// Ordered so that a caller can compare: the deepest level that answered is the
// most specific thing known about a point.
type Level uint8

const (
	Country Level = iota
	Region

	// Water is the named sea, ocean, strait or bay a coordinate is over.
	//
	// Not a rung of the land hierarchy and deliberately independent of it: a
	// point can have both, and a flight between mainland Australia and
	// Tasmania is over Bass Strait while still being somewhere a reader would
	// describe in terms of Australia. It sits here because it is an area of
	// that SIZE, not because water is a kind of region.
	//
	// It is the level that makes a flight describable. A trans-Pacific track
	// has no country under it for most of its length, and containment
	// correctly reports nothing -- which is honest and nearly useless. "North
	// Pacific Ocean" is the answer that was wanted.
	Water

	Locality
	Macrohood
	Neighbourhood
	Street
)

// String names a level in the schema's own vocabulary rather than in the one a
// geocoding service would use.
//
// Mapbox would call Locality a "place" and Neighbourhood a "neighborhood", and
// borrowing that vocabulary would imply the guarantees that come with it --
// containment, a bounding box, a postcode. This reads from a tile schema, and
// naming the levels as that schema does is the honest choice.
func (l Level) String() string {
	switch l {
	case Country:
		return "country"
	case Region:
		return "region"
	case Water:
		return "water"
	case Locality:
		return "locality"
	case Macrohood:
		return "macrohood"
	case Neighbourhood:
		return "neighbourhood"
	case Street:
		return "street"
	}
	return fmt.Sprintf("level(%d)", uint8(l))
}

// MarshalJSON writes a level as its name.
//
// A number would be meaningless in the output and worse than meaningless
// later: the constants are ordered widest-first so that a caller can compare
// them, and inserting a level between two existing ones -- a district, say --
// would silently renumber every value already written to a file.
func (l Level) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// Source says how a match was arrived at.
type Source uint8

const (
	// Near means the nearest named feature of that level, at Match.DistanceM.
	// It is an inference and must be presented as one.
	Near Source = iota

	// Contained means a named boundary polygon that holds the point. A
	// statement of fact, and not available from tiles alone -- see
	// Options.Boundaries.
	Contained
)

func (s Source) String() string {
	if s == Contained {
		return "contained"
	}
	return "near"
}

// MarshalJSON writes a source as "near" or "contained". See Level.MarshalJSON.
func (s Source) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// Match is one level's answer.
type Match struct {
	Level Level  `json:"level"`
	Name  string `json:"name"`

	// Kind is the schema's own kind_detail where it has one -- "city",
	// "state", "suburb" -- and is empty otherwise. Passed through rather than
	// normalised: it is the producer's own description and this package has no
	// better one.
	Kind string `json:"kind,omitempty"`

	Source Source `json:"source"`

	// DistanceM is how far the evidence was, in metres, and is 0 for a
	// Contained match. It is the number that makes a Near match usable: "400 m"
	// and "12 km" are both answers and only one of them means anything.
	//
	// Rounded to a tenth of a metre. The underlying arithmetic yields a float
	// with eleven decimals, and printing them claims a precision that nothing
	// here has: the distance is to a LABEL ANCHOR placed by a cartographer
	// somewhere near the middle of a town, and a millimetre on that is noise
	// dressed as data.
	DistanceM float64 `json:"distance_m"`

	// Attribution is what this answer's source asks to be said about it,
	// empty when it asks for nothing.
	//
	// Per match rather than per document, because one lookup can mix
	// licences: a country from Natural Earth, which is public domain, and a
	// suburb from a file derived from OpenStreetMap, which is not. A single
	// line covering both either over-credits one source or under-credits the
	// other, and only one of those is merely untidy.
	//
	// It is not always an obligation. Natural Earth's string says "public
	// domain" in as many words, because a reader can act on knowing that two
	// names in one answer came from different places under different terms,
	// and cannot act on silence.
	Attribution string `json:"attribution,omitempty"`
}

// Place is everything known about one coordinate.
type Place struct {
	Lat float64 `json:"latitude"`
	Lon float64 `json:"longitude"`

	// Matches are ordered widest level first, and a level with no answer is
	// absent rather than present and empty. Absence is the honest report: past
	// the distance cap there is no answer, not a distant one.
	Matches []Match `json:"matches"`
}

// Match returns the answer for a level, and whether there was one.
func (p Place) Match(l Level) (Match, bool) {
	for _, m := range p.Matches {
		if m.Level == l {
			return m, true
		}
	}
	return Match{}, false
}

// Deepest is the most specific thing known about the point.
func (p Place) Deepest() (Match, bool) {
	if len(p.Matches) == 0 {
		return Match{}, false
	}
	return p.Matches[len(p.Matches)-1], true
}

// Levels are every level, widest first, which is also the order Matches comes
// back in.
var Levels = []Level{Country, Region, Water, Locality, Macrohood, Neighbourhood, Street}

// ParseLevel turns a name back into a level.
func ParseLevel(s string) (Level, bool) {
	for _, l := range Levels {
		if l.String() == s {
			return l, true
		}
	}
	return 0, false
}
