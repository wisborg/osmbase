package osm

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmpbf"
)

// blobRanges is where each block's blob sits in a PBF file, in file order,
// header block included.
func blobRanges(t *testing.T, file []byte) [][2]int {
	t.Helper()
	var out [][2]int
	for p := 0; p < len(file); {
		hl := int(binary.BigEndian.Uint32(file[p:]))
		h := file[p+4 : p+4+hl]
		size := -1
		for q := 0; q < len(h); {
			tag, n := binary.Uvarint(h[q:])
			q += n
			switch tag & 7 {
			case 0:
				v, n := binary.Uvarint(h[q:])
				q += n
				if tag>>3 == 3 {
					size = int(v)
				}
			case 2:
				l, n := binary.Uvarint(h[q:])
				q += n + int(l)
			default:
				t.Fatalf("unexpected wire type in a blob header")
			}
		}
		start := p + 4 + hl
		out = append(out, [2]int{start, start + size})
		p = start + size
	}
	return out
}

// corrupting returns a copy of file with the given blocks' blobs overwritten
// by bytes that cannot be decoded, at the same length, so the framing -- and
// every other block -- is untouched.
func corrupting(t *testing.T, file []byte, blocks ...int) []byte {
	t.Helper()
	out := bytes.Clone(file)
	r := blobRanges(t, file)
	for _, b := range blocks {
		for i := r[b][0]; i < r[b][1]; i++ {
			out[i] = 0xff
		}
	}
	return out
}

// The way pass reads only the blocks holding ways, and never inflates or
// decodes the rest. Proved by handing it a file whose node and relation
// blocks are corrupt -- the first pass read the intact file and knows where
// the ways are -- which it could not survive if it read them.
func TestTheLaterPassesReadOnlyTheBlocksTheyNeed(t *testing.T) {
	file := square(t)
	if n := len(blobRanges(t, file)); n != 4 {
		t.Fatalf("precondition: the fixture is %d blocks, want header, nodes, ways, relations", n)
	}
	const nodes, ways, relations = 1, 2, 3

	found, wantedWays, kinds, err := readRelations(from(file), Options{Limits: DefaultLimits()})
	if err != nil || len(found) == 0 {
		t.Fatalf("pass 1: %v (%d found)", err, len(found))
	}
	if want := (blockKinds{osmpbf.HoldsNodes, osmpbf.HoldsWays, osmpbf.HoldsRelations}); !equalKinds(kinds, want) {
		t.Fatalf("pass 1 recorded %v, want %v", kinds, want)
	}

	// Precondition: a corrupt block is fatal to anything that reads it.
	if _, err := Read(from(corrupting(t, file, nodes)), Options{}); err == nil {
		t.Fatal("precondition: a corrupt node block was read without complaint, so this proves nothing")
	}

	wayNodes, wantedNodes, err := readWays(from(corrupting(t, file, nodes, relations)), wantedWays, kinds, DefaultLimits())
	if err != nil {
		t.Fatalf("the way pass read a block it did not need: %v", err)
	}
	if _, err := readNodes(from(corrupting(t, file, ways, relations)), wantedNodes, kinds); err != nil {
		t.Fatalf("the node pass read a block it did not need: %v", err)
	}
	if len(wayNodes) == 0 {
		t.Error("the way pass found no ways")
	}
}

// The map describes a file as the first pass read it. A later pass that finds
// the file shorter is reading a different one, and says so rather than skip
// by a map of something else.
func TestAPassNoticesTheExtractChangedUnderIt(t *testing.T) {
	file := square(t)
	_, wantedWays, kinds, err := readRelations(from(file), Options{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	r := blobRanges(t, file)
	shorter := file[:r[1][1]] // the header and the node block only
	_, _, err = readWays(from(shorter), wantedWays, kinds, DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "changed between passes") {
		t.Errorf("got %v, want the change named", err)
	}
}

func equalKinds(a, b blockKinds) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
