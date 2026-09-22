package protobuf

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"testing"
)

// This package had no tests of its own until it acquired a second caller.
// That was survivable while the vector tile decoder was the only one --
// its suite exercised every path -- and is not now: the OSM decoder uses
// accessors the tile decoder never calls, and an error's attribution is
// something neither caller's tests can see.

// testMax is a cap high enough not to interfere with tests that are about
// something else. The cap's own behaviour is tested separately.
const testMax = 1 << 20

func tag(field, wire int) []byte { return binary.AppendUvarint(nil, uint64(field)<<3|uint64(wire)) }

func varint(field int, v uint64) []byte { return binary.AppendUvarint(tag(field, WireVarint), v) }

func bytesField(field int, b []byte) []byte {
	out := binary.AppendUvarint(tag(field, WireBytes), uint64(len(b)))
	return append(out, b...)
}

// packedOf encodes values as one length-delimited run, which is how a modern
// encoder writes a repeated numeric field.
func packedOf(field int, vs ...uint64) []byte {
	var payload []byte
	for _, v := range vs {
		payload = binary.AppendUvarint(payload, v)
	}
	return bytesField(field, payload)
}

// repeatedOf encodes the same values one varint at a time, which is what an
// older encoder writes for the same field. Both are valid protobuf and a
// decoder has to accept either.
func repeatedOf(field int, vs ...uint64) []byte {
	var out []byte
	for _, v := range vs {
		out = append(out, varint(field, v)...)
	}
	return out
}

// The three signed encodings are not interchangeable, and reading one as
// another does not fail -- it returns a plausible wrong number. These pin
// each against values derived from the encoding rule rather than from the
// code.
func TestSignedVarintsAreReadAsWhatTheyAre(t *testing.T) {
	t.Run("int64 is two's complement", func(t *testing.T) {
		for _, want := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64, -1_000_000_000} {
			r := New(varint(1, uint64(want)), "test", "a message")
			if _, _, err := r.Tag(); err != nil {
				t.Fatalf("Tag: %v", err)
			}
			got, err := r.Int64("a value")
			if err != nil || got != want {
				t.Errorf("Int64 = %d, %v, want %d", got, err, want)
			}
		}
	})

	t.Run("sint64 is zigzag", func(t *testing.T) {
		// Derived from the rule: an encoded 2n is +n, an encoded 2n-1 is -n.
		for encoded, want := range map[uint64]int64{0: 0, 1: -1, 2: 1, 3: -2, 4: 2} {
			r := New(varint(1, encoded), "test", "a message")
			if _, _, err := r.Tag(); err != nil {
				t.Fatalf("Tag: %v", err)
			}
			got, err := r.Sint64("a delta")
			if err != nil || got != want {
				t.Errorf("Sint64(%d) = %d, %v, want %d", encoded, got, err, want)
			}
		}
	})

	t.Run("an sint read as an int is a different number", func(t *testing.T) {
		// Stated as a test because it is the mistake the separate names
		// exist to prevent, and because it fails silently: -1 as sint64 is
		// the single byte 1, which as an int64 is +1.
		r := New(varint(1, 1), "test", "a message")
		if _, _, err := r.Tag(); err != nil {
			t.Fatalf("Tag: %v", err)
		}
		if got, _ := r.Int64("a value"); got != 1 {
			t.Errorf("Int64 of the sint64 encoding of -1 = %d, want +1", got)
		}
	})
}

// A negative int32 is sign-extended to 64 bits on the wire, so it arrives as
// ten bytes of ones. Range-checking the raw uint64 would reject every
// negative value there is.
func TestInt32AcceptsSignExtendedNegatives(t *testing.T) {
	for _, want := range []int32{0, 1, -1, math.MaxInt32, math.MinInt32} {
		r := New(varint(1, uint64(int64(want))), "test", "a message")
		if _, _, err := r.Tag(); err != nil {
			t.Fatalf("Tag: %v", err)
		}
		got, err := r.Int32("an index")
		if err != nil || got != want {
			t.Errorf("Int32 = %d, %v, want %d", got, err, want)
		}
	}
}

func TestInt32RefusesWhatDoesNotFit(t *testing.T) {
	for _, v := range []int64{math.MaxInt32 + 1, math.MinInt32 - 1} {
		r := New(varint(1, uint64(v)), "test", "a message")
		if _, _, err := r.Tag(); err != nil {
			t.Fatalf("Tag: %v", err)
		}
		if _, err := r.Int32("an index"); err == nil {
			t.Errorf("Int32 accepted %d, which does not fit in 32 bits", v)
		}
	}
}

