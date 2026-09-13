package acquire_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// serve starts a local server that answers range requests over body, and
// returns its URL. It reaches nothing outside this process: the point of these
// tests is the range protocol, and a test that needed a host on the internet
// would be a test that fails on a train.
func serve(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "archive.pmtiles", epoch, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/archive.pmtiles"
}

// epoch is the modification time every test server reports. A fixed one keeps
// the responses identical between runs.
var epoch = time.Unix(0, 0)

// pattern is a body whose every byte says where it is, so a read at the wrong
// offset produces wrong content rather than plausible zeroes.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// TestRangeReader_ReadsExactlyTheBytesAskedFor checks the property io.ReaderAt
// is defined by, at an offset that is not zero.
//
// An off-by-one in the Range header is invisible at offset 0 with a short
// read, because the bytes that arrive still start in the right place; it shows
// up as content shifted by one, which here is a byte value that says so.
func TestRangeReader_ReadsExactlyTheBytesAskedFor(t *testing.T) {
	body := pattern(4096)
	r, err := acquire.NewRangeReader(serve(t, body))
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	got := make([]byte, 100)
	n, err := r.ReadAt(got, 1000)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 100 {
		t.Errorf("ReadAt returned %d bytes, want 100", n)
	}
	if !bytes.Equal(got, body[1000:1100]) {
		t.Errorf("ReadAt(100 bytes at 1000) returned the wrong bytes: first is %d, want %d", got[0], body[1000])
	}
	if reqs, read := r.Stats(); reqs != 1 || read != 100 {
		t.Errorf("Stats() = %d requests, %d bytes; want 1 request and 100 bytes", reqs, read)
	}
	// The total length is learned from Content-Range, so it is known after a
	// read and not before one.
	if size, ok := r.Size(); !ok || size != int64(len(body)) {
		t.Errorf("Size() = %d, %v; want %d, true", size, ok, len(body))
	}
}

// TestRangeReader_SendsTheRangeAndTheContact checks the two headers every
// request carries.
//
// The Range header is checked as a whole string because its end is INCLUSIVE:
// asking for bytes 1000-1100 fetches 101 bytes, not 100, and the extra byte is
// silently discarded by a reader that then looks entirely correct.
//
// The User-Agent is checked because it is an obligation rather than a detail.
// It is what lets a host reach a person instead of blocking an address range,
// and nothing else in the test suite would notice it disappearing.
func TestRangeReader_SendsTheRangeAndTheContact(t *testing.T) {
	var gotRange, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotAgent = r.Header.Get("User-Agent")
		http.ServeContent(w, r, "a.pmtiles", epoch, bytes.NewReader(pattern(4096)))
	}))
	defer srv.Close()

	r, err := acquire.NewRangeReader(srv.URL)
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 100), 1000); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if want := "bytes=1000-1099"; gotRange != want {
		t.Errorf("Range header was %q, want %q", gotRange, want)
	}
	if !strings.Contains(gotAgent, "osmbase") || !strings.Contains(gotAgent, "osmbase@wisborg.dk") {
		t.Errorf("User-Agent was %q; it must name the program and the contact address", gotAgent)
	}
}

// TestRangeReader_RefusesAServerThatIgnoresTheRange is the expensive failure.
//
// A server answering 200 is offering the whole archive, which for the planet
// build is over a hundred gigabytes. The first bytes of that body would even
// be the right answer for an offset of zero, so a reader that just read what
// arrived would look correct while downloading a planet.
func TestRangeReader_RefusesAServerThatIgnoresTheRange(t *testing.T) {
	body := pattern(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	r, err := acquire.NewRangeReader(srv.URL)
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	_, err = r.ReadAt(make([]byte, 10), 1000)
	if err == nil {
		t.Fatal("ReadAt succeeded against a server that ignored the range; want an error")
	}
	if !strings.Contains(err.Error(), "range requests") {
		t.Errorf("error was %q; it should say the host does not support range requests", err)
	}
}

// TestRangeReader_PastTheEndIsEOF covers both shapes of running off the end,
// because the PMTiles reader distinguishes a truncated archive from a network
// failure by exactly this.
//
// A range starting past the end is answered 416 by a conforming server and one
// that straddles the end comes back short with a 206; a file reader reports
// both as io.EOF, and so must this.
func TestRangeReader_PastTheEndIsEOF(t *testing.T) {
	body := pattern(1000)
	r, err := acquire.NewRangeReader(serve(t, body))
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}

	n, err := r.ReadAt(make([]byte, 10), 5000)
	if !errors.Is(err, io.EOF) {
		t.Errorf("reading past the end gave %d bytes and %v, want io.EOF", n, err)
	}

	got := make([]byte, 100)
	n, err = r.ReadAt(got, 950)
	if !errors.Is(err, io.EOF) {
		t.Errorf("reading across the end gave %v, want io.EOF", err)
	}
	if n != 50 {
		t.Errorf("reading across the end returned %d bytes, want the 50 that exist", n)
	}
	if !bytes.Equal(got[:n], body[950:]) {
		t.Error("the bytes before the end were not the ones the archive holds")
	}
}

