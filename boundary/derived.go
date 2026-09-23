package boundary

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/wisborg/osmbase/internal/protobuf"
)

// The derived boundary file.
//
// It exists because deriving these areas is expensive and the result is not:
// turning a half-gigabyte OpenStreetMap extract into a country's
// administrative outlines is three passes and half a minute, and what comes
// out is a few megabytes that answer a lookup instantly. So it is computed
// once and kept.
//
// The format is this module's own, for the reason the others are: GeoJSON
// would be ten times the size and a parse per lookup, and a general
// serialisation format is a dependency in the go.sum of every program that
// renders a map. See docs/architecture.md.
//
// It is on somebody's disk, which is what the version byte is for. A file
// written by a later osmbase than the one reading it must be refused by name
// rather than misread: the alternative is a boundary silently in the wrong
// place, and nothing about a wrong answer here looks wrong.

// Magic and version.
//
// The version is compared for EQUALITY, in both directions, and that is only
// affordable because of the header length below. A file from a later osmbase
// read as this version does not fail -- it yields boundaries somewhere else,
// and nothing about a wrong place looks wrong. Refusing an OLDER file is the
// expensive half of that rule: every file on disk becomes a re-download of a
// country extract. So the version is reserved for changes to the GEOMETRY
// encoding, which cannot be skipped over, and anything added to the header
// rides the length prefix instead and needs no bump at all.
const (
	derivedMagic   = "osmb"
	derivedVersion = 1
)

// Coordinates are stored as integers in ten-millionths of a degree.
//
// That is about a centimetre, and it is not an approximation of the source:
// OpenStreetMap stores a coordinate as a nanodegree scaled by a block's
// granularity, which every producer writes as 100 -- ten-millionths of a
// degree exactly. So a round trip through this file is lossless for the data
// it is built from, and a Natural Earth ring written through it is rounded to
// finer than the shapes are drawn.
const derivedScale = 1e7

// ErrDerivedFormat reports bytes that are not a derived boundary file, or are
// a version this cannot read.
var ErrDerivedFormat = errors.New("boundary: not a readable derived boundary file")

// Provenance is what the file says about itself.
//
// It exists because of the licence. A derived boundary file is a Derivative
// Database under the ODbL rather than a Produced Work -- it IS the map data
// in another shape -- so share-alike attaches to the file itself, and
// whoever receives it must receive it under that licence. A format carrying
// only geometry hands somebody a Derivative Database with no notice inside
// it, and telling the person who BUILT it is telling the one person who
// already knows.
//
// This module had already decided the same question the same way for tile
// slices: "the attribution is DATA and not a constant... writing it here,
// from whatever source the bytes actually came from, is what stops a change
// of source leaving the old credit spelled into the program."
//
// Source and Levels earn their place for a second reason: two stores' files
// are otherwise indistinguishable, and a file swapped underneath a program
// that believed it had the other changes every answer near a border with
// nothing to say it had.
type Provenance struct {
	// Source names what the file was built from -- an extract's name or the
	// URL it came from.
	Source string

	// Attribution is the credit the source's licence requires, empty when it
	// requires none. Natural Earth is public domain; OpenStreetMap is not.
	Attribution string

	// Created is when the file was derived.
	Created time.Time

	// Levels are the admin levels it was built for. Without them a reader
	// cannot tell "this file has no suburbs" from "this file was not asked
	// for suburbs".
	Levels []int
}

// Limits on what a file may declare.
//
// A derived file is built locally, but it is a file on disk like any other:
// it can be truncated, corrupted, or replaced, and every count below decides
// an allocation. They are checked as each item is read rather than after the
// count, because a limit consulted afterwards has already spent what it is
// refusing -- which is the mistake this module has now made four times
// elsewhere and does not need to make again.
const (
	maxDerivedAreas    = 1 << 22
	maxDerivedPolygons = 1 << 20
	maxDerivedRings    = 1 << 20
	maxDerivedPoints   = 1 << 26
	maxDerivedString   = 1 << 12
	maxDerivedLevels   = 64
	maxDerivedHeader   = 1 << 16

	// maxDerivedTotalPolygons and maxDerivedTotalRings bound the file as a
	// whole, which the per-item limits above do not.
	//
	// They are not the same kind of limit and the difference is the whole
	// point. Per item nothing is disproportionate: a declared count is
	// refused before it is allocated for. Across items it is, because an
	// EMPTY structure still costs a header -- a file of areas each declaring
	// a million polygons of no rings costs one byte per polygon and yields
	// eighty bytes of slice and struct, measured at 59 times the file live
	// and 109 times its peak.
	//
	// There is deliberately no total for POINTS. A point costs two bytes on
	// the wire, so the file's own length bounds it to within a factor of
	// eight of the memory it becomes, and a budget reachable only by a
	// hundred-megabyte file is a branch no test can honestly exercise. An
	// empty polygon or an empty ring costs one byte and becomes eighty, and
	// those are the ones worth counting.
	//
	// Sydney's whole file is 0.41 MB with 471 areas, so these leave several
	// orders of magnitude of room.
	maxDerivedTotalPolygons = 1 << 22
	maxDerivedTotalRings    = 1 << 22
)

