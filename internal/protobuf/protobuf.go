// Package protobuf is a protocol buffers wire-format reader.
//
// It exists because two formats this library decodes are protobuf -- Mapbox
// Vector Tiles and OpenStreetMap PBF -- and one of them arriving second is
// not a reason to have two readers. It is about a hundred lines because a
// wire-format reader is small, and because this module admits no third-party
// dependency: google.golang.org/protobuf would arrive in the go.sum and
// NOTICE of every program that renders a map, to decode formats that have not
// changed in a decade. See docs/architecture.md.
//
// What is deliberately NOT here is any notion of a message type, a descriptor
// or a default. Each message is decoded by a function that knows its own field
// numbers, and every unknown field number is skipped, which is what makes a
// file carrying a field this has never heard of decode rather than fail.
//
// internal because it is an implementation detail of the decoders and not
// something a consumer of this library should be offered as an API.
package protobuf

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Protobuf wire types. Types 3 and 4 (start group, end group) were removed
// from the language long before either of the schemas this reads was written
// and are not handled; encountering one means the bytes are not of the format
// they claim to be.
const (
	WireVarint     = 0
	WireFixed64    = 1
	WireBytes      = 2
	WireStartGroup = 3
	WireEndGroup   = 4
	WireFixed32    = 5
)

// Reader walks a protobuf message. Every method reports where it ran out
// of bytes, because "unexpected EOF" alone does not distinguish a truncated
// tile from a field length that was misread.
type Reader struct {
	b      []byte
	i      int
	format string // the file format being read, for errors
	what   string // the message being read, for errors
}

// New returns a reader over a message. format names the file format --
// "mvt", "osmpbf" -- and what names the message within it -- "a layer", "a
// relation" -- because "unexpected EOF" alone says neither which file was
// being read nor which of its nested messages ran out.
//
// format is a parameter rather than a constant because this reader is shared
// between the formats. It was extracted from the vector tile decoder, where
// the prefix was hardcoded to "mvt"; left that way, a malformed OSM extract
// would report itself as a broken vector tile and send the reader looking in
// the wrong file.
func New(b []byte, format, what string) *Reader {
	return &Reader{b: b, format: format, what: what}
}

func (r *Reader) Done() bool { return r.i >= len(r.b) }

func (r *Reader) Uvarint(field string) (uint64, error) {
	v, n := binary.Uvarint(r.b[r.i:])
	switch {
	case n == 0:
		return 0, fmt.Errorf("%s: %s ends part way through %s at byte %d of %d", r.format, r.what, field, r.i, len(r.b))
	case n < 0:
		return 0, fmt.Errorf("%s: %s has a %s at byte %d that does not fit in 64 bits", r.format, r.what, field, r.i)
	}
	r.i += n
	return v, nil
}

// tag reads a field number and wire type.
func (r *Reader) Tag() (field int, wire int, err error) {
	key, err := r.Uvarint("a field tag")
	if err != nil {
		return 0, 0, err
	}
	field = int(key >> 3)
	wire = int(key & 7)
	if field == 0 {
		return 0, 0, fmt.Errorf("%s: %s has field number 0 at byte %d, which protobuf does not allow", r.format, r.what, r.i)
	}
	return field, wire, nil
}

// bytes reads a length-delimited field's payload. The slice aliases the input,
// which is why Decode's contract says the caller must not modify the tile
// bytes afterwards.
func (r *Reader) Bytes(field string) ([]byte, error) {
	n, err := r.Uvarint("the length of " + field)
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)-r.i) {
		return nil, fmt.Errorf("%s: %s says %s is %d bytes but only %d remain", r.format, r.what, field, n, len(r.b)-r.i)
	}
	start := r.i
	r.i += int(n)
	return r.b[start:r.i], nil
}

func (r *Reader) Fixed32(field string) (uint32, error) {
	if len(r.b)-r.i < 4 {
		return 0, fmt.Errorf("%s: %s ends part way through %s at byte %d of %d", r.format, r.what, field, r.i, len(r.b))
	}
	v := binary.LittleEndian.Uint32(r.b[r.i:])
	r.i += 4
	return v, nil
}

func (r *Reader) Fixed64(field string) (uint64, error) {
	if len(r.b)-r.i < 8 {
		return 0, fmt.Errorf("%s: %s ends part way through %s at byte %d of %d", r.format, r.what, field, r.i, len(r.b))
	}
	v := binary.LittleEndian.Uint64(r.b[r.i:])
	r.i += 8
	return v, nil
}

// skip steps over a field this decoder does not use. Skipping rather than
// failing is what lets a tile written against a later schema -- or by a
// producer that added something of its own -- decode with the fields we do
// know about.
func (r *Reader) Skip(field, wire int) error {
	switch wire {
	case WireVarint:
		_, err := r.Uvarint(fmt.Sprintf("field %d", field))
		return err
	case WireFixed64:
		_, err := r.Fixed64(fmt.Sprintf("field %d", field))
		return err
	case WireBytes:
		_, err := r.Bytes(fmt.Sprintf("field %d", field))
		return err
	case WireFixed32:
		_, err := r.Fixed32(fmt.Sprintf("field %d", field))
		return err
	case WireStartGroup, WireEndGroup:
		return fmt.Errorf("%s: %s uses a protobuf group for field %d; this format's schema has none, so these are not %s bytes", r.format, r.what, field, r.format)
	}
	return fmt.Errorf("%s: %s has field %d with wire type %d, which protobuf does not define", r.format, r.what, field, wire)
}