// TestRangeReader_ReportsAnHTTPError checks that a status this reader cannot
// use is reported with the status in it, because "404 Not Found" is a thing a
// user can act on and "read failed" is not.
func TestRangeReader_ReportsAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	r, err := acquire.NewRangeReader(srv.URL + "/missing.pmtiles")
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	_, err = r.ReadAt(make([]byte, 10), 0)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("ReadAt against a 404 gave %v; the error should carry the status", err)
	}
}

// TestRangeReader_CallsTraceOncePerRequest checks the progress hook a command
// uses so that a user is not left watching a blank terminal.
func TestRangeReader_CallsTraceOncePerRequest(t *testing.T) {
	r, err := acquire.NewRangeReader(serve(t, pattern(4096)))
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	var offsets []int64
	var counts []int
	r.Trace = func(off int64, n int, _ time.Duration) {
		offsets = append(offsets, off)
		counts = append(counts, n)
	}
	if _, err := r.ReadAt(make([]byte, 16), 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 32), 2048); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 2048 || counts[0] != 16 || counts[1] != 32 {
		t.Errorf("Trace saw offsets %v and counts %v; want offsets [0 2048] and counts [16 32]", offsets, counts)
	}
}

// TestNewRangeReader_RefusesWhatItCannotFetch covers the scheme check, which
// is what turns a path typed where a URL was wanted into a sentence rather
// than a request to a host called "c".
func TestNewRangeReader_RefusesWhatItCannotFetch(t *testing.T) {
	for _, raw := range []string{
		"s3://bucket/planet.pmtiles",
		"file:///tmp/planet.pmtiles",
		"/var/tmp/planet.pmtiles",
		"planet.pmtiles",
		"https://",
	} {
		if _, err := acquire.NewRangeReader(raw); err == nil {
			t.Errorf("NewRangeReader(%q) succeeded; want an error", raw)
		}
	}
}

// TestRangeReader_DrivesThePMTilesReader is the whole point of the type: the
// archive reader written against io.ReaderAt must read a remote archive with
// no change at all, and the tile that comes back must be byte-identical to the
// one that went in.
//
// The archive is synthetic and the server is local, so this asserts the
// plumbing rather than anybody's planet build.
func TestRangeReader_DrivesThePMTilesReader(t *testing.T) {
	id, err := pmtiles.ZxyToID(14, 15073, 9831)
	if err != nil {
		t.Fatalf("ZxyToID: %v", err)
	}
	want := []byte("the tile at the opera house")
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:               []osmbasetest.ArchiveTile{{ID: id, Data: want}},
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
		TileType:            pmtiles.TileTypeMVT,
		MaxZoom:             15,
	})
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}

	src, err := acquire.NewRangeReader(serve(t, built.Bytes))
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("pmtiles.NewReader over HTTP: %v", err)
	}
	got, ok, err := r.Tile(14, 15073, 9831)
	if err != nil || !ok {
		t.Fatalf("Tile(14, 15073, 9831) = ok %v, err %v", ok, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("tile came back as %q, want %q", got, want)
	}
	// Three round trips: the header, the root directory, the tile. The number
	// is pinned because the whole design rests on a tile costing a couple of
	// round trips rather than a dozen -- an earlier version of the archive
	// reader took fourteen for one directory, and nothing but a count like
	// this notices that happening again.
	//
	// It is three rather than two because the header and the root are read
	// separately. The format caps the two at 16 KiB together so that a client
	// CAN fetch them in one request, and a later reader may; this records what
	// today costs, not what it must cost.
	if reqs, _ := src.Stats(); reqs != 3 {
		t.Errorf("opening the archive and reading one tile took %d requests, want 3", reqs)
	}
	if _, ok, err := r.Tile(14, 0, 0); err != nil || ok {
		t.Errorf("Tile(14, 0, 0) = ok %v, err %v; the archive holds no tile there", ok, err)
	}
}