// The coordinate system, in stored units. A vertex outside it is not a
// boundary under any reading of the format, and the read is the only place
// it can be caught: the deltas accumulate in int64 and wrap silently, which
// turns corruption in the last bytes of one ring into an area whose bounding
// box spans most of the plane -- and because Set.At prefers the smallest
// containing box, such an area wins exactly where the honest answer is
// "nowhere".
const (
	maxStoredLat = 90 * derivedScale
	maxStoredLon = 180 * derivedScale
)

// NewArea builds an area from rings, computing the bounding boxes a lookup
// needs.
func NewArea(name, kind string, polys []Polygon) Area {
	out := make([][]Ring, 0, len(polys))
	for _, p := range polys {
		rings := make([]Ring, 0, 1+len(p.Holes))
		rings = append(rings, p.Outer)
		rings = append(rings, p.Holes...)
		out = append(out, rings)
	}
	return newArea(name, kind, out)
}

// NewSet returns a set of areas, searchable by coordinate.
func NewSet(p Provenance, areas []Area) *Set { return &Set{areas: areas, prov: p} }

// Polygons pairs holes with the outlines that contain them.
//
// Ring assembly yields outlines and holes in two flat lists, because which
// hole belongs to which outline is a containment question and assembly is a
// joining one. This is where it is answered, and it is answered here rather
// than by the caller because the test it needs -- is this point inside that
// ring -- already exists in this package. A third ray-cast written elsewhere
// would be the third in a module that has been told twice about the cost of a
// rule with two spellings.
//
// The SMALLEST containing outline wins, for the same reason Set.At takes the
// smallest containing area: outlines nest. An enclave inside an enclave is
// rare on administrative boundaries and not impossible, and taking the first
// would cut the hole out of the wrong one.
//
// A hole inside nothing is returned rather than discarded. It means the
// outline that should contain it was cut off by the extract's edge, and that
// is a fact about the data the caller may want to report.
func Polygons(outers, holes []Ring) (polys []Polygon, orphans []Ring, err error) {
	// Quadratic in the two lengths, so it is bounded. The box prefilter
	// makes it cheap on real data -- outlines of one relation rarely
	// overlap -- and defeats nothing at all when every ring sits at the same
	// coordinate, which a crafted relation can arrange: measured at 800
	// outlines against 800 holes it took most of a second, and a single
	// relation may name enough ways to spend the whole geometry budget.
	if len(outers)*len(holes) > maxPairings {
		return nil, nil, fmt.Errorf("boundary: pairing %d outlines against %d holes is past the %d this does",
			len(outers), len(holes), maxPairings)
	}

	polys = make([]Polygon, len(outers))
	boxes := make([]box, len(outers))
	for i, o := range outers {
		polys[i] = Polygon{Outer: o}
		boxes[i] = boxOf(o)
	}

	for _, h := range holes {
		if len(h) == 0 {
			// Skipped, and deliberately NOT an orphan: an orphan means the
			// outline that should hold this hole was cut off at the edge of
			// the extract, and a ring with no points says nothing of the
			// kind. Ring assembly cannot produce one -- a ring enclosing
			// nothing is refused there -- but Polygons is exported.
			continue
		}
		best := holder(outers, boxes, h)
		if best < 0 {
			orphans = append(orphans, h)
			continue
		}
		polys[best].Holes = append(polys[best].Holes, h)
	}
	return polys, orphans, nil
}

// maxPairings bounds the work Polygons will do. A relation with more
// outlines than a country has islands, each with a hole, is not a boundary.
const maxPairings = 1 << 20

