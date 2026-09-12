package mvt

import (
	"encoding/binary"
	"fmt"
)

// The Mapbox Vector Tile schema is protocol buffers, so this file is a
// protobuf wire-format reader. It is about a hundred lines because the schema
// is six messages and uses four of the wire's field types, and because this
// module admits no third-party dependency: google.golang.org/protobuf would
// arrive in the go.sum and NOTICE of every program that renders a map, to
// decode a format that has not changed since 2016. See docs/architecture.md,
// "Zero third-party modules".
//
// What is deliberately NOT here is any notion of a message type, a descriptor
// or a default. Each message is decoded by a function that knows its own field
// numbers, and every unknown field number is skipped, which is what makes a
// tile carrying a field this decoder has never heard of decode rather than
// fail.

// Protobuf wire types. Types 3 and 4 (start group, end group) were removed
// from the language long before this schema was written and are not handled;
// encountering one means the bytes are not a vector tile.
const (
	wireVarint     = 0
	wireFixed64    = 1
	wireBytes      = 2
	wireStartGroup = 3
	wireEndGroup   = 4
	wireFixed32    = 5
)

// protoReader walks a protobuf message. Every method reports where it ran out
// of bytes, because "unexpected EOF" alone does not distinguish a truncated
// tile from a field length that was misread.
type protoReader struct {
	b    []byte
	i    int
	what string // the message being read, for errors
}

func (r *protoReader) done() bool { return r.i >= len(r.b) }

func (r *protoReader) uvarint(field string) (uint64, error) {
	v, n := binary.Uvarint(r.b[r.i:])
	switch {
	case n == 0:
		return 0, fmt.Errorf("mvt: %s ends part way through %s at byte %d of %d", r.what, field, r.i, len(r.b))
	case n < 0:
		return 0, fmt.Errorf("mvt: %s has a %s at byte %d that does not fit in 64 bits", r.what, field, r.i)
	}
	r.i += n
	return v, nil
}

// tag reads a field number and wire type.
func (r *protoReader) tag() (field int, wire int, err error) {
	key, err := r.uvarint("a field tag")
	if err != nil {
		return 0, 0, err
	}
	field = int(key >> 3)
	wire = int(key & 7)
	if field == 0 {
		return 0, 0, fmt.Errorf("mvt: %s has field number 0 at byte %d, which protobuf does not allow", r.what, r.i)
	}
	return field, wire, nil
}

// bytes reads a length-delimited field's payload. The slice aliases the input,
// which is why Decode's contract says the caller must not modify the tile
// bytes afterwards.
func (r *protoReader) bytes(field string) ([]byte, error) {
	n, err := r.uvarint("the length of " + field)
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)-r.i) {
		return nil, fmt.Errorf("mvt: %s says %s is %d bytes but only %d remain", r.what, field, n, len(r.b)-r.i)
	}
	start := r.i
	r.i += int(n)
	return r.b[start:r.i], nil
}

func (r *protoReader) fixed32(field string) (uint32, error) {
	if len(r.b)-r.i < 4 {
		return 0, fmt.Errorf("mvt: %s ends part way through %s at byte %d of %d", r.what, field, r.i, len(r.b))
	}
	v := binary.LittleEndian.Uint32(r.b[r.i:])
	r.i += 4
	return v, nil
}

func (r *protoReader) fixed64(field string) (uint64, error) {
	if len(r.b)-r.i < 8 {
		return 0, fmt.Errorf("mvt: %s ends part way through %s at byte %d of %d", r.what, field, r.i, len(r.b))
	}
	v := binary.LittleEndian.Uint64(r.b[r.i:])
	r.i += 8
	return v, nil
}

// skip steps over a field this decoder does not use. Skipping rather than
// failing is what lets a tile written against a later schema -- or by a
// producer that added something of its own -- decode with the fields we do
// know about.
func (r *protoReader) skip(field, wire int) error {
	switch wire {
	case wireVarint:
		_, err := r.uvarint(fmt.Sprintf("field %d", field))
		return err
	case wireFixed64:
		_, err := r.fixed64(fmt.Sprintf("field %d", field))
		return err
	case wireBytes:
		_, err := r.bytes(fmt.Sprintf("field %d", field))
		return err
	case wireFixed32:
		_, err := r.fixed32(fmt.Sprintf("field %d", field))
		return err
	case wireStartGroup, wireEndGroup:
		return fmt.Errorf("mvt: %s uses a protobuf group for field %d; the vector tile schema has none, so these are not vector tile bytes", r.what, field)
	}
	return fmt.Errorf("mvt: %s has field %d with wire type %d, which protobuf does not define", r.what, field, wire)
}

// packedUint32 reads a repeated uint32 field that may be packed into one
// length-delimited run or, from an older or simpler encoder, repeated one
// varint at a time. Both are valid protobuf for the same field, so both are
// accepted; out is appended to so the unpacked case accumulates.
func (r *protoReader) packedUint32(out []uint32, field string, wire int) ([]uint32, error) {
	if wire == wireVarint {
		v, err := r.uvarint(field)
		if err != nil {
			return nil, err
		}
		if v > 0xffffffff {
			return nil, fmt.Errorf("mvt: %s has a value in %s that does not fit in 32 bits", r.what, field)
		}
		return append(out, uint32(v)), nil
	}
	if wire != wireBytes {
		return nil, fmt.Errorf("mvt: %s has %s with wire type %d, and it must be a packed or repeated varint", r.what, field, wire)
	}
	payload, err := r.bytes(field)
	if err != nil {
		return nil, err
	}
	inner := protoReader{b: payload, what: r.what}
	for !inner.done() {
		v, err := inner.uvarint(field)
		if err != nil {
			return nil, err
		}
		if v > 0xffffffff {
			return nil, fmt.Errorf("mvt: %s has a value in %s that does not fit in 32 bits", r.what, field)
		}
		out = append(out, uint32(v))
	}
	return out, nil
}
