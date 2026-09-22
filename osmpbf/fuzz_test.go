package osmpbf

import (
	"bytes"
	"math"
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
	f.Add(frame(TypeHeader, rawBlob(header([]string{"OsmSchema-V0.6"}, nil))))
	f.Add(frame(TypeData, zlibBlob(primitiveBlock(stringTable("", "name"), nil, []byte("group")))))
	f.Add(frame(TypeData, zlibBlobDeclaring(bytes.Repeat([]byte{0}, 1<<16), 8)))
	f.Add(frame("", rawBlob(nil)))
	f.Add([]byte{0, 0, 0, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		d := NewReader(bytes.NewReader(data))
		for i := 0; i < 64; i++ {
			b, err := d.Next()
			if err != nil {
				// Either answer ends this input. Which one it is is a
				// question for the tests that build a specific file; from
				// arbitrary bytes both are correct.
				return
			}
			if len(b.Data) > MaxBlockBytes {
				t.Fatalf("a block of %d bytes came back, past the %d byte limit", len(b.Data), MaxBlockBytes)
			}
			if b.Type != TypeData {
				continue
			}
			// Errors are fine; a panic or a runaway allocation is not.
			pb, err := DecodePrimitiveBlock(b.Data)
			if err != nil {
				continue
			}
			// A block that decoded must carry a granularity coordinates can
			// be scaled by. Stated here because that refusal is what stops a
			// malformed extract from stacking a whole municipality on one
			// point, and arbitrary bytes are where a path around it shows.
			if pb.Granularity <= 0 {
				t.Fatalf("a block decoded with a granularity of %d", pb.Granularity)
			}
			// Nothing may come back larger than what went in: the strings and
			// the groups point into the block, so their total cannot exceed
			// it. A decode that fabricated or repeated bytes -- the shape a
			// length field believed over the bytes present takes -- fails here.
			var total int
			for _, s := range pb.Strings {
				total += len(s)
			}
			for _, g := range pb.Groups {
				total += len(g)
			}
			if total > len(b.Data) {
				t.Fatalf("a %d byte block yielded %d bytes of strings and groups", len(b.Data), total)
			}
			for i := -1; i <= len(pb.Strings); i++ {
				if s := pb.StringAt(i); (i < 0 || i >= len(pb.Strings)) && s != "" {
					t.Fatalf("StringAt(%d) = %q with %d entries, want the empty string", i, s, len(pb.Strings))
				}
			}
			lat, lon := pb.Degrees(1<<40, -(1 << 40))
			if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) {
				t.Fatalf("Degrees returned %v,%v from granularity %d and offsets %d,%d",
					lat, lon, pb.Granularity, pb.LatOffset, pb.LonOffset)
			}
		}
	})
}
