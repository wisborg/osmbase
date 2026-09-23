package boundary

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/wisborg/osmbase/locate"
)

// File names the two files a store holds, per detail level.
//
// The detail is in the name because the two are not interchangeable and a
// store may hold either: a file swapped underneath a program that believed it
// had the other would change every answer near a border with nothing to say
// it had.
func File(detail string, layer Layer) string {
	switch layer {
	case Regions:
		return fmt.Sprintf("ne_%s_admin_1_states_provinces.geojson", detail)
	case Waters:
		return fmt.Sprintf("ne_%s_geography_marine_polys.geojson", detail)
	default:
		return fmt.Sprintf("ne_%s_admin_0_countries.geojson", detail)
	}
}

// DerivedFile names the derived boundary file for a region.
//
// The region is part of the name for the same reason the detail is: a store
// may hold several, and a file swapped underneath a program that believed it
// had the other changes every answer near a border with nothing to say it
// had.
//
// It returns an error rather than a name for an unusable region, and the
// check is HERE rather than in the command, because this is where the value
// becomes a path component. The same interpolation on the detail escaped this
// directory once -- a value containing ".." read a planted file from outside
// the store and printed its contents as a country name -- and the lesson
// recorded then was that a check every caller has to remember is a check that
// will be forgotten.
func DerivedFile(region string) (string, error) {
	if !ValidRegion(region) {
		return "", fmt.Errorf("boundary: %q is not a usable region name; it may hold letters, digits, dots, dashes and underscores", region)
	}
	return derivedPrefix + region + DerivedExt, nil
}

// DerivedExt is the extension a derived boundary file carries, and
// derivedPrefix what its name begins with.
const (
	DerivedExt    = ".osmb"
	derivedPrefix = "osm_"

	// extractPrefix is the working copy of an extract, which lives in the
	// same directory while it is being read.
	extractPrefix = "extract_"
	extractExt    = ".osm.pbf"
)

// ExtractFile names the working copy of an extract for a region.
//
// Beside DerivedFile and validated the same way, because it is the second
// path component built from the same user-supplied string -- and the command
// that builds it was spelling it inline. That is safe only while the other
// name happens to be built first, which makes an ordering the check. The
// lesson this package already recorded is that a check every caller has to
// remember is a check that will be forgotten.
func ExtractFile(region string) (string, error) {
	if !ValidRegion(region) {
		return "", fmt.Errorf("boundary: %q is not a usable region name; it may hold letters, digits, dots, dashes and underscores", region)
	}
	return extractPrefix + region + extractExt, nil
}

// RegionOf recovers the region from a derived file's name, and reports
// whether the name is one.
//
// The inverse of DerivedFile, here rather than in whatever wants it, because
// the prefix is this package's and a caller that had to know it would be the
// second place the naming rule is written. locate has to find these files
// without ever being told a region.
func RegionOf(name string) (string, bool) {
	if !strings.HasPrefix(name, derivedPrefix) || !strings.HasSuffix(name, DerivedExt) {
		return "", false
	}
	region := name[len(derivedPrefix) : len(name)-len(DerivedExt)]
	if !ValidRegion(region) {
		return "", false
	}
	return region, true
}

