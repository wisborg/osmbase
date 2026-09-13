package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// result is what one run of the program produced.
type result struct {
	code   int
	stdout string
	stderr string
}

// runCLI runs the program as a shell would and returns everything it produced.
//
// It also enforces the rule this whole test file depends on: NOTHING here may
// reach the network. Every test names a local fixture, and the check below is
// what would catch a future test that forgot to, which would otherwise pass
// quietly on a connected machine and fail on a train.
func runCLI(t *testing.T, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	r := result{code: code, stdout: stdout.String(), stderr: stderr.String()}
	// The two sentences openArchive prints when, and only when, it is about to
	// talk to a host. Matching on the URL instead would be wrong: the help
	// text names the default archive without fetching it, which is the point
	// of the help text.
	for _, marker := range []string{"over the network", "over HTTP range requests"} {
		if strings.Contains(r.stderr, marker) {
			t.Fatalf("this test reached for an archive over the network:\n%s", r.stderr)
		}
	}
	return r
}

// worldTile is the fixture most of these tests use: one tile covering the
// whole world, at zoom 0, with an extent of 4096.
//
// Zoom 0 is chosen so that every coordinate in the expected output can be
// worked out on paper. The tile spans the entire Web Mercator square, so tile
// x 0 is longitude -180, x 2048 is 0, x 4096 is +180; and tile y 0 is the
// northern cut at +85.0511288 degrees, y 2048 is the equator and y 4096 is the
// southern cut.
func worldTile() osmbasetest.TileSpec {
	return osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{
		{
			Name:   "places",
			Extent: 4096,
			Features: []osmbasetest.FeatureSpec{{
				ID:       7,
				HasID:    true,
				Type:     mvt.GeomPoint,
				Tags:     []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("Null Island")}},
				Geometry: mvt.Geometry{Points: []mvt.Point{{X: 2048, Y: 2048}}},
			}},
		},
		{
			Name:   "roads",
			Extent: 4096,
			Features: []osmbasetest.FeatureSpec{{
				Type: mvt.GeomLineString,
				Tags: []osmbasetest.Tag{{Key: "kind", Value: mvt.StringValue("path")}},
				Geometry: mvt.Geometry{Lines: [][]mvt.Point{{
					{X: 0, Y: 0}, {X: 2048, Y: 2048}, {X: 4096, Y: 4096},
				}}},
			}},
		},
		{
			Name:   "water",
			Extent: 4096,
			Features: []osmbasetest.FeatureSpec{{
				Type: mvt.GeomPolygon,
				Tags: []osmbasetest.Tag{{Key: "kind", Value: mvt.StringValue("lake")}},
				Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
					// Clockwise on screen, which is positive area with y
					// running down, which is what an MVT exterior is.
					Exterior: mvt.Ring{{X: 1024, Y: 1024}, {X: 3072, Y: 1024}, {X: 3072, Y: 3072}, {X: 1024, Y: 3072}},
					// The other way round: an island in the lake.
					Holes: []mvt.Ring{{{X: 1536, Y: 1536}, {X: 1536, Y: 2560}, {X: 2560, Y: 2560}, {X: 2560, Y: 1536}}},
				}}},
			}},
		},
	}}
}

// fixtureArchive writes a synthetic archive holding one tile and returns its
// path. Nothing in it comes from a real recording or a real download.
func fixtureArchive(t *testing.T, z uint8, x, y uint32, spec osmbasetest.TileSpec) string {
	t.Helper()
	data, err := osmbasetest.BuildTile(spec)
	if err != nil {
		t.Fatalf("BuildTile: %v", err)
	}
	id, err := pmtiles.ZxyToID(z, x, y)
	if err != nil {
		t.Fatalf("ZxyToID: %v", err)
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:               []osmbasetest.ArchiveTile{{ID: id, Data: data}},
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
		TileType:            pmtiles.TileTypeMVT,
		MinZoom:             z,
		MaxZoom:             z,
		MinLon:              -180, MinLat: -85, MaxLon: 180, MaxLat: 85,
	})
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fixture.pmtiles")
	if err := os.WriteFile(path, built.Bytes, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

// TestRun_WithNoArgumentsListsTheCommands covers the first thing anybody does
// with an unfamiliar program. An error with no list of what to type instead is
// a dead end.
func TestRun_WithNoArgumentsListsTheCommands(t *testing.T) {
	r := runCLI(t)
	if r.code != 2 {
		t.Errorf("exit code %d, want 2", r.code)
	}
	for _, want := range []string{"inspect", "tile", "geojson", "SOURCE"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the bare usage never mentions %q:\n%s", want, r.stderr)
		}
	}
}

