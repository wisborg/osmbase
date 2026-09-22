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
	f.Add(frame(TypeData, zlibBlob(primitiveBlock(stringTable("", "name", "boundary", "administrative"), nil,
		append(append(denseNodes([]int64{1, 2}, []int64{0, 1}, []int64{0, 1}, tagRun([][]int32{{1, 2}, nil})),
			way(3, []int64{1, 2}, []int32{2}, []int32{3})...),
			relation(4, []int32{1}, []int64{3}, []int32{1}, []int32{2}, []int32{3})...)))))
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
			// The element decoding, which is where a crafted block reaches the
			// delta arithmetic and the parallel-run length checks. Errors are
			// expected and fine; a panic or a runaway is not.
			_ = pb.EachNode(func(n Node) error {
				pb.Degrees(n.Lat, n.Lon)
				return tagsAreConsistent(t, &pb, n.Tags)
			})
			_ = pb.EachWay(func(w Way) error {
				if len(w.Refs) > MaxWayRefs {
					t.Fatalf("a way came back with %d references, past the %d cap", len(w.Refs), MaxWayRefs)
				}
				return tagsAreConsistent(t, &pb, w.Tags)
			})
			_ = pb.EachRelation(func(r Relation) error {
				for _, m := range r.Members {
					if m.Type != MemberNode && m.Type != MemberWay && m.Type != MemberRelation {
						t.Fatalf("a member came back with type %d", int(m.Type))
					}
				}
				return tagsAreConsistent(t, &pb, r.Tags)
			})

			lat, lon := pb.Degrees(1<<40, -(1 << 40))
			if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) {
				t.Fatalf("Degrees returned %v,%v from granularity %d and offsets %d,%d",
					lat, lon, pb.Granularity, pb.LatOffset, pb.LonOffset)
			}
		}
	})
}

// tagsAreConsistent checks what Tags must hold whatever the file said.
//
// Not "At gives a key that Has finds": both resolve through the same index
// and the same BytesAt, so that can never fail for any input. What can fail
// is the relationship between Tags and the block it points into -- a pair
// count past the cap, or an index that resolved to a string the table does
// not hold.
func tagsAreConsistent(t *testing.T, b *PrimitiveBlock, tags Tags) error {
	if tags.Len() > MaxTags {
		t.Fatalf("an element came back with %d tags, past the %d cap", tags.Len(), MaxTags)
	}
	for i := 0; i < tags.Len() && i < 64; i++ {
		k, v := tags.At(i)
		if !inTable(b, k) || !inTable(b, v) {
			t.Fatalf("tag %d resolved to %q=%q, which the string table does not hold", i, k, v)
		}
	}
	return nil
}

// inTable reports whether s is the empty string -- which every out-of-range
// index resolves to -- or an entry the block actually carries.
func inTable(b *PrimitiveBlock, s string) bool {
	if s == "" {
		return true
	}
	for _, e := range b.Strings {
		if string(e) == s {
			return true
		}
	}
	return false
}
