package osmpbf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

// readAll walks every block, copying each one's Data, because the reader
// reuses its buffer and a test that kept the slices would be comparing the
// last block against itself.
func readAll(t *testing.T, file []byte) []Block {
	t.Helper()
	d := NewReader(bytes.NewReader(file))
	var out []Block
	for {
		b, err := d.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, Block{Type: b.Type, Data: bytes.Clone(b.Data)})
	}
}

func TestReaderWalksEveryBlock(t *testing.T) {
	file := append(
		frame(TypeHeader, rawBlob([]byte("header payload"))),
		frame(TypeData, zlibBlob([]byte("data payload")))...,
	)
	file = append(file, frame(TypeData, rawBlob([]byte("second data")))...)

	blocks := readAll(t, file)

	want := []Block{
		{TypeHeader, []byte("header payload")},
		{TypeData, []byte("data payload")},
		{TypeData, []byte("second data")},
	}
	if len(blocks) != len(want) {
		t.Fatalf("read %d blocks, want %d", len(blocks), len(want))
	}
	for i, w := range want {
		if blocks[i].Type != w.Type || !bytes.Equal(blocks[i].Data, w.Data) {
			t.Errorf("block %d = %s %q, want %s %q",
				i, blocks[i].Type, blocks[i].Data, w.Type, w.Data)
		}
	}
}

// A compressed blob and an uncompressed one must produce the same bytes.
// Stated on its own because the two paths do not share a line of code, and a
// reader that inflated correctly but returned the compressed bytes for a raw
// blob would still pass a test that only ever used one of them.
func TestRawAndCompressedAgree(t *testing.T) {
	payload := bytes.Repeat([]byte("boundary=administrative;"), 400)

	raw := readAll(t, frame(TypeData, rawBlob(payload)))
	zipped := readAll(t, frame(TypeData, zlibBlob(payload)))

	if len(raw) != 1 || len(zipped) != 1 {
		t.Fatalf("read %d raw and %d compressed blocks, want one each", len(raw), len(zipped))
	}
	if !bytes.Equal(raw[0].Data, payload) {
		t.Errorf("the raw blob read back %d bytes, want the %d written", len(raw[0].Data), len(payload))
	}
	if !bytes.Equal(zipped[0].Data, payload) {
		t.Errorf("the compressed blob read back %d bytes, want the %d written", len(zipped[0].Data), len(payload))
	}
}

// An unfamiliar block type is data, not a failure: the format says a reader
// must skip block types it does not know, which is how a file written by a
// newer producer still reads.
func TestUnknownBlockTypePassesThrough(t *testing.T) {
	file := append(
		frame("OSMSomethingNew", rawBlob([]byte("future"))),
		frame(TypeData, rawBlob([]byte("data")))...,
	)
	blocks := readAll(t, file)
	if len(blocks) != 2 {
		t.Fatalf("read %d blocks, want 2; an unknown type must not end the file", len(blocks))
	}
	if blocks[0].Type != "OSMSomethingNew" {
		t.Errorf("block type %q, want the file's own spelling passed through", blocks[0].Type)
	}
}

// The end of the file and a file cut short are different answers, and only
// the first is io.EOF. A reader that returned io.EOF for both would report a
// truncated download as a complete file with fewer boundaries in it.
func TestEndOfFileIsNotTruncation(t *testing.T) {
	whole := frame(TypeData, rawBlob([]byte("payload")))

	t.Run("clean end", func(t *testing.T) {
		d := NewReader(bytes.NewReader(whole))
		if _, err := d.Next(); err != nil {
			t.Fatalf("first block: %v", err)
		}
		if _, err := d.Next(); !errors.Is(err, io.EOF) {
			t.Errorf("at the end of the file: %v, want io.EOF", err)
		}
	})

	for _, cut := range []struct {
		name string
		at   int
	}{
		{"part way through the length prefix", 2},
		{"part way through the blob header", 6},
		{"part way through the blob", len(whole) - 3},
	} {
		t.Run(cut.name, func(t *testing.T) {
			d := NewReader(bytes.NewReader(whole[:cut.at]))
			_, err := d.Next()
			if err == nil {
				t.Fatal("a truncated file read as a whole one")
			}
			if errors.Is(err, io.EOF) {
				t.Errorf("a truncated file reported io.EOF, which means a clean end: %v", err)
			}
		})
	}
}

