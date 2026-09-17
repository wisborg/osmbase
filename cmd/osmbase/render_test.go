package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

// decodePNG reads back what the command wrote.
func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the rendered PNG: %v", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return img
}

func sameColour(t *testing.T, img image.Image, x, y int, want color.RGBA, why string) {
	t.Helper()
	r, g, b, _ := img.At(x, y).RGBA()
	got := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
	const tol = 2
	near := func(p, q uint8) bool { return int(p)-int(q) <= tol && int(q)-int(p) <= tol }
	if !near(got.R, want.R) || !near(got.G, want.G) || !near(got.B, want.B) {
		t.Errorf("pixel (%d, %d) is %v, want %v: %s", x, y, got, want, why)
	}
}

// TestRender_WritesAPngOfTheArchivesGeometry is the end-to-end check that the
// first picture is a picture of the data.
//
// The fixture is the whole world in one zoom-0 tile, and the view is that tile
// on 256 by 256 pixels, so one tile unit is 256/4096 = a sixteenth of a pixel.
// The lake runs from tile unit 1024 to 3072, which is pixels 64 to 192, and the
// island in it from 1536 to 2560, which is pixels 96 to 160.
//
// So the centre of the image is INSIDE the island and must not be water. That
// is the whole of trap T13 travelling through the entire stack at once: the
// hole is a ring wound against its exterior, it stays that way through the
// decoder, the clip and the path, and the rasterizer subtracts it. Wound the
// other way, or filled in a separate pass, the island fills solid and the
// picture is a lake with no island in it -- which still looks like a map.
func TestRender_WritesAPngOfTheArchivesGeometry(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	out := filepath.Join(t.TempDir(), "map.png")

	r := runCLI(t, "render", archive,
		"--lat", "0", "--lon", "0", "--zoom", "0",
		"--width", "256", "--height", "256", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr:\n%s", r.code, r.stderr)
	}

	img := decodePNG(t, out)
	if b := img.Bounds(); b.Dx() != 256 || b.Dy() != 256 {
		t.Fatalf("the PNG is %d by %d, want 256 by 256", b.Dx(), b.Dy())
	}

	p := render.LightPalette()
	sameColour(t, img, 75, 128, p.Water, "tile unit 1200 is inside the lake and outside the island")
	sameColour(t, img, 128, 128, p.Background, "the centre of the image is inside the island, which is a hole in the lake")
	sameColour(t, img, 20, 128, p.Background, "tile unit 320 is outside the lake, and this archive has no land polygon")

	for _, want := range []string{"zoom", "covered", "overzoomed", "no data", out} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("the report never mentions %q:\n%s", want, r.stdout)
		}
	}
}

// TestRender_OverzoomIsDrawnAndSaidOutLoud covers the other half of the report.
//
// The archive holds zoom 0 and nothing else, and the view asks for zoom 3, so
// every square of it comes from four levels up. The picture is sharp, which is
// exactly why the command has to say so: there is no way to tell by looking.
func TestRender_OverzoomIsDrawnAndSaidOutLoud(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	out := filepath.Join(t.TempDir(), "map.png")

	r := runCLI(t, "render", archive,
		"--lat", "10", "--lon", "10", "--zoom", "3",
		"--width", "256", "--height", "256", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr:\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "overzoomed") {
		t.Errorf("stderr should warn that the zoom is deeper than the archive holds:\n%s", r.stderr)
	}
	if !strings.Contains(r.stdout, "100.0% of the image, drawn from a shallower tile") {
		t.Errorf("the report should say the whole image was overzoomed:\n%s", r.stdout)
	}
}

