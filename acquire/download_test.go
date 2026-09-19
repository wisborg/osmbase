package acquire_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/acquire"
)

// TestDownload_RefusesAResponseLongerThanTheLimit is the bound the first
// version of the boundary fetch did not have.
//
// It did its own http.Get with a plain io.Copy, so a misbehaving or
// compromised host could write to the user's disk for as long as the timeout
// allowed. Both halves are asserted: a declared length over the limit is
// refused before a byte is read, and a host that declares nothing is still
// held to it.
func TestDownload_RefusesAResponseLongerThanTheLimit(t *testing.T) {
	body := strings.Repeat("x", 4096)

	t.Run("a declared length over the limit costs no transfer at all", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			io.WriteString(w, body)
		}))
		defer srv.Close()

		var sink strings.Builder
		if _, err := acquire.Download(srv.URL, &sink, 100); err == nil {
			t.Fatal("a response declaring a length over the limit was accepted")
		}
		// Nothing at all, because the declaration is checked before a byte is
		// read. That is the whole value of checking it: an oversized file
		// costs one request rather than a limit's worth of traffic.
		if sink.Len() != 0 {
			t.Errorf("%d bytes were transferred for a response whose own header said it was too big", sink.Len())
		}
	})

	t.Run("an undeclared length over the limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Chunked, so there is no Content-Length to check up front.
			w.Header().Set("Transfer-Encoding", "chunked")
			for range 8 {
				io.WriteString(w, body)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}))
		defer srv.Close()

		var sink strings.Builder
		if _, err := acquire.Download(srv.URL, &sink, 1000); err == nil {
			t.Fatal("an undeclared oversized response was accepted; the limit must not depend on the host being honest")
		}
		// Bounded rather than zero, and the bound is what matters: a host
		// that declares nothing has to be read before its size is known, so
		// the reader stops one byte past the limit. The caller's writer is a
		// temporary file that is then discarded, so one byte of overshoot
		// costs nothing and a missing bound would cost the disk.
		if sink.Len() > 1001 {
			t.Errorf("%d bytes were written for a 1000 byte limit; the overshoot is meant to be one byte", sink.Len())
		}
	})

	t.Run("a response within the limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "small")
		}))
		defer srv.Close()

		var sink strings.Builder
		n, err := acquire.Download(srv.URL, &sink, 1000)
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if n != 5 || sink.String() != "small" {
			t.Errorf("got %d bytes %q, want 5 %q", n, sink.String(), "small")
		}
	})
}

// TestDownload_IdentifiesItselfAndRefusesAHostChange covers the two things
// this package centralises that a bare http.Get gives up.
//
// The User-Agent is how a host knows who is calling -- it carries a contact
// address for exactly that reason -- and the redirect policy is what stops a
// hostile or compromised host bouncing a request somewhere else.
func TestDownload_IdentifiesItselfAndRefusesAHostChange(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	if _, err := acquire.Download(srv.URL, io.Discard, 0); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if seen != acquire.UserAgent {
		t.Errorf("User-Agent = %q, want %q", seen, acquire.UserAgent)
	}

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "somewhere else entirely")
	}))
	defer elsewhere.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer redirector.Close()

	var sink strings.Builder
	if _, err := acquire.Download(redirector.URL, &sink, 0); err == nil {
		t.Errorf("a redirect to another host was followed, writing %q", sink.String())
	}
}

// TestDownload_RefusesASchemeItDoesNotSpeak keeps a file:// or similar URL
// from being handed to the HTTP client at all.
func TestDownload_RefusesASchemeItDoesNotSpeak(t *testing.T) {
	if _, err := acquire.Download("file:///etc/passwd", io.Discard, 0); err == nil {
		t.Fatal("a file:// URL was accepted")
	}
}