// holder finds the outline a hole belongs in, or -1.
//
// It asks about several of the hole's vertices rather than one. A vertex
// lying exactly ON an outline is undecidable for ray casting -- inRing is
// careful to say its on-boundary conventions are conventions -- and holes
// that touch the outline around them are ordinary in administrative data.
// Deciding from one vertex turns that into an orphan, which Report then
// attributes to the extract's edge, and the hole is dropped: the lake fills
// in and the area answers "inside" for every point in it.
func holder(outers []Ring, boxes []box, h Ring) int {
	const probes = 5
	step := max(len(h)/probes, 1)

	for at := 0; at < len(h); at += step {
		c := h[at]
		best := -1
		for i, o := range outers {
			if !boxes[i].holds(c.Lat, c.Lon) || !inRing(o, c.Lat, c.Lon) {
				continue
			}
			// The SMALLEST containing outline wins, for the same reason
			// Set.At takes the smallest containing area: outlines nest. An
			// enclave inside an enclave is rare on administrative boundaries
			// and not impossible, and taking the first would cut the hole
			// out of the wrong one. Box area orders nested rings correctly;
			// it would not order merely overlapping ones, and nothing here
			// produces those.
			if best < 0 || boxes[i].area() < boxes[best].area() {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
	}
	return -1
}

// WriteDerived writes a set in the derived format.
//
// It takes and ReadDerived returns the same type, so the round trip is one in
// the type system too -- and the provenance a reader will look for is
// something the writer cannot leave out by forgetting a parameter.
func WriteDerived(w io.Writer, set *Set) error {
	if set == nil {
		set = &Set{}
	}
	if err := checkWritable(set); err != nil {
		return err
	}

	bw := bufio.NewWriter(w)
	e := &encoder{w: bw}

	e.raw([]byte(derivedMagic))
	e.uvarint(derivedVersion)

	// The header goes out length-prefixed, so a later osmbase can add a
	// field to it and a reader of this version steps over what it does not
	// know instead of refusing the file. Building it in memory first is what
	// makes the length knowable.
	var head encoder
	head.str(set.prov.Source)
	head.str(set.prov.Attribution)
	head.uvarint(uint64(max(set.prov.Created.Unix(), 0)))
	head.uvarint(uint64(len(set.prov.Levels)))
	for _, l := range set.prov.Levels {
		head.uvarint(uint64(l))
	}
	if head.err == nil && len(head.buf) > maxDerivedHeader {
		return fmt.Errorf("boundary: the header is %d bytes, past the %d this format holds",
			len(head.buf), maxDerivedHeader)
	}
	if head.err != nil {
		return head.err
	}
	e.uvarint(uint64(len(head.buf)))
	e.raw(head.buf)

	e.uvarint(uint64(len(set.areas)))
	for _, a := range set.areas {
		e.str(a.Name)
		e.str(a.Kind)
		e.uvarint(uint64(len(a.polygons)))
		for _, p := range a.polygons {
			e.uvarint(uint64(len(p.rings)))
			for _, r := range p.rings {
				e.uvarint(uint64(len(r)))
				// Delta coded against the previous vertex, then zigzagged.
				// Consecutive vertices of a boundary are metres apart, so a
				// step is one or two bytes where an absolute coordinate is
				// five -- which is most of why this file is a few megabytes
				// rather than tens.
				var lat, lon int64
				for _, pt := range r {
					qLat, qLon := quantise(pt.Lat), quantise(pt.Lon)
					e.uvarint(protobuf.Zigzag64(qLat - lat))
					e.uvarint(protobuf.Zigzag64(qLon - lon))
					lat, lon = qLat, qLon
				}
			}
		}
		if e.err != nil {
			return e.err
		}
	}
	if e.err != nil {
		return e.err
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("boundary: writing a derived boundary file: %w", err)
	}
	return nil
}

// checkWritable refuses a set this format cannot store.
//
// Every limit ReadDerived enforces, enforced here too. Without it a set can
// be written successfully and then refused on load -- a file nobody can read,
// discovered on a different day than it was produced.
func checkWritable(set *Set) error {
	if len(set.areas) > maxDerivedAreas {
		return fmt.Errorf("boundary: %d areas is past the %d this format holds", len(set.areas), maxDerivedAreas)
	}
	if len(set.prov.Levels) > maxDerivedLevels {
		return fmt.Errorf("boundary: %d levels is past the %d this format holds",
			len(set.prov.Levels), maxDerivedLevels)
	}
	var polygons, rings int
	for _, a := range set.areas {
		if a.Name == "" {
			// The other two readers in this package both refuse a nameless
			// shape, because it cannot answer the question the package
			// exists for. Refused here so the file never holds one.
			return errors.New("boundary: an area has no name, and a nameless area cannot answer a lookup")
		}
		if len(a.Name) > maxDerivedString || len(a.Kind) > maxDerivedString {
			return fmt.Errorf("boundary: a name of %d bytes is past the %d this format holds",
				max(len(a.Name), len(a.Kind)), maxDerivedString)
		}
		polygons += len(a.polygons)
		if len(a.polygons) > maxDerivedPolygons {
			return fmt.Errorf("boundary: %q has %d polygons, past the %d this format holds",
				a.Name, len(a.polygons), maxDerivedPolygons)
		}
		for _, p := range a.polygons {
			if len(p.rings) > maxDerivedRings {
				return fmt.Errorf("boundary: %q has a polygon of %d rings, past the %d this format holds",
					a.Name, len(p.rings), maxDerivedRings)
			}
			rings += len(p.rings)
			for _, r := range p.rings {
				if len(r) > maxDerivedPoints {
					return fmt.Errorf("boundary: %q has a ring of %d points, past the %d this format holds",
						a.Name, len(r), maxDerivedPoints)
				}
			}
		}
	}
	if polygons > maxDerivedTotalPolygons {
		return fmt.Errorf("boundary: %d polygons is past the %d this format holds",
			polygons, maxDerivedTotalPolygons)
	}
	if rings > maxDerivedTotalRings {
		return fmt.Errorf("boundary: %d rings is past the %d this format holds", rings, maxDerivedTotalRings)
	}
	return nil
}

// encoder accumulates bytes, remembering the first write that failed.
//
// The error is held rather than returned per call because the alternative is
// a check after every one of five thousand writes, and the one that matters
// most -- a file smaller than the buffer, where nothing reaches the disk
// until Flush -- is the one such checks miss.
type encoder struct {
	w   *bufio.Writer
	buf []byte
	err error
}

func (e *encoder) raw(b []byte) {
	if e.err != nil {
		return
	}
	if e.w == nil {
		e.buf = append(e.buf, b...)
		return
	}
	if _, err := e.w.Write(b); err != nil {
		e.err = fmt.Errorf("boundary: writing a derived boundary file: %w", err)
	}
}

func (e *encoder) uvarint(v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	e.raw(binary.AppendUvarint(tmp[:0], v))
}

func (e *encoder) str(s string) {
	if e.err != nil {
		return
	}
	if len(s) > maxDerivedString {
		e.err = fmt.Errorf("boundary: a string of %d bytes is past the %d this format holds",
			len(s), maxDerivedString)
		return
	}
	e.uvarint(uint64(len(s)))
	e.raw([]byte(s))
}

func quantise(deg float64) int64 { return int64(math.Round(deg * derivedScale)) }

// ReadDerived reads a derived boundary file.
func ReadDerived(r io.Reader) (*Set, error) {
	br := bufio.NewReader(r)

	magic := make([]byte, len(derivedMagic))
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDerivedFormat, err)
	}
	if string(magic) != derivedMagic {
		return nil, fmt.Errorf("%w: it does not begin with %q", ErrDerivedFormat, derivedMagic)
	}

	version, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the version: %v", ErrDerivedFormat, err)
	}
	if version != derivedVersion {
		return nil, fmt.Errorf("%w: it is version %d and this reads version %d",
			ErrDerivedFormat, version, derivedVersion)
	}

	prov, err := readHeader(br)
	if err != nil {
		return nil, err
	}

	count, err := readCount(br, "areas", maxDerivedAreas)
	if err != nil {
		return nil, err
	}
	b := &budget{polygons: maxDerivedTotalPolygons, rings: maxDerivedTotalRings}
	areas := make([]Area, 0, min(count, 1024))
	for range count {
		a, err := readArea(br, b)
		if err != nil {
			return nil, err
		}
		areas = append(areas, a)
	}

	// Nothing may follow the last area. The realistic way to get here is not
	// an attacker but a rewrite in place that did not truncate, leaving the
	// tail of a longer previous file behind -- which would otherwise read
	// clean and silently be half of each.
	if _, err := br.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("%w: it carries bytes after the last area", ErrDerivedFormat)
	}
	return &Set{areas: areas, prov: prov}, nil
}

