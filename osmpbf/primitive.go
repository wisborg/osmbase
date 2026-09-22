package osmpbf

import (
	"fmt"

	"github.com/wisborg/osmbase/internal/protobuf"
)

// Defaults the format states for fields a block may leave out. Absent means
// the default here, not zero -- a block that omits granularity is using 100,
// and reading it as 0 would put every coordinate in the file at the offset.
const (
	DefaultGranularity     = 100
	DefaultDateGranularity = 1000
)

// Caps on how many entries a block may hold.
//
// Every other limit in this package bounds BYTES, and that is not enough on
// its own: the cheapest string table entry on the wire is two bytes, so a
// block of exactly MaxBlockBytes -- which passes every blob check there is --
// can declare sixteen million of them, and the slice headers alone are then
// hundreds of megabytes. zlib delivers those 32 MiB from about 32 KiB on
// disk, so the amplification from the file as downloaded is five orders of
// magnitude. Bounding the count is what closes that.
//
// The headroom is large. The format's own convention is at most 8000 elements
// to a block and normally exactly one group, and a real string table runs to
// tens of thousands of entries.
const (
	MaxStrings = 1 << 20
	MaxGroups  = 1 << 16
)

// MaxGranularity is the largest coordinate scaling this accepts.
//
// Granularity is nanodegrees per unit, so 1e9 is one whole degree per unit and
// anything above it describes a step larger than the coordinate system. The
// bound is not pedantry: Degrees multiplies the granularity by a coordinate in
// int64, and Go wraps silently, so an unbounded granularity turns a crafted
// block into confident coordinates in the wrong ocean rather than an error.
const MaxGranularity = 1_000_000_000

// PrimitiveBlock is one OSMData block, decoded as far as its string table.
//
// The elements themselves are left as undecoded Groups. A boundary pipeline
// reads the file three times -- relations, then ways, then nodes -- and each
// pass wants a different one of the element kinds, so decoding all of them on
// every pass would do three times the work to throw most of it away.
type PrimitiveBlock struct {
	// Strings is the block's string table. Every tag key and value in the
	// block is an index into it, which is how the format avoids repeating
	// "boundary" once per relation.
	//
	// The entries point into the Block this was decoded from, so they are
	// only valid until the next call to Reader.Next. Use String to take a
	// copy of one worth keeping.
	Strings [][]byte

	// Groups holds each PrimitiveGroup's undecoded bytes, in file order.
	Groups [][]byte

	// Granularity and the offsets convert an element's integer coordinates
	// into degrees; see Degrees.
	Granularity     int32
	LatOffset       int64
	LonOffset       int64
	DateGranularity int32
}

// StringAt returns a copy of string table entry i, or "" for an index outside
// the table.
//
// Index 0 is the empty string by decree; see BytesAt.
//
// Index 0 is the empty string by convention -- the format uses 0 to mean "no
// string" -- so a caller that resolves a key of 0 gets "" rather than an
// arbitrary entry.
//
// Out of range is "" rather than an error because every caller is resolving a
// tag key or value, where a missing string means the tag does not match, and
// an index out of range in a downloaded extract should not stop a pass over
// several million elements.
func (b PrimitiveBlock) StringAt(i int) string {
	// Index 0 is "" by decree, not by hope. The format reserves it to mean
	// "no string", but nothing stops a file from putting a real string there,
	// and a caller resolving an unset tag key would then pick up whatever the
	// file chose. Returning "" here is what every caller resolving a tag
	// wants, and it is what this comment used to claim without the code
	// doing it.
	return string(b.BytesAt(i))
}

// BytesAt is StringAt without the copy: the same guard, returning the entry as
// it sits in the reader's buffer.
//
// It exists because the passes over an extract resolve a tag key or value for
// every element in the file and discard almost all of them -- the question is
// nearly always whether the string is "boundary" or "name", not what it says.
// StringAt on that path would allocate a Go string per tag across millions of
// elements; indexing Strings directly would answer it without the guard.
//
// The result points into a buffer the reader reuses, so compare it and move
// on, or take StringAt's copy for one worth keeping.
func (b PrimitiveBlock) BytesAt(i int) []byte {
	if i <= 0 || i >= len(b.Strings) {
		return nil
	}
	return b.Strings[i]
}