// TestRender_RefusesWhatItCannotDraw checks that each mistake is exit 2 and
// says which two numbers or which word are in conflict.
func TestRender_RefusesWhatItCannotDraw(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	out := filepath.Join(t.TempDir(), "map.png")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no coordinate", []string{"render", archive}, "needs --lat and --lon"},
		{"unknown palette", []string{"render", archive, "--lat", "0", "--lon", "0", "--palette", "sepia", "--out", out}, "light and dark"},
		{"wider than the world", []string{"render", archive, "--lat", "0", "--lon", "0", "--zoom", "0", "--width", "4000", "--out", out}, "wider than the whole world"},
		{"no image", []string{"render", archive, "--lat", "0", "--lon", "0", "--width", "0", "--out", out}, "is not an image"},

		// The y axis, which was missed when the x axis was written, and fails
		// in a way the x axis does not: nothing downstream rejects a latitude
		// past the Mercator cut, the projection simply clamps, so the view
		// silently loses height and is drawn at a larger scale than the zoom
		// asked for. Measured before the guard existed, --lat 80 --zoom 3 at
		// 1024x768 drew at continuous zoom 3.32 with the requested coordinate
		// 96 pixels above the middle of the image, and reported the
		// post-clamp bounds as though they were what had been asked for.
		{"taller than the world to the north", []string{"render", archive, "--lat", "80", "--lon", "0", "--zoom", "3", "--height", "768", "--out", out}, "past the north edge"},
		{"taller than the world to the south", []string{"render", archive, "--lat", "-80", "--lon", "0", "--zoom", "3", "--height", "768", "--out", out}, "past the south edge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, tc.args...)
			if r.code != 2 {
				t.Errorf("exit code %d, want 2 for a mistake in what was typed\nstderr:\n%s", r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, tc.want) {
				t.Errorf("stderr should mention %q:\n%s", tc.want, r.stderr)
			}
		})
	}
}

// TestRender_DoesNotLeaveAPartialFileBehind checks the rename.
//
// A render is slow enough to be interrupted, and a half-written PNG under the
// name the user asked for opens, shows part of a map and reads as a rendering
// bug. Here the failure is a directory that does not exist, which is the same
// shape of failure with a deterministic cause.
func TestRender_DoesNotLeaveAPartialFileBehind(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	dir := t.TempDir()
	out := filepath.Join(dir, "nowhere", "map.png")

	r := runCLI(t, "render", archive, "--lat", "0", "--lon", "0", "--zoom", "0", "--out", out)
	if r.code == 0 {
		t.Fatalf("writing into a directory that does not exist succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "partial") {
			t.Errorf("a partial file was left behind: %s", e.Name())
		}
	}
}

// manifests builds the source list chooseSource reads, with only the two
// fields it looks at.
func manifests(pairs ...string) []slice.Manifest {
	var out []slice.Manifest
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, slice.Manifest{ID: pairs[i], Source: pairs[i+1]})
	}
	return out
}

