package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
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
       osmbase render [SOURCE] --bbox W,S,E,N [...]
       osmbase render [SOURCE] --place NAME [--place-level country|region] [...]

Draw the map around a coordinate and write it to a PNG. The image is centred on
--lat/--lon and covers whatever ground --width by --height pixels hold at
--zoom, which is the ordinary slippy-map zoom: one tile is 256 pixels, so at
zoom 14 a 1024 by 768 image is four tiles by three. --bbox names a rectangle
instead, and the zoom is chosen to hold all of it. --place names a country or
region, looked up in the Natural Earth outlines in the store ("osmbase
boundaries" fetches them, once); a name that could mean several places is
refused with the list, never guessed.

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
		store         string
		archive       string
		out           string
		labels        string
		bbox          string
		place         placeFlags
	)
	fs := newFlagSet("render", renderUsage)
	coords.bind(fs)
	fs.IntVar(&width, "width", 1024, "width of the output image in pixels")
	fs.IntVar(&height, "height", 768, "height of the output image in pixels")
	fs.StringVar(&palette, "palette", "light", "colours to draw with: light, dark, or dark-linework -- the dark map with its landuse fills dropped, leaving roads, rail and boundaries over near-black with water as the only fill")
	fs.StringVar(&store, "store", "", "draw from a store filled by \"osmbase fetch\" instead of from an archive; nothing reaches the network")
	fs.StringVar(&archive, "archive", "", "which archive in the store to draw from, by ID or by part of its name; only needed when the store holds more than one")
	fs.StringVar(&labels, "labels", "normal", labelHelp())
	fs.StringVar(&out, "out", "map.png", "file to write the PNG to")
	fs.StringVar(&bbox, "bbox", "", "draw this rectangle instead of the ground around --lat/--lon, as west,south,east,north in degrees; "+
		"the zoom is the deepest that holds all of it, unless --zoom says otherwise")
	place.bind(fs)

	source, err := parseArgs(fs, args, stdout)
	if err != nil {
		return err
	}
	if width <= 0 || height <= 0 {
		return usageErrorf("--width %d --height %d is not an image", width, height)
	}
	if width*height > maxRenderPixels {
		return usageErrorf("--width %d --height %d is %d megapixels, and this command draws at most %d",
			width, height, width*height>>20, maxRenderPixels>>20)
	}
	fitted, err := renderTarget(fs, &coords, bbox, place, store, width, height)
	if err != nil {
		return err
	}
	if fitted != "" {
		fmt.Fprintf(stdout, "%-12s %s\n", "fitted", fitted)
	}
	colours, err := paletteNamed(palette)
	if err != nil {
		return err
	}
	style, err := labelStyle(render.BasemapStyle(), labels)
	if err != nil {
		return err
	}

	// A store and an archive are two different things to draw from, and the
	// difference is the point of the store existing: reading one contacts
	// nobody. They are separate flags rather than one SOURCE that guesses,
	// because guessing wrong here means quietly reaching the network on a
	// machine the user believed was offline.
	if store != "" {
		if source != "" {
			return usageErrorf("--store and a SOURCE are two different places to read from; give one or the other")
		}
		return renderFromStore(store, archive, coords, width, height, colours, style, palette, out, stdout, stderr)
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
	if h := a.Reader().Header(); z > h.MaxZoom {
		fmt.Fprintf(stderr, "osmbase: --zoom %d is deeper than the %d this archive holds, so the map is drawn from zoom %d and overzoomed\n", z, h.MaxZoom, h.MaxZoom)
	}

	r, err := render.New(a.Reader(), render.Options{
		Style:        style,
		Palette:      colours,
		Attribution:  attributionOf(a, stderr),
		LabelFace:    labelFace(),
		LabelFaceFor: labelFaceFor,
	})
	if err != nil {
		return err
	}

	res, err := r.Render(context.Background(), view)
	if err != nil {
		if errors.Is(err, render.ErrNoCoverage) {
			return fmt.Errorf("%w. %s holds no tile anywhere near latitude %s, longitude %s; try a coordinate inside the area it covers, which \"osmbase inspect\" prints",
				err, a.Name(), formatCoord(coords.lat), formatCoord(coords.lon))
		}
		return err
	}

	if err := writePNG(out, res); err != nil {
		return err
	}
	writeRenderReport(stdout, out, view, res, palette)
	return nil
}

