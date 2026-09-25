package boundary

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/wisborg/osmbase/locate"
)

// placeNames is what a Natural Earth feature says about itself beyond the one
// name an answer prints.
type placeNames struct {
	// aliases are every name the feature carries -- English, local, long --
	// and its ISO code, any of which a person might type.
	aliases []string
	// context is what a region is IN: its country's name and code. A search
	// qualified "Luxembourg, Belgium" matches the qualifier against these.
	context []string
	// typ is the feature's own word for what it is -- "Province", "State",
	// "Metropolitan Borough" -- for telling candidates apart in a list.
	typ string
}

// The properties each list is read from. The country file keys are upper
// case and the region file's lower case, and the ISO code means something
// different in each: ISO_A2 on a country is its own code, iso_a2 on a region
// is its country's. Keeping the two cases apart is what keeps them straight.
//
// The _EH codes and ADM0_A3 are there because the plain ISO fields are -99
// for France and Norway -- a known Natural Earth quirk -- so "FR" and "FRA"
// would otherwise find nothing.
var (
	aliasKeys = []string{
		"NAME_EN", "NAME", "NAME_LONG", "ADMIN",
		"ISO_A2", "ISO_A2_EH", "ISO_A3", "ISO_A3_EH", "ADM0_A3",
		"name", "name_en", "iso_3166_2",
	}
	contextKeys = []string{"admin", "geonunit", "iso_a2"}
)

func namesOf(props map[string]json.RawMessage, name string) *placeNames {
	n := &placeNames{aliases: []string{name}}
	str := func(k string) string {
		var s string
		if raw, ok := props[k]; ok && json.Unmarshal(raw, &s) == nil {
			// Natural Earth writes -99 for "none", and it is not a name.
			if s = strings.TrimSpace(s); s != "-99" {
				return s
			}
		}
		return ""
	}
	add := func(list *[]string, s string) {
		if s != "" && !slices.ContainsFunc(*list, func(t string) bool { return strings.EqualFold(s, t) }) {
			*list = append(*list, s)
		}
	}
	for _, k := range contextKeys {
		add(&n.context, str(k))
	}
	for _, k := range aliasKeys {
		// A region's name is never the name of the country it is in. Seven
		// regions in the 10m file carry their country's name as their
		// English one -- Hovedstaden's name_en is "Denmark", Guyane
		// française's is "France" -- which is a data error, and taken at
		// its word it made "Denmark" and "France" ambiguous.
		if v := str(k); !slices.ContainsFunc(n.context, func(c string) bool { return strings.EqualFold(c, v) }) {
			add(&n.aliases, v)
		}
	}
	n.typ = str("type_en")
	return n
}

// Extent is a rectangle in degrees.
type Extent struct{ West, South, East, North float64 }

// Candidate is one area a name could mean.
type Candidate struct {
	// Name is the area's own name, as an answer would print it.
	Name string
	// Level is where it sits: Country or Region.
	Level locate.Level
	// Type is its own word for what it is, when the source has one.
	Type string
	// In is the country a region is in, and empty for a country.
	In string

	// Extent is the ground to show: the area's main part and the parts
	// near it. See mainExtent.
	Extent Extent
	// Parts is how many parts the area has, and Shown how many of them
	// Extent holds. A difference is worth saying: France is shown without
	// its overseas departments, and a map of it should not claim otherwise.
	Parts, Shown int

	// exact is whether the query matched a name rather than only an alias
	// or a part of one.
	exact bool
}

// Describe names a candidate the way a list of them has to: enough to tell
// "Luxembourg" the country from "Luxembourg" the Belgian province.
func (c Candidate) Describe() string {
	what := c.Type
	if what == "" {
		what = c.Level.String()
	}
	if c.In != "" {
		return fmt.Sprintf("%s (%s, %s)", c.Name, strings.ToLower(what), c.In)
	}
	return fmt.Sprintf("%s (%s)", c.Name, strings.ToLower(what))
}

// Find returns the areas a name could mean, from the Natural Earth outlines
// in the store.
//
// The query is a name, optionally followed by a comma and what it is in:
// "Luxembourg, Belgium". levels narrows the search to Country or Region, and
// none searches both. A list rather than one Level with a zero meaning "any",
// because the zero Level is Country.
//
// A name matches an area when it equals one of the area's names or codes,
// ignoring case. Only when nothing matches that way are areas whose name
// merely begins with the query returned -- "Newcastle" finds "Newcastle upon
// Tyne" -- and they come back marked as not exact, so a caller can offer them
// rather than take one: the Newcastle a person means may be one this data
// does not hold at all.
//
// Nothing is chosen here. More than one candidate is the ordinary case --
// "Luxembourg" is a country, a district of it and a province of Belgium --
// and deciding between them is the caller's to do, or to refuse.
func (s *Source) Find(query string, levels ...locate.Level) (exact []Candidate, near []Candidate) {
	name, within, _ := strings.Cut(query, ",")
	name, within = strings.TrimSpace(name), strings.TrimSpace(within)
	if name == "" {
		return nil, nil
	}
	for _, l := range []locate.Level{locate.Country, locate.Region} {
		if len(levels) > 0 && !slices.Contains(levels, l) {
			continue
		}
		layer, _ := layerFor(l)
		set := s.set(layer)
		if set == nil {
			continue
		}
		for _, a := range set.areas {
			if a.names == nil {
				continue
			}
			match, isExact := matchName(a.names.aliases, name)
			if !match {
				continue
			}
			if within != "" && !slices.ContainsFunc(a.names.context, func(c string) bool { return strings.EqualFold(c, within) }) {
				continue
			}
			c := candidateOf(a, l)
			c.exact = isExact
			if isExact {
				exact = append(exact, c)
			} else {
				near = append(near, c)
			}
		}
	}
	if len(exact) > 0 {
		return exact, nil
	}
	return nil, near
}

