package osmpbf

import (
	"bytes"
	"runtime"
	"testing"
)

// The caps must bound what a file can make this ALLOCATE, not merely what it
// returns. A cap consulted after the whole packed run has been appended has
// already spent the memory it is refusing: measured before this was fixed, a
// 30 KB file reached 877 MB of live heap.
//
// Written as an allocation measurement rather than an assertion that an error
// came back, because the error came back before too. The error is not the
// property; the bound is.
func TestACraftedRunCannotAllocateInProportionToItself(t *testing.T) {
	const runBytes = 8 << 20 // every byte a one-byte varint, so 8M values

	for _, tc := range []struct {
		name  string
		block []byte
	}{
		{"a way's node references", pbBytes(3, append(pbVarint(1, 1), pbBytes(8, bytes.Repeat([]byte{0}, runBytes))...))},
		{"a dense latitude column", pbBytes(2, pbBytes(8, bytes.Repeat([]byte{0}, runBytes)))},
		{"a dense tag run", pbBytes(2, pbBytes(10, bytes.Repeat([]byte{0}, runBytes)))},
		{"an element's tag keys", pbBytes(3, append(pbVarint(1, 1), pbBytes(2, bytes.Repeat([]byte{0}, runBytes))...))},
		{"a relation's member ids", pbBytes(4, append(pbVarint(1, 1), pbBytes(9, bytes.Repeat([]byte{0}, runBytes))...))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(pbBytes(1, stringTable("", "k")), pbBytes(2, tc.block)...)
			b, err := DecodePrimitiveBlock(data)
			if err != nil {
				return // refused before any of it was decoded, which is finer still
			}

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			_ = b.EachNode(func(Node) error { return nil })
			_ = b.EachWay(func(Way) error { return nil })
			_ = b.EachRelation(func(Relation) error { return nil })

			runtime.ReadMemStats(&after)
			grew := after.TotalAlloc - before.TotalAlloc

			// Generous: the caps allow a few million values across the runs,
			// and append's doubling means a transient of roughly twice the
			// final slice. What this catches is growth in proportion to the
			// 8 MB run, which was hundreds of megabytes.
			if grew > 24<<20 {
				t.Errorf("walking a block holding an %d byte run allocated %d bytes", runBytes, grew)
			}
		})
	}
}
