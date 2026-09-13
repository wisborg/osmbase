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
	"errors"
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

// maxRedirects caps how many redirects one request will follow.
//
// Go's default is ten, to anywhere. Five is plenty for the shapes that
// actually occur -- an http to https upgrade, a path normalisation, a dated
// file behind a stable name -- and a chain longer than that is a host that has
// lost track of where the archive is.
const maxRedirects = 5

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

	// url is what requests are sent to and is the only copy of it that may
	// hold credentials; displayURL is the one that may be printed. Keeping
	// them as two fields, built once, is what makes leaking the first a
	// visible mistake rather than a default.
	url        string
	displayURL string
	client     *http.Client

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
		return nil, fmt.Errorf("acquire: %q names no host to fetch from", redactURL(u))
	}
	return &RangeReader{
		url:        rawURL,
		displayURL: redactURL(u),
		client: &http.Client{
			Timeout:       DefaultTimeout,
			CheckRedirect: checkRedirect,
		},
	}, nil
}

// redactURL renders a URL for printing, with anything secret taken out.
//
// Errors go into terminals, logs and issue trackers, and an archive URL is one
// of the places a credential legitimately lives: userinfo for a private
// mirror, and a signature in the query string for anything presigned by an
// object store. Neither is any use to a reader of the message and both are
// worth having if they are stolen.
//
// The userinfo is replaced rather than removed, and the query is replaced
// rather than dropped, because the fact that a credential was in play is
// itself useful when working out why a host said no. What is lost is a benign
// query string, which is a small price for not having to keep a list of which
// query parameters are secret -- a list that would be wrong the first time
// somebody used a store this one has not heard of.
func redactURL(u *url.URL) string {
	shown := *u
	if shown.User != nil {
		shown.User = url.User("redacted")
	}
	if shown.RawQuery != "" {
		shown.RawQuery = "redacted"
	}
	shown.Fragment, shown.RawFragment = "", ""
	return shown.String()
}