// TestChooseSource_NamesTheWayOutInsteadOfGivingUp covers the case that
// blocked a real render: a shared store holding two archives.
//
// The default store root is the same path for every program built on this
// library, which is the point of it -- a second consumer fetching the same
// area should reuse what is already on disk rather than download it again. A
// machine that has fetched two different archives therefore has two sources
// in one directory, and that is the arrangement working. Refusing it and
// advising "a store per archive" gave up the sharing, and was advice the user
// could not always follow: the second archive may have been fetched by a
// different program entirely.
//
// What the refusal was right about is that picking silently is worse. Each
// render reports its own provenance, so a silent pick produces a map whose
// report names an archive it did not draw from -- a wrong answer in the field
// somebody consults precisely because they are unsure. So ambiguity is still
// refused; it just carries the way out now, and every refusal here is
// asserted to name both the flag and the IDs, since a refusal that does not
// say what to type next is the thing being fixed.
func TestChooseSource_NamesTheWayOutInsteadOfGivingUp(t *testing.T) {
	two := manifests(
		"a1b2c3d4e5f60718", "https://example.test/protomaps.pmtiles",
		"00ff11ee22dd33cc", "/data/custom-build.pmtiles",
	)

	t.Run("one archive needs no flag", func(t *testing.T) {
		got, err := chooseSource("/store", manifests("a1b2c3d4e5f60718", "only.pmtiles"), "")
		if err != nil {
			t.Fatalf("chooseSource: %v", err)
		}
		if got.Source != "only.pmtiles" {
			t.Errorf("chose %q, want the only archive", got.Source)
		}
	})

	t.Run("two archives and no flag says how to choose", func(t *testing.T) {
		_, err := chooseSource("/store", two, "")
		if err == nil {
			t.Fatal("two archives were resolved without the user choosing")
		}
		for _, want := range []string{"--archive", "a1b2c3d4e5f60718", "00ff11ee22dd33cc"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal never mentions %q, so it cannot be acted on:\n%v", want, err)
			}
		}
	})

	t.Run("chosen by part of a name", func(t *testing.T) {
		got, err := chooseSource("/store", two, "protomaps")
		if err != nil {
			t.Fatalf("chooseSource: %v", err)
		}
		if got.ID != "a1b2c3d4e5f60718" {
			t.Errorf("chose %s, want the archive whose name contains it", got.ID)
		}
	})

	t.Run("chosen by an ID prefix", func(t *testing.T) {
		got, err := chooseSource("/store", two, "00ff")
		if err != nil {
			t.Fatalf("chooseSource: %v", err)
		}
		if got.ID != "00ff11ee22dd33cc" {
			t.Errorf("chose %s, want the archive whose ID starts with it", got.ID)
		}
	})

	t.Run("a name matching both is refused rather than guessed", func(t *testing.T) {
		_, err := chooseSource("/store", manifests(
			"1111111111111111", "/data/planet-a.pmtiles",
			"2222222222222222", "/data/planet-b.pmtiles",
		), "planet")
		if err == nil {
			t.Fatal("an ambiguous --archive was resolved by guessing")
		}
		for _, want := range []string{"1111111111111111", "2222222222222222"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not list %q as a candidate:\n%v", want, err)
			}
		}
	})

	t.Run("no match lists what is actually there", func(t *testing.T) {
		_, err := chooseSource("/store", two, "openmaptiles")
		if err == nil {
			t.Fatal("a name matching nothing was accepted")
		}
		if !strings.Contains(err.Error(), "a1b2c3d4e5f60718") {
			t.Errorf("the refusal does not say what the store does hold:\n%v", err)
		}
	})

	t.Run("an empty store says how to fill it", func(t *testing.T) {
		_, err := chooseSource("/store", nil, "")
		if err == nil {
			t.Fatal("an empty store was resolved")
		}
		if !strings.Contains(err.Error(), "osmbase fetch") {
			t.Errorf("the refusal does not say how to fill the store:\n%v", err)
		}
	})
}

// TestWriteRenderReport_CreditsInTextRatherThanMarkup pins the last place the
// raw attribution reached a person.
//
// The credit drawn into the PNG has been converted since it was added; this
// row had not, so a terminal running "osmbase render" printed an anchor tag.
// That discharges nothing -- the obligation is to name whose data drew the
// map, and a line of HTML names it to nobody reading a terminal.
func TestWriteRenderReport_CreditsInTextRatherThanMarkup(t *testing.T) {
	var buf strings.Builder
	writeRenderReport(&buf, "map.png", render.View{}, &render.Result{
		Image:       image.NewRGBA(image.Rect(0, 0, 2, 2)),
		Attribution: `<a href="https://example.test/copyright">&copy; OpenStreetMap</a>`,
	}, "light")

	out := buf.String()
	if !strings.Contains(out, "© OpenStreetMap") {
		t.Errorf("the report does not credit anybody readably:\n%s", out)
	}
	for _, markup := range []string{"<a ", "href=", "&copy;"} {
		if strings.Contains(out, markup) {
			t.Errorf("the report prints %q, which credits nobody a person can read:\n%s", markup, out)
		}
	}
}
