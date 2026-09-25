package acquire

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DownloadTimeout is how long a whole-file download may take by default.
//
// Longer than DefaultTimeout, which bounds a single range request of a few
// hundred kilobytes. A whole file here is tens of megabytes over a link this
// cannot know the speed of, and a timeout that kills a download at ninety per
// cent is worse than one that waits.
//
// Ten minutes is sized for those tens of megabytes, and it is NOT enough for
// every caller: an OpenStreetMap country extract is hundreds of megabytes to
// a gigabyte, which inside ten minutes needs a sustained six to thirteen
// megabits -- so on an ordinary link the download would die at ninety per
// cent, which is the thing this constant's own reasoning says to avoid. A
// caller that knows it is asking for something larger says so with
// DownloadWithin.
const DownloadTimeout = 10 * time.Minute

// Download copies a whole file from an https URL into w, returning how many
// bytes arrived.
//
// # Why this is here and not in the command that wanted it
//
// This package is the only one in the library that opens a socket, and that is
// stated in the architecture as a property a reader can check by reading the
// import list rather than as a convention. The first version of the boundary
// download did its own http.Get from cmd/osmbase, which broke that -- and
// broke it quietly, because the code worked. What it lost was everything this
// package exists to hold in one place: the User-Agent that tells a host who is
// calling, the redirect policy that refuses a host change or a scheme
// downgrade, and any limit at all on how much a response may write to the
// caller's disk.
//
// limit caps the body. A response longer than it is refused rather than
// truncated: a file that arrives half-read is a file that fails to parse
// later, somewhere else, with nothing to connect it to the download that
// produced it. Zero means no limit, which is for a caller that genuinely
// cannot bound the size and has decided that is acceptable.
func Download(rawURL string, w io.Writer, limit int64) (int64, error) {
	return DownloadWithin(rawURL, w, limit, DownloadTimeout)
}

// DownloadWithin is Download with a deadline the caller chooses.
//
// The deadline covers the whole transfer, body included, so it has to be
// sized against what the caller is asking for rather than against what this
// package usually fetches. Zero means DownloadTimeout.
func DownloadWithin(rawURL string, w io.Writer, limit int64, timeout time.Duration) (int64, error) {
	return DownloadContext(context.Background(), rawURL, w, limit, timeout)
}

// DownloadContext is DownloadWithin that stops when ctx is done.
//
// For a caller that can be interrupted: a command given Ctrl-C part way
// through a half-gigabyte extract should stop reading and let its own
// clean-up run, rather than die with a temporary file on disk. The error
// wraps ctx's, so errors.Is(err, context.Canceled) tells an interruption
// from a failure.
func DownloadContext(ctx context.Context, rawURL string, w io.Writer, limit int64, timeout time.Duration) (int64, error) {
	if timeout <= 0 {
		timeout = DownloadTimeout
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, fmt.Errorf("acquire: %q is not a URL: %w", rawURL, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return 0, fmt.Errorf("acquire: downloading %s: this speaks http and https, not %q",
			redactURL(u), u.Scheme)
	}

	client := &http.Client{Timeout: timeout, CheckRedirect: checkRedirect}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("acquire: preparing to download %s: %w", redactURL(u), err)
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("acquire: downloading %s: %w", redactURL(u), unwrapURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("acquire: downloading %s: the host answered %s", redactURL(u), resp.Status)
	}

	// Checked before reading a byte where the host declared a length, so an
	// oversized file costs one request rather than a limit's worth of
	// transfer. A host that declares nothing is still held to the limit
	// below; this only saves the wasted traffic when it does declare.
	if limit > 0 && resp.ContentLength > limit {
		return 0, fmt.Errorf("acquire: downloading %s: the host offers %d bytes and this accepts at most %d",
			redactURL(u), resp.ContentLength, limit)
	}

	var body io.Reader = resp.Body
	if limit > 0 {
		// One byte past the limit, so that hitting it is distinguishable from
		// a file that happens to be exactly the limit long.
		body = io.LimitReader(resp.Body, limit+1)
	}
	n, err := io.Copy(w, body)
	if err != nil {
		return n, fmt.Errorf("acquire: downloading %s: %w", redactURL(u), err)
	}
	if limit > 0 && n > limit {
		return n, fmt.Errorf("acquire: downloading %s: the response is longer than the %d bytes this accepts",
			redactURL(u), limit)
	}
	return n, nil
}
