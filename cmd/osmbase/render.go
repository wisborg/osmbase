package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/render"
)

// A PMTiles reader IS a tile source, with no adapter in between.
//
// That is worth asserting rather than relying on. render declares the
// interface it needs -- one method, "have you got the tile at z/x/y" -- so that
// the renderer depends on nothing that could fetch, and pmtiles arrived
// independently at the same signature because it is the same question. The
// assertion is here, in the program that joins them, because neither package
// should know about the other; if either side moves, this line is what says so
// at compile time instead of at the next render.
var _ render.TileSource = (*pmtiles.Reader)(nil)

// maxRenderPixels caps the output image.
//
// An image is four bytes a pixel and the rasterizer holds a float32 buffer of
// the same shape, so a typo in --width is otherwise an out-of-memory kill with
// no explanation. Sixty-four megapixels is comfortably past 8K and is a number
// a mistake does not reach by accident.
const maxRenderPixels = 64 << 20

func renderUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase render [SOURCE] --lat LAT --lon LON [--zoom N] [--width N] [--height N] [--palette NAME] [--out FILE]

Draw the map around a coordinate and write it to a PNG. The image is centred on
--lat/--lon and covers whatever ground --width by --height pixels hold at
--zoom, which is the ordinary slippy-map zoom: one tile is 256 pixels, so at
zoom 14 a 1024 by 768 image is four tiles by three.

Tiles the archive does not hold are drawn from a shallower tile where there is
one, which is a sharp map with less detail in it, and hatched where there is
not. Both are reported below the image.

examples:
  osmbase render --lat -33.8568 --lon 151.2153 --out sydney.png
      the default archive, over the network, at zoom 14

  osmbase render ./sydney.pmtiles --lat -33.8568 --lon 151.2153 --zoom 15 \
      --width 1920 --height 1080 --palette dark --out sydney.png
      a 1080p dark map from a local archive, contacting nobody

