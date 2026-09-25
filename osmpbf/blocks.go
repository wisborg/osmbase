// Package osmpbf reads OpenStreetMap's PBF format.
//
// Only as much of it as the boundary pipeline needs. So far that is the file
// framing, the block decompression, the header's feature declarations and a
// primitive block's string table; the elements themselves -- nodes, ways and
// relations -- are decoded by the pass that wants them. Changesets, users,
// timestamps and versions are skipped throughout, because a boundary is a
// shape and a name and none of the rest bears on it.
//
// # Why this is written here
//
// github.com/paulmach/osm reads this format well and is MIT, and it pulls
// paulmach/orb, which pulls go.mongodb.org/mongo-driver for BSON. That would
// arrive in the go.sum and NOTICE of every program built on this library --
// which renders maps -- to decode a format whose specification has not changed
// since 2010. See docs/architecture.md, "Almost zero third-party modules".
//
// The format is small enough to be worth it: a length-prefixed header, a blob
// holding either raw or zlib-compressed bytes, and protobuf messages inside.
package osmpbf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/wisborg/osmbase/internal/inflate"
	"github.com/wisborg/osmbase/internal/protobuf"
)

// The limits the format itself states, enforced rather than trusted.
//
// A PBF file is ordinary input from the internet: an extract is downloaded
// from a third party, and a length field read from it decides how much memory
// this allocates. The specification caps both, so a file exceeding either is
// malformed whatever produced it -- and refusing at the limit turns a hostile
// length into an error instead of an allocation.
//
// These are EXCLUSIVE bounds, which is how the specification words them: a
// BlobHeader "must be less than 64 KiB", and a blob's uncompressed length
// "must be less than 32 MiB". So the checks below are >= rather than >, and a
// structure of exactly one of these sizes is already malformed. Nothing turns
// on the byte either way -- a reader is free to be more permissive than the
// format -- but a limit that does not mean what the specification's sentence
// means is a limit the next reader has to go and re-derive.
const (
	// MaxHeaderBytes bounds a BlobHeader. Real ones are a few dozen bytes.
	MaxHeaderBytes = 64 << 10

	// MaxBlobBytes bounds a compressed blob and MaxBlockBytes what it
	// inflates to. Real blocks are well under a megabyte.
	MaxBlobBytes  = 32 << 20
	MaxBlockBytes = 32 << 20
)

// An uncompressed blob's payload is bounded by MaxBlobBytes alone -- it is
// returned as it was read, with nothing between it and the caller. That is
// only safe while MaxBlobBytes does not exceed MaxBlockBytes, and this is
// where that is stated: the constant below does not compile if it ever does.
//
// A runtime check would have been dead code -- unreachable today, untestable,
// and quietly wrong the moment someone raised one constant without the other.
// This fails at the build instead.
const _ = uint(MaxBlockBytes - MaxBlobBytes)

// Block types, as the format spells them.
const (
	// TypeHeader carries the file's bounding box and required features.
	TypeHeader = "OSMHeader"
	// TypeData carries elements.
	TypeData = "OSMData"
)

// ErrUnsupportedCompression is returned for a blob compressed with something
// this does not inflate.
//
// Distinguishable because the answer differs from other failures: a file this
// cannot read because it used lzma is not a corrupt file, and telling somebody
// to re-download it would waste their time. Every extract in circulation uses
// zlib or no compression at all; the other codecs the format allows -- lzma,
// bzip2, lz4, zstd -- are in the specification and effectively unused, so they
// are refused by name rather than implemented on speculation.
var ErrUnsupportedCompression = errors.New("osmpbf: the blob uses a compression this does not read")

// Block is one decompressed block of a PBF file.
type Block struct {
	// Type is TypeHeader or TypeData, or whatever else the file said. An
	// unknown type is passed through rather than refused: the format is
	// explicitly extensible here, and a reader that stopped at an unfamiliar
	// block would be refusing files it could otherwise read most of.
	Type string

	// Data is the block's decompressed bytes, still protobuf.
	Data []byte
}

// Reader walks the blocks of a PBF file.
type Reader struct {
	r io.Reader

	// raw holds the bytes of the blob header or blob currently being read.
	raw bytes.Buffer

	header Header

	// inflated is reused between blocks. A country extract is tens of
	// thousands of blocks of a similar size, so allocating one per block is
	// tens of thousands of multi-megabyte allocations for no reason.
	inflated bytes.Buffer
	zr       io.ReadCloser
}

