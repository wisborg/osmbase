package pmtiles_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// The leaf cache has two jobs and the existing suite checks neither of them
// directly, because both are invisible through the tiles that come out: a
// reader with no cache at all, and a reader whose cache never drops anything,
// both return exactly the right bytes for every tile.
//
// What separates them is how often the archive is READ. A counting source
// makes that observable without a production seam, and it is the same source
// shape the package is built for -- the comment on Reader says it reads over
// an io.ReaderAt so that the same code serves a local file today and an HTTP
// range source later, and over a range source every re-read is a request.
//
// The two tests below are a pair on purpose. A reader that cached nothing
// would fail the first and pass the second; a reader that cached without bound
// would pass the first and fail the second. Either alone proves very little.

// countingSource wraps an archive's bytes and records the reads made against
// the leaf directory section, whose range it takes from the header the way any
// reader would.
type countingSource struct {
	b          []byte
	leafStart  uint64
	leafEnd    uint64
	mu         sync.Mutex
	leafReads  int
	minOffset  int64
	sawNegOff  bool
	totalReads int
	// log records every read as it happens. It is what turns "the tiles came
	// back" into "and it took this many requests to get them", which on a
	// range source is the difference between usable and not.
	log []readRecord
}

// readRecord is one call to ReadAt: the size asked for and where.
type readRecord struct {
	length int
	offset int64
}

func newCountingSource(b []byte) *countingSource {
	leafOffset := binary.LittleEndian.Uint64(b[40:48])
	leafLength := binary.LittleEndian.Uint64(b[48:56])
	return &countingSource{b: b, leafStart: leafOffset, leafEnd: leafOffset + leafLength}
}

func (s *countingSource) ReadAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	s.totalReads++
	s.log = append(s.log, readRecord{length: len(p), offset: off})
	if off < s.minOffset {
		s.minOffset = off
	}
	if off < 0 {
		s.sawNegOff = true
	}
	if off >= 0 && uint64(off) >= s.leafStart && uint64(off) < s.leafEnd {
		s.leafReads++
	}
	s.mu.Unlock()
	if off < 0 {
		return 0, fmt.Errorf("countingSource: negative offset %d", off)
	}
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (s *countingSource) leafReadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafReads
}

// takeReads returns the reads recorded since the last call and clears the log,
// so a test can attribute requests to one operation rather than to everything
// that came before it.
func (s *countingSource) takeReads() []readRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.log
	s.log = nil
	return out
}

// describe renders a set of reads for a failure message, because the count on
// its own does not say whether the extra requests were a second section or one
// section fetched in pieces.
func (s *countingSource) describe(reads []readRecord) string {
	var b strings.Builder
	total := 0
	for _, r := range reads {
		fmt.Fprintf(&b, "\n    %7d bytes at %d", r.length, r.offset)
		total += r.length
	}
	fmt.Fprintf(&b, "\n    (%d reads, %d bytes)", len(reads), total)
	return b.String()
}

// readAllTiles reads ids in order and insists every one comes back.
func readAllTiles(t *testing.T, r *pmtiles.Reader, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		got, ok, err := r.TileByID(uint64(i))
		if err != nil || !ok {
			t.Fatalf("tile %d: ok %v, err %v", i, ok, err)
		}
		if want := fmt.Sprintf("tile-%d", i); string(got) != want {
			t.Fatalf("tile %d = %q, want %q", i, got, want)
		}
	}
}

// TestReader_ASecondPassOverAFewLeavesReadsNoneOfThemAgain is the cache doing
// its job.
//
// Twenty-four tiles at six per leaf is four leaf directories, which is a
// handful by any cache's standards. Reading every tile fills the cache;
// reading every tile again must touch the leaf section not once more. A
// render walking one area re-reads its leaves constantly, and over a range
// source each of those is a request that the cache exists to prevent.
//
// The count is derived from the fixture rather than pinned: whatever the first
// pass read, the second pass must read zero.
func TestReader_ASecondPassOverAFewLeavesReadsNoneOfThemAgain(t *testing.T) {
	const count = 24
	built := build(t, osmbasetest.Archive{Tiles: numberedTiles(count), LeafSize: 6})
	if built.LeafDirectories != 4 {
		t.Fatalf("the fixture has %d leaf directories, want 4; %d tiles at 6 per leaf", built.LeafDirectories, count)
	}

	src := newCountingSource(built.Bytes)
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	readAllTiles(t, r, count)
	first := src.leafReadCount()
	if first != built.LeafDirectories {
		t.Errorf("the first pass read the leaf section %d times, want %d, once per leaf directory", first, built.LeafDirectories)
	}

	readAllTiles(t, r, count)
	if again := src.leafReadCount() - first; again != 0 {
		t.Errorf("a second pass over the same %d leaf directories read the leaf section %d more times; the cache is not holding them", built.LeafDirectories, again)
	}
}