// renderTarget settles where a render looks: --lat/--lon, or a rectangle to
// fit. It fills coords either way, so everything after it has one shape of
// question, and it returns a line for the report when the zoom was chosen
// rather than given.
func renderTarget(fs *flag.FlagSet, coords *coordFlags, bbox string, place placeFlags, store string, width, height int) (string, error) {
	given := 0
	for _, g := range []bool{bbox != "", place.name != "", flagGiven(fs, "lat") || flagGiven(fs, "lon")} {
		if g {
			given++
		}
	}
	if given > 1 {
		return "", usageErrorf("--place, --bbox and --lat/--lon are three ways to say where to look; give one")
	}
	if bbox == "" && place.name == "" {
		if place.level != "" {
			return "", usageErrorf("--place-level narrows --place, which was not given")
		}
		return "", coords.check(fs, "render")
	}

	var (
		b    slice.Bounds
		what string
		err  error
	)
	if place.name != "" {
		// The names are in the store's boundary files, whichever store the
		// render then draws from: --store if given, the default store
		// otherwise, which is where "osmbase boundaries" puts them.
		root := store
		if root == "" {
			if root, err = slice.DefaultRoot(); err != nil {
				return "", fmt.Errorf("finding the default store for --place: %w; pass --store to say where the boundaries are", err)
			}
		}
		c, err := place.resolve(root)
		if err != nil {
			return "", err
		}
		b, what = placeBounds(c), placeReport(c)
	} else {
		if b, err = parseBBox(bbox); err != nil {
			return "", err
		}
		what = "--bbox"
	}
	z, lat, lon, cropped, err := fitZoom(b, width, height)
	if err != nil {
		return "", err
	}
	coords.lat, coords.lon = lat, lon
	if flagGiven(fs, "zoom") {
		// Given, so kept -- a rectangle at a deeper zoom than fits is a
		// crop, which is a reasonable thing to ask for -- but checked as a
		// zoom the way check would have.
		if coords.zoom < 0 || coords.zoom > mercator.MaxZoom {
			return "", usageErrorf("--zoom %d is not a zoom level; they run from 0 to %d", coords.zoom, mercator.MaxZoom)
		}
		return fmt.Sprintf("%s, centred at %s, %s, at the --zoom given", what, formatCoord(lat), formatCoord(lon)), nil
	}
	coords.zoom = z
	if cropped {
		return fmt.Sprintf("%s, at zoom %d, cropped: it is wider or taller than the world is at the zoom that would hold it", what, z), nil
	}
	return fmt.Sprintf("%s, at zoom %d, the deepest that holds all of it", what, z), nil
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
	case "dark-linework":
		return render.DarkLineworkPalette(), nil
	}
	return render.Palette{}, usageErrorf("--palette %q is not one this command knows; it has light, dark and dark-linework", name)
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
	raw, err := a.Reader().Metadata()
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
	// The credit goes into the image before it is encoded, never after: the
	// file on disk is what carries the obligation. See drawCredit.
	drawCredit(res.Image, res.Attribution)
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
	// Converted, like the credit drawn into the image and for the same
	// reason: the archive writes its attribution as HTML because in a browser
	// the credit is a link, and an anchor tag printed into a terminal credits
	// nobody a person can read. This row was the one place the raw string
	// still reached a human.
	if credit := render.PlainCredit(res.Attribution); credit != "" {
		t.blank()
		t.row("credit", credit)
	}
	t.write(w)
}

func percent(f float64) string {
	return strconv.FormatFloat(f*100, 'f', 1, 64) + "%"
}

