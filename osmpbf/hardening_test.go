package osmpbf

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"strings"
	"testing"
)

// Every other limit in this package bounds bytes. Entry counts need their own
// bound, because the cheapest string table entry on the wire is two bytes: a
// block of exactly MaxBlockBytes -- which passes every blob check there is --
// can hold sixteen million of them, and the slice headers alone are then
// hundreds of megabytes, delivered by zlib from about 32 KiB on disk.
func TestEntryCountsAreBounded(t *testing.T) {
	t.Run("the string table", func(t *testing.T) {
		var table []byte
		for i := 0; i <= MaxStrings; i++ {
			table = append(table, pbString(1, "")...)
		}
		_, err := DecodePrimitiveBlock(primitiveBlock(table, nil))
		if err == nil {
			t.Fatalf("a string table of %d entries was accepted", MaxStrings+1)
		}
		if !strings.Contains(err.Error(), "string table") {
			t.Errorf("DecodePrimitiveBlock: %v, want an error naming the string table", err)
		}
	})

	t.Run("the groups", func(t *testing.T) {
		block := pbBytes(1, stringTable(""))
		for i := 0; i <= MaxGroups; i++ {
			block = append(block, pbBytes(2, nil)...)
		}
		_, err := DecodePrimitiveBlock(block)
		if err == nil {
			t.Fatalf("a block of %d groups was accepted", MaxGroups+1)
		}
		if !strings.Contains(err.Error(), "groups") {
			t.Errorf("DecodePrimitiveBlock: %v, want an error naming the groups", err)
		}
	})
}

// The counts a real file uses must stay well inside the caps. A bound that
// refused ordinary extracts would be found only by someone's failed download.
func TestEntryCountsAllowRealisticBlocks(t *testing.T) {
	var table []byte
	for i := 0; i < 64_000; i++ { // a large but ordinary string table
		table = append(table, pbString(1, "name")...)
	}
	block := pbBytes(1, table)
	for i := 0; i < 8; i++ {
		block = append(block, pbBytes(2, []byte("group"))...)
	}
	b, err := DecodePrimitiveBlock(block)
	if err != nil {
		t.Fatalf("an ordinary block was refused: %v", err)
	}
	if len(b.Strings) != 64_000 || len(b.Groups) != 8 {
		t.Errorf("decoded %d strings and %d groups, want 64000 and 8", len(b.Strings), len(b.Groups))
	}
}

// A zero-length raw field is not the same as an absent one. Inferred from
// raw != nil, an empty raw alongside real compressed bytes returns no bytes
// and no error -- an empty block, which reads downstream as a file that
// simply contains nothing. That is the plausible wrong answer this package
// refuses one field over, for an unreadable codec.
func TestAnEmptyRawFieldDoesNotHideCompressedBytes(t *testing.T) {
	blob := pbBytes(1, nil) // raw, zero length
	blob = append(blob, pbVarint(2, 7)...)
	blob = append(blob, pbBytes(3, deflate([]byte("payload")))...)

	d := NewReader(bytes.NewReader(frame(TypeData, blob)))
	b, err := d.Next()
	if err == nil {
		t.Fatalf("a blob holding both raw and compressed bytes was accepted, returning %d bytes", len(b.Data))
	}
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("Next: %v, want an error saying both were set", err)
	}
}

// A size that does not fit in int32 must be an error, not a narrowing. The
// sharp case is raw_size: int32(2^32) is 0, which this code otherwise reads
// as "the blob declared no size" and inflates up to the full limit.
func TestOversizedDeclarationsAreNotNarrowed(t *testing.T) {
	const over = uint64(1) << 32

	t.Run("a blob size", func(t *testing.T) {
		header := append(pbString(1, TypeData), pbVarint(3, over+100)...)
		file := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
		file = append(file, header...)
		file = append(file, bytes.Repeat([]byte{0}, 200)...)

		d := NewReader(bytes.NewReader(file))
		if _, err := d.Next(); err == nil || !strings.Contains(err.Error(), "fewer than") {
			t.Fatalf("Next: %v, want a refusal; narrowed, this reads 100 bytes and desynchronises the framing", err)
		}
	})

	t.Run("an inflated size", func(t *testing.T) {
		blob := append(pbVarint(2, over), pbBytes(3, deflate(bytes.Repeat([]byte("x"), 4096)))...)
		d := NewReader(bytes.NewReader(frame(TypeData, blob)))
		if _, err := d.Next(); err == nil || !strings.Contains(err.Error(), "fewer than") {
			t.Fatalf("Next: %v, want a refusal; narrowed, this becomes 0 and means \"absent\"", err)
		}
	})
}

// datasize is required. Absent it reads as zero and surfaces two functions
// later as "a blob holds neither raw nor compressed bytes" -- an accurate
// sentence pointing at entirely the wrong field.
func TestABlobHeaderMustDeclareItsSize(t *testing.T) {
	header := pbString(1, TypeData)
	file := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	file = append(file, header...)

	d := NewReader(bytes.NewReader(file))
	_, err := d.Next()
	if err == nil || !strings.Contains(err.Error(), "declares no size") {
		t.Fatalf("Next: %v, want an error naming the missing size", err)
	}
}

