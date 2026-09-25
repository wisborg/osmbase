// Package pmtiles reads tiles out of a PMTiles v3 archive.
//
// A PMTiles archive is a single file holding a whole tile pyramid: a 127-byte
// header, a root directory, some JSON metadata, optional leaf directories and
// the tile blobs. Directories are varint-encoded runs over Hilbert tile IDs,
// which makes a lookup two or three range reads and makes a geographic
// neighbourhood a contiguous stretch of the file.
//
// The reader is read-only and works over an io.ReaderAt, so the same code
// reads a local file today and an HTTP range source later. Nothing in this
// package opens a socket.
//
// # Why this is written here rather than imported
//
// github.com/protomaps/go-pmtiles is correct, BSD-3 licensed and does all of
// this. Its module requirements include gocloud.dev, the AWS, Azure and Google
// cloud SDKs, caddyserver/caddy, Prometheus, zap and a cgo-free SQLite --
// which would land in the go.sum and the hand-maintained NOTICE of every
// public program that depends on this library. The format is a 127-byte header
// and two varint encodings, so it is written out here instead. See
// docs/architecture.md, "Zero third-party modules" and trap T12; this comment
// exists because the reason is invisible from the code.
package pmtiles

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"github.com/wisborg/osmbase/internal/inflate"
)

// maxLeafDepth caps how many leaf directories a single lookup will follow.
//
// The spec discourages more than one level and real archives have none or one.
// The cap is not about depth so much as about termination: a leaf entry whose
// offset points back at its own directory is a legal-looking way to make a
// corrupt archive loop forever, and this turns that into an error.
const maxLeafDepth = 4

// maxCachedLeaves is how many decoded leaf directories are kept. A leaf covers
// a contiguous stretch of the Hilbert curve, so a render walking one area
// reuses one or two of them constantly, and the whole cache is dropped rather
// than evicted one entry at a time: picking a victim would mean iterating a
// map, and map iteration order is exactly the sort of thing that makes a
// render stop being reproducible.
const maxCachedLeaves = 64

// Limits bound how far a compressed section of an archive may expand.
//
// Without them a reader is a decompression bomb with a file format wrapped
// round it: gzip reaches about a thousand to one, the archive names the
// compressed length and nothing names the decompressed one, and every section
// -- root directory, leaf, metadata, tile -- goes through the same path. A
// hundred-kilobyte file can ask for hundreds of megabytes, and a directory
// that large then multiplies again into entries.
//
// The defaults are set against what the format and real data actually
// contain, with orders of magnitude of headroom, so raising one should mean a
// deliberate decision about a specific archive rather than a shrug.
type Limits struct {
	// Directory bounds a decompressed root or leaf directory. The format caps
	// a COMPRESSED root at 16,257 bytes, and a leaf is written to be fetched
	// in one request, so this is generous by three orders of magnitude.
	Directory int64
	// Metadata bounds the decompressed JSON metadata section.
	Metadata int64
	// Tile bounds one decompressed tile. A dense vector tile is well under a
	// megabyte.
	Tile int64
}

// DefaultLimits returns the limits a reader starts with.
func DefaultLimits() Limits {
	return Limits{
		Directory: 8 << 20,
		Metadata:  4 << 20,
		Tile:      16 << 20,
	}
}

// Reader reads tiles from one PMTiles archive. It is safe for concurrent use
// as long as the underlying io.ReaderAt is, which is what io.ReaderAt's own
// contract requires.
type Reader struct {
	// Limits bound decompression. NewReader fills them in from DefaultLimits
	// and a caller may raise them, but only BEFORE the first read and never
	// concurrently with one -- they are read without the lock.
	//
	// The root directory has already been read by the time a caller can touch
	// this, which is safe because the format caps a compressed root at under
	// 16 KiB and no conforming one can approach the default.
	Limits Limits

	src    io.ReaderAt
	closer io.Closer // set only when this reader opened the file itself
	header Header
	root   []Entry

	mu     sync.Mutex
	leaves map[leafKey][]Entry
}