// Packed and repeated are two encodings of one field, and a decoder that
// handled only the first would read a file from an older producer as having
// no values at all.
func TestPackedAndRepeatedAgree(t *testing.T) {
	want := []int64{0, -1, 1, -2, 2}
	var encoded []uint64
	for _, v := range want {
		encoded = append(encoded, uint64((v<<1)^(v>>63))) // zigzag, by the rule
	}

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"packed into one run", packedOf(1, encoded...)},
		{"repeated one at a time", repeatedOf(1, encoded...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.data, "test", "a message")
			var got []int64
			for !r.Done() {
				_, wire, err := r.Tag()
				if err != nil {
					t.Fatalf("Tag: %v", err)
				}
				if got, err = r.PackedSint64(got, "the deltas", wire, testMax); err != nil {
					t.Fatalf("PackedSint64: %v", err)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("read %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("read %v, want %v", got, want)
				}
			}
		})
	}
}

// The packed case must stop exactly at the end of its payload. Running over
// would consume the next field; stopping short would drop values silently,
// which on a way's node list means a boundary with a piece missing.
func TestPackedStopsAtTheEndOfItsPayload(t *testing.T) {
	data := packedOf(1, 1, 2, 3)
	data = append(data, varint(2, 99)...)

	r := New(data, "test", "a message")
	_, wire, err := r.Tag()
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	got, err := r.PackedUint32(nil, "the values", wire, testMax)
	if err != nil {
		t.Fatalf("PackedUint32: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %v, want three values; the payload holds exactly three", got)
	}
	field, _, err := r.Tag()
	if err != nil || field != 2 {
		t.Errorf("after the packed field, the next tag is field %d (%v), want field 2", field, err)
	}
}

func TestPackedRefusesValuesTooWideForTheirField(t *testing.T) {
	data := packedOf(1, uint64(math.MaxUint32)+1)
	r := New(data, "test", "a message")
	_, wire, err := r.Tag()
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if _, err := r.PackedUint32(nil, "the values", wire, testMax); err == nil {
		t.Error("PackedUint32 accepted a value that does not fit in 32 bits")
	}
}

// Every error names the format being read. Nothing else can check this: each
// decoder's own tests match on messages whose prefix they supply themselves.
func TestErrorsNameTheFormatBeingRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*Reader) error
		data []byte
	}{
		{"a truncated varint", func(r *Reader) error { _, err := r.Uvarint("a value"); return err }, []byte{0xff}},
		{"a length that overruns", func(r *Reader) error { _, err := r.Bytes("a field"); return err }, []byte{0x7f}},
		{"a truncated fixed32", func(r *Reader) error { _, err := r.Fixed32("a value"); return err }, []byte{0x01}},
		{"a truncated fixed64", func(r *Reader) error { _, err := r.Fixed64("a value"); return err }, []byte{0x01}},
		{"a protobuf group", func(r *Reader) error { return r.Skip(3, WireStartGroup) }, nil},
		{"an undefined wire type", func(r *Reader) error { return r.Skip(3, 6) }, nil},
		{"an int32 that does not fit", func(r *Reader) error { _, err := r.Int32("an index"); return err },
			binary.AppendUvarint(nil, uint64(math.MaxInt32)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.read(New(tc.data, "osmpbf", "a relation"))
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.HasPrefix(err.Error(), "osmpbf: ") {
				t.Errorf("%v, want the format named first", err)
			}
			if strings.Contains(err.Error(), "vector tile") {
				t.Errorf("%v, which names the wrong format entirely", err)
			}
			if !strings.Contains(err.Error(), "a relation") {
				t.Errorf("%v, want the message named", err)
			}
		})
	}
}

// The format must reach errors raised inside a packed payload too. That path
// builds a second reader, and it is the one the OSM element decoding spends
// almost all its time in.
func TestAPackedPayloadCarriesTheFormatInwards(t *testing.T) {
	// A payload whose final varint is cut off.
	data := bytesField(1, []byte{0x01, 0xff})
	r := New(data, "osmpbf", "a way")
	_, wire, err := r.Tag()
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	_, err = r.PackedSint64(nil, "the refs", wire, testMax)
	if err == nil {
		t.Fatal("a truncated packed payload read cleanly")
	}
	if !strings.HasPrefix(err.Error(), "osmpbf: ") {
		t.Errorf("%v, want the format named first; the inner reader must carry it", err)
	}
}

func TestFieldNumberZeroIsRefused(t *testing.T) {
	r := New([]byte{0x00}, "osmpbf", "a message")
	if _, _, err := r.Tag(); err == nil {
		t.Error("field number 0 was accepted, and protobuf does not allow it")
	}
}

