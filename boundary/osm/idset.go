// Package osm turns an OpenStreetMap extract into administrative boundary
// outlines.
//
// The file is read three times, because a PBF extract is ordered nodes, then
// ways, then relations, and a boundary cannot be assembled before the
// geometry it refers to exists:
//
//  1. relations, to find the boundaries wanted and the ways each names;
//  2. ways, to find the nodes those ways name;
//  3. nodes, to find where those nodes are.
//
// Three passes over a gigabyte is more IO than one, and it is the trade this
// design is built on: the alternative is holding every node in the extract in
// memory on a single pass, which for a country is hundreds of megabytes of
// which almost all is buildings and addresses no boundary touches. See
// docs/locate.md.
package osm

import (
	"fmt"
	"slices"
)

// idSet is a set of element ids, built once and then only read.
//
// A sorted slice rather than a map, because that is what the access pattern
// is: every id is added during one pass and every lookup happens in the next,
// so nothing is ever added and searched in turn. The cost is eight bytes an
// entry against roughly fifty for a map[int64]struct{}, which on the several
// million nodes a country's boundaries reference is the difference between
// tens of megabytes and hundreds. BenchmarkIDSet measures both.
type idSet struct {
	ids    []int64
	frozen bool
	max    int
}

// newIDSet returns a set that refuses to grow past max.
//
// The cap is a parameter and not a suggestion: this is the first structure in
// the pipeline whose size is set by the FILE rather than by one block, which
// makes it the place the reviews' recurring finding would land next -- a
// limit that bounds what is returned instead of what is allocated. So it is
// checked in add, before the append, rather than after the pass.
func newIDSet(max int) *idSet { return &idSet{max: max} }

// add records an id. Duplicates are allowed; freeze removes them.
func (s *idSet) add(id int64) error {
	if s.frozen {
		return fmt.Errorf("osm: an id was added to a set already frozen; this is a bug in the pass order")
	}
	if len(s.ids) >= s.max {
		return fmt.Errorf("osm: more than %d ids were wanted, and this pipeline holds at most that many", s.max)
	}
	s.ids = append(s.ids, id)
	return nil
}

// freeze sorts and deduplicates, after which the set may be searched.
//
// Deduplication matters for size rather than correctness: adjacent boundary
// ways share their end nodes, and two administrative areas share their whole
// common border, so the same id arrives many times.
func (s *idSet) freeze() {
	slices.Sort(s.ids)
	s.ids = slices.Compact(s.ids)
	s.ids = slices.Clip(s.ids)
	s.frozen = true
}

// has reports whether the id is in the set. Only valid after freeze.
func (s *idSet) has(id int64) bool {
	_, ok := slices.BinarySearch(s.ids, id)
	return ok
}

// len is the number of distinct ids, once frozen.
func (s *idSet) len() int { return len(s.ids) }