// The declared sizes decide how much this allocates, so each is checked
// against the format's own limit before it is believed.
func TestDeclaredSizesAreRefusedAtTheLimit(t *testing.T) {
	t.Run("an oversized blob header", func(t *testing.T) {
		// Only the four-byte length, so nothing after it has to exist: if the
		// limit is enforced, the reader never reads that far.
		file := binary.BigEndian.AppendUint32(nil, MaxHeaderBytes+1)
		d := NewReader(bytes.NewReader(file))
		_, err := d.Next()
		if err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("Next: %v, want a refusal naming the limit", err)
		}
	})

	t.Run("an oversized blob", func(t *testing.T) {
		header := append(pbString(1, TypeData), pbVarint(3, MaxBlobBytes+1)...)
		file := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
		file = append(file, header...)
		d := NewReader(bytes.NewReader(file))
		_, err := d.Next()
		if err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("Next: %v, want a refusal naming the limit", err)
		}
	})

	t.Run("an oversized inflated size", func(t *testing.T) {
		blob := append(pbVarint(2, MaxBlockBytes+1), pbBytes(3, deflate([]byte("small")))...)
		d := NewReader(bytes.NewReader(frame(TypeData, blob)))
		_, err := d.Next()
		if err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("Next: %v, want a refusal naming the limit", err)
		}
	})
}

// A blob that inflates far past what it declared is the shape a zip bomb
// takes, and this reads files downloaded from third parties. The declared
// size has to act as a limit, not as an allocation hint that is then ignored.
func TestABlobCannotInflatePastWhatItDeclared(t *testing.T) {
	bomb := bytes.Repeat([]byte{0}, 4<<20)
	blob := zlibBlobDeclaring(bomb, 64)

	if len(blob) > 64<<10 {
		t.Fatalf("the fixture is %d bytes compressed; it is meant to be small", len(blob))
	}

	d := NewReader(bytes.NewReader(frame(TypeData, blob)))
	_, err := d.Next()
	if err == nil {
		t.Fatal("a blob inflated 65536 times past its declared size and was accepted")
	}
	if !strings.Contains(err.Error(), "declared") {
		t.Errorf("Next: %v, want an error about the declared size", err)
	}
}

// Exactly the declared size is a legal block, not a bomb. Paired with the
// test above because the limit is checked one byte past the declaration, and
// an off-by-one there would reject every well-formed file in the world.
func TestABlobMayBeExactlyItsDeclaredSize(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1000)
	blocks := readAll(t, frame(TypeData, zlibBlobDeclaring(payload, len(payload))))
	if len(blocks) != 1 || len(blocks[0].Data) != len(payload) {
		t.Fatalf("a blob of exactly its declared size did not read back whole")
	}
}

// A blob omitting raw_size still reads. Real producers set it, but the field
// is optional, and the fallback limit must not be mistaken for a declaration
// of zero -- which would refuse every byte.
func TestACompressedBlobNeedNotDeclareItsSize(t *testing.T) {
	payload := []byte("no raw_size here")
	blocks := readAll(t, frame(TypeData, pbBytes(3, deflate(payload))))
	if len(blocks) != 1 || !bytes.Equal(blocks[0].Data, payload) {
		t.Fatalf("a blob without raw_size read back %v, want the payload", blocks)
	}
}

func TestMalformedBlobsAreNamed(t *testing.T) {
	for _, tc := range []struct {
		name string
		file []byte
		want string
	}{
		{
			name: "a blob header naming no type",
			file: frame("", rawBlob([]byte("payload")))[:],
			want: "not a PBF file",
		},
		{
			name: "a blob holding neither raw nor compressed bytes",
			file: frame(TypeData, pbVarint(2, 10)),
			want: "neither raw nor compressed",
		},
		{
			name: "compressed bytes that are not zlib",
			file: frame(TypeData, append(pbVarint(2, 8), pbBytes(3, []byte("not zlib"))...)),
			want: "not zlib",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewReader(bytes.NewReader(tc.file))
			_, err := d.Next()
			if err == nil {
				t.Fatal("malformed bytes read as a valid block")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Next: %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// A codec this does not inflate is reported as such, by name. Skipping the
// field instead would hand back an empty block, which reads downstream as a
// file that simply contains nothing.
func TestUnsupportedCompressionIsNamed(t *testing.T) {
	for field, codec := range map[int]string{4: "lzma", 5: "bzip2", 6: "lz4", 7: "zstd"} {
		d := NewReader(bytes.NewReader(frame(TypeData, pbBytes(field, []byte("compressed")))))
		_, err := d.Next()
		if !errors.Is(err, ErrUnsupportedCompression) {
			t.Errorf("a %s blob (field %d): %v, want ErrUnsupportedCompression", codec, field, err)
		}
	}
}

// The blob header is protobuf, so a field this does not read must be skipped
// rather than treated as the end of the message. indexdata is the one real
// files carry.
func TestUnknownBlobHeaderFieldsAreSkipped(t *testing.T) {
	blob := rawBlob([]byte("payload"))
	header := pbString(1, TypeData)
	header = append(header, pbBytes(2, []byte("indexdata"))...) // optional, unused
	header = append(header, pbVarint(9, 12345)...)              // a field that does not exist
	header = append(header, pbVarint(3, uint64(len(blob)))...)

	file := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	file = append(append(file, header...), blob...)

	blocks := readAll(t, file)
	if len(blocks) != 1 || !bytes.Equal(blocks[0].Data, []byte("payload")) {
		t.Fatalf("read %v, want one block of the payload", blocks)
	}
}