// leafKey identifies a cached leaf directory.
//
// It is the offset AND the length, not the offset alone. Two entries can name
// the same offset with different lengths -- a damaged archive or a hostile one
// will -- and keying on the offset alone would let one directory be served in
// place of another, which is a lookup that succeeds and returns the wrong
// tile.
type leafKey struct {
	offset uint64
	length uint32
}

// NewReader reads the header and root directory from src and returns a reader
// for the archive.
//
// The root directory is read eagerly because the format guarantees it fits in
// the first 16 KiB: for a range-request source that makes opening an archive a
// single request, and it means a malformed archive fails here rather than on
// the first tile.
func NewReader(src io.ReaderAt) (*Reader, error) {
	if src == nil {
		return nil, fmt.Errorf("pmtiles: no source to read the archive from")
	}
	raw := make([]byte, HeaderSize)
	if _, err := io.ReadFull(io.NewSectionReader(src, 0, HeaderSize), raw); err != nil {
		return nil, fmt.Errorf("pmtiles: reading the %d-byte header: %w", HeaderSize, err)
	}
	h, err := ParseHeader(raw)
	if err != nil {
		return nil, err
	}
	r := &Reader{Limits: DefaultLimits(), src: src, header: h, leaves: make(map[leafKey][]Entry)}
	root, err := r.directoryAt(h.RootOffset, uint32(h.RootLength), "the root directory")
	if err != nil {
		return nil, err
	}
	r.root = root
	return r, nil
}

// Open opens the archive at path. The returned reader owns the file and Close
// closes it.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pmtiles: opening archive: %w", err)
	}
	r, err := NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("pmtiles: reading archive %s: %w", path, err)
	}
	r.closer = f
	return r, nil
}

// Close releases the file Open opened. It is a no-op for a reader built with
// NewReader, whose source belongs to the caller.
//
// Unlike the read methods it is not safe to call concurrently with them: it is
// the end of the reader's life, not part of its use.
func (r *Reader) Close() error {
	if r.closer == nil {
		return nil
	}
	err := r.closer.Close()
	r.closer = nil
	return err
}

// Header returns the archive header, which carries the zoom range, the bounds,
// the tile type and the attribution-relevant counts.
func (r *Reader) Header() Header { return r.header }

// RootEntries returns the decoded root directory. It is the archive's own
// index and is what a future acquisition step plans byte ranges from; callers
// that only want tiles want Tile instead.
func (r *Reader) RootEntries() []Entry {
	out := make([]Entry, len(r.root))
	copy(out, r.root)
	return out
}

// Metadata returns the archive's JSON metadata, decompressed. It is where the
// attribution string and, for vector archives, the layer descriptions live.
//
// An archive with no metadata section returns nil and no error: the spec
// requires one, but a missing one is not a reason to refuse to read tiles.
func (r *Reader) Metadata() ([]byte, error) {
	if r.header.MetadataLength == 0 {
		return nil, nil
	}
	if r.header.MetadataLength > math.MaxUint32 {
		return nil, fmt.Errorf("pmtiles: metadata is %d bytes, which is not a JSON document", r.header.MetadataLength)
	}
	raw, err := r.readAt(r.header.MetadataOffset, uint32(r.header.MetadataLength), r.Limits.Metadata, "the metadata")
	if err != nil {
		return nil, err
	}
	return decompress(r.header.InternalCompression, raw, r.Limits.Metadata, "the metadata")
}

// Tile returns the decompressed bytes of the tile at z/x/y.
//
// The boolean reports whether the archive holds that tile at all, and it is
// not the same question as the error. An archive legitimately has no tile at
// most coordinates -- outside its bounds, outside its zoom range, or over
// ocean a writer chose to omit -- and that is an answer, not a failure. A
// caller that treats a missing tile as an error will refuse to draw perfectly
// good maps; a caller that treats an error as a missing tile will draw a
// blank where a corrupt archive should have been reported.
func (r *Reader) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	id, err := ZxyToID(z, x, y)
	if err != nil {
		return nil, false, err
	}
	return r.tileByID(id, fmt.Sprintf("tile %d/%d/%d", z, x, y))
}

