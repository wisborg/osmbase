package boundary

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// The layout of a version 1 file, byte for byte.
//
// Spelled out rather than round-tripped, because a round trip cannot see a
// change the writer and the reader make TOGETHER. Swap latitude and longitude
// on both sides, or write Kind before Name on both sides, and every existing
// test still passes -- while every file already on somebody's disk now
// decodes to somewhere else. The version byte is what should catch that and
// cannot, because a symmetric change does not make anybody increment it.
// This is what catches it.
//
// Every byte is derived from the format's own description in derived.go
// rather than from what the code emits today:
//
//	6f 73 6d 62   "osmb", the magic
//	01            version 1, as a uvarint
//	04            the header block is four bytes long
//	00            a source of no bytes
//	00            an attribution of no bytes
//	00            created at the zero time
//	00            no levels
//	01            one area
//	01 6e         a name of one byte, "n"
//	01 6b         a kind of one byte, "k"
//	01            one polygon
//	01            one ring in it
//	02            two points in the ring
//	06            latitude  +3 units from 0:  zigzag(3)  = 6
//	14            longitude +10 units from 0: zigzag(10) = 20
//	01            latitude  -1 unit from 3:   zigzag(-1) = 1
//	06            longitude +3 units from 10: zigzag(3)  = 6
//
// A unit is a ten-millionth of a degree, so the ring is (0.0000003,
// 0.0000010) then (0.0000002, 0.0000013): latitude first, each vertex delta
// coded against the one before it and the first against zero.
//
// If this fails because the format was changed deliberately, these bytes
// change with it -- and so does derivedVersion, UNLESS the change is a new
// field appended to the header block, which the length prefix above exists
// so that an older reader can step over.
func TestTheOnDiskLayoutIsWhatVersionOneDocuments(t *testing.T) {
	const u = 1 / derivedScale // one stored unit, in degrees
	ring := Ring{{Lat: 3 * u, Lon: 10 * u}, {Lat: 2 * u, Lon: 13 * u}}
	golden := []byte{
		'o', 's', 'm', 'b',
		1,
		4, 0, 0, 0, 0,
		1,
		1, 'n',
		1, 'k',
		1,
		1,
		2,
		6, 20,
		1, 6,
	}

	t.Run("the writer emits it", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteDerived(&buf, NewSet(Provenance{}, []Area{NewArea("n", "k", []Polygon{{Outer: ring}})})); err != nil {
			t.Fatalf("WriteDerived: %v", err)
		}
		if !bytes.Equal(buf.Bytes(), golden) {
			t.Errorf("the file is\n\t%x\nand version %d is documented as\n\t%x",
				buf.Bytes(), derivedVersion, golden)
		}
	})

	t.Run("the reader reads it back the same way round", func(t *testing.T) {
		set, err := ReadDerived(bytes.NewReader(golden))
		if err != nil {
			t.Fatalf("ReadDerived: %v", err)
		}
		if set.Len() != 1 {
			t.Fatalf("read %d areas, want 1", set.Len())
		}
		a := set.areas[0]
		if a.Name != "n" || a.Kind != "k" {
			t.Errorf("read name %q kind %q, want \"n\" and \"k\"", a.Name, a.Kind)
		}
		got := a.polygons[0].rings[0]
		if len(got) != len(ring) {
			t.Fatalf("read %d points, want %d", len(got), len(ring))
		}
		for i := range ring {
			// Half a unit, as elsewhere in this file: tighter asserts float
			// equality after a divide, looser accepts a moved vertex. The
			// latitudes and longitudes here differ by several units, so a
			// transposition is nowhere near passing this.
			if diff(got[i].Lat, ring[i].Lat) > 0.5*u || diff(got[i].Lon, ring[i].Lon) > 0.5*u {
				t.Errorf("point %d read back as %.8f,%.8f, want %.8f,%.8f",
					i, got[i].Lat, got[i].Lon, ring[i].Lat, ring[i].Lon)
			}
		}
	})
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// The writer's limit on a string and the reader's must be the SAME number.
//
// They are written as two separate comparisons against one constant, and
// nothing holds them together: make either one strict and the writer produces
// a file the reader refuses, which is a boundary set that builds cleanly and
// then cannot be opened. The two tests either side of this one check a name
// one byte PAST the limit, which is the case both comparisons agree on
// whichever way they are spelled; this checks the one they can disagree on.
func TestANameAtExactlyTheFormatsLimitSurvivesTheFile(t *testing.T) {
	name := strings.Repeat("n", maxDerivedString)
	kind := strings.Repeat("k", maxDerivedString)

	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, []Area{NewArea(name, kind, []Polygon{{Outer: square(0, 0, 1)}})})); err != nil {
		t.Fatalf("a name of exactly the %d bytes the format allows was refused on write: %v",
			maxDerivedString, err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("the writer produced a file the reader refuses: %v", err)
	}
	if got.Len() != 1 {
		t.Fatalf("read %d areas, want 1", got.Len())
	}
	if got.areas[0].Name != name || got.areas[0].Kind != kind {
		t.Errorf("read a name of %d bytes and a kind of %d, want %d of each",
			len(got.areas[0].Name), len(got.areas[0].Kind), maxDerivedString)
	}
}

