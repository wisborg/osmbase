package boundary

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	derivedSets []*Set
	derivedDone bool
	// derivedCountries counts the national borders among the derived
	// files' areas, found as they load.
	derivedCountries int

	// countryFrom is where the country level is answered from.
	countryFrom CountrySource
	// limits are what the derived files may cost between them. A field
	// rather than the constants read directly so a test can shrink them to
	// a size whose allocation it can afford to measure.
	limits derivedLimits

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
// 59 times the file live" -- was worked out for ONE file. Reset for each file,
// as they first were, those budgets multiply that figure by however many
// files there are: measured at four files sitting exactly at the per-file
// budget, 16.8 MB on disk became a gigabyte of heap.
//
// So the store shares ONE of each budget rather than resetting them per
// file: the polygons and rings a file may declare, and the bytes it may
// occupy -- which is what bounds its points, as the per-file reasoning
// explains. A directory then costs at most what one maximal file does,
// however it is divided between files.
//
// Counting files and areas is not a substitute, and was tried: a cap of
// sixty-five thousand AREAS let four files of one area each turn 26 MB on
// disk into 420 MB live, because an area's cost is its geometry and not the
// fact of it. Both counts are kept, as the coarse judgement they are --
// sixty-five thousand areas is two orders of magnitude above a country
// (Sydney's extract comes to 471, all of Denmark's to 145), and sixty-four
// files is the same judgement about how many regions a person keeps.
//
// A store is the user's own directory, so this is not an attack surface in
// the way a download is. It is still a directory whose contents nothing
// checked, holding files the NOTICE explicitly contemplates being passed
// between people, read in full on the first lookup and held for the life of
// the process.
const (
	maxDerivedFiles      = 64
	maxDerivedStoreBytes = 256 << 20
	maxDerivedAreasTotal = 1 << 16
)

// derivedLimits are the budgets Source.derived spends across every file.
type derivedLimits struct {
	files, areas int
	bytes        int64
	geometry     budget
}

func defaultDerivedLimits() derivedLimits {
	return derivedLimits{
		files: maxDerivedFiles, areas: maxDerivedAreasTotal,
		bytes: maxDerivedStoreBytes, geometry: newBudget(),
	}
}

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
	left := s.limits
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := RegionOf(e.Name()); !ok {
			continue
		}
		if len(s.derivedSets) >= left.files {
			break
		}
		f, err := os.Open(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		// Read against what is LEFT, and charged only if kept. A file that
		// does not fit is refused as it is read, at the item that overruns,
		// so what it allocated before that is bounded by the same budget; a
		// file that is skipped for any reason leaves nothing behind to pay
		// for.
		geometry := left.geometry
		lr := &io.LimitedReader{R: f, N: left.bytes}
		set, err := readDerived(lr, &geometry)
		f.Close()
		if err != nil {
			// Skipped, not fatal: one unreadable file in a directory must
			// not take a level away from every coordinate in a route. Read
			// once either way -- derivedDone is what stops the retry.
			continue
		}
		if set.Len() > left.areas {
			// Skipped rather than truncated. A partial set answers some
			// coordinates and silently not others, which is worse than a
			// file that is not there.
			continue
		}
		left.areas -= set.Len()
		left.bytes = lr.N
		left.geometry = geometry
		s.derivedSets = append(s.derivedSets, set)
		for _, a := range set.areas {
			if isCountry(a) {
				s.derivedCountries++
			}
		}
	}
	return s.derivedSets
}

// CountrySource names where the country level is answered from.
type CountrySource uint8

const (
	// CountryNaturalEarth answers the country from Natural Earth's outlines,
	// and is the default: they follow the coast, so a point off it is at
	// sea, which for a route along a coast is the answer wanted.
	CountryNaturalEarth CountrySource = iota

	// CountryOSM answers the country from the national borders
	// (admin_level 2) in the store's derived files, and from Natural Earth
	// wherever none of them holds the point.
	//
	// Not the default, because the two draw a country differently at sea.
	// OpenStreetMap's national border runs out to the territorial-waters
	// limit -- measured: a point in Aarhus Bay is inside "Danmark" and inside
	// no kommune -- so a boat a few kilometres offshore is IN the country
	// rather than at sea. On land OpenStreetMap is the more precise of the
	// two, and a caller who wants that, or wants the legal answer at sea,
	// can have it.
	CountryOSM
)

