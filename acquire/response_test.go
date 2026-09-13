package acquire_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
)

// serveRange starts a server that answers every request with a 206, the
// Content-Range the test asks for, and body bytes of the test's choosing.
//
// It is hand-rolled rather than http.ServeContent because the whole point is
// to send responses a correct server would not: that is what the reader has to
// survive, and a correct server cannot be talked into producing one.
func serveRange(t *testing.T, header string, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header != "" {
			w.Header().Set("Content-Range", header)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/archive.pmtiles"
}

// TestRangeReader_RefusesBytesThatAreNotTheBytesAskedFor is the check that
// stops a server handing back a part of the archive nobody asked for.
//
// A 206 says "here is part of the file" and the Content-Range says which part.
// Without comparing the two, ten bytes from offset 0 are accepted as offset
// 5000 and handed to the PMTiles reader as the directory or tile it asked for.
// Nothing downstream can catch it: the format has no checksum anywhere, and a
// directory made of arbitrary bytes usually decodes into plausible entries. It
// surfaces, if ever, as a map with the wrong thing in it.
//
// The end position may come back SHORT, because a range running past the end
// of an archive is answered with what exists. It may not come back long, and
// the start may not move at all.
func TestRangeReader_RefusesBytesThatAreNotTheBytesAskedFor(t *testing.T) {
	const off, size = 5000, 10
	cases := []struct {
		name   string
		header string
		body   []byte
		want   string // a phrase the error must contain; "" means no error
	}{
		{
			name:   "the range asked for",
			header: "bytes 5000-5009/1000000",
			body:   bytes.Repeat([]byte("B"), 10),
		},
		{
			name:   "a short range at the end of the archive",
			header: "bytes 5000-5003/5004",
			body:   bytes.Repeat([]byte("B"), 4),
			// Not an error: this is EOF, checked separately below.
		},
		{
			name:   "the start silently shifted to zero",
			header: "bytes 0-9/1000000",
			body:   bytes.Repeat([]byte("A"), 10),
			want:   "answered with bytes 0 to 9",
		},
		{
			name:   "the start shifted by one",
			header: "bytes 4999-5008/1000000",
			body:   bytes.Repeat([]byte("A"), 10),
			want:   "answered with bytes 4999 to 5008",
		},
		{
			name:   "more bytes than were asked for",
			header: "bytes 5000-5999/1000000",
			body:   bytes.Repeat([]byte("B"), 1000),
			want:   "answered with bytes 5000 to 5999",
		},
		{
			name:   "a range that runs backwards",
			header: "bytes 5009-5000/1000000",
			body:   bytes.Repeat([]byte("B"), 10),
			want:   "runs backwards",
		},
		{
			name:   "no Content-Range at all",
			header: "",
			body:   bytes.Repeat([]byte("A"), 10),
			want:   "no Content-Range header",
		},
		{
			name:   "a Content-Range in some other unit",
			header: "tiles 5000-5009/1000000",
			body:   bytes.Repeat([]byte("B"), 10),
			want:   "not a range of bytes",
		},
		{
			name:   "a Content-Range with no total",
			header: "bytes 5000-5009",
			body:   bytes.Repeat([]byte("B"), 10),
			want:   "names no total length",
		},
		{
			name:   "a Content-Range that is not a range",
			header: "bytes whatever/1000000",
			body:   bytes.Repeat([]byte("B"), 10),
			want:   "not a first-to-last range",
		},
		{
			name:   "a first position that is not a number",
			header: "bytes five-5009/1000000",
			body:   bytes.Repeat([]byte("B"), 10),
			want:   "first byte position is not a number",
		},
		{
			name:   "a total that is not a number",
			header: "bytes 5000-5009/lots",
			body:   bytes.Repeat([]byte("B"), 10),
			want:   "total length is not a number",
		},
		{
			name:   "a body shorter than the server's own Content-Range",
			header: "bytes 5000-5009/1000000",
			body:   bytes.Repeat([]byte("B"), 4),
			want:   "the transfer did not finish",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := acquire.NewRangeReader(serveRange(t, c.header, c.body))
			if err != nil {
				t.Fatalf("NewRangeReader: %v", err)
			}
			got := make([]byte, size)
			n, err := r.ReadAt(got, off)
			if c.want == "" {
				if err != nil && !errors.Is(err, io.EOF) {
					t.Fatalf("ReadAt: %v", err)
				}
				if !bytes.Equal(got[:n], c.body) {
					t.Errorf("read %q, want %q", got[:n], c.body)
				}
				return
			}
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt returned %d bytes and %v; want an error saying %q", n, err, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error is %q; it should say %q", err, c.want)
			}
		})
	}
}