// errDisk stands in for the write that fails because the disk filled up.
var errDisk = errors.New("no space left on device")

// failingWriter accepts n bytes and then fails, or -- with short set -- takes
// fewer bytes than it was given and reports no error, which io.Writer forbids
// and a real device does anyway.
type failingWriter struct {
	n     int
	short bool
	got   int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	room := w.n - w.got
	if room <= 0 {
		return 0, errDisk
	}
	if len(p) <= room {
		w.got += len(p)
		return len(p), nil
	}
	w.got = w.n
	if w.short {
		return room, nil
	}
	return room, errDisk
}

// A file that could not be written must say so.
//
// WriteDerived writes through a bufio.Writer, so for any file smaller than
// the buffer NOTHING reaches the disk until the final Flush -- which means
// the one error return that matters most is the last line of the function. A
// Flush whose error is dropped returns nil for a file that is empty or
// truncated, and the command that built it reports success. The boundaries
// are then missing at lookup time, on a different day, with nothing to say
// why.
//
// The large case is here because it fails somewhere else entirely: past the
// buffer the writes go to the device as they are made, so the error comes
// back from a put() in the middle of a ring rather than from Flush.
func TestWriteDerivedReportsAWriterThatFails(t *testing.T) {
	small := []Area{NewArea("n", "k", []Polygon{{Outer: square(0, 0, 1)}})}

	// Enough points that the output is several times the 4096-byte buffer,
	// so writes reach the underlying writer before the end.
	big := make(Ring, 8000)
	for i := range big {
		big[i] = Coord{Lat: float64(i) * 1e-5, Lon: float64(i) * 2e-5}
	}
	large := []Area{NewArea("n", "k", []Polygon{{Outer: big}})}

	for _, tc := range []struct {
		name  string
		areas []Area
		w     *failingWriter
	}{
		{"a small file, where only the flush ever reaches the writer", small, &failingWriter{n: 0}},
		{"a large file, failing part way through", large, &failingWriter{n: 5000}},
		{"a writer that takes fewer bytes than it was given", large, &failingWriter{n: 5000, short: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := WriteDerived(tc.w, NewSet(Provenance{}, tc.areas)); err == nil {
				t.Errorf("WriteDerived returned nil after the writer failed; %d of the file's bytes were accepted",
					tc.w.got)
			}
		})
	}
}

// ReadDerived takes an io.Reader, and a Reader may return less than it was
// asked for at any point: a pipe, a network filesystem, an http body. Every
// other test in this file hands it a bytes.Reader, which never does.
//
// A read that takes what one call happens to return, instead of filling the
// buffer, does not fail on a bytes.Reader and does not fail on a small local
// file either -- it fails on somebody else's machine, on a file it then
// reports as corrupt.
func TestReadDerivedAcceptsAStreamThatArrivesInPieces(t *testing.T) {
	areas := []Area{
		NewArea("first", "7", []Polygon{{Outer: rect(0, 2, 10, 40), Holes: []Ring{rect(0.5, 1.5, 15, 25)}}}),
		NewArea("second", "9", []Polygon{{Outer: rect(-5, -3, 60, 70)}}),
	}
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, areas)); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	file := buf.Bytes()

	for _, tc := range []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"one byte at a time", func(r io.Reader) io.Reader { return iotest.OneByteReader(r) }},
		{"half of what was asked for", func(r io.Reader) io.Reader { return iotest.HalfReader(r) }},
		{"all at once", func(r io.Reader) io.Reader { return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadDerived(tc.wrap(bytes.NewReader(file)))
			if err != nil {
				t.Fatalf("ReadDerived: %v", err)
			}
			if got.Len() != len(areas) {
				t.Fatalf("read %d areas, want %d", got.Len(), len(areas))
			}
			for _, q := range []struct {
				lat, lon float64
				want     string
			}{
				{1, 12, "first"},
				{1, 20, ""}, // in the hole
				{-4, 65, "second"},
				{65, -4, ""}, // the same coordinate the wrong way round
			} {
				a, ok := got.At(q.lat, q.lon)
				if name := areaName(a, ok); name != q.want {
					t.Errorf("at %v,%v the set says %q, want %q", q.lat, q.lon, name, q.want)
				}
			}
		})
	}
}

func areaName(a Area, ok bool) string {
	if !ok {
		return ""
	}
	return a.Name
}