// RawTile returns the bytes of the tile at z/x/y exactly as the archive stores
// them, still compressed with Header().TileCompression.
//
// This is what a store writes to disk. Keeping a tile compressed on the way
// through means the cache holds what the source holds, decompression happens
// once per render instead of once per download, and -- the part that matters
// most -- a compression this reader cannot decode can still be copied through
// rather than turning acquisition into a failure.
func (r *Reader) RawTile(z uint8, x, y uint32) ([]byte, bool, error) {
	id, err := ZxyToID(z, x, y)
	if err != nil {
		return nil, false, err
	}
	return r.rawTileByID(id, fmt.Sprintf("tile %d/%d/%d", z, x, y))
}

// Location is where one tile's stored bytes lie: an ABSOLUTE offset from the
// start of the archive, and the stored -- that is, still compressed -- length.
//
// The offset being absolute is the point of the type. A directory entry
// carries a section-relative offset, and turning that into a file offset means
// adding the section base and checking the result lies wholly inside the
// section the header declared -- the check that stops a damaged directory
// addressing the rest of the file. That check ought to happen in exactly one
// place, and handing a caller a raw Entry would be handing it the job.
type Location struct {
	Offset int64
	Length int64
}

// End is the offset one byte past this tile.
func (l Location) End() int64 { return l.Offset + l.Length }

// Locate says where the tile at z/x/y is, WITHOUT reading it.
//
// This is what the acquisition step plans a download from, and it is the
// reason a fetch can state its exact cost before it transfers a byte: walking
// the directories is two or three range reads however large the archive, so
// asking this about the eighty-five tiles of a cell costs a few kilobytes and
// yields the precise number of bytes the download will move. A confirmation
// prompt that can state the true cost is a different thing from one that
// guesses, and the difference is a property of the format rather than of any
// estimate this code could make. See docs/architecture.md, "Acquisition: the
// only network access".
//
// The boolean is the same question Tile's is, and is not the same question as
// the error: an archive legitimately holds no tile at most coordinates.
//
// TWO TILES MAY SHARE ONE LOCATION. The format stores an identical tile once
// and serves it to a run of consecutive tile IDs through a single entry, which
// is why a cell over open water is a few kilobytes rather than eighty-five
// separate ones. A caller adding up what a download costs has to deduplicate
// on the Location, or it will count the same bytes many times and quote a
// figure far above what will actually be transferred.
func (r *Reader) Locate(z uint8, x, y uint32) (Location, bool, error) {
	id, err := ZxyToID(z, x, y)
	if err != nil {
		return Location{}, false, err
	}
	return r.locateByID(id, fmt.Sprintf("tile %d/%d/%d", z, x, y))
}

// TileByID is Tile addressed by tile ID rather than by coordinates.
//
// The ID is converted to coordinates, but only to reject one that names no
// tile at all and to say which tile an error is about. The LOOKUP uses the ID
// as given: converting to z/x/y and back would make an ID lookup depend on
// ZxyToID and IDToZxy being exact inverses across the whole range, which is a
// property they have and which nothing needs to stake a tile on.
func (r *Reader) TileByID(id uint64) ([]byte, bool, error) {
	z, x, y, err := IDToZxy(id)
	if err != nil {
		return nil, false, err
	}
	return r.tileByID(id, fmt.Sprintf("tile %d/%d/%d", z, x, y))
}

