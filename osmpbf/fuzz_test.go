package osmpbf

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// FuzzReader feeds arbitrary bytes to the reader, which is what a downloaded
// extract is: this module's whole input surface is a file fetched from a third
// party over HTTP.
//
// The property is not that any given input decodes. It is that every input
// either decodes or returns an error -- never a panic, and never an
// allocation the input itself chose the size of. Both of those are reachable
// from a length field in the file, which is why the limits are checked here
// rather than trusted.
func FuzzReader(f *testing.F) {
	f.Add(frame(TypeHeader, rawBlob([]byte("header"))))
	f.Add(frame(TypeData, zlibBlob(primitiveBlock(stringTable("", "name"), nil, []byte("group")))))
	f.Add(frame(TypeData, zlibBlobDeclaring(bytes.Repeat([]byte{0}, 1<<16), 8)))
	f.Add(frame("", rawBlob(nil)))
	f.Add([]byte{0, 0, 0, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		d := NewReader(bytes.NewReader(data))
		for i := 0; i < 64; i++ {
			b, err := d.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}
			if len(b.Data) > MaxBlockBytes {
				t.Fatalf("a block of %d bytes came back, past the %d byte limit", len(b.Data), MaxBlockBytes)
			}
			if b.Type == TypeData {
				// Errors are fine; a panic or a runaway allocation is not.
				if pb, err := DecodePrimitiveBlock(b.Data); err == nil {
					for i := range pb.Strings {
						_ = pb.String(i)
					}
					pb.Degrees(0, 0)
				}
			}
		}
	})
}