// TestRun_HelpGoesToStdoutAndComplaintsToStderr keeps the two streams apart.
// Help is what the user asked for and must survive a pipe into a pager; an
// error is a complaint and must not end up in a file the user is building.
func TestRun_HelpGoesToStdoutAndComplaintsToStderr(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"inspect", "-h"}, {"tile", "-h"}, {"geojson", "-h"}} {
		r := runCLI(t, args...)
		if r.code != 0 {
			t.Errorf("%v: exit code %d, want 0", args, r.code)
		}
		if r.stdout == "" {
			t.Errorf("%v: printed no help to stdout", args)
		}
		if r.stderr != "" {
			t.Errorf("%v: wrote to stderr:\n%s", args, r.stderr)
		}
	}
}

// TestRun_EveryCommandHelpCarriesAWorkedExampleAndTheSourceNotice checks the
// two things the help has to do: show a command that can be typed as it
// stands, and say that the default source contacts somebody.
//
// The privacy notice is tested rather than trusted because it is the sentence
// most likely to be lost in a later tidy-up of the help text, and its absence
// is invisible: everything still works, and the user is simply no longer told
// where their coordinates went.
func TestRun_EveryCommandHelpCarriesAWorkedExampleAndTheSourceNotice(t *testing.T) {
	for _, cmd := range []string{"inspect", "tile", "geojson"} {
		r := runCLI(t, cmd, "-h")
		if !strings.Contains(r.stdout, "osmbase "+cmd+" ") {
			t.Errorf("%s -h shows no example of running it:\n%s", cmd, r.stdout)
		}
		if !strings.Contains(r.stdout, defaultSource) {
			t.Errorf("%s -h does not name the default archive", cmd)
		}
		if !strings.Contains(r.stdout, "third party") {
			t.Errorf("%s -h does not say that the default contacts a third party", cmd)
		}
	}
	for _, cmd := range []string{"tile", "geojson"} {
		r := runCLI(t, cmd, "-h")
		// A worked example with coordinates in it, so that the first run is a
		// copy and paste rather than a guess about argument order.
		if !strings.Contains(r.stdout, "--lat -33.8568 --lon 151.2153") {
			t.Errorf("%s -h has no worked example with coordinates:\n%s", cmd, r.stdout)
		}
	}
}

// TestRun_UnknownCommandSaysSo distinguishes a typo from a failure: exit 2 and
// the list of what does exist.
func TestRun_UnknownCommandSaysSo(t *testing.T) {
	r := runCLI(t, "render")
	if r.code != 2 {
		t.Errorf("exit code %d, want 2", r.code)
	}
	if !strings.Contains(r.stderr, `"render"`) || !strings.Contains(r.stderr, "geojson") {
		t.Errorf("stderr should name the unknown command and list the real ones:\n%s", r.stderr)
	}
}