// tileByID is the shared body of Tile and TileByID: find the stored bytes and
// decompress them. what names the tile in any error.
func (r *Reader) tileByID(id uint64, what string) ([]byte, bool, error) {
	raw, ok, err := r.rawTileByID(id, what)
	if err != nil || !ok {
		return nil, ok, err
	}
	data, err := decompress(r.header.TileCompression, raw, r.Limits.Tile, what)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// rawTileByID locates a tile and reads its stored bytes.
//
// Finding where a tile is and reading it are two jobs, and they are split
// because the acquisition step wants the first without the second: planning a
// download means knowing where every tile lives, and a planner that had to
// read each one to find out would have downloaded the thing it was costing.
func (r *Reader) rawTileByID(id uint64, what string) ([]byte, bool, error) {
	loc, ok, err := r.locateByID(id, what)
	if err != nil || !ok {
		return nil, ok, err
	}
	data, err := r.readAt(uint64(loc.Offset), uint32(loc.Length), r.Limits.Tile, what)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// locateByID walks the directories for id and returns where its bytes are.
//
// It reads directories -- which for a remote archive is a range request each,
// mostly served from the leaf cache -- and never the tile data section. That
// separation is what a dry run rests on: a plan may read an archive's index
// and must not read a byte of its contents.
func (r *Reader) locateByID(id uint64, what string) (Location, bool, error) {
	entries := r.root
	where := "the root directory"
	for depth := 0; depth <= maxLeafDepth; depth++ {
		e, ok := find(entries, id)
		if !ok {
			return Location{}, false, nil
		}
		if !e.IsLeaf() {
			offset, err := sectionOffset(r.header.TileDataOffset, r.header.TileDataLength, e, "the tile data section", what)
			if err != nil {
				return Location{}, false, err
			}
			// The offset is bounded by the tile data section, which the header
			// states as a uint64, so an archive larger than 2^63 bytes would
			// be needed to make this conversion lose anything. io.ReaderAt
			// speaks int64 offsets, so such an archive is unreadable anyway,
			// and saying so here is better than an offset that comes out
			// negative further down.
			if offset > math.MaxInt64 || uint64(e.Length) > math.MaxInt64-offset {
				return Location{}, false, fmt.Errorf("pmtiles: %s is at offset %d of the archive, past where an io.ReaderAt can address", what, offset)
			}
			return Location{Offset: int64(offset), Length: int64(e.Length)}, true, nil
		}
		leaf, err := r.leafAt(e)
		if err != nil {
			return Location{}, false, fmt.Errorf("pmtiles: following %s to the leaf for %s: %w", where, what, err)
		}
		entries = leaf
		where = fmt.Sprintf("the leaf directory at %d", e.Offset)
	}
	return Location{}, false, fmt.Errorf("pmtiles: %s is still behind a leaf directory after %d of them; the archive's directories point at each other", what, maxLeafDepth)
}

// sectionOffset turns an entry's section-relative offset into an absolute one,
// refusing anything that does not lie wholly inside the section the header
// declared.
//
// This is the check that stops a directory entry addressing the rest of the
// file. Without it, an offset near 2^64 wraps when the section base is added
// and lands back at the start of the archive: a lookup that returns ok, no
// error, and the archive's own header bytes dressed as a tile. The header
// carries both section lengths precisely so that this question can be asked.
func sectionOffset(base, size uint64, e Entry, section, what string) (uint64, error) {
	end := e.Offset + uint64(e.Length)
	if end < e.Offset {
		return 0, fmt.Errorf("pmtiles: %s is %d bytes at offset %d of %s, which overflows; the directory is damaged", what, e.Length, e.Offset, section)
	}
	if end > size {
		return 0, fmt.Errorf("pmtiles: %s is %d bytes at offset %d of %s, which is only %d bytes long", what, e.Length, e.Offset, section, size)
	}
	abs := base + e.Offset
	if abs < base {
		return 0, fmt.Errorf("pmtiles: %s is at offset %d of %s, which starts at %d; the two overflow together", what, e.Offset, section, base)
	}
	return abs, nil
}

// leafAt returns the decoded leaf directory an entry points at, reading it if
// it is not already cached.
func (r *Reader) leafAt(e Entry) ([]Entry, error) {
	key := leafKey{offset: e.Offset, length: e.Length}
	r.mu.Lock()
	cached, ok := r.leaves[key]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}
	offset, err := sectionOffset(r.header.LeafOffset, r.header.LeafLength, e, "the leaf directory section", "a leaf directory")
	if err != nil {
		return nil, err
	}
	entries, err := r.directoryAt(offset, e.Length, "a leaf directory")
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if len(r.leaves) >= maxCachedLeaves {
		clear(r.leaves)
	}
	r.leaves[key] = entries
	r.mu.Unlock()
	return entries, nil
}

// directoryAt reads, decompresses and decodes the directory stored at an
// absolute archive offset.
func (r *Reader) directoryAt(offset uint64, length uint32, what string) ([]Entry, error) {
	raw, err := r.readAt(offset, length, r.Limits.Directory, what)
	if err != nil {
		return nil, err
	}
	plain, err := decompress(r.header.InternalCompression, raw, r.Limits.Directory, what)
	if err != nil {
		return nil, err
	}
	entries, err := DecodeDirectory(plain)
	if err != nil {
		return nil, fmt.Errorf("pmtiles: decoding %s at offset %d: %w", what, offset, err)
	}
	return entries, nil
}

// readAt reads exactly length bytes from offset in ONE request, naming what was
// being read when it could not.
//
// # Why one request matters here and not in most readers
//
// This reader exists to be pointed at an HTTP range source: that is what
// io.ReaderAt buys, and the acquisition step is the reason the whole package is
// shaped this way. Over such a source every call to ReadAt is a request and a
// round trip, so how many calls a section takes is not a performance detail, it
// is the difference between a usable reader and one that hammers a host this
// project has no agreement with.
//
// An earlier version grew the buffer instead, with io.ReadAll over a section
// reader. Against the real planet archive that cost fourteen requests for one
// 88 KB leaf directory and fourteen more for the tile behind it: twenty-eight
// round trips and about thirteen seconds for a single tile, and roughly 2,400
// requests for a cell that needs 85. The byte count was fine; it was purely the
// trips.
//
// # Why that was ever reasonable, and what changed
//
// Growing the buffer was a defence: lengths come out of directories, a
// directory can be corrupt, the format has no checksum, and "allocate the next
// four gigabytes" is a thing a damaged entry can ask for. Reading incrementally
// meant a bogus length cost only what the source actually had.
//
// It was never much of a defence -- it bounds the cost by what the SOURCE will
// hand over, and over a range request that is whatever the remote chooses to
// send, the remote being the party that wrote the length in the first place --
// and it is no longer needed. Limits gives a real ceiling and sectionOffset has
// already checked the length against the section it claims to live in, so the
// length can be rejected BEFORE anything is allocated. That is strictly
// stronger than reading four gigabytes in instalments and failing afterwards.
//
// One honest cost: a damaged length now allocates up to the limit in one go,
// where growing the buffer allocated only what a local file actually held. The
// ceiling is Limits either way and it is freed on the way out, so this is a
// worse worst case against a file and a far better one against a network -- and
// it is the trade this reader is for. Anyone tempted to put the growth back
// should reach for a lower Limits instead.
//
// io.ReadFull rather than a bare ReadAt because io.ReaderAt's contract allows a
// short read, and it loops on the remainder rather than on a growing buffer: a
// conforming source fills the request in one call, and a source that does not
// costs one call per short read instead of one per doubling.
func (r *Reader) readAt(offset uint64, length uint32, limit int64, what string) ([]byte, error) {
	if offset > math.MaxInt64 {
		return nil, fmt.Errorf("pmtiles: %s is at offset %d, beyond any file", what, offset)
	}
	// Checked before the allocation, which is the whole point of checking it
	// here. The limit is a ceiling on the memory a section may occupy, and the
	// stored bytes occupy memory just as the decompressed ones do.
	if int64(length) > limit {
		return nil, fmt.Errorf("pmtiles: %s is %d stored bytes, past this reader's limit of %d; raise Reader.Limits if the archive is one you trust", what, length, limit)
	}
	buf := make([]byte, length)
	n, err := io.ReadFull(io.NewSectionReader(r.src, int64(offset), int64(length)), buf)
	// Compared exactly rather than with errors.Is, deliberately. These two are
	// io.ReadFull's own way of saying "the section reader ran out", which is
	// the truncation reported below. A source that wraps an EOF of its own is
	// saying something else -- a connection closed part way through, most
	// likely -- and that message is more use to whoever has to fix it than
	// "the archive is truncated" would be, so it is passed through instead.
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("pmtiles: reading %d bytes at offset %d for %s: %w", length, offset, what, err)
	}
	if n != int(length) {
		return nil, fmt.Errorf("pmtiles: %s wants %d bytes at offset %d and the archive has %d there; it is truncated or its directories are damaged", what, length, offset, n)
	}
	return buf, nil
}