// matchName reports whether a query names an area, and whether exactly.
func matchName(aliases []string, query string) (match, exact bool) {
	for _, a := range aliases {
		if strings.EqualFold(a, query) {
			return true, true
		}
	}
	q := strings.ToLower(query) + " "
	for _, a := range aliases {
		if strings.HasPrefix(strings.ToLower(a), q) {
			return true, false
		}
	}
	return false, false
}

func candidateOf(a Area, l locate.Level) Candidate {
	c := Candidate{Name: a.Name, Level: l, Parts: len(a.polygons)}
	if a.names != nil {
		c.Type = a.names.typ
		if l == locate.Region && len(a.names.context) > 0 {
			c.In = a.names.context[0]
		}
	}
	c.Extent, c.Shown = mainExtent(a)
	return c
}

// mainExtent is the ground a map of an area should show, and how many of its
// parts that holds.
//
// Not the rectangle around every part. France's outline has 21 parts from
// longitude -61.8 to +55.9, because the overseas departments are France; the
// rectangle around all of them is the Atlantic, and a map of it is a map of
// nothing. So the extent starts from the largest part and takes in every part
// within maxPartGapKM of a part already taken, until nothing more is: Denmark
// gathers its islands one crossing at a time, Bornholm included, France
// gathers Corsica, Australia gathers Tasmania across Bass Strait.
//
// The reach is a distance on the ground, fixed, and that took a second
// attempt. It was first half the size of what had been taken so far, which
// snowballed: once Australia's mainland was in, the reach was twenty degrees,
// and it took Macquarie Island, 1,500 km south of Tasmania, and centred a map
// of Australia on the Southern Ocean with the Top End cut off. A strait is a
// strait whatever the size of the country on either side of it.
//
// The same rule leaves Alaska and Hawaii out of the United States -- a
// judgement a cartographer might make either way; --bbox is there for anyone
// who wants the other one.
func mainExtent(a Area) (Extent, int) {
	var parts []box
	for _, p := range a.polygons {
		if b := p.box.area(); !math.IsInf(b, 0) && !math.IsNaN(b) {
			parts = append(parts, p.box)
		}
	}
	if len(parts) == 0 {
		return Extent{}, 0
	}
	largest := 0
	for i, p := range parts {
		if p.area() > parts[largest].area() {
			largest = i
		}
	}
	taken := make([]bool, len(parts))
	taken[largest] = true
	u, shown := parts[largest], 1
	for grew := true; grew; {
		grew = false
		for i, p := range parts {
			if taken[i] {
				continue
			}
			for j, q := range parts {
				if taken[j] && gapKM(p, q) <= maxPartGapKM {
					taken[i], grew = true, true
					shown++
					u.west, u.east = math.Min(u.west, p.west), math.Max(u.east, p.east)
					u.south, u.north = math.Min(u.south, p.south), math.Max(u.north, p.north)
					break
				}
			}
		}
	}
	return Extent{West: u.west, South: u.south, East: u.east, North: u.north}, shown
}

// maxPartGapKM is the widest sea crossing that keeps two parts of an area on
// one map. Bass Strait, between Victoria and Tasmania, is about 240 km;
// Bornholm is about 130 km from Zealand; Lord Howe is 600 km off the coast
// and Hawaii nearly 4,000.
const maxPartGapKM = 300

// gapKM is the distance on the ground between two boxes, zero where they
// touch or overlap.
//
// Measured at the latitude where the two face each other: within the band
// both span when they share one, and between their facing edges when they do
// not -- in either case nearest the equator, where a degree of longitude is
// widest. Taking the latitude nearest the equator of either WHOLE box was the
// first attempt, and overstated the gap between Russia's mainland, which
// reaches 41 degrees north, and Kaliningrad at 54: 335 km, and Kaliningrad was
// left off a map of Russia.
func gapKM(a, b box) float64 {
	dLon := math.Max(0, math.Max(b.west-a.east, a.west-b.east))
	dLat := math.Max(0, math.Max(b.south-a.north, a.south-b.north))
	lo, hi := math.Max(a.south, b.south), math.Min(a.north, b.north)
	if lo > hi {
		// No shared band: the gap runs between the facing edges.
		lo, hi = hi, lo
	}
	lat := 0.0
	switch {
	case lo > 0:
		lat = lo
	case hi < 0:
		lat = hi
	}
	const kmPerDegree = 111.32
	return math.Hypot(dLon*kmPerDegree*math.Cos(lat*math.Pi/180), dLat*kmPerDegree)
}