// TestParseArgs_SourceComesBeforeOrAfterTheFlags covers both orders, because
// both read naturally and a program that accepted only one would be a thing to
// remember for no reason.
func TestParseArgs_SourceComesBeforeOrAfterTheFlags(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantSource string
		wantLat    float64
		wantZoom   int
	}{
		{"source first", []string{"a.pmtiles", "--lat", "-33.8568", "--lon", "151.2153"}, "a.pmtiles", -33.8568, defaultZoom},
		{"source last", []string{"--lat", "-33.8568", "--lon", "151.2153", "a.pmtiles"}, "a.pmtiles", -33.8568, defaultZoom},
		{"no source", []string{"--lat", "-33.8568", "--lon", "151.2153"}, "", -33.8568, defaultZoom},
		{"zoom given", []string{"--lat", "1", "--lon", "2", "--zoom", "15"}, "", 1, 15},
		{"single dash flags", []string{"-lat", "1", "-lon", "2", "-zoom", "9"}, "", 1, 9},
		{"equals form", []string{"--lat=-33.8568", "--lon=151.2153"}, "", -33.8568, defaultZoom},
		{"url as source", []string{"https://example.invalid/a.pmtiles", "--lat", "1", "--lon", "2"}, "https://example.invalid/a.pmtiles", 1, defaultZoom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var coords coordFlags
			fs := newFlagSet("tile", tileUsage)
			coords.bind(fs)
			source, err := parseArgs(fs, c.args, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", c.args, err)
			}
			if source != c.wantSource {
				t.Errorf("source = %q, want %q", source, c.wantSource)
			}
			if coords.lat != c.wantLat {
				t.Errorf("lat = %v, want %v", coords.lat, c.wantLat)
			}
			if coords.zoom != c.wantZoom {
				t.Errorf("zoom = %d, want %d", coords.zoom, c.wantZoom)
			}
		})
	}
}

// TestParseArgs_RefusesArgumentsItCannotMakeSenseOf covers the ways a command
// line can be wrong, each of which must be a sentence rather than a guess.
func TestParseArgs_RefusesArgumentsItCannotMakeSenseOf(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"two sources", []string{"a.pmtiles", "--lat", "1", "--lon", "2", "b.pmtiles"}, "twice"},
		{"three arguments", []string{"--lat", "1", "--lon", "2", "a.pmtiles", "b.pmtiles"}, "one SOURCE"},
		{"unknown flag", []string{"--latitude", "1"}, "not defined"},
		{"latitude that is not a number", []string{"--lat", "north"}, "invalid value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var coords coordFlags
			fs := newFlagSet("tile", tileUsage)
			coords.bind(fs)
			_, err := parseArgs(fs, c.args, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("parseArgs(%v) succeeded; want an error", c.args)
			}
			if !isUsageError(err) {
				t.Errorf("parseArgs(%v) gave %v, which is not a usage error", c.args, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// TestCoordFlags_RequireLatAndLonBecauseZeroZeroIsAPlace is the reason
// absence is read from which flags were SET rather than from their values.
//
// 0, 0 is in the Gulf of Guinea. A command that defaulted to it would answer a
// question nobody asked, with a tile of ocean, and look like it had worked.
func TestCoordFlags_RequireLatAndLonBecauseZeroZeroIsAPlace(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"neither given", []string{}, "--lat and --lon"},
		{"only latitude", []string{"--lat", "-33.8568"}, "--lon"},
		{"only longitude", []string{"--lon", "151.2153"}, "--lat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var coords coordFlags
			fs := newFlagSet("tile", tileUsage)
			coords.bind(fs)
			if _, err := parseArgs(fs, c.args, &bytes.Buffer{}); err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			err := coords.check(fs, "tile")
			if err == nil {
				t.Fatalf("check() accepted %v", c.args)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name the missing flag %q", err, c.want)
			}
		})
	}

	// Zero given explicitly is a coordinate and must be accepted.
	var coords coordFlags
	fs := newFlagSet("tile", tileUsage)
	coords.bind(fs)
	if _, err := parseArgs(fs, []string{"--lat", "0", "--lon", "0"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if err := coords.check(fs, "tile"); err != nil {
		t.Errorf("check() refused an explicit 0, 0: %v", err)
	}
}

// TestRun_InspectReportsTheArchivesShape checks the fields someone would paste
// into a bug report.
func TestRun_InspectReportsTheArchivesShape(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())
	r := runCLI(t, "inspect", path)
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		"format version", "tile type", "mvt", "internal compression", "gzip",
		"clustered", "zoom levels", "bounds", "root directory", "tile data",
		"root entries", "leaf pointers", "tile runs",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("inspect never mentions %q:\n%s", want, r.stdout)
		}
	}
	// A local archive costs no requests, so there is no traffic to report and
	// reporting "0 requests" would invite the number to be read as meaningful.
	if strings.Contains(r.stderr, "range requests") {
		t.Errorf("inspect of a local file reported network traffic:\n%s", r.stderr)
	}
}