// NewReader reads PBF blocks from r.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Next returns the next block, or io.EOF at the end of the file.
//
// The returned Data is only valid until the next call: it points into a buffer
// this reader reuses. A caller that keeps part of a block must copy it, which
// is what the element decoding does with the strings it keeps.
func (d *Reader) Next() (Block, error) {
	size, err := d.headerSize()
	if err != nil {
		return Block{}, err
	}
	raw, err := d.read(int(size))
	if err != nil {
		return Block{}, fmt.Errorf("osmpbf: reading a blob header: %w", err)
	}
	kind, dataSize, err := parseBlobHeader(raw)
	if err != nil {
		return Block{}, err
	}
	blob, err := d.read(int(dataSize))
	if err != nil {
		return Block{}, fmt.Errorf("osmpbf: reading a %s blob: %w", kind, err)
	}
	data, err := d.inflate(blob)
	if err != nil {
		return Block{}, err
	}

	// The header is checked here rather than left to the caller, because a
	// caller who forgets gets no error -- just the wrong boundaries. The
	// specification requires a reader to stop on a required feature it does
	// not implement, and every failure that rule prevents is a quiet one:
	// see DecodeHeader.
	if kind == TypeHeader {
		h, err := DecodeHeader(data)
		if err != nil {
			return Block{}, err
		}
		d.header = h
	}

	return Block{Type: kind, Data: data}, nil
}

// Header returns the file's feature declarations, once its OSMHeader block
// has been read. A file whose header has not been reached yet -- or that has
// none, which is malformed but readable -- reports no features.
func (d *Reader) Header() Header { return d.header }

// headerSize reads the four-byte big-endian length that precedes each blob
// header, and is the only place io.EOF means "the file ended" rather than
// "the file was cut short".
func (d *Reader) headerSize() (uint32, error) {
	var n [4]byte
	if _, err := io.ReadFull(d.r, n[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("osmpbf: reading a blob header's length: %w", err)
	}
	size := binary.BigEndian.Uint32(n[:])
	if size >= MaxHeaderBytes {
		return 0, fmt.Errorf("osmpbf: a blob header declares %d bytes, and this reads fewer than %d",
			size, MaxHeaderBytes)
	}
	return size, nil
}

// read fills the reusable buffer with n bytes.
//
// The buffer grows to what actually arrives rather than to what the file
// declared. Allocating n up front would let a fifteen-byte file claiming a 32
// MiB blob take 32 MiB before failing on the very next line -- bounded, and
// reused, but still an allocation whose size the input chose, which is the
// property this package says it does not have.
func (d *Reader) read(n int) ([]byte, error) {
	d.raw.Reset()
	if _, err := io.CopyN(&d.raw, d.r, int64(n)); err != nil {
		if errors.Is(err, io.EOF) {
			// The file ran out mid-structure. io.EOF here would mean a clean
			// end, which this is not.
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return d.raw.Bytes(), nil
}

// parseBlobHeader reads the type and payload size.
func parseBlobHeader(b []byte) (kind string, size int32, err error) {
	var sawSize bool
	r := protobuf.New(b, "osmpbf", "a blob header")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return "", 0, err
		}
		switch {
		case field == 1 && wire == protobuf.WireBytes: // type
			v, err := r.Bytes("the blob type")
			if err != nil {
				return "", 0, err
			}
			kind = string(v)
		case field == 3 && wire == protobuf.WireVarint: // datasize
			v, err := r.Uvarint("the blob size")
			if err != nil {
				return "", 0, err
			}
			// Bounded before it is narrowed. int32(v) first would turn a
			// declaration of 2^32+100 into 100, which passes every check
			// below and then desynchronises the file framing silently,
			// because the reader consumes 100 bytes where the file meant
			// four gigabytes.
			if v >= MaxBlobBytes {
				return "", 0, fmt.Errorf("osmpbf: a blob declares %d bytes, and this reads fewer than %d",
					v, MaxBlobBytes)
			}
			size = int32(v)
			sawSize = true
		default:
			if err := r.Skip(field, wire); err != nil {
				return "", 0, err
			}
		}
	}
	if kind == "" {
		return "", 0, errors.New("osmpbf: a blob header names no type; these bytes are not a PBF file")
	}
	// Caught here rather than left to default. The field is required, and an
	// absent one reads as zero, which surfaces two functions later as "a blob
	// holds neither raw nor compressed bytes" -- an accurate sentence
	// pointing at entirely the wrong field.
	if !sawSize {
		return "", 0, fmt.Errorf("osmpbf: a %s blob header declares no size, and the field is required", kind)
	}
	return kind, size, nil
}

// inflate returns a blob's payload, decompressing it if it is compressed.
func (d *Reader) inflate(blob []byte) ([]byte, error) {
	var raw, zlibData []byte
	var sawRaw bool
	var rawSize int32

	r := protobuf.New(blob, "osmpbf", "a blob")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch {
		case field == 1 && wire == protobuf.WireBytes: // raw
			if raw, err = r.Bytes("an uncompressed blob"); err != nil {
				return nil, err
			}
			sawRaw = true
		case field == 2 && wire == protobuf.WireVarint: // raw_size
			v, err := r.Uvarint("a blob's uncompressed size")
			if err != nil {
				return nil, err
			}
			// Bounded before narrowing, as above, and for a sharper reason
			// here: int32(2^32) is 0, which this code reads as "the blob
			// declared no size" and then inflates up to the full limit. A
			// declaration of four gigabytes must be an error, not a synonym
			// for having omitted the field.
			if v >= MaxBlockBytes {
				return nil, fmt.Errorf("osmpbf: a blob says it inflates to %d bytes, and this reads fewer than %d",
					v, MaxBlockBytes)
			}
			rawSize = int32(v)
		case field == 3 && wire == protobuf.WireBytes: // zlib_data
			if zlibData, err = r.Bytes("a compressed blob"); err != nil {
				return nil, err
			}
		case (field == 4 || field == 5 || field == 6 || field == 7) && wire == protobuf.WireBytes:
			// lzma, bzip2, lz4, zstd. Named rather than skipped: skipping
			// would leave an empty block that reads as a file with nothing in
			// it, which is the wrong thing to tell somebody.
			return nil, fmt.Errorf("%w (field %d)", ErrUnsupportedCompression, field)
		default:
			if err := r.Skip(field, wire); err != nil {
				return nil, err
			}
		}
	}

	// Presence is tracked explicitly rather than inferred from raw != nil,
	// because Bytes returns a non-nil empty slice for a zero-length field.
	// Inferred, a blob carrying an empty raw alongside real compressed bytes
	// would return no bytes and no error -- an empty block, which reads
	// downstream as a file that simply contains nothing. That is the same
	// plausible-wrong-answer this refuses for an unreadable codec, one field
	// over.
	if sawRaw && zlibData != nil {
		return nil, errors.New("osmpbf: a blob holds both raw and compressed bytes, and the format has them as alternatives")
	}
	if sawRaw {
		return raw, nil
	}
	if zlibData == nil {
		return nil, errors.New("osmpbf: a blob holds neither raw nor compressed bytes")
	}
	return d.unzlib(zlibData, int(rawSize))
}

