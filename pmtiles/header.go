package pmtiles

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// HeaderSize is the fixed length of a PMTiles v3 header, in bytes. It is the
// first thing in the archive and it describes where everything else is.
const HeaderSize = 127

// MaxRootDirectory is the largest a COMPRESSED root directory may be.
//
// The format caps the header plus the root at 16 KiB so that a client reading
// over HTTP can fetch both in one range request and know it has the whole
// root. It is exported because a writer has to respect the same number, and a
// second copy of it somewhere else is a second copy that can drift.
const MaxRootDirectory = 16384 - HeaderSize

// magic is the 7-byte signature every archive starts with.
var magic = []byte("PMTiles")

// Version is the only archive version this reader understands. The format
// changed shape entirely between v2 and v3, so a v2 file is not a degraded v3
// file and must be rejected rather than half-read.
const Version = 3

// Compression names how a run of bytes in the archive was compressed. The same
// enum describes the internal compression (root directory, metadata and every
// leaf directory) and the tile compression, which are set independently.
type Compression uint8

const (
	CompressionUnknown Compression = 0
	CompressionNone    Compression = 1
	CompressionGzip    Compression = 2
	CompressionBrotli  Compression = 3
	CompressionZstd    Compression = 4
)

func (c Compression) String() string {
	switch c {
	case CompressionUnknown:
		return "unknown"
	case CompressionNone:
		return "none"
	case CompressionGzip:
		return "gzip"
	case CompressionBrotli:
		return "brotli"
	case CompressionZstd:
		return "zstd"
	}
	return fmt.Sprintf("compression(%d)", uint8(c))
}

// TileType names what the tile blobs in the archive actually are. This reader
// returns bytes and does not care, but a caller handing them to an MVT decoder
// very much does, and the header is the only place that says.
type TileType uint8

const (
	TileTypeUnknown TileType = 0
	TileTypeMVT     TileType = 1
	TileTypePNG     TileType = 2
	TileTypeJPEG    TileType = 3
	TileTypeWebP    TileType = 4
	TileTypeAVIF    TileType = 5
	// TileTypeMapLibreVT is MapLibre's vector tile variant. It is not MVT and
	// must not be fed to the mvt package.
	TileTypeMapLibreVT TileType = 6
)

func (t TileType) String() string {
	switch t {
	case TileTypeUnknown:
		return "unknown"
	case TileTypeMVT:
		return "mvt"
	case TileTypePNG:
		return "png"
	case TileTypeJPEG:
		return "jpeg"
	case TileTypeWebP:
		return "webp"
	case TileTypeAVIF:
		return "avif"
	case TileTypeMapLibreVT:
		return "maplibre-vt"
	}
	return fmt.Sprintf("tiletype(%d)", uint8(t))
}

// Header is the decoded 127-byte header.
//
// Offsets are absolute, from the first byte of the archive; the offsets held
// in directory ENTRIES are not, and are relative to the tile data section or
// the leaf directory section instead. Keeping the two straight is the only
// arithmetic in this format that can produce a tile rather than an error when
// it is wrong.
type Header struct {
	Version uint8

	RootOffset     uint64
	RootLength     uint64
	MetadataOffset uint64
	MetadataLength uint64
	LeafOffset     uint64
	LeafLength     uint64
	TileDataOffset uint64
	TileDataLength uint64

	// AddressedTiles is the tile count before run-length encoding, TileEntries
	// the number of directory entries that describe tiles, and TileContents
	// the number of distinct blobs in the tile data section. Any of them may
	// be 0, which the spec defines as "unknown" rather than as "none", so none
	// of them can be used to decide that an archive is empty.
	AddressedTiles uint64
	TileEntries    uint64
	TileContents   uint64

	// Clustered says the tile blobs are stored in tile ID order. It is a
	// promise about layout, useful for planning coalesced range requests; it
	// is not needed to read a tile.
	Clustered bool

	InternalCompression Compression
	TileCompression     Compression
	TileType            TileType

	MinZoom uint8
	MaxZoom uint8

	// The bounds and the centre, in degrees. The format stores them as
	// hundred-nanodegree integers, so they are exact to seven decimal places
	// and no further.
	MinLon, MinLat float64
	MaxLon, MaxLat float64
	CenterZoom     uint8
	CenterLon      float64
	CenterLat      float64
}