// decompress expands a run of bytes according to the compression the header
// declared for it, refusing to produce more than limit bytes.
//
// The limit is the only thing standing between this reader and a decompression
// bomb, and it is applied here rather than at the four call sites because here
// is where the amplification happens. The archive states a section's
// COMPRESSED length and never its decompressed one, so nothing before this
// point knows how much memory a section is about to cost; gzip reaches roughly
// a thousand to one, so a small file can ask for a great deal. A directory is
// worse still, because its decompressed bytes are then multiplied again into
// entries.
//
// It is checked by reading one byte past the limit rather than by inspecting
// the result: a stream that would expand to gigabytes has to be stopped while
// it is expanding, not after.
//
// Brotli and zstd are named rather than attempted. Both are defined by the
// format and neither is in the standard library, and this module admits no
// compression dependency, so the honest outcome is an error that says which
// compression the archive used and what to do about it -- not a decode that
// returns the compressed bytes as though they were a tile.
func decompress(c Compression, b []byte, limit int64, what string) ([]byte, error) {
	switch c {
	case CompressionNone:
		// Uncompressed bytes cannot amplify, but the limit is about how much
		// memory a section may occupy and that is the same question either
		// way. A reader that let an uncompressed 500 MB tile through and
		// refused a compressed one would be enforcing a rule about gzip
		// rather than about memory.
		//
		// Nothing in this package can reach this branch any more: readAt
		// refuses a stored length past the limit before it allocates, so
		// anything arriving here is already inside it. It stays because the
		// limit is this function's contract rather than its callers', and a
		// future caller that reads bytes some other way should not have to
		// rediscover the rule.
		if int64(len(b)) > limit {
			return nil, tooLarge(what, limit)
		}
		return b, nil
	case CompressionGzip:
		// The bound itself is internal/inflate's, shared with slice and
		// osmpbf; what is this package's is the wording.
		out, err := inflate.Gzip(b, limit)
		switch {
		case errors.Is(err, inflate.ErrTooLarge):
			return nil, tooLarge(what, limit)
		case errors.Is(err, inflate.ErrNotGzip):
			return nil, fmt.Errorf("pmtiles: %s is not the gzip stream the header says it is: %w", what, err)
		case err != nil:
			return nil, fmt.Errorf("pmtiles: decompressing %s: %w", what, err)
		}
		return out, nil
	case CompressionBrotli, CompressionZstd:
		return nil, fmt.Errorf("pmtiles: %s is compressed with %s, which this reader does not implement; re-encode the archive with gzip or with no compression", what, c)
	case CompressionUnknown:
		return nil, fmt.Errorf("pmtiles: %s has compression code 0, meaning the writer did not record how it was compressed, so it cannot be decoded", what)
	}
	return nil, fmt.Errorf("pmtiles: %s has compression code %d, which the format does not define", what, uint8(c))
}

// tooLarge reports a section that exceeded its limit, and says which knob
// raises it -- the limit is a policy this package chose, so refusing without
// naming the way past it would be refusing without recourse.
func tooLarge(what string, limit int64) error {
	return fmt.Errorf("pmtiles: %s expands past this reader's limit of %d bytes; raise Reader.Limits if the archive is one you trust", what, limit)
}
