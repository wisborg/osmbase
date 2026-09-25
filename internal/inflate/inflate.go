// Package inflate reads a compressed stream into memory without letting it
// decide how much memory that is.
//
// The rule is one rule, and it was written three times: in pmtiles, in slice
// and in osmpbf, with three error vocabularies, and it had already diverged
// once. A decompressor reaches roughly a thousand to one and nothing on disk
// states the true size, so the limit is enforced WHILE expanding -- read one
// byte past it, and refuse if that byte exists -- never by inspecting the
// result, which has already been allocated by then. That is the part worth
// having once.
//
// What is left to each caller is what it says: errors here are sentinels, and
// a caller matches them with errors.Is and words the refusal in its own terms
// ("this archive's limit", "the size the blob declared").
package inflate

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// ErrTooLarge is a stream that expands past its limit.
var ErrTooLarge = errors.New("inflate: expands past its limit")

// ErrNotGzip is bytes that do not begin a gzip stream.
var ErrNotGzip = errors.New("inflate: not a gzip stream")

// Gzip expands a gzip stream of at most limit bytes.
//
// ErrNotGzip for bytes that are not gzip at all, ErrTooLarge for a stream
// that holds more than limit, and otherwise the decompressor's own error for a
// stream that is gzip and broken part way.
func Gzip(b []byte, limit int64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotGzip, err)
	}
	defer zr.Close()
	return ReadAll(zr, limit)
}

// ReadAll reads r to its end, refusing it with ErrTooLarge if it holds more
// than limit bytes. At most limit+1 bytes are read, whatever r holds.
func ReadAll(r io.Reader, limit int64) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, ErrTooLarge
	}
	return out, nil
}

// Into is ReadAll into a buffer the caller keeps, for a reader of many blocks
// that should not allocate one per block. dst is appended to, and holds at
// most limit+1 bytes of r whatever happens.
func Into(dst *bytes.Buffer, r io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(r, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, ErrTooLarge
	}
	return n, nil
}
