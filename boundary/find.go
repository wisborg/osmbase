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
// nothing. So the extent starts from the largest part and takes in every
// other part that comes within half its size of what has been taken so far,
// until nothing more does: Denmark gathers Funen, Zealand and then Bornholm
// one step at a time, France gathers Corsica and stops, and a part an ocean
// away is left out and counted.
//
// The same rule keeps Alaska and Hawaii in the United States, which is a
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
	main := slices.MaxFunc(parts, func(x, y box) int {
		switch {
		case x.area() < y.area():
			return -1
		case x.area() > y.area():
			return 1
		}
		return 0
	})
	u, taken := main, make([]bool, len(parts))
	shown := 0
	for grew := true; grew; {
		grew = false
		margin := math.Max(u.east-u.west, u.north-u.south) / 2
		reach := box{west: u.west - margin, south: u.south - margin, east: u.east + margin, north: u.north + margin}
		for i, p := range parts {
			if taken[i] || !overlaps(reach, p) {
				continue
			}
			taken[i], grew = true, true
			shown++
			u.west, u.east = math.Min(u.west, p.west), math.Max(u.east, p.east)
			u.south, u.north = math.Min(u.south, p.south), math.Max(u.north, p.north)
		}
	}
	return Extent{West: u.west, South: u.south, East: u.east, North: u.north}, shown
}

func overlaps(a, b box) bool {
	return a.west <= b.east && b.west <= a.east && a.south <= b.north && b.south <= a.north
}