// checkRedirect decides whether one redirect may be followed.
//
// Go's default follows up to ten, to any host, over any scheme, carrying the
// headers with it -- which for this reader means a host the caller named can
// hand the request to a host the caller has never heard of. Three rules
// replace it, and each one is here for a reason that is not visible from the
// code:
//
//   - NO SCHEME DOWNGRADE. A server that answers an https request with a
//     redirect to http has turned a private request into a public one, and
//     nothing about fetching an archive needs that to be possible.
//
//   - NO CHANGE OF HOST. This is the strict choice and it is deliberate. The
//     whole premise of this library is that the user decides which hosts see
//     where they are interested in; a redirect is that decision being made for
//     them by the host they did choose. The response would not parse as an
//     archive, so this is not a way to steal data, but the request is still
//     issued, and a hostile archive host would otherwise have a way to make
//     this machine probe things it can reach and nobody else can -- a cloud
//     metadata endpoint, a service on localhost.
//
//     The cost is real: object storage does sometimes redirect to a CDN, and
//     such an archive will not be read until the user names the destination
//     themselves. That is why the error says which host was declined. It turns
//     a silent change of host into a one-line decision the user makes, which
//     is the same shape as every other source decision in this design, rather
//     than into something that quietly worked.
//
//   - A LOW CAP. Five, not ten. See maxRedirects.
//
// Anyone loosening the middle rule should be sure they want the third-party
// request it permits, and should say so here.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("acquire: %s redirected more than %d times; the host has lost track of where the archive is",
			redactURL(via[0].URL), maxRedirects)
	}
	prev := via[len(via)-1].URL
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("acquire: %s redirected to the %q scheme, and this reader speaks http and https",
			redactURL(prev), req.URL.Scheme)
	}
	// The host is checked before the scheme downgrade so that a redirect which
	// breaks both rules is reported by the more useful one. "It sent you to
	// 169.254.169.254" says what happened; "it downgraded to http" describes a
	// detail of it.
	if !strings.EqualFold(prev.Host, req.URL.Host) {
		return fmt.Errorf("acquire: %s redirected to %s, a host you did not name; if that host is the right one, pass its URL as the source",
			redactURL(prev), req.URL.Host)
	}
	if prev.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("acquire: %s redirected to http, which would send the rest of this in the clear; refusing to downgrade",
			redactURL(prev))
	}
	return nil
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
	last := off + int64(len(p)) - 1

	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, fmt.Errorf("acquire: building a request for %s: %w", r.displayURL, err)
	}
	// Inclusive on both ends, which is what the HTTP range unit means and one
	// of the two ways this is routinely written wrong; the other is asking for
	// len(p) bytes and getting len(p)+1.
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, last))
	req.Header.Set("User-Agent", UserAgent)

	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("acquire: requesting bytes %d to %d of %s: %w", off, last, r.displayURL, unwrapURLError(err))
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// What was asked for -- or at least, what was asked for is what the
		// status claims. Which bytes actually came back is checked below.
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
		return 0, fmt.Errorf("acquire: %s answered 200 and sent the whole file instead of the %d bytes asked for; this reader needs a host that supports HTTP range requests", r.displayURL, len(p))
	default:
		return 0, fmt.Errorf("acquire: requesting bytes %d to %d of %s: %s", off, last, r.displayURL, resp.Status)
	}

	cr, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil {
		return 0, fmt.Errorf("acquire: asking %s for bytes %d to %d: %w", r.displayURL, off, last, err)
	}
	// THE BYTES THAT CAME BACK MUST BE THE BYTES ASKED FOR.
	//
	// A 206 says "here is part of the file" and the Content-Range says WHICH
	// part. Reading the body without comparing the two accepts any part the
	// server felt like sending as though it were the part requested -- ten
	// bytes from offset 0 handed over as offset 5000, and then decoded as the
	// directory or tile that was asked for. Nothing downstream can notice:
	// PMTiles has no checksum anywhere, a directory of arbitrary bytes usually
	// decodes into plausible entries, and the failure surfaces, if at all, as
	// a map with the wrong thing in it.
	//
	// This is the same class of defect the archive reader spends its length
	// checks on -- serving plausible wrong bytes under the right name -- and
	// this is the door it comes through when the archive is remote. The header
	// that closes it is already being parsed for the total.
	//
	// The end may come back SHORT, and only short: a range that runs past the
	// end of the archive is answered with what exists. It may not come back
	// long, and it may not start anywhere but where it was asked to.
	if cr.first != off || cr.last < cr.first || cr.last > last {
		return 0, fmt.Errorf("acquire: asked %s for bytes %d to %d and it answered with bytes %d to %d; those bytes would have been read as though they came from offset %d",
			r.displayURL, off, last, cr.first, cr.last, off)
	}
	if err := r.recordSize(cr); err != nil {
		return 0, err
	}

	n, err := io.ReadFull(resp.Body, p[:cr.last-cr.first+1])
	elapsed := time.Since(start)
	r.mu.Lock()
	r.requests++
	r.read += int64(n)
	r.mu.Unlock()
	if r.Trace != nil {
		r.Trace(off, n, elapsed)
	}
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return n, fmt.Errorf("acquire: reading bytes %d to %d of %s: %w", off, last, r.displayURL, unwrapURLError(err))
	}
	// A body shorter than the server's OWN Content-Range is a transfer that
	// broke, not the end of the archive, and saying EOF for it would have the
	// archive reader report a truncated file when the truth is a dropped
	// connection. The two are told apart here because this is the only place
	// that knows what the server said it was sending.
	if want := int(cr.last - cr.first + 1); n != want {
		return n, fmt.Errorf("acquire: %s said it was sending bytes %d to %d of the archive and sent %d of those %d bytes; the transfer did not finish",
			r.displayURL, cr.first, cr.last, n, want)
	}
	if n < len(p) {
		// The server had fewer bytes than were asked for and said so. That is
		// the end of the archive, which is EOF, exactly as a file would report
		// it.
		return n, io.EOF
	}
	return n, nil
}

