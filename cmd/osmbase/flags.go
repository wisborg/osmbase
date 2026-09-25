package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/slice"
)

// defaultZoom is the zoom used when none is given.
//
// 14 is chosen so that --lat and --lon alone do something worth looking at:
// roads, buildings, water and landuse are all present, and one tile is about
// two kilometres across at mid latitudes, which is a neighbourhood rather than
// a city or a street corner. The public builds this reads stop at 15, so it is
// also one step back from the edge.
const defaultZoom = 14

// usageError marks a mistake in what was typed rather than a failure to carry
// it out. run turns it into exit status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usageErrorf(format string, a ...any) error {
	return usageError{msg: fmt.Sprintf(format, a...)}
}

func isUsageError(err error) bool {
	var u usageError
	return errors.As(err, &u)
}

// coordFlags are the flags naming a place: every command but inspect takes
// them.
type coordFlags struct {
	lat, lon float64
	zoom     int
}

func (c *coordFlags) bind(fs *flag.FlagSet) {
	fs.Float64Var(&c.lat, "lat", 0, "latitude in degrees, north positive (required)")
	fs.Float64Var(&c.lon, "lon", 0, "longitude in degrees, east positive (required)")
	fs.IntVar(&c.zoom, "zoom", defaultZoom, "zoom level of the tile to read")
}

// check reports a coordinate that was not given or cannot be one.
//
// Absence is read from fs.Visit rather than from the value, because 0, 0 is a
// real coordinate -- it is in the Gulf of Guinea -- and defaulting silently to
// it would answer a question nobody asked with an ocean tile. The zoom is
// checked here only for being a zoom at all; whether the ARCHIVE holds it is
// checked against the header, where the error can say what it does hold.
func (c *coordFlags) check(fs *flag.FlagSet, command string) error {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var missing []string
	if !given["lat"] {
		missing = append(missing, "--lat")
	}
	if !given["lon"] {
		missing = append(missing, "--lon")
	}
	if len(missing) > 0 {
		return usageErrorf("%s needs %s; for example: osmbase %s --lat -33.8568 --lon 151.2153",
			command, strings.Join(missing, " and "), command)
	}
	if c.zoom < 0 || c.zoom > mercator.MaxZoom {
		return usageErrorf("--zoom %d is not a zoom level; they run from 0 to %d, and the public vector tile builds stop at 15", c.zoom, mercator.MaxZoom)
	}
	return nil
}

// newFlagSet returns a flag set that prints nothing by itself.
//
// The flag package writes both its help and its errors to one writer, and
// those belong in different places: help is what the user asked for and goes
// to stdout, an error is a complaint and goes to stderr. Discarding here and
// printing in parseArgs is what keeps them apart.
func newFlagSet(name string, describe func(w io.Writer, fs *flag.FlagSet)) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() { describe(fs.Output(), fs) }
	return fs
}

// parseArgs parses one command's arguments and returns SOURCE.
//
// SOURCE may come before the flags or after them, because both readings are
// natural -- "osmbase tile FILE --lat ..." reads as a subject followed by
// qualifiers, and "osmbase tile --lat ... FILE" is how the flag package
// expects it -- and a first attempt that only accepted one of them is a
// pointless thing for a person to have to remember. It may not be given twice.
//
// An empty SOURCE means the caller did not name one, which is not the same as
// naming the default: the caller decides what that means, and prints the
// notice that goes with it.
func parseArgs(fs *flag.FlagSet, args []string, stdout io.Writer) (source string, err error) {
	// The leading non-flag argument is taken out before the flag package sees
	// it, because flag stops at the first non-flag argument and would treat
	// everything after it as positional.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		source, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stdout)
			fs.Usage()
			return "", flag.ErrHelp
		}
		return "", usageErrorf("%v (try: osmbase %s -h)", err, fs.Name())
	}
	switch {
	case fs.NArg() == 0:
	case fs.NArg() == 1 && source == "":
		source = fs.Arg(0)
	case fs.NArg() == 1:
		return "", usageErrorf("SOURCE was given twice, as %q and as %q", source, fs.Arg(0))
	default:
		return "", usageErrorf("there are %d arguments after the flags (%s), and a command takes one SOURCE",
			fs.NArg(), strings.Join(fs.Args(), " "))
	}
	return source, nil
}