`)
	printFlags(w, fs)
	fmt.Fprint(w, "\n"+sourceHelp)
}

func renderCommand(args []string, stdout, stderr io.Writer) error {
	var (
		coords        coordFlags
		width, height int
		palette       string
		out           string
	)
	fs := newFlagSet("render", renderUsage)
	coords.bind(fs)
	fs.IntVar(&width, "width", 1024, "width of the output image in pixels")
	fs.IntVar(&height, "height", 768, "height of the output image in pixels")
	fs.StringVar(&palette, "palette", "light", "colours to draw with: light or dark")
	fs.StringVar(&out, "out", "map.png", "file to write the PNG to")

	source, err := parseArgs(fs, args, stdout)
	if err != nil {
		return err
	}
	if err := coords.check(fs, "render"); err != nil {
		return err
	}
	if width <= 0 || height <= 0 {
		return usageErrorf("--width %d --height %d is not an image", width, height)
	}
	if width*height > maxRenderPixels {
		return usageErrorf("--width %d --height %d is %d megapixels, and this command draws at most %d",
			width, height, width*height>>20, maxRenderPixels>>20)
	}
	colours, err := paletteNamed(palette)
	if err != nil {
		return err
	}

	a, err := openArchive(source, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	defer a.reportTraffic(stderr)
	if err := a.requireVectorTiles(); err != nil {
		return err
	}

	z := uint8(coords.zoom)
	view, err := viewAround(z, coords.lon, coords.lat, width, height)
	if err != nil {
		return err
	}

	// A zoom deeper than the archive holds is not refused here, unlike in the
	// reading commands. Overzoom is the design's answer to it -- draw the
	// deepest ancestor there is, sharp and less detailed -- and the result says
	// how much of the picture came out that way. Saying so in advance is what
	// stops the report reading like a fault.
	if h := a.Header(); z > h.MaxZoom {
		fmt.Fprintf(stderr, "osmbase: --zoom %d is deeper than the %d this archive holds, so the map is drawn from zoom %d and overzoomed\n", z, h.MaxZoom, h.MaxZoom)
	}

	r, err := render.New(a.Reader, render.Options{
		Style:       render.BasemapStyle(),
		Palette:     colours,
		Attribution: attributionOf(a, stderr),
	})
	if err != nil {
		return err
	}

	res, err := r.Render(context.Background(), view)
	if err != nil {
		if errors.Is(err, render.ErrNoCoverage) {
			return fmt.Errorf("%w. %s holds no tile anywhere near latitude %s, longitude %s; try a coordinate inside the area it covers, which \"osmbase inspect\" prints",
				err, a.name, formatCoord(coords.lat), formatCoord(coords.lon))
		}
		return err
	}

	if err := writePNG(out, res); err != nil {
		return err
	}
	writeRenderReport(stdout, out, view, res, palette)
	return nil
}

// paletteNamed resolves --palette.
//
// The named palettes are the library's own placeholders. A consumer with a
// theme supplies its colours directly rather than picking one of these, which
// is the whole reason the palette is a caller's value and not part of the
// style.
func paletteNamed(name string) (render.Palette, error) {
	switch name {
	case "light":
		return render.LightPalette(), nil
	case "dark":
		return render.DarkPalette(), nil
	}
	return render.Palette{}, usageErrorf("--palette %q is not one this command knows; it has light and dark", name)
}

// viewAround builds the view of width by height pixels centred on a coordinate
// at a given zoom.
//
// The library takes a rectangle in degrees, because that is the question a
// consumer asks: it has a route and knows the ground it wants on the screen. A
// person at a terminal has a place and a zoom instead, so the conversion lives
// here rather than in the library, where a second way of saying where to look
// would be a second way for the two to disagree.
//
// Zoom z puts 256*2^z pixels across the world, so half the image is
// width/(2*256*2^z) of the world either side of the centre.
func viewAround(z uint8, lon, lat float64, width, height int) (render.View, error) {
	if z > mercator.MaxZoom {
		return render.View{}, usageErrorf("--zoom %d is not a zoom level; they run from 0 to %d", z, mercator.MaxZoom)
	}
	world := 256 * math.Exp2(float64(z))
	cx, cy := mercator.Project(lon, lat)
	west, north := mercator.Unproject(cx-float64(width)/(2*world), cy-float64(height)/(2*world))
	east, south := mercator.Unproject(cx+float64(width)/(2*world), cy+float64(height)/(2*world))

	// A longitude off the end of the world means the image is wider than the
	// world is at this zoom. The library refuses such a rectangle -- it would
	// have to be drawn as two views -- and the useful thing to say here is
	// which two numbers are in conflict.
	if west < -180 || east > 180 {
		return render.View{}, usageErrorf("--width %d at --zoom %d is wider than the whole world, which is %.0f pixels across at that zoom; use a deeper --zoom or a narrower image",
			width, z, world)
	}

	// The same question on the other axis, and it has to be asked HERE rather
	// than left to the projection, because the two directions fail differently.
	//
	// Off the end of the world in longitude produces a number outside [-180,
	// 180] that nothing downstream will accept. Off the end in latitude does
	// not: Unproject runs past the Mercator cut happily, Bounds.validate
	// accepts anything up to 90, and then the projection CLAMPS -- so the
	// rectangle silently loses height and the scale comes out larger than the
	// zoom that was asked for. Measured at 1024x768: --lat 80 --zoom 3 asks
	// for 768 pixels of world above the centre and gets 614, so the picture is
	// drawn at zoom 3.32 with the requested coordinate nowhere near the middle
	// of it, and every number in the report is the post-clamp one and reads as
	// correct.
	//
	// The check is on the projected axis rather than on latitude, because the
	// clamp is what it is guarding against and the clamp lives there.
	if top := cy - float64(height)/(2*world); top < 0 {
		return render.View{}, usageErrorf("--height %d at --zoom %d reaches past the north edge of the map at --lat %g; the world is %.0f pixels tall at that zoom and only %.0f of them lie north of there, so use a deeper --zoom, a shorter image, or a latitude further from the pole",
			height, z, lat, world, cy*world)
	}
	if bottom := cy + float64(height)/(2*world); bottom > 1 {
		return render.View{}, usageErrorf("--height %d at --zoom %d reaches past the south edge of the map at --lat %g; the world is %.0f pixels tall at that zoom and only %.0f of them lie south of there, so use a deeper --zoom, a shorter image, or a latitude further from the pole",
			height, z, lat, world, (1-cy)*world)
	}

	return render.View{
		Bounds: render.Bounds{West: west, South: south, East: east, North: north},
		Width:  width, Height: height,
	}, nil
}

// attributionOf reads the credit string out of the archive's metadata.
//
// It is DATA and not a constant on purpose: the obligation belongs to whoever
// made the tiles, the string is written into the archive by them, and a credit
// spelled into this program's source would go on being printed after somebody
// pointed --source at a different archive. An archive that declares none says
// so out loud rather than being given one.
func attributionOf(a *archive, stderr io.Writer) string {
	raw, err := a.Metadata()
	if err != nil {
		fmt.Fprintf(stderr, "osmbase: could not read the archive's metadata, so the render carries no attribution: %v\n", err)
		return ""
	}
	var meta struct {
		Attribution string `json:"attribution"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			fmt.Fprintf(stderr, "osmbase: the archive's metadata is not JSON this can read, so the render carries no attribution: %v\n", err)
			return ""
		}
	}
	if meta.Attribution == "" {
		fmt.Fprintf(stderr, "osmbase: this archive declares no attribution. A rendered map is still somebody's data:\n")
		fmt.Fprintf(stderr, "osmbase:   credit whoever made it wherever you show the image.\n")
	}
	return meta.Attribution
}