// recordSize remembers the archive's total length, and refuses an archive
// whose length has changed since the last request.
//
// A changed total means the bytes behind this URL are not the bytes the
// directories already read describe -- a daily build republished under the
// same name, most likely -- and every offset held by the caller is now
// pointing into a different file. Continuing would read whatever now lives at
// those offsets and call it a tile.
func (r *RangeReader) recordSize(cr contentRange) error {
	if !cr.hasTotal {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasSize && r.size != cr.total {
		return fmt.Errorf("acquire: %s was %d bytes and is now %d; it changed while it was being read, so every offset taken from its directories is stale",
			r.displayURL, r.size, cr.total)
	}
	r.size, r.hasSize = cr.total, true
	return nil
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

// URL returns the archive's URL with any credentials taken out of it, which is
// the only form of it this type will give anybody.
//
// There is deliberately no accessor for the URL as supplied. A caller has it
// already -- it passed it in -- and everything a caller does with one it got
// back from here ends up on a screen or in a file.
func (r *RangeReader) URL() string { return r.displayURL }

// contentRange is a parsed Content-Range response header.
type contentRange struct {
	// first and last are inclusive byte positions in the whole archive.
	first, last int64
	// total is the archive's full length. A server may legally decline to say,
	// writing "*", which is hasTotal false rather than a total of zero.
	total    int64
	hasTotal bool
}

// parseContentRange parses a Content-Range of the form
// "bytes 0-126/134812420554", or "bytes 0-126/*".
//
// It is strict, and it refuses an absent header outright. A 206 without a
// Content-Range is malformed -- the response says it is part of something and
// then declines to say which part -- and the only thing a reader can do with
// the body is assume it is the part that was asked for, which is the
// assumption this whole function exists to stop being made. The multipart form
// a server sends for several ranges at once is refused by the same rule, and
// correctly: this reader never asks for more than one.
func parseContentRange(v string) (contentRange, error) {
	var cr contentRange
	v = strings.TrimSpace(v)
	if v == "" {
		return cr, errors.New("the server answered 206 with no Content-Range header, so there is nothing to say which bytes these are")
	}
	unit, spec, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(unit, "bytes") {
		return cr, fmt.Errorf("the server answered 206 with Content-Range %q, which is not a range of bytes", v)
	}
	positions, total, ok := strings.Cut(spec, "/")
	if !ok {
		return cr, fmt.Errorf("the server answered 206 with Content-Range %q, which names no total length", v)
	}
	firstText, lastText, ok := strings.Cut(positions, "-")
	if !ok {
		return cr, fmt.Errorf("the server answered 206 with Content-Range %q, which is not a first-to-last range", v)
	}
	first, err := strconv.ParseInt(strings.TrimSpace(firstText), 10, 64)
	if err != nil {
		return cr, fmt.Errorf("the server answered 206 with Content-Range %q, whose first byte position is not a number", v)
	}
	lastPos, err := strconv.ParseInt(strings.TrimSpace(lastText), 10, 64)
	if err != nil {
		return cr, fmt.Errorf("the server answered 206 with Content-Range %q, whose last byte position is not a number", v)
	}
	if first < 0 || lastPos < first {
		return cr, fmt.Errorf("the server answered 206 with Content-Range %q, which runs backwards", v)
	}
	cr.first, cr.last = first, lastPos
	if total = strings.TrimSpace(total); total != "*" {
		n, err := strconv.ParseInt(total, 10, 64)
		if err != nil || n < 0 {
			return cr, fmt.Errorf("the server answered 206 with Content-Range %q, whose total length is not a number", v)
		}
		cr.total, cr.hasTotal = n, true
	}
	return cr, nil
}

// unwrapURLError replaces a *url.Error with the error inside it.
//
// url.Error prints the URL it was for, and the http client builds it from the
// URL as supplied -- which may carry credentials. It strips a password and
// keeps a username. Since every message here already names the archive in its
// redacted form, the wrapper adds nothing but that risk.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