// String is the name the command's flag takes.
func (c CountrySource) String() string {
	if c == CountryOSM {
		return "osm"
	}
	return "natural-earth"
}

// ParseCountrySource is the inverse of String.
func ParseCountrySource(s string) (CountrySource, bool) {
	switch s {
	case "natural-earth":
		return CountryNaturalEarth, true
	case "osm":
		return CountryOSM, true
	}
	return 0, false
}

// Options configure a Source. The zero value is what Open gives.
type Options struct {
	// Detail is the Natural Earth resolution; see Open.
	Detail string

	// Country is where the country level is answered from.
	Country CountrySource
}

// HasDerivedCountries reports whether any derived file in the store holds a
// national border, which is what CountryOSM answers from.
//
// Exported so a caller that asked for CountryOSM can say so when the store
// cannot give it -- a file built with "--levels 8,9,10" holds suburbs and no
// country -- rather than every answer quietly coming from Natural Earth.
func (s *Source) HasDerivedCountries() bool {
	s.derived()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.derivedCountries > 0
}

// Open prepares to read boundary files from a store, without reading any yet.
//
// It does not verify the files exist. A store with no boundaries is the
// ordinary case -- they are an optional download -- and the honest place to
// report that is where a level is asked for, not here.
//
// A detail this does not know yields a source that answers nothing, rather
// than one that reads a file somewhere unexpected. Refusing loudly would be
// the other reasonable choice and is what the command does before it gets
// here; at this level a caller has already decided to fall back to
// nearest-feature when boundaries are unavailable, and an unknown detail is
// a kind of unavailable.
func Open(storeRoot, detail string) *Source {
	return OpenWith(storeRoot, Options{Detail: detail})
}

