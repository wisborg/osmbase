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
	// Copied into an array of exactly the right size, which is the only thing
	// that actually releases the one append grew.
	//
	// slices.Clip was here and does not: it lowers the cap so a later append
	// copies, but the array stays reachable through the slice's data pointer,
	// and that array is sized by the number of add calls rather than by the
	// number of distinct ids. Since the same id arrives many times -- the
	// paragraph above is the whole reason -- it is several times the size of
	// the set it holds, and the eight-bytes-an-id this design rests on is
	// only true once it is handed back. slices.Clone does release it but
	// appends, so it rounds the new array up to a size class; make and copy
	// ask for exactly what is needed. The copy is paid at the one moment the
	// old array is about to die anyway.
	exact := make([]int64, len(s.ids))
	copy(exact, s.ids)
	s.ids = exact
	s.frozen = true
}

// find returns the id's position in the frozen set, and whether it is there.
//
// The position is the point of it: everything the passes record about an
// element is held in a slice parallel to this one, so a lookup that returned
// only a yes would make the caller search twice. It is also the only search
// in the package -- reaching past this into s.ids, which four call sites used
// to do, leaves the "only valid after freeze" contract documented on a method
// nothing calls.
func (s *idSet) find(id int64) (int, bool) {
	return slices.BinarySearch(s.ids, id)
}

// has reports whether the id is in the set. Only valid after freeze.
func (s *idSet) has(id int64) bool {
	_, ok := s.find(id)
	return ok
}

// len is the number of distinct ids, once frozen.
func (s *idSet) len() int { return len(s.ids) }