// TestRangeReader_AShortRangeTheServerDeclaresIsEOF separates the two ways a
// response can be shorter than the request.
//
// A server that says it is sending four bytes and sends four has reached the
// end of the archive, which is EOF and is how the PMTiles reader recognises a
// truncated file. A server that says ten and sends four has dropped the
// transfer, which is a failure and must not be reported as the archive being
// short -- that would send whoever reads the message looking at the archive
// instead of at the network.
func TestRangeReader_AShortRangeTheServerDeclaresIsEOF(t *testing.T) {
	r, err := acquire.NewRangeReader(serveRange(t, "bytes 5000-5003/5004", []byte("BBBB")))
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	got := make([]byte, 10)
	n, err := r.ReadAt(got, 5000)
	if !errors.Is(err, io.EOF) {
		t.Errorf("a declared short range gave %v, want io.EOF", err)
	}
	if n != 4 {
		t.Errorf("read %d bytes, want the 4 that exist", n)
	}
	if size, ok := r.Size(); !ok || size != 5004 {
		t.Errorf("Size() = %d, %v; want 5004, true", size, ok)
	}
}

// TestRangeReader_RefusesAnArchiveThatChangesSizeMidRead covers the archive
// being replaced underneath the reader.
//
// PMTiles offsets come out of a directory read earlier in the same session. If
// the file behind the URL is republished -- a daily build under a stable name
// is exactly this -- those offsets now point into a different file, and
// reading them returns whatever happens to live there. The total length is the
// only thing in the protocol that notices.
func TestRangeReader_RefusesAnArchiveThatChangesSizeMidRead(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		total := 1000000
		if requests > 1 {
			total = 900000
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-9/%d", total))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(bytes.Repeat([]byte("B"), 10))
	}))
	defer srv.Close()

	r, err := acquire.NewRangeReader(srv.URL)
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 0); err != nil {
		t.Fatalf("the first read failed: %v", err)
	}
	_, err = r.ReadAt(make([]byte, 10), 0)
	if err == nil {
		t.Fatal("the second read accepted an archive of a different length")
	}
	for _, want := range []string{"1000000", "900000", "stale"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// TestRangeReader_ATotalTheServerDeclinesToGive checks the legal "*" form: the
// range is still verified, and the size is simply not known.
func TestRangeReader_ATotalTheServerDeclinesToGive(t *testing.T) {
	r, err := acquire.NewRangeReader(serveRange(t, "bytes 0-9/*", bytes.Repeat([]byte("B"), 10)))
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if size, ok := r.Size(); ok {
		t.Errorf("Size() = %d, true; the server said nothing about the total", size)
	}
}

// TestRangeReader_FollowsARedirectOnTheSameHost keeps the policy from being
// "refuse everything": a host moving an archive to another path of its own is
// ordinary, and so is an http-to-https upgrade.
func TestRangeReader_FollowsARedirectOnTheSameHost(t *testing.T) {
	body := pattern(1000)
	mux := http.NewServeMux()
	mux.HandleFunc("/moved.pmtiles", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/real.pmtiles", http.StatusFound)
	})
	mux.HandleFunc("/real.pmtiles", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "real.pmtiles", epoch, bytes.NewReader(body))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := acquire.NewRangeReader(srv.URL + "/moved.pmtiles")
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	got := make([]byte, 10)
	if _, err := r.ReadAt(got, 100); err != nil {
		t.Fatalf("ReadAt through a redirect: %v", err)
	}
	if !bytes.Equal(got, body[100:110]) {
		t.Error("the bytes that came back through the redirect are not the ones asked for")
	}
}