// TestRun_TileReportsEveryLayerAndGeometryType checks the table the tile
// command exists for, against a fixture whose counts are known by
// construction: three layers, one point, one line and one polygon.
func TestRun_TileReportsEveryLayerAndGeometryType(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())
	r := runCLI(t, "tile", path, "--lat", "0", "--lon", "0", "--zoom", "0")
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{"0/0/0", "places", "roads", "water", "LAYER", "POLYGONS", "decoded"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("the tile report never mentions %q:\n%s", want, r.stdout)
		}
	}
	// The vertex count is the one number that is not just a feature count: the
	// three features hold 1 + 3 + (4 exterior + 4 hole) = 12 vertices.
	if !strings.Contains(r.stdout, "12") {
		t.Errorf("the tile report does not show the 12 vertices the fixture has:\n%s", r.stdout)
	}
}

// TestRun_TileWithTagsShowsTheSchema checks the --tags listing, which is how
// someone finds out what a tile schema offers before writing against it.
func TestRun_TileWithTagsShowsTheSchema(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())
	r := runCLI(t, "tile", path, "--lat", "0", "--lon", "0", "--zoom", "0", "--tags")
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{`layer "places"`, "name", `"Null Island"`, `layer "water"`, "kind", `"lake"`} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("--tags never shows %q:\n%s", want, r.stdout)
		}
	}
}

// TestRun_TileRefusesAZoomTheArchiveDoesNotHold is trap T4 as an error
// message: a zoom past the archive's maximum is not an empty place, it is a
// question this archive cannot be asked, and saying "no tile there" would send
// someone looking for missing data.
func TestRun_TileRefusesAZoomTheArchiveDoesNotHold(t *testing.T) {
	path := fixtureArchive(t, 0, 0, 0, worldTile())
	r := runCLI(t, "tile", path, "--lat", "0", "--lon", "0", "--zoom", "17")
	if r.code != 2 {
		t.Errorf("exit code %d, want 2", r.code)
	}
	if !strings.Contains(r.stderr, "0 to 0") {
		t.Errorf("the error should say which zooms the archive holds:\n%s", r.stderr)
	}
}

// TestRun_TileThatIsNotInTheArchiveExplainsWhere covers the answer that is not
// a failure: an archive legitimately holds no tile at most coordinates.
func TestRun_TileThatIsNotInTheArchiveExplainsWhere(t *testing.T) {
	// A fixture holding one zoom-2 tile in the north-west of the world, asked
	// about a coordinate in the south-east.
	path := fixtureArchive(t, 2, 0, 0, worldTile())
	r := runCLI(t, "tile", path, "--lat", "-33.8568", "--lon", "151.2153", "--zoom", "2")
	if r.code != 1 {
		t.Errorf("exit code %d, want 1", r.code)
	}
	for _, want := range []string{"holds no tile", "2/3/2", "151.2153"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the error should name the tile and the coordinate; %q is missing:\n%s", want, r.stderr)
		}
	}
}

// TestRun_SourceThatCannotBeOpened covers the three ways SOURCE goes wrong,
// each of which must say what to do instead.
func TestRun_SourceThatCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	notAnArchive := filepath.Join(dir, "notes.txt")
	// Longer than a PMTiles header, so that it is refused for what it says
	// rather than for being too short; both are errors, and the magic check is
	// the one worth covering here.
	if err := os.WriteFile(notAnArchive, bytes.Repeat([]byte("notes about the map\n"), 16), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		source   string
		wantCode int
		want     string
	}{
		{"no such file", filepath.Join(dir, "missing.pmtiles"), 1, "there is no file at"},
		{"a directory", dir, 1, "is a directory"},
		{"not an archive", notAnArchive, 1, "not a PMTiles archive"},
		{"a scheme this program does not speak", "s3://bucket/planet.pmtiles", 2, `"s3" scheme`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runCLI(t, "inspect", c.source)
			if r.code != c.wantCode {
				t.Errorf("exit code %d, want %d (stderr: %s)", r.code, c.wantCode, r.stderr)
			}
			if !strings.Contains(r.stderr, c.want) {
				t.Errorf("stderr does not contain %q:\n%s", c.want, r.stderr)
			}
		})
	}
}