// printFlags writes the flag list under a heading, for a command's help.
func printFlags(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, "flags:\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// parseBBox reads --bbox, which fetch and render spell the same way.
func parseBBox(s string) (slice.Bounds, error) {
	var w, so, e, n float64
	if _, err := fmt.Sscanf(s, "%g,%g,%g,%g", &w, &so, &e, &n); err != nil {
		return slice.Bounds{}, usageErrorf("--bbox wants four numbers, west,south,east,north, and got %q", s)
	}
	return slice.Bounds{West: w, South: so, East: e, North: n}, nil
}

// maxFitZoom is the deepest zoom a fitted view is drawn at. The public builds
// stop at 15, so a rectangle small enough to want more is drawn at 15 with
// ground around it rather than overzoomed into a smear.
const maxFitZoom = 15

// fitZoom is the deepest zoom at which a rectangle fits in an image, and the
// coordinate at the middle of it.
//
// The middle is taken in the PROJECTION, not in degrees: halfway between 54.5
// and 57.8 degrees north is not the middle of the picture, because Mercator
// stretches the north more, and centring on the degree average leaves more
// margin below than above.
func fitZoom(b slice.Bounds, width, height int) (z int, lat, lon float64, cropped bool, err error) {
	switch {
	case b.West < -180 || b.East > 180 || b.South < -90 || b.North > 90 ||
		b.West != b.West || b.East != b.East || b.South != b.South || b.North != b.North:
		return 0, 0, 0, false, usageErrorf("--bbox %g,%g,%g,%g is not a rectangle on the earth; longitudes run -180 to 180 and latitudes -90 to 90",
			b.West, b.South, b.East, b.North)
	case b.East < b.West:
		return 0, 0, 0, false, usageErrorf("--bbox has its east edge (%g) west of its west edge (%g); a rectangle across the antimeridian has to be drawn as two", b.East, b.West)
	case b.North < b.South:
		return 0, 0, 0, false, usageErrorf("--bbox has its north edge (%g) south of its south edge (%g)", b.North, b.South)
	}
	x0, y0 := mercator.Project(b.West, b.North)
	x1, y1 := mercator.Project(b.East, b.South)
	fit := math.Inf(1)
	if dx := x1 - x0; dx > 0 {
		fit = math.Log2(float64(width) / (256 * dx))
	}
	if dy := y1 - y0; dy > 0 {
		fit = math.Min(fit, math.Log2(float64(height)/(256*dy)))
	}
	z = maxFitZoom
	if fit < maxFitZoom {
		z = max(int(math.Floor(fit)), 0)
	}
	// And no shallower than the image allows: at zoom z the world is
	// 256*2^z pixels across, and an image wider or taller than the world
	// cannot be drawn as one view. The whole world at 1024 by 768 fits at
	// zoom 1, where the world is 512 pixels wide. Deeper than the fit means
	// part of the rectangle is cut off, which is the only way to draw it
	// at all, and cropped says so.
	if floor := int(math.Ceil(math.Log2(float64(max(width, height)) / 256))); z < floor {
		z, cropped = floor, true
	}
	cx, cy := (x0+x1)/2, (y0+y1)/2
	if cropped {
		// Kept inside the world vertically, so the image does not reach
		// past the Mercator cut -- which viewAround refuses.
		half := float64(height) / (2 * 256 * math.Exp2(float64(z)))
		cy = min(max(cy, half), 1-half)
	}
	lon, lat = mercator.Unproject(cx, cy)
	return z, lat, lon, cropped, nil
}