// DerivedRegions lists the regions a store holds derived boundaries for.
//
// Sorted, so two runs agree. A name this package did not write is skipped
// rather than reported: the directory also holds the Natural Earth files and,
// while a build is running or after one was interrupted, a working copy of an
// extract.
func DerivedRegions(storeRoot string) ([]string, error) {
	entries, err := os.ReadDir(Dir(storeRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if region, ok := RegionOf(e.Name()); ok {
			out = append(out, region)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ValidRegion reports whether a string may be part of a derived file's name.
//
// Deliberately narrow: no separator of any platform, no leading dot, nothing
// that a shell or a path join can read as anything but a name. A region comes
// from a URL or a filename the user supplied, so it is input.
func ValidRegion(region string) bool {
	if region == "" || len(region) > 64 || strings.HasPrefix(region, ".") {
		return false
	}
	for _, r := range region {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// Layer is one of the files a boundary store holds.
//
// A named type rather than the bool this began as, because there are three
// now and a second bool beside the first would be a parameter list nobody can
// read at the call site.
type Layer uint8

const (
	Countries Layer = iota
	Regions
	Waters
)

// Layers are the files a complete store holds.
var Layers = []Layer{Countries, Regions, Waters}

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
	derivedSets    []*Set
	derivedCredits []string
	derivedDone    bool

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
	mu     sync.Mutex
	sets   map[Layer]*Set
	loaded map[string]error
}

// NaturalEarthCredit names the source of the country, region and water
// outlines.
//
// It is public domain and owes nothing, so this is a statement of provenance
// rather than a credit that is required -- and it says so in as many words,
// because a reader can act on knowing that two names in one answer came from
// different places under different terms, and cannot act on silence.
const NaturalEarthCredit = "Natural Earth (public domain)"

// What the derived files in a store may cost between them.
//
// ReadDerived bounds each file, and the reasoning it records -- "measured at
// 59 times the file live" -- was worked out for ONE file. Those budgets reset
// for the next one, so a directory of them multiplies the figure by however
// many there are: measured at four files sitting exactly at the per-file
// budget, 16.8 MB on disk became a gigabyte of heap, and a hundred such files
// would be twenty-five.
//
// A store is the user's own directory, so this is not an attack surface in
// the way a download is. It is still a directory whose contents nothing
// checked, holding files the NOTICE explicitly contemplates being passed
// between people, read in full on the first lookup and held for the life of
// the process.
const (
	maxDerivedFiles      = 64
	maxDerivedFileBytes  = 256 << 20
	maxDerivedAreasTotal = 1 << 16
)

// Sixty-five thousand areas is two orders of magnitude above a country --
// every administrative boundary in a Sydney extract comes to 471 and all of
// Denmark's to 145 -- so a store reaching it is holding something other than
// countries. Sixty-four files is the same judgement about how many regions a
// person keeps.

// derived loads every derived boundary file in the store, once.
//
// All of them rather than one named region, because nothing tells a lookup
// which region a coordinate is in -- that is the question being asked. A
// store holding two countries answers for both, and a store holding none
// answers nothing and leaves the levels to the tiles.
func (s *Source) derived() []*Set {
	if s.loaded == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.derivedDone {
		return s.derivedSets
	}
	s.derivedDone = true

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	areasLeft := maxDerivedAreasTotal
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := RegionOf(e.Name()); !ok {
			continue
		}
		if len(s.derivedSets) >= maxDerivedFiles {
			break
		}
		f, err := os.Open(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		// Bounded per file as well as in total, because one enormous file
		// costs as much as many.
		set, err := ReadDerived(io.LimitReader(f, maxDerivedFileBytes))
		f.Close()
		if err != nil {
			// Skipped, not fatal: one unreadable file in a directory must
			// not take a level away from every coordinate in a route. Read
			// once either way -- derivedDone is what stops the retry.
			continue
		}
		if set.Len() > areasLeft {
			// Skipped rather than truncated. A partial set answers some
			// coordinates and silently not others, which is worse than a
			// file that is not there.
			continue
		}
		areasLeft -= set.Len()
		s.derivedSets = append(s.derivedSets, set)
		if c := set.Provenance().Attribution; c != "" && !seen[c] {
			seen[c] = true
			s.derivedCredits = append(s.derivedCredits, c)
		}
	}
	return s.derivedSets
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
	return &Source{
		dir: Dir(storeRoot), detail: detail,
		sets: map[Layer]*Set{}, loaded: map[string]error{},
	}
}

// Available reports whether a store holds boundary data of any kind.
func Available(storeRoot, detail string) bool {
	if !ValidDetail(detail) {
		return false
	}
	if detail == "" {
		detail = DefaultDetail
	}
	complete := true
	for _, layer := range Layers {
		if _, err := os.Stat(filepath.Join(Dir(storeRoot), File(detail, layer))); err != nil {
			complete = false
			break
		}
	}
	if complete {
		return true
	}
	// A store may hold derived files and no Natural Earth outlines at all,
	// which is what running "boundaries --osm" without running "boundaries"
	// leaves behind. Answering false there made the derived file dead: the
	// command never opened a source, so the file nobody could see was never
	// read. Covers still answers per level, so a source opened on this
	// account offers the levels it actually has and leaves the rest to the
	// tiles.
	regions, err := DerivedRegions(storeRoot)
	return err == nil && len(regions) > 0
}

// set loads one file, once, remembering a failure so a broken file is not
// re-read and re-reported for every coordinate in a route.
func (s *Source) set(layer Layer) *Set {
	if s.loaded == nil {
		// A source Open refused. It holds no directory and answers nothing.
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	name := File(s.detail, layer)
	if _, done := s.loaded[name]; done {
		// The set is nil for a file that would not load, which is the same
		// answer as a file that held nothing -- see Contains.
		return s.sets[layer]
	}

	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		s.loaded[name] = err
		return nil
	}
	defer f.Close()

	// The error is remembered so a broken file is not re-read and
	// re-reported for every coordinate in a route.
	//
	// Storing nil here instead would be an equivalent mutant rather than a
	// bug, and it is worth saying so: the guard below still returns nil on
	// this call, and a later call taking the cached path returns the map
	// entry, which was never assigned and is also nil. No test can tell the
	// two apart, so nobody should write one trying.
	set, err := Read(f, layerKind(layer), layer == Waters)
	s.loaded[name] = err
	if err != nil {
		return nil
	}
	s.sets[layer] = set
	return set
}

// layerKind is the fallback kind for features in a file that do not name
// their own. The admin files carry no kind per feature; the marine file does.
func layerKind(layer Layer) string {
	switch layer {
	case Regions:
		return "state"
	case Waters:
		return "water"
	default:
		return "country"
	}
}

// Covers reports whether this source can answer a level.
//
// Country, region and water, and nothing below. Natural Earth publishes no
// suburb outlines, and reporting a locality as "contained" by the state that
// holds it would be a true statement answering the wrong question.
//
// Water is independent of the other two rather than an alternative to them. A
// point can be inside a country's outline and inside a named bay at once, and
// both are worth saying.
func (s *Source) Covers(l locate.Level) bool {
	if layer, ok := layerFor(l); ok {
		// The FILE has to be there, not just the layer name. This was
		// unconditional while Available guaranteed all three Natural Earth
		// files existed before anyone opened a source; a store holding only
		// a derived file broke that guarantee, and claiming country then
		// took it away from the tiles and answered nothing -- which is the
		// hazard the paragraph below describes, in the other half of the
		// same function.
		set := s.set(layer)
		return set != nil && set.Len() > 0
	}
	// The levels below region are covered only when a derived file is
	// actually present. A source that said it covered them regardless would
	// take them away from the tiles and then answer nothing, turning a
	// nearest-feature answer into no answer at all.
	if !derivedLevel(l) {
		return false
	}
	// Areas, not files. A run that found boundaries and closed none of them
	// writes a file holding nothing, and says so; claiming the levels on the
	// strength of that file takes them from the tiles and answers nothing.
	for _, set := range s.derived() {
		if set.Len() > 0 {
			return true
		}
	}
	return false
}

// derivedLevel reports whether a level is one the derived files answer.
//
// Country and region come from Natural Earth and water has no administrative
// equivalent; street is a line, not an area, and no boundary file holds one.
// What is left is the three levels between a region and a street, which is
// exactly the range OpenStreetMap's administrative relations cover and the
// range the tiles could only ever answer by nearest label.
func derivedLevel(l locate.Level) bool {
	switch l {
	case locate.Locality, locate.Macrohood, locate.Neighbourhood:
		return true
	}
	return false
}

// rankDerived assigns the areas containing a point to levels.
//
// The rule is the nesting itself, because nothing else is available: an
// admin_level is a number whose meaning differs by country -- seven is the
// municipality in Denmark and nine is the suburb in Australia -- so a table
// mapping levels to names would be asserting one country's scheme over every
// other.
//
// Only the THREE INNERMOST areas are ranked, and that is the part that took a
// second attempt. Ranking the whole stack sounds equivalent and is not: a
// file built the way the command builds one by default keeps every
// admin_level, so in Denmark the outermost containing area is the COUNTRY.
// Locality then came back as "Danmark", duplicating the answer Natural Earth
// had already given at its own level, and the two names a person would
// actually have said -- the region and the kommune -- appeared at no level at
// all. Anything wider than three deep is a region or a country and is
// answered from elsewhere.
//
// Of those three, the outermost is the locality and the innermost the
// neighbourhood. One area is a locality and nothing else: the place has one
// administrative name at this range, and inventing two from it would be a
// claim the data does not make.
func rankDerived(areas []Area) map[locate.Level]Area {
	out := map[locate.Level]Area{}
	if len(areas) == 0 {
		return out
	}
	if n := len(areas); n > 3 {
		areas = areas[n-3:]
	}
	if len(areas) == 1 {
		out[locate.Locality] = areas[0]
		return out
	}
	out[locate.Locality] = areas[0]
	out[locate.Neighbourhood] = areas[len(areas)-1]
	if len(areas) == 3 {
		out[locate.Macrohood] = areas[1]
	}
	return out
}

// layerFor maps a level to the file that answers it.
func layerFor(l locate.Level) (Layer, bool) {
	switch l {
	case locate.Country:
		return Countries, true
	case locate.Region:
		return Regions, true
	case locate.Water:
		return Waters, true
	}
	return 0, false
}

// Contains returns the area holding a coordinate.
//
// A level this source covers but has no file for answers false, which sends
// the caller no answer rather than a wrong one. That is the same outcome as a
// point in the sea and deliberately so: both mean "no area contains this", and
// a lookup has no business distinguishing "we do not know" from "there is
// nothing there" when the caller can do nothing differently about either.
func (s *Source) Contains(l locate.Level, lat, lon float64) (string, string, string, bool) {
	layer, ok := layerFor(l)
	if !ok {
		if !derivedLevel(l) {
			return "", "", "", false
		}
		// The credit travels beside each area, because the answer owes what
		// the file it came from owes -- and a store may hold one file
		// derived from OpenStreetMap beside one that is public domain. A
		// credit taken from the store as a whole attributes an answer to
		// whichever file did not give it.
		var stack []Area
		var credits []string
		for _, set := range s.derived() {
			found := set.Containing(lat, lon)
			stack = append(stack, found...)
			for range found {
				credits = append(credits, set.Provenance().Attribution)
			}
		}
		order := make([]int, len(stack))
		for i := range order {
			order[i] = i
		}
		sortOutermostFirstIndexed(stack, order)
		ranked := make([]Area, len(stack))
		rankedCredits := make([]string, len(stack))
		for i, j := range order {
			ranked[i], rankedCredits[i] = stack[j], credits[j]
		}
		a, ok := rankDerivedAt(ranked, l)
		if !ok {
			return "", "", "", false
		}
		return ranked[a].Name, ranked[a].Kind, rankedCredits[a], true
	}
	set := s.set(layer)
	if set == nil {
		return "", "", "", false
	}
	a, ok := set.At(lat, lon)
	if !ok {
		return "", "", "", false
	}
	// Natural Earth is public domain, so this is a statement of where the
	// answer came from rather than a credit that is owed -- and the string
	// says which. Returning nothing instead would be accurate about the
	// obligation and would lose the reader the one thing they can act on,
	// which is knowing that this name and the suburb below it came from
	// different places under different terms.
	return a.Name, a.Kind, NaturalEarthCredit, true
}

// sortOutermostFirstIndexed orders an index permutation by the areas it
// points at, so that a parallel slice can be reordered with it.
func sortOutermostFirstIndexed(areas []Area, order []int) {
	slices.SortStableFunc(order, func(i, j int) int {
		return cmp.Compare(areas[j].boxArea(), areas[i].boxArea())
	})
}

// rankDerivedAt returns the index in the ranked stack for a level.
func rankDerivedAt(areas []Area, l locate.Level) (int, bool) {
	base := 0
	if n := len(areas); n > 3 {
		base = n - 3
		areas = areas[base:]
	}
	switch {
	case len(areas) == 0:
		return 0, false
	case len(areas) == 1:
		return base, l == locate.Locality
	}
	switch l {
	case locate.Locality:
		return base, true
	case locate.Neighbourhood:
		return base + len(areas) - 1, true
	case locate.Macrohood:
		if len(areas) == 3 {
			return base + 1, true
		}
	}
	return 0, false
}
