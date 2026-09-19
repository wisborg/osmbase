package boundary

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/wisborg/osmbase/locate"
)

// File names the two files a store holds, per detail level.
//
// The detail is in the name because the two are not interchangeable and a
// store may hold either: a file swapped underneath a program that believed it
// had the other would change every answer near a border with nothing to say
// it had.
func File(detail string, region bool) string {
	if region {
		return fmt.Sprintf("ne_%s_admin_1_states_provinces.geojson", detail)
	}
	return fmt.Sprintf("ne_%s_admin_0_countries.geojson", detail)
}

// Details are the resolutions this can use, coarsest first.
var Details = []string{"110m", "50m", "10m"}

// DefaultDetail is what a fetch takes when the caller does not choose.
//
// 10m, and the reason is COVERAGE rather than accuracy. This was first set to
// 50m on the grounds that the 10m state file is forty megabytes of JSON and
// would be slow, which was a guess about size standing in for a look at the
// contents. The 50m state file turns out to carry 294 subdivisions across
// NINE countries -- the United States, Australia, Brazil and a few others --
// and none at all for Denmark, Germany, France, Norway or the United Kingdom.
// A region level that answers for nine countries is not a region level.
//
// The 10m file carries 4,596 subdivisions across 253 countries, and the cost
// it was rejected for did not survive measurement either: both files together
// parse in about 1.1 seconds, once per process and only for the levels
// actually asked about.
//
// 50m remains available and is a reasonable choice for country alone, which it
// covers fully at a fifth of the size.
const DefaultDetail = "10m"

// ValidDetail reports whether a string is one of the resolutions this knows.
//
// Exported and checked in Open and Available rather than left to each caller,
// because the first version left it to the caller and one of two callers
// forgot. The boundaries command validated its flag; the locate command
// passed the same flag straight through, and since File interpolates it into
// a name that is then joined to a path, a value containing ".." escaped the
// store's boundary directory entirely -- reproduced, reading a planted file
// from outside the store and printing its contents as a country name.
//
// A check every caller has to remember is a check that will be forgotten
// again. This one is where the value is used.
func ValidDetail(detail string) bool {
	return detail == "" || slices.Contains(Details, detail)
}

// Dir is where a store keeps its boundary files.
//
// A sibling of the tile sources rather than one of them. A boundary file has
// no cell zoom, no pyramid and no per-cell eviction, so making it a slice
// source would mean teaching slice about a second kind of thing it cannot
// serve -- for the convenience of one directory fewer.
func Dir(storeRoot string) string { return filepath.Join(storeRoot, "boundaries") }

// Source reads a store's boundary files and answers containment from them.
//
// Loaded lazily and once: a lookup that asks only about streets should not pay
// to parse the world's coastlines, and a route of thousands of points should
// not pay twice.
type Source struct {
	dir    string
	detail string

	// mu guards everything below it.
	//
	// The type is built to be loaded once and reused -- that is the point of
	// it -- which is exactly the shape a consumer shares between goroutines.
	// locate.AtEach exists for routes of thousands of points, and a caller
	// processing several routes at once would naturally hand them one Source.
	// Two goroutines writing loaded is a concurrent map write, which is a
	// crash rather than a race worth arguing about.
	mu                 sync.Mutex
	countries, regions *Set
	loaded             map[string]error
}

// Open prepares to read boundary files from a store, without reading any yet.
//
// It does not verify the files exist. A store with no boundaries is the
// ordinary case -- they are an optional download -- and the honest place to
// report that is where a level is asked for, not here.
// A detail this does not know yields a source that answers nothing, rather
// than one that reads a file somewhere unexpected. Refusing loudly would be
// the other reasonable choice and is what the command does before it gets
// here; at this level a caller has already decided to fall back to
// nearest-feature when boundaries are unavailable, and an unknown detail is
// a kind of unavailable.
func Open(storeRoot, detail string) *Source {
	if !ValidDetail(detail) {
		return &Source{}
	}
	if detail == "" {
		detail = DefaultDetail
	}
	return &Source{dir: Dir(storeRoot), detail: detail, loaded: map[string]error{}}
}

// Available reports whether a store holds the files for a detail.
func Available(storeRoot, detail string) bool {
	if !ValidDetail(detail) {
		return false
	}
	if detail == "" {
		detail = DefaultDetail
	}
	for _, region := range []bool{false, true} {
		if _, err := os.Stat(filepath.Join(Dir(storeRoot), File(detail, region))); err != nil {
			return false
		}
	}
	return true
}

// set loads one file, once, remembering a failure so a broken file is not
// re-read and re-reported for every coordinate in a route.
func (s *Source) set(region bool) *Set {
	if s.loaded == nil {
		// A source Open refused. It holds no directory and answers nothing.
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	name := File(s.detail, region)
	if err, done := s.loaded[name]; done {
		if err != nil {
			return nil
		}
		if region {
			return s.regions
		}
		return s.countries
	}

	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		s.loaded[name] = err
		return nil
	}
	defer f.Close()

	kind := "country"
	if region {
		kind = "state"
	}
	// The error is remembered so a broken file is not re-read and
	// re-reported for every coordinate in a route.
	//
	// Storing nil here instead would be an equivalent mutant rather than a
	// bug, and it is worth saying so: the guard below still returns nil on
	// this call, and a later call taking the cached-success path returns the
	// field, which was never assigned and is also nil. No test can tell the
	// two apart, so nobody should write one trying.
	set, err := Read(f, kind)
	s.loaded[name] = err
	if err != nil {
		return nil
	}
	if region {
		s.regions = set
	} else {
		s.countries = set
	}
	return set
}

// Covers reports whether this source can answer a level.
//
// Country and region, and nothing below. Natural Earth publishes no suburb
// outlines, and reporting a locality as "contained" by the state that holds it
// would be a true statement answering the wrong question.
func (s *Source) Covers(l locate.Level) bool {
	return l == locate.Country || l == locate.Region
}

// Contains returns the area holding a coordinate.
//
// A level this source covers but has no file for answers false, which sends
// the caller no answer rather than a wrong one. That is the same outcome as a
// point in the sea and deliberately so: both mean "no area contains this", and
// a lookup has no business distinguishing "we do not know" from "there is
// nothing there" when the caller can do nothing differently about either.
func (s *Source) Contains(l locate.Level, lat, lon float64) (string, string, bool) {
	if !s.Covers(l) {
		return "", "", false
	}
	set := s.set(l == locate.Region)
	if set == nil {
		return "", "", false
	}
	a, ok := set.At(lat, lon)
	if !ok {
		return "", "", false
	}
	return a.Name, a.Kind, true
}