// When a blob declared no size, the message must not report the declaration
// it did not make. "past the 0 bytes it declared" sends whoever is debugging
// a real extract to look at a field that is not there.
func TestTheLimitMessageNamesWhichLimitApplied(t *testing.T) {
	payload := bytes.Repeat([]byte{0}, MaxBlockBytes+1)

	t.Run("declared", func(t *testing.T) {
		d := NewReader(bytes.NewReader(frame(TypeData, zlibBlobDeclaring(payload, 64))))
		_, err := d.Next()
		if err == nil || !strings.Contains(err.Error(), "the 64 bytes it declared") {
			t.Fatalf("Next: %v, want the declared size named", err)
		}
	})

	t.Run("undeclared", func(t *testing.T) {
		d := NewReader(bytes.NewReader(frame(TypeData, pbBytes(3, deflate(payload)))))
		_, err := d.Next()
		if err == nil {
			t.Fatal("a blob past the reader's limit was accepted")
		}
		if strings.Contains(err.Error(), "0 bytes it declared") {
			t.Errorf("Next: %v, which claims the file declared a size of zero", err)
		}
		if !strings.Contains(err.Error(), "declaring no size") {
			t.Errorf("Next: %v, want the reader's own limit named", err)
		}
	})
}

// The buffer must grow to what arrives, not to what the file claimed. A
// fifteen-byte file declaring a 32 MiB blob should cost fifteen bytes and an
// error, which is the property this package states about its limits.
func TestReadDoesNotAllocateTheDeclaredSize(t *testing.T) {
	header := append(pbString(1, TypeData), pbVarint(3, MaxBlobBytes)...)
	file := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	file = append(file, header...)
	file = append(file, []byte("short")...)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	d := NewReader(bytes.NewReader(file))
	if _, err := d.Next(); err == nil {
		t.Fatal("a truncated blob read as a whole one")
	}
	runtime.ReadMemStats(&after)

	grew := after.TotalAlloc - before.TotalAlloc
	if grew > 1<<20 {
		t.Errorf("reading a truncated blob allocated %d bytes; the file declared %d and delivered 5",
			grew, MaxBlobBytes)
	}
}

// Granularity has an upper bound as well as a lower one. Degrees multiplies
// it by a coordinate in int64 and Go wraps silently, so an unbounded one
// turns a crafted block into confident coordinates in the wrong ocean.
func TestGranularityHasAnUpperBound(t *testing.T) {
	data := primitiveBlock(stringTable(""), pbVarint(17, MaxGranularity+1))
	if _, err := DecodePrimitiveBlock(data); err == nil {
		t.Error("a granularity larger than a whole degree per unit was accepted")
	}
	data = primitiveBlock(stringTable(""), pbVarint(17, MaxGranularity))
	if _, err := DecodePrimitiveBlock(data); err != nil {
		t.Errorf("a granularity of exactly the bound was refused: %v", err)
	}
}

// Index 0 means "no string" in this format, and the code must enforce that
// rather than trust the file to have put "" there. A caller resolving an
// unset tag key would otherwise pick up whatever string the file chose.
func TestIndexZeroIsAlwaysEmpty(t *testing.T) {
	// A table that does NOT begin with the empty string, which is what a
	// hostile or merely sloppy producer writes.
	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable("boundary", "name"), nil))
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	if got := b.StringAt(0); got != "" {
		t.Errorf("StringAt(0) = %q, want the empty string whatever the file put there", got)
	}
	if got := b.BytesAt(0); got != nil {
		t.Errorf("BytesAt(0) = %q, want nil", got)
	}
	if got := b.StringAt(1); got != "name" {
		t.Errorf("StringAt(1) = %q, want %q; the guard must not shift the table", got, "name")
	}
}

// BytesAt is the allocation-free path the element passes use, so it must
// share StringAt's guard and alias the buffer rather than copy it.
func TestBytesAtSharesTheGuardAndTheBuffer(t *testing.T) {
	data := primitiveBlock(stringTable("", "boundary"), nil)
	b, err := DecodePrimitiveBlock(data)
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	for _, i := range []int{-1, 0, 2, 1 << 20} {
		if got := b.BytesAt(i); got != nil {
			t.Errorf("BytesAt(%d) = %q, want nil", i, got)
		}
	}
	got := b.BytesAt(1)
	if !bytes.Equal(got, []byte("boundary")) {
		t.Fatalf("BytesAt(1) = %q, want %q", got, "boundary")
	}
	// Aliasing is the point: a copy here would put an allocation back on the
	// path this exists to keep clear of them.
	if &got[0] != &data[len(data)-len("boundary")] {
		t.Error("BytesAt copied; it is meant to point into the reader's buffer")
	}
}