// readHeader reads the length-prefixed provenance block, stepping over
// anything a later version added that this does not know.
func readHeader(br *bufio.Reader) (Provenance, error) {
	n, err := readCount(br, "header bytes", maxDerivedHeader)
	if err != nil {
		return Provenance{}, err
	}
	head := make([]byte, n)
	if _, err := io.ReadFull(br, head); err != nil {
		return Provenance{}, fmt.Errorf("%w: reading the header: %v", ErrDerivedFormat, err)
	}

	var p Provenance
	hr := bufio.NewReader(bytes.NewReader(head))
	if p.Source, err = readString(hr); err != nil {
		return Provenance{}, err
	}
	if p.Attribution, err = readString(hr); err != nil {
		return Provenance{}, err
	}
	created, err := binary.ReadUvarint(hr)
	if err != nil {
		return Provenance{}, fmt.Errorf("%w: reading the creation time: %v", ErrDerivedFormat, err)
	}
	if created > 0 {
		p.Created = time.Unix(int64(created), 0).UTC()
	}
	levels, err := readCount(hr, "levels", maxDerivedLevels)
	if err != nil {
		return Provenance{}, err
	}
	for range levels {
		v, err := binary.ReadUvarint(hr)
		if err != nil {
			return Provenance{}, fmt.Errorf("%w: reading a level: %v", ErrDerivedFormat, err)
		}
		p.Levels = append(p.Levels, int(v))
	}
	// Whatever remains is a field from a later version. Stepping over it is
	// the point of the length prefix.
	return p, nil
}