// Signed varint accessors.
//
// Protobuf has three ways of putting a signed number in a varint and they are
// not interchangeable, which is why each has its own name here rather than a
// cast at the call site. int32 and int64 are two's complement, so a negative
// one is sign-extended to 64 bits and always costs ten bytes; sint64 is
// zigzag, so a small negative costs one. Reading an sint as an int does not
// fail -- it returns a large positive number -- and that is precisely the kind
// of mistake this package exists to make impossible to write by accident.

// Int64 reads a two's complement signed varint.
func (r *Reader) Int64(field string) (int64, error) {
	v, err := r.Uvarint(field)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

// Int32 reads a two's complement signed varint that must fit in 32 bits.
func (r *Reader) Int32(field string) (int32, error) {
	v, err := r.Uvarint(field)
	if err != nil {
		return 0, err
	}
	i, err := toInt32(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %s: %s: %w", r.format, r.what, field, err)
	}
	return i, nil
}

// Sint64 reads a zigzag signed varint.
func (r *Reader) Sint64(field string) (int64, error) {
	v, err := r.Uvarint(field)
	if err != nil {
		return 0, err
	}
	return Unzigzag64(v), nil
}

// Packed reads a repeated numeric field that may be packed into one
// length-delimited run or, from an older or simpler encoder, repeated one
// varint at a time. Both are valid protobuf for the same field, so both are
// accepted; out is appended to so the unpacked case accumulates.
//
// A function rather than a method because Go does not allow type parameters
// on methods, and one generic rather than a loop per width because the loop
// is the part that is easy to get subtly wrong -- the packed case has to
// consume the whole payload and stop exactly at its end.
func Packed[T any](r *Reader, out []T, field string, wire int, decode func(uint64) (T, error)) ([]T, error) {
	read := func(from *Reader) error {
		v, err := from.Uvarint(field)
		if err != nil {
			return err
		}
		x, err := decode(v)
		if err != nil {
			return fmt.Errorf("%s: %s: %s: %w", r.format, r.what, field, err)
		}
		out = append(out, x)
		return nil
	}

	if wire == WireVarint {
		return out, read(r)
	}
	if wire != WireBytes {
		return nil, fmt.Errorf("%s: %s has %s with wire type %d, and it must be a packed or repeated varint", r.format, r.what, field, wire)
	}
	payload, err := r.Bytes(field)
	if err != nil {
		return nil, err
	}
	inner := New(payload, r.format, r.what)
	for !inner.Done() {
		if err := read(inner); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PackedUint32, PackedInt32 and PackedSint64 are Packed at the widths the two
// formats use: vector tile tag indices, OSM tag and role indices, and OSM
// deltas respectively.
func (r *Reader) PackedUint32(out []uint32, field string, wire int) ([]uint32, error) {
	return Packed(r, out, field, wire, toUint32)
}

func (r *Reader) PackedInt32(out []int32, field string, wire int) ([]int32, error) {
	return Packed(r, out, field, wire, toInt32)
}

func (r *Reader) PackedSint64(out []int64, field string, wire int) ([]int64, error) {
	return Packed(r, out, field, wire, toSint64)
}

func toUint32(v uint64) (uint32, error) {
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("the value %d does not fit in 32 bits", v)
	}
	return uint32(v), nil
}

func toInt32(v uint64) (int32, error) {
	// Through int64 first: protobuf sign-extends a negative int32 to 64 bits,
	// so -1 arrives as ten bytes of ones and must be reinterpreted before it
	// can be range-checked. Checking the uint64 directly would reject every
	// negative value there is.
	i := int64(v)
	if i < math.MinInt32 || i > math.MaxInt32 {
		return 0, fmt.Errorf("the value %d does not fit in 32 bits", i)
	}
	return int32(i), nil
}

func toSint64(v uint64) (int64, error) { return Unzigzag64(v), nil }

// Unzigzag32 decodes a vector tile geometry parameter, and Unzigzag64 an
// sint attribute value or an OSM element's delta.
//
// Deltas are commonly small and as often negative as positive, so the format
// interleaves the two signs onto the naturals -- 0, -1, 1, -2 -> 0, 1, 2, 3 --
// and a varint then spends one byte on both directions instead of ten on every
// step west or north. An sint value is the same encoding at 64 bits.
//
// The two are written together, and named for their width, because they are
// one rule with two spellings: the 64-bit one used to sit inline in the value
// decoder where nothing connected it to this, and a shift or a cast at the
// wrong width is invisible except at the extremes of the range. They live here
// rather than in either decoder because both formats use the encoding -- the
// vector tile for its geometry deltas, OSM PBF for node coordinates, way refs
// and relation members -- and a second copy would be the same mistake this
// package was extracted to undo.
func Unzigzag32(v uint32) int32 {
	return int32(v>>1) ^ -int32(v&1)
}

func Unzigzag64(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
}
