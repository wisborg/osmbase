// Package acquire is the only package in this module that opens a socket.
//
// Everything else here reads bytes it is handed. That separation is the point:
// a reader wondering whether this library can reach the internet while a map
// is being drawn should be able to answer it from the import list, and the
// answer is that only the package named after fetching can. See
// docs/architecture.md, "Acquisition: the only network access".
//
// So far it holds one thing, RangeReader, which is the io.ReaderAt the PMTiles
// reader wants with HTTP range requests behind it. The rest of the package --
// the fetch plan, the byte-range coalescing, the on-disk slice -- is not
// written yet.
//
// # Why this is here rather than in cmd/osmbase
//
// A one-screen ReadAt over http.Get is easy to write in a command, and the
// first version of this was exactly that, in a throwaway probe. What is not
// one screen is the part that matters: refusing a 200 response that would
// deliver a hundred gigabytes because the server ignored the Range header,
// telling a range past the end of the file apart from a network failure,
// honouring io.ReaderAt's contract about short reads, and sending a
// User-Agent that lets a host reach a person instead of blocking a subnet.
// Every one of those is a rule about correctness that the fetch planner will
// need to obey too, and the alternative to putting them here is discovering
// them twice.
//
// It also makes them testable. A range reader in a main package is tested by
// running the program; here it is tested against httptest, offline, which is
// what step 4 of the build order asks for.
package acquire

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is what this library reports to the hosts it fetches from.
//
// It lives here because this is where requests are built and there is no
// root package yet to hold it; it moves when there is one.
const Version = "0.1.0-dev"

// UserAgent identifies the program to the host serving an archive.
//
// Neither of the hosts this design names publishes an acceptable-use policy
// for automated range requests, so this is not a compliance measure. It is
// what those hosts have said they want: a name and an address that lets an
// operator ask a question instead of blocking a range of IP addresses. The
// contact exists for exactly this. See docs/architecture.md, "What the hosts
// actually ask for".
const UserAgent = "osmbase/" + Version + " (+https://github.com/wisborg/osmbase; osmbase@wisborg.dk)"

// DefaultTimeout bounds one range request. It is generous because a range
// request against a large archive on a cold cache is sometimes slow, and the
// failure it exists to prevent is a request that never returns at all.
const DefaultTimeout = 2 * time.Minute

// RangeReader reads an archive over HTTP range requests.
//
// It satisfies io.ReaderAt, which is all pmtiles.NewReader wants, so the same
// reader code reads a local file and a remote archive with no branch anywhere
// between. Every call to ReadAt is one HTTP request and one round trip, which
// is why the PMTiles reader goes to the trouble of reading each section in a
// single call.
//
// It is safe for concurrent use.
type RangeReader struct {
	// Trace, when set, is called after every completed range request. It is
	// for a command that would otherwise leave a user watching a blank
	// terminal while a hundred kilobytes arrive from the other side of the
	// world; nothing in the library depends on it. Set it before the first
	// read. It is called from whichever goroutine made the request.
	Trace func(offset int64, n int, elapsed time.Duration)

	url    string
	client *http.Client

	mu       sync.Mutex
	requests int
	read     int64
	size     int64
	hasSize  bool
}

// NewRangeReader returns a reader over the archive at rawURL.
//
// It makes no request: the first one happens when something reads. That means
// a mistyped host is reported by the first ReadAt rather than here, and it
// also means constructing one of these is free and silent, which is what a
// library that promises not to touch the network unasked has to be.
func NewRangeReader(rawURL string) (*RangeReader, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("acquire: %q is not a URL: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("acquire: %q uses the %q scheme, and this reader speaks http and https", rawURL, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("acquire: %q names no host to fetch from", rawURL)
	}
	return &RangeReader{url: rawURL, client: &http.Client{Timeout: DefaultTimeout}}, nil
}

// ReadAt reads len(p) bytes from offset off, in one request.
//
// It follows io.ReaderAt: it reads exactly len(p) bytes or returns an error
// saying why it could not, and a read that runs past the end of the archive
// returns io.EOF. Both matter to the PMTiles reader, which uses the second to
// tell a truncated archive from a damaged one.
func (r *RangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("acquire: cannot read at offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}

	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, fmt.Errorf("acquire: building a request for %s: %w", r.url, err)
	}
	// Inclusive on both ends, which is what the HTTP range unit means and one
	// of the two ways this is routinely written wrong; the other is asking for
	// len(p) bytes and getting len(p)+1.
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	req.Header.Set("User-Agent", UserAgent)

	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("acquire: requesting bytes %d to %d of %s: %w", off, off+int64(len(p))-1, r.url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// What was asked for.
	case http.StatusRequestedRangeNotSatisfiable:
		// The range starts past the end of the archive. That is the same
		// condition a file reader reports as EOF, and the PMTiles reader
		// relies on being able to tell it from a failure: one means the
		// archive is truncated or its directories point past its end, the
		// other means the network did.
		return 0, io.EOF
	case http.StatusOK:
		// The server ignored the Range header and is sending the WHOLE
		// archive, which for a planet build is over a hundred gigabytes.
		// Refused rather than read: the body would arrive, the first len(p)
		// bytes would even be the right ones for an offset of zero, and the
		// program would appear to work while downloading the planet.
		return 0, fmt.Errorf("acquire: %s answered 200 and sent the whole file instead of the %d bytes asked for; this reader needs a host that supports HTTP range requests", r.url, len(p))
	default:
		return 0, fmt.Errorf("acquire: requesting bytes %d to %d of %s: %s", off, off+int64(len(p))-1, r.url, resp.Status)
	}

	if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
		r.mu.Lock()
		r.size, r.hasSize = total, true
		r.mu.Unlock()
	}

	n, err := io.ReadFull(resp.Body, p)
	elapsed := time.Since(start)
	r.mu.Lock()
	r.requests++
	r.read += int64(n)
	r.mu.Unlock()
	if r.Trace != nil {
		r.Trace(off, n, elapsed)
	}
	switch err {
	case nil:
		return n, nil
	case io.ErrUnexpectedEOF, io.EOF:
		// The server gave a 206 and then fewer bytes than the range asked
		// for, which is what the end of the archive looks like over HTTP.
		// Reported as EOF, with the bytes that did arrive, exactly as a file
		// would.
		return n, io.EOF
	default:
		return n, fmt.Errorf("acquire: reading bytes %d to %d of %s: %w", off, off+int64(len(p))-1, r.url, err)
	}
}

// Stats reports how many requests have been made and how many bytes have come
// back. It is what lets a command say "four requests, 96 KiB" about an archive
// of a hundred gigabytes, which is the claim the format is chosen for.
func (r *RangeReader) Stats() (requests int, bytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests, r.read
}

// Size reports the archive's total length in bytes and whether it is known
// yet.
//
// It is learned from the Content-Range header of a response that has already
// happened, never from a request of its own: a size this reader does not
// already know is not worth a round trip to a host, and a caller that has read
// nothing gets false rather than a number obtained behind its back.
func (r *RangeReader) Size() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size, r.hasSize
}

// URL returns the archive's URL.
func (r *RangeReader) URL() string { return r.url }

// parseContentRangeTotal pulls the total length out of a Content-Range header
// of the form "bytes 0-126/134812420554".
//
// A total of "*" means the server declines to say, which is legal, and is
// reported as unknown rather than as zero.
func parseContentRangeTotal(v string) (int64, bool) {
	slash := strings.LastIndex(v, "/")
	if slash < 0 {
		return 0, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(v[slash+1:]), 10, 64)
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}
