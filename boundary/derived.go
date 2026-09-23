package boundary

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
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
)

// Coord is a position in degrees, latitude first.
type Coord struct{ Lat, Lon float64 }

// Ring is a closed ring, with the first point NOT repeated at the end.
type Ring []Coord

// Polygon is one part of an area: an outline and the holes in it.
type Polygon struct {
	Outer Ring
	Holes []Ring
}

// NewArea builds an area from rings, computing the bounding boxes a lookup
// needs.
func NewArea(name, kind string, polys []Polygon) Area {
	out := make([][][]point, 0, len(polys))
	for _, p := range polys {
		rings := make([][]point, 0, 1+len(p.Holes))
		rings = append(rings, toPoints(p.Outer))
		for _, h := range p.Holes {
			rings = append(rings, toPoints(h))
		}
		out = append(out, rings)
	}
	return newArea(name, kind, out)
}

// NewSet returns a set of areas, searchable by coordinate.
func NewSet(areas []Area) *Set { return &Set{areas: areas} }

func toPoints(r Ring) []point {
	out := make([]point, len(r))
	for i, c := range r {
		out[i] = point{lat: c.Lat, lon: c.Lon}
	}
	return out
}

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
func Polygons(outers, holes []Ring) (polys []Polygon, orphans []Ring) {
	polys = make([]Polygon, len(outers))
	boxes := make([]box, len(outers))
	for i, o := range outers {
		polys[i] = Polygon{Outer: o}
		boxes[i] = boxOf(o)
	}

	for _, h := range holes {
		if len(h) == 0 {
			continue
		}
		best := -1
		for i, o := range outers {
			if !boxes[i].holds(h[0]) || !inRing(toPoints(o), h[0].Lat, h[0].Lon) {
				continue
			}
			if best < 0 || boxes[i].area() < boxes[best].area() {
				best = i
			}
		}
		if best < 0 {
			orphans = append(orphans, h)
			continue
		}
		polys[best].Holes = append(polys[best].Holes, h)
	}
	return polys, orphans
}

type box struct{ west, south, east, north float64 }

func boxOf(r Ring) box {
	b := box{west: math.Inf(1), south: math.Inf(1), east: math.Inf(-1), north: math.Inf(-1)}
	for _, c := range r {
		b.west, b.east = math.Min(b.west, c.Lon), math.Max(b.east, c.Lon)
		b.south, b.north = math.Min(b.south, c.Lat), math.Max(b.north, c.Lat)
	}
	return b
}

func (b box) holds(c Coord) bool {
	return c.Lon >= b.west && c.Lon <= b.east && c.Lat >= b.south && c.Lat <= b.north
}

func (b box) area() float64 { return (b.east - b.west) * (b.north - b.south) }

// WriteDerived writes areas in the derived format.
func WriteDerived(w io.Writer, areas []Area) error {
	bw := bufio.NewWriter(w)

	if _, err := bw.WriteString(derivedMagic); err != nil {
		return err
	}
	var buf []byte
	put := func(v uint64) error {
		buf = binary.AppendUvarint(buf[:0], v)
		_, err := bw.Write(buf)
		return err
	}
	putStr := func(s string) error {
		if len(s) > maxDerivedString {
			return fmt.Errorf("boundary: a name of %d bytes is past the %d this format holds", len(s), maxDerivedString)
		}
		if err := put(uint64(len(s))); err != nil {
			return err
		}
		_, err := bw.WriteString(s)
		return err
	}

	if err := put(derivedVersion); err != nil {
		return err
	}
	if err := put(uint64(len(areas))); err != nil {
		return err
	}

	for _, a := range areas {
		if err := putStr(a.Name); err != nil {
			return err
		}
		if err := putStr(a.Kind); err != nil {
			return err
		}
		if err := put(uint64(len(a.polygons))); err != nil {
			return err
		}
		for _, p := range a.polygons {
			if err := put(uint64(len(p.rings))); err != nil {
				return err
			}
			for _, r := range p.rings {
				if err := put(uint64(len(r))); err != nil {
					return err
				}
				// Delta coded against the previous vertex, then zigzagged.
				// Consecutive vertices of a boundary are metres apart, so a
				// step is one or two bytes where an absolute coordinate is
				// five -- which is most of why this file is a few megabytes
				// rather than tens.
				var lat, lon int64
				for _, pt := range r {
					dLat := quantise(pt.lat) - lat
					dLon := quantise(pt.lon) - lon
					lat, lon = lat+dLat, lon+dLon
					if err := put(zigzag(dLat)); err != nil {
						return err
					}
					if err := put(zigzag(dLon)); err != nil {
						return err
					}
				}
			}
		}
	}
	return bw.Flush()
}

func quantise(deg float64) int64 { return int64(math.Round(deg * derivedScale)) }

func zigzag(v int64) uint64 { return uint64((v << 1) ^ (v >> 63)) }

func unzigzag(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }

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
		// Named rather than attempted. A file from a later osmbase read as
		// this version does not fail -- it yields boundaries somewhere else
		// entirely, and nothing about a wrong place looks wrong.
		return nil, fmt.Errorf("%w: it is version %d and this reads version %d",
			ErrDerivedFormat, version, derivedVersion)
	}

	count, err := readCount(br, "areas", maxDerivedAreas)
	if err != nil {
		return nil, err
	}
	areas := make([]Area, 0, min(count, 1024))
	for range count {
		a, err := readArea(br)
		if err != nil {
			return nil, err
		}
		areas = append(areas, a)
	}
	return &Set{areas: areas}, nil
}

func readArea(br *bufio.Reader) (Area, error) {
	name, err := readString(br)
	if err != nil {
		return Area{}, err
	}
	kind, err := readString(br)
	if err != nil {
		return Area{}, err
	}
	polyCount, err := readCount(br, "polygons", maxDerivedPolygons)
	if err != nil {
		return Area{}, err
	}

	polys := make([][][]point, 0, min(polyCount, 64))
	for range polyCount {
		ringCount, err := readCount(br, "rings", maxDerivedRings)
		if err != nil {
			return Area{}, err
		}
		rings := make([][]point, 0, min(ringCount, 16))
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

func readRing(br *bufio.Reader) ([]point, error) {
	n, err := readCount(br, "points", maxDerivedPoints)
	if err != nil {
		return nil, err
	}
	ring := make([]point, 0, min(n, 4096))
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
		lat += unzigzag(dLat)
		lon += unzigzag(dLon)
		ring = append(ring, point{lat: float64(lat) / derivedScale, lon: float64(lon) / derivedScale})
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
