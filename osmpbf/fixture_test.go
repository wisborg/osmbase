package osmpbf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
)

// A minimal protobuf and PBF writer, for building fixtures.
//
// Synthetic rather than a trimmed real extract, and deliberately so: the cases
// that matter here are the malformed ones -- a length that overruns the file,
// a blob that inflates past what it declared, a granularity of zero -- and a
// real file cannot express any of them. It is also the reason this writer is
// separate from the reader rather than a round-trip through it: a fixture
// built by the code under test agrees with that code by construction, and
// would pass whatever the reader did.

// pbTag encodes a field number and wire type.
func pbTag(field, wire int) []byte {
	return binary.AppendUvarint(nil, uint64(field)<<3|uint64(wire))
}

// pbVarint encodes a varint field.
func pbVarint(field int, v uint64) []byte {
	return binary.AppendUvarint(pbTag(field, 0), v)
}

// pbBytes encodes a length-delimited field.
func pbBytes(field int, b []byte) []byte {
	out := binary.AppendUvarint(pbTag(field, 2), uint64(len(b)))
	return append(out, b...)
}

// pbString is pbBytes for a Go string.
func pbString(field int, s string) []byte { return pbBytes(field, []byte(s)) }

// deflate zlib-compresses b, as the format's blobs are.
func deflate(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// frame wraps a blob payload in the length-prefixed BlobHeader the file
// format puts before every one.
func frame(kind string, blob []byte) []byte {
	header := append(pbString(1, kind), pbVarint(3, uint64(len(blob)))...)
	out := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	out = append(out, header...)
	return append(out, blob...)
}

// rawBlob is an uncompressed blob holding payload.
func rawBlob(payload []byte) []byte { return pbBytes(1, payload) }

// zlibBlob is a compressed blob declaring its true inflated size.
func zlibBlob(payload []byte) []byte {
	return zlibBlobDeclaring(payload, len(payload))
}

// zlibBlobDeclaring is a compressed blob that declares a size of its own
// choosing, so a test can build one that lies about how far it inflates.
func zlibBlobDeclaring(payload []byte, declared int) []byte {
	out := pbVarint(2, uint64(declared))
	return append(out, pbBytes(3, deflate(payload))...)
}

// stringTable encodes a StringTable message.
//
// Callers pass "" first, because the format reserves index 0 to mean "no
// string"; a fixture that omitted it would put a real string where every
// reader expects the empty one.
func stringTable(ss ...string) []byte {
	var out []byte
	for _, s := range ss {
		out = append(out, pbString(1, s)...)
	}
	return out
}

// primitiveBlock encodes a PrimitiveBlock with a string table and groups.
// Extra encoded fields -- granularity, offsets -- are appended as given.
func primitiveBlock(strings []byte, extra []byte, groups ...[]byte) []byte {
	out := pbBytes(1, strings)
	for _, g := range groups {
		out = append(out, pbBytes(2, g)...)
	}
	return append(out, extra...)
}
