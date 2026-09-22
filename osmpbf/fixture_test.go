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

// paddedHeader builds a BlobHeader of exactly total bytes, for a blob of
// blobLen bytes, padding it out with a field no decoder reads.
//
// Exact length matters because the header limit is a boundary: a test that
// only ever exceeds it cannot tell an enforced limit from one enforced a byte
// early, and a header of exactly 64 KiB must still read.
func paddedHeader(kind string, blobLen, total int) []byte {
	base := append(pbString(1, kind), pbVarint(3, uint64(blobLen))...)
	// One byte of tag for field 9, then the padding's own length varint.
	for n := 0; n <= total; n++ {
		if len(base)+1+len(binary.AppendUvarint(nil, uint64(n)))+n == total {
			return append(base, pbBytes(9, make([]byte, n))...)
		}
	}
	panic("no padding length gives a header of exactly that size")
}

// framedWith wraps a blob in a header the caller built, rather than in the
// minimal one frame writes.
func framedWith(header, blob []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	out = append(out, header...)
	return append(out, blob...)
}

// The two protobuf wire types these fixtures write, spelled locally so a test
// naming one does not have to import the internal reader.
const (
	protobufWireVarint = 0
	protobufWireBytes  = 2
)

// truncatedZlibBlob is a compressed blob whose zlib stream is cut short by
// drop bytes, as a download interrupted mid-file would be. It still declares
// the payload's true length, so what fails is the inflate and not a limit.
func truncatedZlibBlob(payload []byte, drop int) []byte {
	z := deflate(payload)
	out := pbVarint(2, uint64(len(payload)))
	return append(out, pbBytes(3, z[:len(z)-drop])...)
}

// header encodes a HeaderBlock's feature declarations.
func header(required, optional []string) []byte {
	var out []byte
	for _, f := range required {
		out = append(out, pbString(4, f)...)
	}
	for _, f := range optional {
		out = append(out, pbString(5, f)...)
	}
	return out
}

// zigzag encodes a signed value the way the format's sint64 fields do. The
// rule, not a call into the decoder: a fixture that encoded through the code
// under test would agree with it however wrong both were.
func zigzag(v int64) uint64 { return uint64((v << 1) ^ (v >> 63)) }

// packedSint encodes a run of signed values as one delta-coded packed field,
// which is how the format stores every list of ids it repeats.
func packedSint(field int, vs ...int64) []byte {
	var payload []byte
	var prev int64
	for _, v := range vs {
		payload = binary.AppendUvarint(payload, zigzag(v-prev))
		prev = v
	}
	return pbBytes(field, payload)
}

// packedInt encodes a run of plain (not zigzag, not delta) int32 values.
func packedInt(field int, vs ...int32) []byte {
	var payload []byte
	for _, v := range vs {
		payload = binary.AppendUvarint(payload, uint64(int64(v)))
	}
	return pbBytes(field, payload)
}

// tagRun flattens per-element tag pairs into the dense keys_vals encoding:
// each element's pairs in order, terminated by a zero.
func tagRun(perNode [][]int32) []int32 {
	var out []int32
	for _, pairs := range perNode {
		out = append(out, pairs...)
		out = append(out, 0)
	}
	return out
}

// denseNodes encodes a DenseNodes message. ids, lats and lons are absolute;
// the deltas are computed here.
func denseNodes(ids, lats, lons []int64, keysVals []int32) []byte {
	out := packedSint(1, ids...)
	out = append(out, packedSint(8, lats...)...)
	out = append(out, packedSint(9, lons...)...)
	if keysVals != nil {
		out = append(out, packedInt(10, keysVals...)...)
	}
	return pbBytes(2, out) // field 2 of PrimitiveGroup
}

// plainNode encodes a Node message -- the per-node form, which real extracts
// use only for what a dense run cannot hold.
func plainNode(id, lat, lon int64, keys, vals []int32) []byte {
	out := append(pbTag(1, 0), binary.AppendUvarint(nil, zigzag(id))...)
	if keys != nil {
		out = append(out, packedInt(2, keys...)...)
		out = append(out, packedInt(3, vals...)...)
	}
	out = append(out, append(pbTag(8, 0), binary.AppendUvarint(nil, zigzag(lat))...)...)
	out = append(out, append(pbTag(9, 0), binary.AppendUvarint(nil, zigzag(lon))...)...)
	return pbBytes(1, out) // field 1 of PrimitiveGroup
}

// way encodes a Way message. refs are absolute; the deltas are computed here.
// The id is a plain int64, not a zigzag one -- that asymmetry is in the
// format, and a fixture that zigzagged it would hide a decoder doing the same.
func way(id int64, refs []int64, keys, vals []int32) []byte {
	out := pbVarint(1, uint64(id))
	if keys != nil {
		out = append(out, packedInt(2, keys...)...)
		out = append(out, packedInt(3, vals...)...)
	}
	if refs != nil {
		out = append(out, packedSint(8, refs...)...)
	}
	return pbBytes(3, out) // field 3 of PrimitiveGroup
}

// relation encodes a Relation message. memids are absolute.
func relation(id int64, roles []int32, memids []int64, types []int32, keys, vals []int32) []byte {
	out := pbVarint(1, uint64(id))
	if keys != nil {
		out = append(out, packedInt(2, keys...)...)
		out = append(out, packedInt(3, vals...)...)
	}
	out = append(out, packedInt(8, roles...)...)
	out = append(out, packedSint(9, memids...)...)
	out = append(out, packedInt(10, types...)...)
	return pbBytes(4, out) // field 4 of PrimitiveGroup
}