// TestRangeReader_RefusesARedirectToAnotherHost is the rule that keeps the
// choice of host with the person who named it.
//
// Go's default follows up to ten redirects to anywhere, carrying the headers
// along, which hands a host the user named the ability to aim this program at
// a host the user has never heard of -- a cloud metadata endpoint, a service
// on localhost. The response would not parse as an archive, so this is not a
// way to steal data; the request is issued all the same, and its timing and
// its errors are observable.
//
// The test asserts what the policy decides, not what Go does: the second
// server must receive NOTHING, and the error must name the host that was
// declined so the user can point at it deliberately if it is the right one.
func TestRangeReader_RefusesARedirectToAnotherHost(t *testing.T) {
	var reached bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte("secrets"))
	}))
	defer internal.Close()

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/internal/secret", http.StatusFound)
	}))
	defer front.Close()

	r, err := acquire.NewRangeReader(front.URL + "/archive.pmtiles")
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	_, err = r.ReadAt(make([]byte, 10), 0)
	if err == nil {
		t.Fatal("ReadAt followed a redirect to another host")
	}
	if reached {
		t.Error("the request reached a host the caller never named")
	}
	host := strings.TrimPrefix(internal.URL, "http://")
	if !strings.Contains(err.Error(), host) {
		t.Errorf("error %q should name the host it declined, %s", err, host)
	}
}

// TestRangeReader_RefusesARedirectLoop caps a host that keeps pointing
// somewhere else on itself. The cap is five rather than Go's ten; either way
// the loop has to end.
func TestRangeReader_RefusesARedirectLoop(t *testing.T) {
	var hops int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, fmt.Sprintf("/hop%d.pmtiles", hops), http.StatusFound)
	}))
	defer srv.Close()

	r, err := acquire.NewRangeReader(srv.URL + "/archive.pmtiles")
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 0); err == nil {
		t.Fatal("ReadAt followed redirects for ever")
	}
	if hops > 8 {
		t.Errorf("the server was asked %d times; the cap should have stopped it well before Go's ten", hops)
	}
}

// TestRangeReader_KeepsCredentialsOutOfEveryMessage covers what reaches a
// terminal, a log or an issue tracker.
//
// A private mirror behind userinfo and a presigned URL with a signature in its
// query are both ordinary ways to name an archive, and both put a secret in
// the string this type formats into every error it returns.
func TestRangeReader_KeepsCredentialsOutOfEveryMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	raw := "http://someuser:hunter2@" + host + "/archive.pmtiles?X-Amz-Signature=abcdef123456"
	r, err := acquire.NewRangeReader(raw)
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	_, err = r.ReadAt(make([]byte, 4), 0)
	if err == nil {
		t.Fatal("ReadAt succeeded against a 403")
	}
	for _, secret := range []string{"hunter2", "someuser", "abcdef123456"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error leaks %q:\n%s", secret, err)
		}
		if strings.Contains(r.URL(), secret) {
			t.Errorf("URL() leaks %q: %s", secret, r.URL())
		}
	}
	// The message still has to identify the archive, and to say that a
	// credential was involved at all.
	for _, want := range []string{host, "redacted", "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should still contain %q", err, want)
		}
	}
}

// TestRangeReader_KeepsCredentialsOutOfATransportError covers the other path
// into an error message: the one where no response ever arrives, and the error
// is Go's own *url.Error, which prints the URL it was built from.
func TestRangeReader_KeepsCredentialsOutOfATransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	host := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing is listening now, so the request cannot connect

	r, err := acquire.NewRangeReader("http://someuser:hunter2@" + host + "/archive.pmtiles")
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	_, err = r.ReadAt(make([]byte, 4), 0)
	if err == nil {
		t.Fatal("ReadAt reached a server that is not running")
	}
	for _, secret := range []string{"hunter2", "someuser"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the transport error leaks %q:\n%s", secret, err)
		}
	}
}
