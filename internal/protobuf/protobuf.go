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

// packedUint32 reads a repeated uint32 field that may be packed into one
// length-delimited run or, from an older or simpler encoder, repeated one
// varint at a time. Both are valid protobuf for the same field, so both are
// accepted; out is appended to so the unpacked case accumulates.
func (r *Reader) PackedUint32(out []uint32, field string, wire int) ([]uint32, error) {
	if wire == WireVarint {
		v, err := r.Uvarint(field)
		if err != nil {
			return nil, err
		}
		if v > 0xffffffff {
			return nil, fmt.Errorf("%s: %s has a value in %s that does not fit in 32 bits", r.format, r.what, field)
		}
		return append(out, uint32(v)), nil
	}
	if wire != WireBytes {
		return nil, fmt.Errorf("%s: %s has %s with wire type %d, and it must be a packed or repeated varint", r.format, r.what, field, wire)
	}
	payload, err := r.Bytes(field)
	if err != nil {
		return nil, err
	}
	inner := *New(payload, r.format, r.what)
	for !inner.Done() {
		v, err := inner.Uvarint(field)
		if err != nil {
			return nil, err
		}
		if v > 0xffffffff {
			return nil, fmt.Errorf("%s: %s has a value in %s that does not fit in 32 bits", r.format, r.what, field)
		}
		out = append(out, uint32(v))
	}
	return out, nil
}