// OpenWith is Open with the choices Open leaves at their defaults.
func OpenWith(storeRoot string, o Options) *Source {
	if !ValidDetail(o.Detail) {
		return &Source{}
	}
	detail := o.Detail
	if detail == "" {
		detail = DefaultDetail
	}
	return &Source{
		dir: Dir(storeRoot), detail: detail, limits: defaultDerivedLimits(),
		countryFrom: o.Country,
		sets:        map[Layer]*Set{}, loaded: map[string]error{},
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
// Country, region and water from Natural Earth -- and country from a derived
// file's national borders too, with CountryOSM -- and the levels below a
// region from derived files. Natural Earth publishes no
// suburb outlines, and reporting a locality as "contained" by the state that
// holds it would be a true statement answering the wrong question.
//
// Water is independent of the other two rather than an alternative to them. A
// point can be inside a country's outline and inside a named bay at once, and
// both are worth saying.
func (s *Source) Covers(l locate.Level) bool {
	if l == locate.Country && s.countryFrom == CountryOSM && s.HasDerivedCountries() {
		return true
	}
	if layer, ok := layerFor(l); ok {
		// The FILE has to be there, not just the layer name. This was
		// unconditional while Available guaranteed all three Natural Earth
		// files existed before anyone opened a source; a store holding only
		// a derived file broke that guarantee, and claiming country then
		// took it away from the tiles and answered nothing -- which is the
		// hazard the paragraph below describes, in the other half of the
		// same function.
		//
		// The Len check cannot fire today -- Read refuses a Natural Earth
		// file with no named areas -- and removing it is an equivalent
		// mutant. It is kept because the rule it states is this function's,
		// and the derived levels below need it for real.
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

// Contains returns the area holding a coordinate at a level.
//
// For country, region and water it answers Inside or Outside and never
// NoData: Natural Earth covers the world, so a point in no country is at sea,
// and that is the answer. (A level whose file is missing is not covered, and
// is never asked.) With CountryOSM, a national border in a derived file
// answers the country first, and Natural Earth wherever none holds the
// point.
//
// For the levels below a region it answers NoData wherever no derived file
// holds any area containing the point. A derived file covers one extract, and
// outside it the file says nothing -- the tiles answer there. Wherever some
// area does contain the point the file knows the place, and a level the
// ranking leaves empty is Outside: two nested areas are a locality and a
// neighbourhood with nothing between them, and a nearest label from the
// tiles would be a guess placed beside two facts.
func (s *Source) Contains(l locate.Level, lat, lon float64) (name, kind, credit string, c locate.Containment) {
	if l == locate.Country && s.countryFrom == CountryOSM {
		if a, credit, ok := s.derivedCountry(lat, lon); ok {
			return a.Name, a.Kind, credit, locate.Inside
		}
		// No derived file holds the point, so Natural Earth answers as it
		// would by default -- including "at sea", which past the
		// territorial-waters limit is what both sources agree on.
	}
	layer, ok := layerFor(l)
	if !ok {
		if !derivedLevel(l) {
			return "", "", "", locate.NoData
		}
		a := answerFromStack(s.derivedStack(lat, lon), l)
		return a.Name, a.Kind, a.Credit, a.Containment
	}
	set := s.set(layer)
	if set == nil {
		return "", "", "", locate.NoData
	}
	a, ok := set.At(lat, lon)
	if !ok {
		return "", "", "", locate.Outside
	}
	// Natural Earth is public domain, so this is a statement of where the
	// answer came from rather than a credit that is owed -- and the string
	// says which. Returning nothing instead would be accurate about the
	// obligation and would lose the reader the one thing they can act on,
	// which is knowing that this name and the suburb below it came from
	// different places under different terms.
	return a.Name, a.Kind, NaturalEarthCredit, locate.Inside
}

// ContainsLevels answers several levels for one coordinate, building the
// derived files' containment stack once for all the levels below a region
// rather than once for each. Each answer is what Contains says for its level.
func (s *Source) ContainsLevels(lat, lon float64, levels []locate.Level) []locate.Answer {
	out := make([]locate.Answer, len(levels))
	var stack []credited
	built := false
	for i, l := range levels {
		_, natural := layerFor(l)
		if natural || !derivedLevel(l) {
			name, kind, credit, c := s.Contains(l, lat, lon)
			out[i] = locate.Answer{Name: name, Kind: kind, Credit: credit, Containment: c}
			continue
		}
		if !built {
			stack, built = s.derivedStack(lat, lon), true
		}
		out[i] = answerFromStack(stack, l)
	}
	return out
}

// answerFromStack is a derived level's answer from a point's containment
// stack: NoData where no derived area holds the point, Outside where one
// does and the ranking leaves the level empty.
func answerFromStack(stack []credited, l locate.Level) locate.Answer {
	if len(stack) == 0 {
		return locate.Answer{Containment: locate.NoData}
	}
	i, ok := rankIndex(len(stack), l)
	if !ok {
		return locate.Answer{Containment: locate.Outside}
	}
	return locate.Answer{Name: stack[i].area.Name, Kind: stack[i].area.Kind, Credit: stack[i].credit, Containment: locate.Inside}
}

// credited is an area with the attribution of the file it came from.
//
// The credit travels beside each area because the answer owes what the file
// it came from owes -- and a store may hold one file derived from
// OpenStreetMap beside one that is public domain. A credit taken from the
// store as a whole attributes an answer to whichever file did not give it.
type credited struct {
	area   Area
	credit string
}

// derivedStack is every area in every derived file that holds a point,
// outermost first, with each area once.
//
// Sorted again after merging even though each file's stack arrives sorted:
// a store may hold several regions, and sorting the merged stack is what
// makes their nesting a fact about the data rather than about which file
// was read first.
//
// And each area once. Two extracts that overlap -- a country and a province
// cut from it -- hold the same boundaries, and counting one twice spent two
// of the three levels on it: the kommune came back as both macrohood and
// neighbourhood. The copy from the file read first is kept, with its credit.
//
// And without the country. See isCountry.
func (s *Source) derivedStack(lat, lon float64) []credited {
	var stack []credited
	for _, set := range s.derived() {
		for _, a := range set.Containing(lat, lon) {
			if isCountry(a) {
				continue
			}
			if !slices.ContainsFunc(stack, func(c credited) bool { return sameArea(c.area, a) }) {
				stack = append(stack, credited{a, set.Provenance().Attribution})
			}
		}
	}
	slices.SortStableFunc(stack, func(a, b credited) int { return outermostFirst(a.area, b.area) })
	return stack
}

// derivedCountry is the national border holding a point, from the derived
// files, with the credit of the file it came from.
//
// The innermost if several do, as Set.At takes the smallest: two files cut
// from different extracts can both hold a border, and a country's border can
// sit inside a wider one's where a territory is tagged at level 2 too.
func (s *Source) derivedCountry(lat, lon float64) (Area, string, bool) {
	var found []credited
	for _, set := range s.derived() {
		for _, a := range set.Containing(lat, lon) {
			if isCountry(a) {
				found = append(found, credited{a, set.Provenance().Attribution})
			}
		}
	}
	if len(found) == 0 {
		return Area{}, "", false
	}
	slices.SortStableFunc(found, func(a, b credited) int { return outermostFirst(a.area, b.area) })
	last := found[len(found)-1]
	return last.area, last.credit, true
}

// isCountry reports whether an area is a national border, admin_level 2.
//
// Left out of the stack the levels below a region are ranked from. Ranking
// the three innermost areas keeps it out only when a point sits four or more
// deep, and a file built at every level in Denmark holds country, region and
// kommune -- three -- for most of the land: Horsens came back as locality
// "Danmark". Unlike what any other number means, admin_level 2 is the national
// border in every country's tagging, so this is not the per-country table the
// ranking refuses to be.
//
// The country LEVEL is Natural Earth's by default, and deliberately: its
// outline follows the coast, where OpenStreetMap's national border runs out
// to the territorial-waters limit, and for a route along a coast "at sea" is
// the answer wanted. CountryOSM answers it from these areas instead -- but
// from the country level, never by ranking one into the levels below.
// Leaving the area out here also means a point inside only the country --
// that strip of water -- is NoData for those levels and goes to the tiles.
func isCountry(a Area) bool {
	n, err := strconv.Atoi(a.Kind)
	return err == nil && n == 2
}

// sameArea reports whether two areas are one boundary read from two files.
//
// By name, kind and extent rather than by geometry: two files cut from
// different extracts may close a shared boundary with different vertex
// counts, and comparing points would call those two areas. Two different
// boundaries with the same name, the same admin_level and the same rectangle
// that both hold one point are not a case worth a slower rule.
func sameArea(a, b Area) bool {
	if a.Name != b.Name || a.Kind != b.Kind {
		return false
	}
	ea, okA := a.extent()
	eb, okB := b.extent()
	return okA == okB && ea == eb
}

// rankIndex is where in a stack of n areas, outermost first, a level's answer
// sits -- and false when the level has none.
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
// had already given at its own level. Anything wider than three deep is a
// region or a country and is answered from elsewhere.
//
// The country itself never reaches this: derivedStack leaves it out, because
// a stack exactly three deep -- country, region, kommune -- is most of
// Denmark, and three innermost would have kept it.
//
// Of those three, the outermost is the locality and the innermost the
// neighbourhood. One area is a locality and nothing else: the place has one
// administrative name at this range, and inventing two from it would be a
// claim the data does not make.
func rankIndex(n int, l locate.Level) (int, bool) {
	base := max(n-3, 0)
	switch n - base {
	case 0:
		return 0, false
	case 1:
		return base, l == locate.Locality
	}
	switch l {
	case locate.Locality:
		return base, true
	case locate.Neighbourhood:
		return n - 1, true
	case locate.Macrohood:
		if n-base == 3 {
			return base + 1, true
		}
	}
	return 0, false
}