// Skipping rather than failing is what lets a file written against a later
// schema decode with the fields this does know.
func TestSkipStepsOverEveryDefinedWireType(t *testing.T) {
	data := varint(1, 300)
	data = append(data, bytesField(2, []byte("some bytes"))...)
	data = append(data, append(tag(3, WireFixed32), 1, 2, 3, 4)...)
	data = append(data, append(tag(4, WireFixed64), 1, 2, 3, 4, 5, 6, 7, 8)...)
	data = append(data, varint(5, 7)...)

	r := New(data, "test", "a message")
	var last int
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			t.Fatalf("Tag: %v", err)
		}
		if err := r.Skip(field, wire); err != nil {
			t.Fatalf("Skip(field %d, wire %d): %v", field, wire, err)
		}
		last = field
	}
	if last != 5 {
		t.Errorf("stopped after field %d, want 5; every field must be stepped over exactly", last)
	}
}

// A varint is at most ten bytes, and binary.Uvarint reports one that is
// longer -- or whose tenth byte carries bits past 64 -- as a NEGATIVE count.
// Added to the reader's position that walks it backwards, and the next read
// slices the buffer at a negative index and panics. Both spellings of the
// overflow are here because they arrive by different routes: eleven bytes of
// continuation, and ten bytes whose last one overflows.
//
// The check is in the one function every other accessor is built on, so it
// stands between a downloaded extract and a panic in a library.
func TestAVarintTooLongForSixtyFourBitsIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"eleven bytes of continuation", append(bytes.Repeat([]byte{0x80}, 10), 0x01)},
		{"ten bytes whose last overflows", append(bytes.Repeat([]byte{0xff}, 9), 0x02)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.data, "osmpbf", "a way")
			v, err := r.Uvarint("a node id")
			if err == nil {
				t.Fatalf("Uvarint = %d, want a refusal; %d bytes cannot be a 64-bit varint", v, len(tc.data))
			}
			// The position is the part that bites. A negative count added to
			// it makes every later read slice at a negative index.
			if r.i < 0 {
				t.Errorf("the reader is at byte %d after refusing; a position may not go backwards", r.i)
			}
			if !strings.Contains(err.Error(), "64 bits") {
				t.Errorf("%v, want the width named", err)
			}
			if !strings.HasPrefix(err.Error(), "osmpbf: ") {
				t.Errorf("%v, want the format named first", err)
			}
		})
	}
}

// The same bytes reached through the accessors built on Uvarint. Each one is
// a separate error path, and a caller that dropped the error from any of them
// would hand back a plausible zero.
func TestAnOverlongVarintIsRefusedByEveryAccessor(t *testing.T) {
	overlong := append(bytes.Repeat([]byte{0x80}, 10), 0x01)
	for _, tc := range []struct {
		name string
		read func(*Reader) error
	}{
		{"Int64", func(r *Reader) error { _, err := r.Int64("a way id"); return err }},
		{"Int32", func(r *Reader) error { _, err := r.Int32("a tag index"); return err }},
		{"Sint64", func(r *Reader) error { _, err := r.Sint64("a delta"); return err }},
		{"Tag", func(r *Reader) error { _, _, err := r.Tag(); return err }},
		{"Bytes", func(r *Reader) error { _, err := r.Bytes("a payload"); return err }},
		{"Packed", func(r *Reader) error {
			_, err := r.PackedSint64(nil, "the refs", WireVarint, testMax)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(overlong, "osmpbf", "a way")
			if err := tc.read(r); err == nil {
				t.Error("an overlong varint read cleanly")
			}
		})
	}
}

// A length-delimited field may be exactly the rest of the buffer and must
// not be one byte more. Tested from both sides because an off-by-one here is
// invisible from the failing side alone: a check written with >= refuses the
// last field of every message, and one written with a slack byte walks the
// position past the end and panics on the slice.
func TestBytesTakesExactlyWhatRemainsAndNoMore(t *testing.T) {
	payload := []byte("boundary")

	t.Run("exactly what remains", func(t *testing.T) {
		r := New(bytesField(1, payload), "osmpbf", "a string table")
		if _, _, err := r.Tag(); err != nil {
			t.Fatalf("Tag: %v", err)
		}
		got, err := r.Bytes("a string")
		if err != nil {
			t.Fatalf("a field filling the rest of the buffer was refused: %v", err)
		}
		if string(got) != string(payload) {
			t.Errorf("Bytes = %q, want %q", got, payload)
		}
		if !r.Done() {
			t.Error("the reader is not done after a field that ran to the end")
		}
	})

	t.Run("one byte more than remains", func(t *testing.T) {
		data := bytesField(1, payload)
		data = data[:len(data)-1] // the length still says 8; only 7 are there
		r := New(data, "osmpbf", "a string table")
		if _, _, err := r.Tag(); err != nil {
			t.Fatalf("Tag: %v", err)
		}
		if got, err := r.Bytes("a string"); err == nil {
			t.Fatalf("Bytes = %q for a field declaring one byte more than the buffer holds", got)
		}
	})
}

