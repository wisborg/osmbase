package inflate

import (
	"bytes"
	"compress/gzip"
	"errors"
	"runtime"
	"testing"
)

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Exactly the limit is fine and one byte more is refused: the boundary is the
// thing a hand-written copy of this gets wrong.
func TestGzipTheLimitIsInclusive(t *testing.T) {
	const limit = 1000
	if out, err := Gzip(gz(t, make([]byte, limit)), limit); err != nil || len(out) != limit {
		t.Errorf("exactly the limit: %d bytes, %v", len(out), err)
	}
	if _, err := Gzip(gz(t, make([]byte, limit+1)), limit); !errors.Is(err, ErrTooLarge) {
		t.Errorf("one past the limit: %v, want ErrTooLarge", err)
	}
	if _, err := Gzip([]byte("not gzip at all"), limit); !errors.Is(err, ErrNotGzip) {
		t.Errorf("not gzip: %v, want ErrNotGzip", err)
	}
	truncated := gz(t, make([]byte, limit))
	if _, err := Gzip(truncated[:len(truncated)-8], limit); err == nil || errors.Is(err, ErrTooLarge) || errors.Is(err, ErrNotGzip) {
		t.Errorf("a stream cut short: %v, want the decompressor's own error", err)
	}
}

// A bomb is stopped while it expands. Measured, not asserted from the error:
// an error comes back from a version that allocated the whole expansion
// first, and this rule has been got wrong that way in this module before.
// 256 MB of zeros is about 250 KB of gzip.
func TestGzipABombAllocatesAboutItsLimit(t *testing.T) {
	bomb := gz(t, make([]byte, 256<<20))
	const limit = 1 << 20

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := Gzip(bomb, limit)
	runtime.ReadMemStats(&after)

	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a bomb: %v, want ErrTooLarge", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8*limit {
		t.Errorf("refusing a %d-byte bomb allocated %d bytes, want a few times the %d-byte limit", len(bomb), grew, limit)
	}
}

// Into appends, keeps the caller's buffer, and holds no more than limit+1.
func TestIntoIsBoundedInTheCallersBuffer(t *testing.T) {
	var dst bytes.Buffer
	n, err := Into(&dst, bytes.NewReader(make([]byte, 5000)), 100)
	if !errors.Is(err, ErrTooLarge) || dst.Len() > 101 || n > 101 {
		t.Errorf("Into: n %d, len %d, %v; want ErrTooLarge with at most 101 bytes", n, dst.Len(), err)
	}
	dst.Reset()
	if n, err := Into(&dst, bytes.NewReader([]byte("abc")), 3); err != nil || n != 3 || dst.String() != "abc" {
		t.Errorf("Into at the limit: %d %q %v", n, dst.String(), err)
	}
}
