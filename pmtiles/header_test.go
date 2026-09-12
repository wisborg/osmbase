package pmtiles_test

import (
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// TestParseHeader_ReadsEveryField builds an archive with every header field
// set to something distinguishable and reads them all back.
//
// The coordinates are chosen to be exactly representable in binary floating
// point and to survive the format's hundred-nanodegree integer encoding
// without rounding, so a failure here is a field at the wrong offset rather
// than a decimal that did not round trip. They are arbitrary numbers and not a
// place.
func TestParseHeader_ReadsEveryField(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles:               []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
		TileType:            pmtiles.TileTypeMVT,
		MinZoom:             3,
		MaxZoom:             14,
		MinLon:              -1.25,
		MinLat:              -2.5,
		MaxLon:              3.75,
		MaxLat:              4.125,
		CenterZoom:          9,
		CenterLon:           1.0625,
		CenterLat:           0.5,
		Metadata:            []byte(`{"name":"synthetic"}`),
	})

	h, err := pmtiles.ParseHeader(built.Bytes)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.Version != pmtiles.Version {
		t.Errorf("version = %d, want %d", h.Version, pmtiles.Version)
	}
	if h.InternalCompression != pmtiles.CompressionGzip {
		t.Errorf("internal compression = %v, want gzip", h.InternalCompression)
	}
	if h.TileCompression != pmtiles.CompressionGzip {
		t.Errorf("tile compression = %v, want gzip", h.TileCompression)
	}
	if h.TileType != pmtiles.TileTypeMVT {
		t.Errorf("tile type = %v, want mvt", h.TileType)
	}
	if h.MinZoom != 3 || h.MaxZoom != 14 {
		t.Errorf("zoom range = %d..%d, want 3..14", h.MinZoom, h.MaxZoom)
	}
	if h.CenterZoom != 9 {
		t.Errorf("centre zoom = %d, want 9", h.CenterZoom)
	}
	if !h.Clustered {
		t.Error("clustered flag is not set on a clustered archive")
	}
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"min longitude", h.MinLon, -1.25},
		{"min latitude", h.MinLat, -2.5},
		{"max longitude", h.MaxLon, 3.75},
		{"max latitude", h.MaxLat, 4.125},
		{"centre longitude", h.CenterLon, 1.0625},
		{"centre latitude", h.CenterLat, 0.5},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// The section offsets must be internally consistent: each section starts
	// where the one before it ends, beginning after the header.
	if h.RootOffset != pmtiles.HeaderSize {
		t.Errorf("root directory starts at %d, want %d", h.RootOffset, pmtiles.HeaderSize)
	}
	if got, want := h.MetadataOffset, h.RootOffset+h.RootLength; got != want {
		t.Errorf("metadata starts at %d, want %d", got, want)
	}
	if got, want := h.LeafOffset, h.MetadataOffset+h.MetadataLength; got != want {
		t.Errorf("leaf directories start at %d, want %d", got, want)
	}
	if got, want := h.TileDataOffset, h.LeafOffset+h.LeafLength; got != want {
		t.Errorf("tile data starts at %d, want %d", got, want)
	}
	if got, want := uint64(len(built.Bytes)), h.TileDataOffset+h.TileDataLength; got != want {
		t.Errorf("the archive is %d bytes and the header accounts for %d", got, want)
	}
	if h.AddressedTiles != 1 || h.TileEntries != 1 {
		t.Errorf("counts: %d addressed tiles and %d entries, want 1 and 1", h.AddressedTiles, h.TileEntries)
	}
}

// TestParseHeader_RejectsHeadersItCannotActOn covers the header damage that
// would otherwise send the reader to an arbitrary offset.
func TestParseHeader_RejectsHeadersItCannotActOn(t *testing.T) {
	good := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 0, Data: []byte("tile")}},
	}).Bytes

	cases := []struct {
		name    string
		damage  func([]byte)
		wantMsg string
	}{
		{"wrong magic", func(b []byte) { b[0] = 'X' }, "not a PMTiles archive"},
		{"version 2", func(b []byte) { b[7] = 2 }, "version 2"},
		{"an undefined internal compression", func(b []byte) { b[97] = 9 }, "internal compression 9"},
		{"an undefined tile compression", func(b []byte) { b[98] = 200 }, "tile compression 200"},
		{"a zoom range that runs backwards", func(b []byte) { b[100], b[101] = 10, 4 }, "runs backwards"},
		{"a maximum zoom past what an ID can address", func(b []byte) { b[100], b[101] = 0, 40 }, "deeper than"},
		{"an empty root directory", func(b []byte) {
			for i := 16; i < 24; i++ {
				b[i] = 0
			}
		}, "root directory is empty"},
		{"a root directory past the 16 KiB cap", func(b []byte) { b[16], b[17] = 0x00, 0x80 }, "caps it at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := append([]byte(nil), good...)
			c.damage(b)
			_, err := pmtiles.ParseHeader(b)
			if err == nil {
				t.Fatal("ParseHeader accepted the damaged header")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should mention %q so the reader can act on it", err, c.wantMsg)
			}
		})
	}
}

// TestParseHeader_RejectsAShortHeader. A range request that came back short is
// the ordinary failure for this format, and a 127-byte structure read out of
// 40 bytes must not be a struct full of whatever followed.
func TestParseHeader_RejectsAShortHeader(t *testing.T) {
	good := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 0, Data: []byte("tile")}},
	}).Bytes
	for n := 0; n < pmtiles.HeaderSize; n++ {
		if _, err := pmtiles.ParseHeader(good[:n]); err == nil {
			t.Fatalf("ParseHeader accepted %d of %d bytes", n, pmtiles.HeaderSize)
		}
	}
}

func build(t *testing.T, a osmbasetest.Archive) osmbasetest.BuiltArchive {
	t.Helper()
	built, err := osmbasetest.BuildArchive(a)
	if err != nil {
		t.Fatalf("building the fixture archive: %v", err)
	}
	return built
}