// ParseHeader decodes the archive header from the first HeaderSize bytes of b.
//
// It rejects a header it cannot act on -- wrong magic, wrong version, a zoom
// range that runs backwards, a compression code the format does not define --
// rather than returning a struct whose fields would send the caller reading at
// an arbitrary offset. It does NOT reject CompressionUnknown, which is a
// defined value: that failure belongs at the point the bytes are decompressed,
// where the error can say which section could not be read.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("pmtiles: header is %d bytes, and a PMTiles v3 header is %d", len(b), HeaderSize)
	}
	if !bytes.Equal(b[0:7], magic) {
		return Header{}, fmt.Errorf("pmtiles: this is not a PMTiles archive: it starts with %q, not %q", printable(b[0:7]), magic)
	}
	h := Header{Version: b[7]}
	if h.Version != Version {
		return Header{}, fmt.Errorf("pmtiles: archive is version %d, and this reader implements version %d", h.Version, Version)
	}

	u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off : off+8]) }
	h.RootOffset = u64(8)
	h.RootLength = u64(16)
	h.MetadataOffset = u64(24)
	h.MetadataLength = u64(32)
	h.LeafOffset = u64(40)
	h.LeafLength = u64(48)
	h.TileDataOffset = u64(56)
	h.TileDataLength = u64(64)
	h.AddressedTiles = u64(72)
	h.TileEntries = u64(80)
	h.TileContents = u64(88)
	h.Clustered = b[96] != 0
	h.InternalCompression = Compression(b[97])
	h.TileCompression = Compression(b[98])
	h.TileType = TileType(b[99])
	h.MinZoom = b[100]
	h.MaxZoom = b[101]
	h.MinLon, h.MinLat = decodePosition(b[102:110])
	h.MaxLon, h.MaxLat = decodePosition(b[110:118])
	h.CenterZoom = b[118]
	h.CenterLon, h.CenterLat = decodePosition(b[119:127])

	if h.InternalCompression > CompressionZstd {
		return Header{}, fmt.Errorf("pmtiles: header names internal compression %d, which the format does not define", uint8(h.InternalCompression))
	}
	if h.TileCompression > CompressionZstd {
		return Header{}, fmt.Errorf("pmtiles: header names tile compression %d, which the format does not define", uint8(h.TileCompression))
	}
	if h.MaxZoom < h.MinZoom {
		return Header{}, fmt.Errorf("pmtiles: header says zoom %d to %d, which runs backwards", h.MinZoom, h.MaxZoom)
	}
	if h.MaxZoom > MaxZoom {
		return Header{}, fmt.Errorf("pmtiles: header says maximum zoom %d, deeper than the deepest addressable zoom %d", h.MaxZoom, MaxZoom)
	}
	if h.RootLength == 0 {
		return Header{}, fmt.Errorf("pmtiles: header says the root directory is empty, and every archive has a root directory of at least one entry")
	}
	if h.RootLength > MaxRootDirectory {
		return Header{}, fmt.Errorf("pmtiles: root directory is %d bytes, and the format caps it at %d so that a client can fetch it with the header", h.RootLength, MaxRootDirectory)
	}
	return h, nil
}

// decodePosition reads the longitude/latitude pair the format packs into eight
// bytes: longitude first, then latitude, each a little-endian signed 32-bit
// count of hundred-nanodegrees.
func decodePosition(b []byte) (lon, lat float64) {
	lon = float64(int32(binary.LittleEndian.Uint32(b[0:4]))) / 1e7
	lat = float64(int32(binary.LittleEndian.Uint32(b[4:8]))) / 1e7
	return lon, lat
}

// printable renders the bytes we expected to be the magic number for an error
// message, without letting arbitrary file content into the terminal.
func printable(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
			continue
		}
		out = append(out, '.')
	}
	return string(out)
}