// Degrees converts a block-relative coordinate pair into degrees.
//
// The format stores coordinates as integers in units of a nanodegree scaled
// by the block's granularity, offset by the block's origin. Dividing by 1e9
// at the end rather than scaling each term keeps the arithmetic in int64
// until the last step, where a float64 still holds nine decimal places of a
// degree exactly.
func (b PrimitiveBlock) Degrees(lat, lon int64) (float64, float64) {
	g := int64(b.Granularity)
	return float64(b.LatOffset+g*lat) / 1e9, float64(b.LonOffset+g*lon) / 1e9
}

// DecodePrimitiveBlock decodes a block's string table, groups and coordinate
// scaling. data must be an OSMData block's decompressed bytes.
func DecodePrimitiveBlock(data []byte) (PrimitiveBlock, error) {
	b := PrimitiveBlock{
		Granularity:     DefaultGranularity,
		DateGranularity: DefaultDateGranularity,
	}
	r := protobuf.New(data, "osmpbf", "a primitive block")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return PrimitiveBlock{}, err
		}
		switch {
		case field == 1 && wire == protobuf.WireBytes: // stringtable
			v, err := r.Bytes("a string table")
			if err != nil {
				return PrimitiveBlock{}, err
			}
			if b.Strings, err = decodeStringTable(v); err != nil {
				return PrimitiveBlock{}, err
			}
		case field == 2 && wire == protobuf.WireBytes: // primitivegroup
			v, err := r.Bytes("a primitive group")
			if err != nil {
				return PrimitiveBlock{}, err
			}
			if len(b.Groups) >= MaxGroups {
				return PrimitiveBlock{}, fmt.Errorf(
					"osmpbf: a primitive block holds more than %d groups, and a real one holds about one", MaxGroups)
			}
			b.Groups = append(b.Groups, v)
		case field == 17 && wire == protobuf.WireVarint: // granularity
			v, err := r.Uvarint("a granularity")
			if err != nil {
				return PrimitiveBlock{}, err
			}
			b.Granularity = int32(v)
		case field == 18 && wire == protobuf.WireVarint: // date_granularity
			v, err := r.Uvarint("a date granularity")
			if err != nil {
				return PrimitiveBlock{}, err
			}
			b.DateGranularity = int32(v)
		case field == 19 && wire == protobuf.WireVarint: // lat_offset
			v, err := r.Uvarint("a latitude offset")
			if err != nil {
				return PrimitiveBlock{}, err
			}
			b.LatOffset = int64(v)
		case field == 20 && wire == protobuf.WireVarint: // lon_offset
			v, err := r.Uvarint("a longitude offset")
			if err != nil {
				return PrimitiveBlock{}, err
			}
			b.LonOffset = int64(v)
		default:
			if err := r.Skip(field, wire); err != nil {
				return PrimitiveBlock{}, err
			}
		}
	}

	// Refused rather than defaulted. A granularity of zero multiplies every
	// coordinate in the block to nothing, which does not fail -- it silently
	// stacks every node in the block on the block's origin, and the pipeline
	// downstream would report a boundary as a single point rather than an
	// error. A negative one is not a value the format has.
	if b.Granularity <= 0 || b.Granularity > MaxGranularity {
		return PrimitiveBlock{}, fmt.Errorf(
			"osmpbf: a primitive block declares a granularity of %d, and coordinates are scaled by it", b.Granularity)
	}
	return b, nil
}

// decodeStringTable reads the repeated bytes of a StringTable.
func decodeStringTable(data []byte) ([][]byte, error) {
	var out [][]byte
	r := protobuf.New(data, "osmpbf", "a string table")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return nil, err
		}
		if field == 1 && wire == protobuf.WireBytes {
			v, err := r.Bytes("a string")
			if err != nil {
				return nil, err
			}
			if len(out) >= MaxStrings {
				return nil, fmt.Errorf(
					"osmpbf: a string table holds more than %d entries, and the block's own size cannot account for that many", MaxStrings)
			}
			out = append(out, v)
			continue
		}
		if err := r.Skip(field, wire); err != nil {
			return nil, err
		}
	}
	return out, nil
}
