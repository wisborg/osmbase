package osmpbf

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"
)

// A data block NextData is told to pass over is not inflated. Proved with a
// blob that could not be: its "zlib" bytes are not zlib at all, so reading it
// through the inflater fails, and passing over it must not.
func TestNextDataPassesOverABlockWithoutInflatingIt(t *testing.T) {
	good := primitiveBlock(stringTable(""), nil)
	notZlib := pbBytes(3, []byte("this is not a zlib stream"))
	file := slices.Concat(
		frame(TypeHeader, rawBlob(header([]string{"OsmSchema-V0.6"}, nil))),
		frame(TypeData, notZlib),
		frame(TypeData, rawBlob(good)),
	)

	d := NewReader(bytes.NewReader(file))
	b, i, err := d.NextData(func(i int) bool { return i != 0 })
	if err != nil {
		t.Fatalf("passing over the unreadable block: %v", err)
	}
	if i != 1 || !bytes.Equal(b.Data, good) {
		t.Errorf("got block %d, want block 1 and its bytes", i)
	}
	if _, _, err := d.NextData(func(int) bool { return true }); !errors.Is(err, io.EOF) {
		t.Errorf("after the last block: %v, want io.EOF", err)
	}

	// The same file read through Next does inflate it, and fails -- which is
	// what makes the passing-over above a proof rather than a coincidence.
	d = NewReader(bytes.NewReader(file))
	d.Next() // the header
	if _, err := d.Next(); err == nil {
		t.Fatal("precondition: the unreadable block inflated after all, so this proves nothing")
	}
}

// Passing over every data block still checks the header: a file requiring a
// feature this does not implement is refused whatever the caller wanted.
func TestNextDataStillChecksTheHeader(t *testing.T) {
	file := slices.Concat(
		frame(TypeHeader, rawBlob(header([]string{"OsmSchema-V0.6", "HistoricalInformation"}, nil))),
		frame(TypeData, rawBlob(primitiveBlock(stringTable(""), nil))),
	)
	_, _, err := NewReader(bytes.NewReader(file)).NextData(func(int) bool { return false })
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("HistoricalInformation")) {
		t.Errorf("got %v, want the file refused for its required feature", err)
	}
}

// Indices name the same block on every read of a file, which is what lets a
// first pass record what each block holds and a later one skip by it.
func TestNextDataIndicesAreStableAcrossReads(t *testing.T) {
	var blocks [][]byte
	for i := range 4 {
		blocks = append(blocks, frame(TypeData, rawBlob(primitiveBlock(stringTable("", string(rune('a'+i))), nil))))
	}
	file := slices.Concat(slices.Concat(blocks...), nil)
	read := func(keep func(int) bool) map[int]string {
		got := map[int]string{}
		d := NewReader(bytes.NewReader(file))
		for {
			b, i, err := d.NextData(keep)
			if errors.Is(err, io.EOF) {
				return got
			}
			if err != nil {
				t.Fatal(err)
			}
			got[i] = string(b.Data)
		}
	}
	all := read(func(int) bool { return true })
	odd := read(func(i int) bool { return i%2 == 1 })
	if len(all) != 4 || len(odd) != 2 {
		t.Fatalf("read %d and %d blocks, want 4 and 2", len(all), len(odd))
	}
	for i, data := range odd {
		if all[i] != data {
			t.Errorf("block %d differs between reads", i)
		}
	}
}

// A file cut short inside a block being passed over is an error, not a clean
// end: io.EOF there would read as "the file ended" and lose the rest.
func TestNextDataReportsAFileCutShortWhilePassingOver(t *testing.T) {
	file := frame(TypeData, rawBlob(primitiveBlock(stringTable(""), nil)))
	_, _, err := NewReader(bytes.NewReader(file[:len(file)-3])).NextData(func(int) bool { return false })
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

// Holds reads a block's contents from its groups' field numbers.
func TestHoldsNamesTheKindsInABlock(t *testing.T) {
	st := stringTable("")
	for _, tc := range []struct {
		name   string
		groups [][]byte
		want   Kinds
	}{
		{"dense nodes", [][]byte{pbBytes(2, denseNodes([]int64{1}, []int64{0}, []int64{0}, nil))}, HoldsNodes},
		{"plain nodes", [][]byte{pbBytes(1, plainNode(1, 0, 0, nil, nil))}, HoldsNodes},
		{"ways", [][]byte{pbBytes(3, way(1, []int64{1, 2}, nil, nil))}, HoldsWays},
		{"relations", [][]byte{pbBytes(4, relation(1, nil, nil, nil, nil, nil))}, HoldsRelations},
		{"a mixed block", [][]byte{
			pbBytes(2, denseNodes([]int64{1}, []int64{0}, []int64{0}, nil)),
			pbBytes(3, way(1, []int64{1, 2}, nil, nil)),
		}, HoldsNodes | HoldsWays},
		{"nothing", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pb, err := DecodePrimitiveBlock(primitiveBlock(st, nil, tc.groups...))
			if err != nil {
				t.Fatal(err)
			}
			got, err := pb.Holds()
			if err != nil || got != tc.want {
				t.Errorf("Holds = %b (%v), want %b", got, err, tc.want)
			}
		})
	}
}