// writePNG writes the image, to a temporary name first.
//
// The rename is not ceremony. A render takes seconds and is interrupted by
// impatience as often as by anything else, and a half-written PNG left under
// the name the user asked for is a file that opens, shows part of a map, and
// looks like a rendering bug.
func writePNG(path string, res *render.Result) error {
	tmp := path + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if err := png.Encode(f, res.Image); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("encoding %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}

// writeRenderReport says what was drawn, including the parts that are not the
// map the user asked for.
//
// Overzoomed and hatched ground are reported whether or not there is any,
// because "0 tiles" is the answer that makes the other numbers mean something.
// An overzoomed map looks sharp and complete, and the only way to know the
// detail is missing is to be told.
func writeRenderReport(w io.Writer, out string, v render.View, res *render.Result, palette string) {
	var t table
	t.rightAlign(1)
	t.row("file", out)
	t.row("image", fmt.Sprintf("%d x %d pixels, %s palette", res.Image.Bounds().Dx(), res.Image.Bounds().Dy(), palette))
	t.row("bounds", fmt.Sprintf("west %s, south %s, east %s, north %s",
		formatCoord(v.Bounds.West), formatCoord(v.Bounds.South),
		formatCoord(v.Bounds.East), formatCoord(v.Bounds.North)))
	t.row("zoom", fmt.Sprintf("%d (%s continuous)", res.Zoom, strconv.FormatFloat(res.ContinuousZoom, 'f', 2, 64)))
	t.blank()
	t.row("tiles", fmt.Sprintf("%d drawn of %d covering the view", res.TilesDrawn, res.TilesRequested))
	t.row("covered", percent(res.Covered))
	t.row("overzoomed", fmt.Sprintf("%s of the image, drawn from a shallower tile", percent(res.Overzoomed)))
	t.row("no data", fmt.Sprintf("%s of the image, hatched in %d rectangles", percent(1-res.Covered), len(res.Gaps)))
	if res.Attribution != "" {
		t.blank()
		t.row("credit", res.Attribution)
	}
	t.write(w)
}

func percent(f float64) string {
	return strconv.FormatFloat(f*100, 'f', 1, 64) + "%"
}