// TestReader_TheLeafCacheDoesNotGrowWithoutBound is the other side, and it is
// the side nothing checked.
//
// A reader that never drops a leaf serves every tile correctly and reads each
// leaf exactly once ever, so every assertion about tiles passes while the
// cache grows with the number of distinct leaves the render touches. That is
// not a theoretical quantity: a planet archive's leaf directories number in
// the tens of thousands, a decoded leaf is a slice of entries rather than the
// compressed bytes, and nothing in a render's walk revisits an area it has
// left.
//
// So the property asserted is boundedness, stated without naming the bound:
// walk far more leaves than any fixed-size cache could hold, twice, and at
// least one leaf must have been dropped and re-read. Deleting the eviction
// branch makes the second pass read the leaf section zero times, and this is
// the only test that notices.
//
// The paired test above is what stops this one being satisfied by a reader
// that simply caches nothing.
func TestReader_TheLeafCacheDoesNotGrowWithoutBound(t *testing.T) {
	// Enough leaves to overrun any cache a reader of this format would
	// sensibly keep. Twelve entries per directory keeps the archive three
	// levels deep, which is within the reader's own cap on how many leaves one
	// lookup may follow; a smaller LeafSize would build a fifth level and the
	// fixture would be testing that cap instead.
	const count = 2000
	built := build(t, osmbasetest.Archive{Tiles: numberedTiles(count), LeafSize: 12})
	if built.LeafDirectories < 100 {
		t.Fatalf("the fixture has only %d leaf directories, which may not overrun the cache", built.LeafDirectories)
	}

	src := newCountingSource(built.Bytes)
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	readAllTiles(t, r, count)
	first := src.leafReadCount()
	readAllTiles(t, r, count)
	second := src.leafReadCount() - first

	if second == 0 {
		t.Errorf("after walking %d leaf directories, a second pass re-read none of them: the cache is holding every leaf it has ever seen and grows with the archive", built.LeafDirectories)
	}
	// And the tiles were all correct on both passes, which readAllTiles
	// already insisted on -- boundedness must not cost correctness.
}

func numberedTiles(n int) []osmbasetest.ArchiveTile {
	tiles := make([]osmbasetest.ArchiveTile, n)
	for i := range tiles {
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i), Data: []byte(fmt.Sprintf("tile-%d", i))}
	}
	return tiles
}

// TestReader_NeverAsksItsSourceForANegativeOffset.
//
// Every offset in this format is a uint64 and every offset handed to an
// io.ReaderAt is an int64, so a header naming an offset above 2^63 converts to
// a negative one. io.ReaderAt's contract says nothing whatever about negative
// offsets: bytes.Reader returns an error, os.File returns an error, and the
// HTTP range source this package is shaped for would format the number into a
// Range header and get back whatever the remote made of it.
//
// The reader therefore owes it to the source not to ask. What is asserted here
// is that directly -- the source records every offset it is given and none may
// be negative -- rather than the resulting error message, because the error is
// the source's to choose and the contract is the reader's to keep.
//
// The archive is honest apart from the one field. A reader that passed the
// offset through would, against bytes.Reader, still fail; against a source
// that reinterpreted the sign it would read some other part of the file and
// call it a directory.
func TestReader_NeverAsksItsSourceForANegativeOffset(t *testing.T) {
	good := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
	})

	cases := []struct {
		name  string
		patch func(b []byte)
		// read is what to do after opening, for the fields that only matter
		// once a tile or the metadata is asked for.
		read func(r *pmtiles.Reader) error
	}{
		{
			name:  "a root directory above 2^63",
			patch: func(b []byte) { binary.LittleEndian.PutUint64(b[8:], 1<<63) },
		},
		{
			name:  "a metadata section above 2^63",
			patch: func(b []byte) { binary.LittleEndian.PutUint64(b[24:], 1<<63|1) },
			read: func(r *pmtiles.Reader) error {
				_, err := r.Metadata()
				return err
			},
		},
		{
			name: "a tile data section above 2^63",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint64(b[56:], 1<<63)
				binary.LittleEndian.PutUint64(b[64:], 1<<20)
			},
			read: func(r *pmtiles.Reader) error {
				_, _, err := r.TileByID(5)
				return err
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := append([]byte(nil), good.Bytes...)
			c.patch(b)
			src := newCountingSource(b)
			r, err := pmtiles.NewReader(src)
			if err == nil && c.read != nil {
				err = c.read(r)
			}
			if err == nil {
				t.Error("the archive read without complaint despite an offset no file can have")
			}
			if src.sawNegOff {
				t.Errorf("the reader asked its source to read at offset %d; io.ReaderAt does not define what a negative offset means and a range source would send it to the remote", src.minOffset)
			}
		})
	}

	// The paired case: the same archive untouched never goes near the limit
	// and reads its tile, so the assertions above are not passing merely
	// because every offset happens to be small.
	src := newCountingSource(bytes.Clone(good.Bytes))
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader on the undamaged archive: %v", err)
	}
	if _, ok, err := r.TileByID(5); err != nil || !ok {
		t.Fatalf("the undamaged archive did not serve its tile: ok %v, err %v", ok, err)
	}
	if src.sawNegOff {
		t.Error("the undamaged archive produced a negative offset")
	}
}
