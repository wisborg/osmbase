package main

import (
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
)

// TestHumanBytes_ReadsAsASize checks the rendering at each unit boundary.
//
// The values are derived from the definition: a kibibyte is 1024 bytes, so
// 1536 is 1.5 KiB and 134812420554 -- the size of the planet archive this
// program defaults to -- is 134812420554 / 1024^3 = 125.6 GiB.
func TestHumanBytes_ReadsAsASize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 bytes"},
		{1, "1 bytes"},
		{1023, "1023 bytes"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{18 * 1024 * 1024, "18.0 MiB"},
		{134812420554, "125.6 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBytesExact_KeepsTheNumberABugReportNeeds: the readable form is what
// tells a person the archive is enormous, and the exact count is what makes
// two reports comparable.
func TestBytesExact_KeepsTheNumberABugReportNeeds(t *testing.T) {
	if got, want := bytesExact(1536), "1.5 KiB (1536 bytes)"; got != want {
		t.Errorf("bytesExact(1536) = %q, want %q", got, want)
	}
	// Below a kibibyte the two forms would be the same number twice.
	if got, want := bytesExact(512), "512 bytes"; got != want {
		t.Errorf("bytesExact(512) = %q, want %q", got, want)
	}
}

// TestFormatCoord_IsAnExactDecimalAPersonCanRead covers the three things the
// coordinate format has to do: never go exponential, which no JSON reader and
// no human wants; trim trailing zeros so a round number reads as round; and
// keep seven decimal places, which is about a centimetre and finer than any
// tile geometry this reads.
func TestFormatCoord_IsAnExactDecimalAPersonCanRead(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{-0, "0"},
		{180, "180"},
		{-180, "-180"},
		{151.2153, "151.2153"},
		{-33.8568, "-33.8568"},
		{85.05112877980659, "85.0511288"},
		{0.0000001, "0.0000001"},
		// A value small enough that %g would render it as 1.5e-05.
		{0.000015, "0.000015"},
		// Finer than the format's last place, which is about a millimetre, so
		// it collapses to zero rather than turning into 1e-09.
		{0.000000001, "0"},
	}
	for _, c := range cases {
		got := formatCoord(c.in)
		if got != c.want {
			t.Errorf("formatCoord(%v) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "eE") {
			t.Errorf("formatCoord(%v) = %q, which is exponential", c.in, got)
		}
	}
}

// TestSampleTags_IsDeterministicAndCountsFeatures checks the --tags listing.
//
// Determinism is the point: the values come out in the order the FEATURES
// carry them, never in the order a Go map hands them over, so two runs of the
// same command produce the same report. The distinct count is separate from
// the sample because a key with four hundred values is worth knowing about
// even when only six are shown.
func TestSampleTags_IsDeterministicAndCountsFeatures(t *testing.T) {
	layer := mvt.Layer{Name: "roads", Features: []mvt.Feature{
		{Tags: map[string]mvt.Value{"kind": mvt.StringValue("path"), "name": mvt.StringValue("one")}},
		{Tags: map[string]mvt.Value{"kind": mvt.StringValue("minor_road"), "name": mvt.StringValue("two")}},
		{Tags: map[string]mvt.Value{"kind": mvt.StringValue("path")}},
		{Tags: map[string]mvt.Value{"kind": mvt.StringValue("service"), "level": mvt.IntValue(1)}},
	}}
	keys, samples := sampleTags(layer)
	wantKeys := []string{"kind", "level", "name"}
	if len(keys) != len(wantKeys) {
		t.Fatalf("keys = %v, want %v", keys, wantKeys)
	}
	for i := range keys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("keys = %v, want them sorted as %v", keys, wantKeys)
		}
	}
	kind := samples["kind"]
	if kind.features != 4 {
		t.Errorf("kind is on %d features, want 4", kind.features)
	}
	if kind.distinct != 3 {
		t.Errorf("kind has %d distinct values, want 3: path, minor_road and service", kind.distinct)
	}
	wantValues := []string{`"path"`, `"minor_road"`, `"service"`}
	for i, v := range wantValues {
		if kind.values[i] != v {
			t.Errorf("kind's values are %v, want them in feature order: %v", kind.values, wantValues)
		}
	}
	if samples["level"].features != 1 {
		t.Errorf("level is on %d features, want 1", samples["level"].features)
	}
	if samples["level"].kind != "int" {
		t.Errorf("level's kind is %q, want int", samples["level"].kind)
	}

	// A key encoded two ways is reported as mixed rather than as whichever
	// came first, because a schema doing that is worth noticing.
	mixed := mvt.Layer{Features: []mvt.Feature{
		{Tags: map[string]mvt.Value{"level": mvt.IntValue(1)}},
		{Tags: map[string]mvt.Value{"level": mvt.StringValue("1")}},
	}}
	_, samples = sampleTags(mixed)
	if samples["level"].kind != "mixed" {
		t.Errorf("a key encoded as both an int and a string is reported as %q, want mixed", samples["level"].kind)
	}
}

// TestSampleTags_CapsTheValuesItPrints keeps a "name" key from printing a
// street directory, while still saying how many were left out.
func TestSampleTags_CapsTheValuesItPrints(t *testing.T) {
	var features []mvt.Feature
	for i := 0; i < maxSampleValues*3; i++ {
		features = append(features, mvt.Feature{Tags: map[string]mvt.Value{"name": mvt.IntValue(int64(i))}})
	}
	_, samples := sampleTags(mvt.Layer{Features: features})
	s := samples["name"]
	if len(s.values) != maxSampleValues {
		t.Errorf("printed %d values, want the cap of %d", len(s.values), maxSampleValues)
	}
	if s.distinct != maxSampleValues*3 {
		t.Errorf("counted %d distinct values, want %d", s.distinct, maxSampleValues*3)
	}
}