// unzlib inflates a blob, refusing one that expands past the declared size.
//
// The declared size is a claim by the file, so it is used as a LIMIT and not
// as an allocation: a blob that says it inflates to a kilobyte and then
// produces a gigabyte is stopped at the kilobyte rather than believed. That is
// the shape a zip bomb takes, and this reads files from the internet.
func (d *Reader) unzlib(compressed []byte, declared int) ([]byte, error) {
	var err error
	if d.zr == nil {
		d.zr, err = zlib.NewReader(bytes.NewReader(compressed))
	} else {
		err = d.zr.(zlib.Resetter).Reset(bytes.NewReader(compressed), nil)
	}
	if err != nil {
		return nil, fmt.Errorf("osmpbf: a blob's compressed bytes are not zlib: %w", err)
	}

	limit := declared
	if limit <= 0 {
		// One under, because MaxBlockBytes is the exclusive bound: a block of
		// exactly that size is malformed whether or not it declared one.
		limit = MaxBlockBytes - 1
	}
	d.inflated.Reset()
	if declared > 0 {
		// Only on a declared size. Growing to the fallback limit would
		// allocate the format's 32 MiB maximum for every blob that omitted
		// raw_size, to hold a block that is usually well under one.
		d.inflated.Grow(declared)
	}
	// The bound itself is internal/inflate's, shared with pmtiles and slice:
	// one byte past the limit, so reaching it is distinguishable from a
	// block exactly that long.
	_, err = inflate.Into(&d.inflated, d.zr, int64(limit))
	if err != nil && !errors.Is(err, inflate.ErrTooLarge) {
		return nil, fmt.Errorf("osmpbf: inflating a blob: %w", err)
	}
	if err != nil {
		// Two limits reach here and only one of them is the file's. Reporting
		// the declaration when there was none says "the producer wrote
		// raw_size = 0", and sends whoever is debugging a real extract to
		// look at a field that is not there.
		if declared > 0 {
			return nil, fmt.Errorf("osmpbf: a blob inflates past the %d bytes it declared", declared)
		}
		return nil, fmt.Errorf("osmpbf: a blob declaring no size inflates past this reader's %d byte limit", limit)
	}
	return d.inflated.Bytes(), nil
}
