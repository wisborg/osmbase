package slice

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// Compression names how the tile files in a source are encoded on disk.
//
// Tiles are stored exactly as they were fetched, still compressed, which is
// what keeps the store small and moves decompression to once per render
// instead of once per download. So this is not a choice the store makes; it is
// the source's own encoding, recorded so the bytes can be undone.
//
// # Why it is a string, and why it is not pmtiles.Compression
//
// The values are spelled the same as pmtiles.Compression.String() produces,
// and a caller filling a store from an archive passes that string straight
// through. It is not that type because this package does not import the
// archive reader: a store holds bytes and a label, and making it depend on one
// format's enum would mean a slice could only ever be filled from that format
// -- which the design explicitly rules out, since a file the user already has,
// obtained however they like, is a first-class source.
//
// The cost of the copy is that a rename in the archive reader would arrive
// here as an unrecognised name. That is a loud failure with the name in the
// message, not a silent one, which is the trade a small closed set can afford.
type Compression string

const (
	CompressionNone Compression = "none"
	CompressionGzip Compression = "gzip"
)

// maxTileBytes bounds what one stored tile may expand to.
//
// A store holds what an archive reader already accepted, and that reader
// applies its own ceiling, so this is a second line rather than the first. It
// is a constant rather than a knob for that reason: a tile arriving here past
// this size means the file on disk is not what was fetched, which is a reason
// to stop rather than a reason to raise a limit. Sixteen mebibytes is the same
// figure the archive reader defaults to, and a dense vector tile is well under
// a megabyte.
const maxTileBytes = 16 << 20

// supported reports whether this package can read tiles encoded this way.
//
// It is checked when a source is ADDED rather than when a tile is read. A
// store that accepted a brotli source would download a cell and then be unable
// to draw a single tile of it, which is the worst possible moment to find out.
func (c Compression) supported() bool {
	switch c {
	case CompressionNone, CompressionGzip:
		return true
	}
	return false
}

// decompress expands a stored tile.
//
// The limit is checked by reading one byte past it rather than by inspecting
// the result: a stream that would expand to gigabytes has to be stopped while
// it is expanding, not afterwards. gzip reaches roughly a thousand to one and
// nothing on disk states the decompressed size.
func (c Compression) decompress(b []byte, what string) ([]byte, error) {
	switch c {
	case CompressionNone:
		if int64(len(b)) > maxTileBytes {
			return nil, fmt.Errorf("slice: %s is %d bytes, past this store's limit of %d", what, len(b), maxTileBytes)
		}
		return b, nil
	case CompressionGzip:
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("slice: %s is not the gzip stream the manifest says it is: %w", what, err)
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, maxTileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("slice: decompressing %s: %w", what, err)
		}
		if int64(len(out)) > maxTileBytes {
			return nil, fmt.Errorf("slice: %s expands past this store's limit of %d bytes", what, maxTileBytes)
		}
		return out, nil
	}
	return nil, fmt.Errorf("slice: %s is stored as %q, which this store cannot decode", what, string(c))
}
