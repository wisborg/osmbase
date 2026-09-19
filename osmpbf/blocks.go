// Package osmpbf reads OpenStreetMap's PBF format.
//
// Only as much of it as the boundary pipeline needs: the file framing, the
// block decompression, and the elements -- nodes, ways and relations -- with
// their tags. Changesets, users, timestamps and versions are skipped, because
// a boundary is a shape and a name and none of the rest bears on it.
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

	"github.com/wisborg/osmbase/internal/protobuf"
)

// The limits the format itself states, enforced rather than trusted.
//
// A PBF file is ordinary input from the internet: an extract is downloaded
// from a third party, and a length field read from it decides how much memory
// this allocates. The specification caps both, so a file exceeding either is
// malformed whatever produced it -- and refusing at the limit turns a hostile
// length into an error instead of an allocation.
const (
	// MaxHeaderBytes is the largest a BlobHeader may be. The specification
	// says 64 KiB; real ones are a few dozen bytes.
	MaxHeaderBytes = 64 << 10

	// MaxBlobBytes is the largest a compressed blob may be, and
	// MaxBlockBytes the largest it may inflate to. The specification says 32
	// MiB for the uncompressed side; real blocks are well under one.
	MaxBlobBytes  = 32 << 20
	MaxBlockBytes = 32 << 20
)

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
	r   io.Reader
	buf []byte

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
	if dataSize < 0 || dataSize > MaxBlobBytes {
		return Block{}, fmt.Errorf("osmpbf: a %s blob declares %d bytes, and this reads at most %d",
			kind, dataSize, MaxBlobBytes)
	}
	blob, err := d.read(int(dataSize))
	if err != nil {
		return Block{}, fmt.Errorf("osmpbf: reading a %s blob: %w", kind, err)
	}
	data, err := d.inflate(blob)
	if err != nil {
		return Block{}, err
	}
	return Block{Type: kind, Data: data}, nil
}

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
	if size > MaxHeaderBytes {
		return 0, fmt.Errorf("osmpbf: a blob header declares %d bytes, and this reads at most %d",
			size, MaxHeaderBytes)
	}
	return size, nil
}

// read fills the reusable buffer with n bytes.
func (d *Reader) read(n int) ([]byte, error) {
	if cap(d.buf) < n {
		d.buf = make([]byte, n)
	}
	d.buf = d.buf[:n]
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		return nil, err
	}
	return d.buf, nil
}

// parseBlobHeader reads the type and payload size.
func parseBlobHeader(b []byte) (kind string, size int32, err error) {
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
			size = int32(v)
		default:
			if err := r.Skip(field, wire); err != nil {
				return "", 0, err
			}
		}
	}
	if kind == "" {
		return "", 0, errors.New("osmpbf: a blob header names no type; these bytes are not a PBF file")
	}
	return kind, size, nil
}

// inflate returns a blob's payload, decompressing it if it is compressed.
func (d *Reader) inflate(blob []byte) ([]byte, error) {
	var raw, zlibData []byte
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
		case field == 2 && wire == protobuf.WireVarint: // raw_size
			v, err := r.Uvarint("a blob's uncompressed size")
			if err != nil {
				return nil, err
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

	if raw != nil {
		return raw, nil
	}
	if zlibData == nil {
		return nil, errors.New("osmpbf: a blob holds neither raw nor compressed bytes")
	}
	if rawSize < 0 || rawSize > MaxBlockBytes {
		return nil, fmt.Errorf("osmpbf: a blob says it inflates to %d bytes, and this reads at most %d",
			rawSize, MaxBlockBytes)
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
		limit = MaxBlockBytes
	}
	d.inflated.Reset()
	if declared > 0 {
		// Only on a declared size. Growing to the fallback limit would
		// allocate the format's 32 MiB maximum for every blob that omitted
		// raw_size, to hold a block that is usually well under one.
		d.inflated.Grow(declared)
	}
	// One byte past the limit, so that reaching it is distinguishable from a
	// block that is exactly that long.
	n, err := io.Copy(&d.inflated, io.LimitReader(d.zr, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("osmpbf: inflating a blob: %w", err)
	}
	if n > int64(limit) {
		return nil, fmt.Errorf("osmpbf: a blob inflates past the %d bytes it declared", declared)
	}
	return d.inflated.Bytes(), nil
}