// budget is what the file as a whole may spend.
type budget struct{ polygons, rings int }

func (b *budget) spend(what string, n int, left *int) error {
	if n > *left {
		return fmt.Errorf("%w: it holds more than %s than this reads", ErrDerivedFormat, what)
	}
	*left -= n
	return nil
}

func readArea(br *bufio.Reader, b *budget) (Area, error) {
	name, err := readString(br)
	if err != nil {
		return Area{}, err
	}
	if name == "" {
		// The other readers in this package both refuse a nameless shape.
		// Skipped here, a corrupt file yields an area that answers
		// containment true and names nowhere.
		return Area{}, fmt.Errorf("%w: it holds an area with no name", ErrDerivedFormat)
	}
	kind, err := readString(br)
	if err != nil {
		return Area{}, err
	}
	polyCount, err := readCount(br, "polygons", maxDerivedPolygons)
	if err != nil {
		return Area{}, err
	}
	if err := b.spend("polygons", polyCount, &b.polygons); err != nil {
		return Area{}, err
	}

	polys := make([][]Ring, 0, min(polyCount, 64))
	for range polyCount {
		ringCount, err := readCount(br, "rings", maxDerivedRings)
		if err != nil {
			return Area{}, err
		}
		if err := b.spend("rings", ringCount, &b.rings); err != nil {
			return Area{}, err
		}
		rings := make([]Ring, 0, min(ringCount, 16))
		for range ringCount {
			ring, err := readRing(br)
			if err != nil {
				return Area{}, err
			}
			rings = append(rings, ring)
		}
		polys = append(polys, rings)
	}
	// Through the same constructor the GeoJSON reader uses, so the bounding
	// boxes a lookup depends on are computed the one way rather than stored
	// and believed.
	return newArea(name, kind, polys), nil
}

func readRing(br *bufio.Reader) (Ring, error) {
	n, err := readCount(br, "points", maxDerivedPoints)
	if err != nil {
		return nil, err
	}
	ring := make(Ring, 0, min(n, 4096))
	var lat, lon int64
	for range n {
		dLat, err := binary.ReadUvarint(br)
		if err != nil {
			return nil, fmt.Errorf("%w: reading a latitude: %v", ErrDerivedFormat, err)
		}
		dLon, err := binary.ReadUvarint(br)
		if err != nil {
			return nil, fmt.Errorf("%w: reading a longitude: %v", ErrDerivedFormat, err)
		}
		lat += protobuf.Unzigzag64(dLat)
		lon += protobuf.Unzigzag64(dLon)
		if lat < -maxStoredLat || lat > maxStoredLat || lon < -maxStoredLon || lon > maxStoredLon {
			return nil, fmt.Errorf("%w: it holds a vertex at %d,%d, which is off the Earth",
				ErrDerivedFormat, lat, lon)
		}
		ring = append(ring, Coord{Lat: float64(lat) / derivedScale, Lon: float64(lon) / derivedScale})
	}
	return ring, nil
}

func readString(br *bufio.Reader) (string, error) {
	n, err := readCount(br, "a name", maxDerivedString)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return "", fmt.Errorf("%w: reading a name: %v", ErrDerivedFormat, err)
	}
	return string(b), nil
}

// readCount reads a declared count and refuses one past its limit before
// anything is allocated for it.
func readCount(br *bufio.Reader, what string, max int) (int, error) {
	v, err := binary.ReadUvarint(br)
	if err != nil {
		return 0, fmt.Errorf("%w: reading a count of %s: %v", ErrDerivedFormat, what, err)
	}
	if v > uint64(max) {
		return 0, fmt.Errorf("%w: it declares %d %s and this reads at most %d", ErrDerivedFormat, v, what, max)
	}
	return int(v), nil
}
