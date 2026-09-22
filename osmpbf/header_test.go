package osmpbf

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestDecodeHeaderReadsTheFeatureDeclarations(t *testing.T) {
	h, err := DecodeHeader(header(
		[]string{"OsmSchema-V0.6", "DenseNodes"},
		[]string{"Sort.Type_then_ID", "Has_Metadata"},
	))
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if !slices.Equal(h.RequiredFeatures, []string{"OsmSchema-V0.6", "DenseNodes"}) {
		t.Errorf("required features = %q", h.RequiredFeatures)
	}
	if !slices.Equal(h.OptionalFeatures, []string{"Sort.Type_then_ID", "Has_Metadata"}) {
		t.Errorf("optional features = %q", h.OptionalFeatures)
	}
}

// A file that requires something this does not implement must stop the read.
// The failure it prevents is a quiet wrong answer, not a crash: an extract
// declaring HistoricalInformation carries deleted and superseded elements, so
// a pipeline that read it as an ordinary extract would accept a relation
// abolished years ago and draw the suburb it used to describe.
func TestARequiredFeatureThisDoesNotImplementStopsTheRead(t *testing.T) {
	for _, feature := range []string{
		"HistoricalInformation", // full history, including deleted elements
		"LocationsOnWays",       // coordinates inlined on ways
		"OsmSchema-V0.7",        // an element model this has never seen
	} {
		h, err := DecodeHeader(header([]string{"OsmSchema-V0.6", feature}, nil))
		if err == nil {
			t.Errorf("a file requiring %q was accepted", feature)
			continue
		}
		if !strings.Contains(err.Error(), feature) {
			t.Errorf("DecodeHeader: %v, want the feature named so a caller can say which", err)
		}
		// The Header comes back alongside the error, so the caller can
		// report the whole declaration rather than just the first refusal.
		if len(h.RequiredFeatures) != 2 {
			t.Errorf("the refused header carried %d required features, want both", len(h.RequiredFeatures))
		}
	}
}

// An optional feature is by definition one a reader may ignore. Refusing one
// would reject ordinary extracts, which all declare a sort order.
func TestAnOptionalFeatureIsIgnored(t *testing.T) {
	for _, feature := range []string{"Sort.Type_then_ID", "Sort.Geographic", "something from 2031"} {
		if _, err := DecodeHeader(header([]string{"OsmSchema-V0.6"}, []string{feature})); err != nil {
			t.Errorf("a file offering the optional feature %q was refused: %v", feature, err)
		}
	}
}

// A header declaring nothing is readable. Required features are optional in
// the schema, and a reader that insisted on them would refuse a minimal but
// valid file.
func TestAHeaderNeedDeclareNothing(t *testing.T) {
	if _, err := DecodeHeader(nil); err != nil {
		t.Errorf("an empty header was refused: %v", err)
	}
}

// The fields this does not read -- the bounding box, the writing program,
// the replication timestamps -- must be skipped, not treated as the end of
// the message. Every real header carries several.
func TestUnknownHeaderFieldsAreSkipped(t *testing.T) {
	h := pbBytes(1, []byte{0x08, 0x01}) // bbox, a nested message
	h = append(h, pbString(4, "OsmSchema-V0.6")...)
	h = append(h, pbString(16, "osmium/1.14.0")...) // writingprogram
	h = append(h, pbVarint(32, 1_700_000_000)...)   // replication timestamp
	h = append(h, pbString(5, "Sort.Type_then_ID")...)

	got, err := DecodeHeader(h)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if len(got.RequiredFeatures) != 1 || len(got.OptionalFeatures) != 1 {
		t.Errorf("unknown fields cost the header its declarations: %+v", got)
	}
}

// Feature counts are bounded for the same reason the string table's are: the
// entries are unbounded in number and the header is bounded only in bytes.
func TestFeatureCountsAreBounded(t *testing.T) {
	// A feature this DOES implement, so that only the cap can refuse the
	// header. An unsupported name would be refused by the feature check
	// instead, and the test would pass with the cap deleted.
	for _, field := range []int{4, 5} {
		var h []byte
		for i := 0; i <= MaxFeatures; i++ {
			h = append(h, pbString(field, "DenseNodes")...)
		}
		_, err := DecodeHeader(h)
		if err == nil {
			t.Errorf("a header declaring %d features in field %d was accepted", MaxFeatures+1, field)
			continue
		}
		if !strings.Contains(err.Error(), "features") {
			t.Errorf("DecodeHeader: %v, want an error naming the feature count", err)
		}
	}
}

// The check must run on an ordinary read, not only when a caller thinks to
// ask. A caller who forgets does not get an error -- they get the wrong
// boundaries, drawn from elements that were deleted years ago.
func TestTheReaderRefusesAFileItCannotReadCorrectly(t *testing.T) {
	file := append(
		frame(TypeHeader, rawBlob(header([]string{"OsmSchema-V0.6", "HistoricalInformation"}, nil))),
		frame(TypeData, rawBlob(primitiveBlock(stringTable(""), nil)))...,
	)

	d := NewReader(bytes.NewReader(file))
	_, err := d.Next()
	if err == nil {
		t.Fatal("a file requiring HistoricalInformation was read without complaint")
	}
	if !strings.Contains(err.Error(), "HistoricalInformation") {
		t.Errorf("Next: %v, want the feature named", err)
	}
}

func TestTheReaderRemembersTheHeader(t *testing.T) {
	file := append(
		frame(TypeHeader, rawBlob(header([]string{"OsmSchema-V0.6"}, []string{"Sort.Type_then_ID"}))),
		frame(TypeData, rawBlob(primitiveBlock(stringTable(""), nil)))...,
	)

	d := NewReader(bytes.NewReader(file))
	if got := d.Header(); len(got.RequiredFeatures) != 0 {
		t.Errorf("before the header was read, Header() = %+v, want nothing", got)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Next(); err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
	}
	got := d.Header()
	if !slices.Equal(got.RequiredFeatures, []string{"OsmSchema-V0.6"}) ||
		!slices.Equal(got.OptionalFeatures, []string{"Sort.Type_then_ID"}) {
		t.Errorf("Header() = %+v, want the file's declarations", got)
	}
}