// A packed field must arrive as a length-delimited run or as repeated
// varints. Anything else is refused rather than guessed at: read as bytes, a
// fixed64 field's first byte becomes a length and the rest of the message is
// parsed from the wrong offset, which yields node references that are
// arithmetic on somebody's timestamp rather than an error.
func TestPackedRefusesAWireTypeItCannotBe(t *testing.T) {
	for _, wire := range []int{WireFixed64, WireFixed32, WireStartGroup} {
		data := append(tag(1, wire), 1, 2, 3, 4, 5, 6, 7, 8)
		r := New(data, "osmpbf", "a way")
		field, gotWire, err := r.Tag()
		if err != nil || field != 1 || gotWire != wire {
			t.Fatalf("Tag = %d, %d, %v", field, gotWire, err)
		}
		got, err := r.PackedSint64(nil, "the node references", gotWire, testMax)
		if err == nil {
			t.Errorf("PackedSint64 read wire type %d as %v, want a refusal", wire, got)
			continue
		}
		if !strings.Contains(err.Error(), "packed or repeated varint") {
			t.Errorf("wire type %d: %v, want the encodings it may have named", wire, err)
		}
	}
}

// PackedInt32 is the accessor the OSM decoder uses for every tag key, tag
// value and relation role, and nothing in this package's own tests reached
// it. The two-byte widths are not interchangeable: a negative int32 is
// sign-extended to ten bytes on the wire, so a packed run of them is a run of
// ten-byte varints, and a decoder that range-checked the raw uint64 would
// reject the lot.
func TestPackedInt32CarriesSignExtendedValues(t *testing.T) {
	want := []int32{0, 1, -1, math.MaxInt32, math.MinInt32}
	var encoded []uint64
	for _, v := range want {
		encoded = append(encoded, uint64(int64(v))) // two's complement, by the rule
	}

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"packed into one run", packedOf(1, encoded...)},
		{"repeated one at a time", repeatedOf(1, encoded...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.data, "osmpbf", "a way")
			var got []int32
			for !r.Done() {
				_, wire, err := r.Tag()
				if err != nil {
					t.Fatalf("Tag: %v", err)
				}
				if got, err = r.PackedInt32(got, "the tag keys", wire, testMax); err != nil {
					t.Fatalf("PackedInt32: %v", err)
				}
			}
			if !slices.Equal(got, want) {
				t.Errorf("read %v, want %v", got, want)
			}
		})
	}
}

// The range check inside a packed run must fire too, and name the format:
// the run builds a second reader, and an error raised there is the one a
// caller sees for a tag index that is not an index.
func TestPackedInt32RefusesWhatDoesNotFit(t *testing.T) {
	for _, v := range []int64{math.MaxInt32 + 1, math.MinInt32 - 1} {
		data := packedOf(1, uint64(v))
		r := New(data, "osmpbf", "a way")
		_, wire, err := r.Tag()
		if err != nil {
			t.Fatalf("Tag: %v", err)
		}
		got, err := r.PackedInt32(nil, "the tag keys", wire, testMax)
		if err == nil {
			t.Errorf("PackedInt32 read %d as %v, and it does not fit in 32 bits", v, got)
			continue
		}
		if !strings.HasPrefix(err.Error(), "osmpbf: ") {
			t.Errorf("%v, want the format named first", err)
		}
	}
}

// A packed run's own length is read inside the accessor, below the field
// reads the caller makes, and it is the length a malformed file has most to
// gain from: everything repeated in these formats lives in one.
func TestAPackedRunCannotDeclareMoreThanTheMessageHolds(t *testing.T) {
	data := tag(1, WireBytes)
	data = binary.AppendUvarint(data, 200)
	data = append(data, 0x01, 0x02)

	r := New(data, "osmpbf", "a way")
	_, wire, err := r.Tag()
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	got, err := r.PackedSint64(nil, "the node references", wire, testMax)
	if err == nil {
		t.Fatalf("PackedSint64 = %v for a run declaring 200 bytes of a 2 byte message", got)
	}
	if !strings.Contains(err.Error(), "the node references") {
		t.Errorf("%v, want the field named", err)
	}
}