// renderFromStore draws from a filled store instead of an archive.
//
// The whole of its value is in what it does NOT do: there is no range reader
// here, no URL, and nothing that could contact anyone. slice imports neither
// acquire nor net/http, so "this render is offline" is a property of the
// import graph rather than a promise in a comment.
func renderFromStore(root, archive string, coords coordFlags, width, height int, colours render.Palette, style render.Style, palette, out string, stdout, stderr io.Writer) error {
	st, err := slice.Open(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("there is no store at %s; fill one first with \"osmbase fetch --store %s\"", root, root)
		}
		return fmt.Errorf("opening the store at %s: %w", root, err)
	}
	sources, err := st.Sources()
	if err != nil {
		return err
	}
	chosen, err := chooseSource(root, sources, archive)
	if err != nil {
		return err
	}
	src, err := st.Source(chosen.ID)
	if err != nil {
		return err
	}

	z := uint8(coords.zoom)
	view, err := viewAround(z, coords.lon, coords.lat, width, height)
	if err != nil {
		return err
	}

	r, err := render.New(src, render.Options{
		Style:        style,
		Palette:      colours,
		Attribution:  sources[0].Attribution,
		LabelFace:    labelFace(),
		LabelFaceFor: labelFaceFor,
	})
	if err != nil {
		return err
	}
	res, err := r.Render(context.Background(), view)
	if err != nil {
		if errors.Is(err, render.ErrNoCoverage) {
			return fmt.Errorf("%w. The store at %s holds nothing near latitude %s, longitude %s; fetch that area first",
				err, root, formatCoord(coords.lat), formatCoord(coords.lon))
		}
		return err
	}
	if err := writePNG(out, res); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%-12s %s\n", "store", root)
	writeRenderReport(stdout, out, view, res, palette)
	return nil
}

// chooseSource picks which archive in a store to draw from.
//
// A store holding one archive is the ordinary case and needs no flag. More
// than one is what a SHARED store looks like: the default root is the same
// path for every program that uses this library, so a machine that has
// fetched Protomaps for one project and a different build for another has two
// sources in one directory, and that is the arrangement working rather than
// failing. This used to refuse outright and advise a store per archive, which
// is the wrong advice -- it gives up the sharing the default root exists for,
// and it is not even possible for a user whose second archive was fetched by
// another program.
//
// What must NOT happen is picking one silently. Every render reports its own
// provenance, and a report naming the archive the render did not draw from is
// worse than a refusal: it is a wrong answer in the field somebody consults
// precisely when they are unsure. So the refusal stays for the ambiguous
// case; it just carries the way out now.
//
// want matches an ID, an ID prefix, or any part of the archive's name, so
// "protomaps" is usually enough and the full ID is always available. A match
// that could mean two archives is refused for the same reason silence is.
func chooseSource(root string, sources []slice.Manifest, want string) (slice.Manifest, error) {
	if len(sources) == 0 {
		return slice.Manifest{}, fmt.Errorf("the store at %s is empty; fill it with \"osmbase fetch --store %s\"", root, root)
	}
	if want == "" {
		if len(sources) == 1 {
			return sources[0], nil
		}
		return slice.Manifest{}, fmt.Errorf("the store at %s holds %d archives and this command draws from one; add --archive with an ID or part of a name:\n%s",
			root, len(sources), describeSources(sources))
	}

	var hits []slice.Manifest
	for _, m := range sources {
		if m.ID == want {
			return m, nil
		}
		if strings.HasPrefix(m.ID, want) || strings.Contains(strings.ToLower(m.Source), strings.ToLower(want)) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return slice.Manifest{}, fmt.Errorf("the store at %s holds no archive matching --archive %q:\n%s",
			root, want, describeSources(sources))
	default:
		return slice.Manifest{}, fmt.Errorf("--archive %q matches %d of the archives in %s; use an ID or a longer name:\n%s",
			want, len(hits), root, describeSources(hits))
	}
}

// describeSources lists archives the way the user has to name them: the ID
// first, because it is the one form that is always unambiguous.
func describeSources(sources []slice.Manifest) string {
	var b strings.Builder
	for _, m := range sources {
		fmt.Fprintf(&b, "  %s  %s\n", m.ID, m.Source)
	}
	return strings.TrimRight(b.String(), "\n")
}
