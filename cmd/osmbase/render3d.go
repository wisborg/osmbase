package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"slices"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/perspective"
	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
	"github.com/wisborg/osmbase/terrain"
)

func render3dUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase render3d --lat LAT --lon LON [--heading DEG] [--pitch DEG] [--distance M] [--out FILE]

Draw the map in three dimensions: the ground shaped by its heights, the map
draped over it, seen from a camera in the sky. The camera looks at --lat/--lon
from --distance metres away, along the compass --heading, tipped --pitch
degrees below the horizontal -- 90 is the map from straight above, a few
degrees the horizon.

The map draped is the one "osmbase render --terrain" draws, of the ground
within --radius kilometres of the point, shading and contours and all. Both
the map and the terrain come from the stores on this machine, filled by
"osmbase fetch --terrain"; nothing reaches the network. Names along lines --
streets, rivers, contour heights -- are painted on the ground they follow;
the names of places stand upright where the place is seen, and are left out
where a hill hides it.

examples:
  osmbase render3d --lat -33.7025 --lon 151.0990 --out hornsby.png
      Hornsby from the south, 35 degrees down

  osmbase render3d --lat -33.7025 --lon 151.0990 --heading 300 --pitch 20 \
      --exaggeration 2 --out hornsby.png
      from the south-east, lower, the hills twice their height

`)
	printFlags(w, fs)
}

func render3dCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var (
		lat, lon         float64
		radius           float64
		distance         float64
		heading, pitch   float64
		fov, exaggerate  float64
		width, height    int
		mapSize          int
		store, terrainAt string
		terrainSrc       string
		yes              bool
		palette, labels  string
		contours         bool
		out              string
	)
	fs := newFlagSet("render3d", render3dUsage)
	fs.Float64Var(&lat, "lat", math.NaN(), "latitude of the point looked at, in degrees (required)")
	fs.Float64Var(&lon, "lon", math.NaN(), "longitude of the point looked at, in degrees (required)")
	fs.Float64Var(&radius, "radius", 0, "how far around the point the map reaches, in kilometres (default: as far as the camera sees, into the haze)")
	fs.Float64Var(&distance, "distance", 3000, "how far the camera is from the point, in metres")
	fs.Float64Var(&heading, "heading", 0, "the compass bearing the camera looks along, in degrees: 0 looks north")
	fs.Float64Var(&pitch, "pitch", 35, "how far below the horizontal the camera looks, in degrees: 90 is straight down")
	fs.Float64Var(&fov, "fov", perspective.DefaultFOV, "the vertical field of view, in degrees")
	fs.Float64Var(&exaggerate, "exaggeration", 1, "how many times their height the hills are drawn")
	fs.IntVar(&width, "width", defaultWidth, "width of the image in pixels")
	fs.IntVar(&height, "height", defaultHeight, "height of the image in pixels")
	fs.IntVar(&mapSize, "map-size", 0, "the longer side of the map draped over the ground, in pixels; more is sharper and slower (default: about one map pixel to one image pixel at the point looked at)")
	fs.StringVar(&store, "store", "", "the map store to draw from (default: osmbase's own)")
	fs.StringVar(&terrainAt, "terrain-store", "", "where the terrain is kept (default: beside the map's store, its name ending -terrain)")
	fs.StringVar(&palette, "palette", "outdoors", "colours to draw the map with: outdoors, light, dark, or dark-linework")
	fs.StringVar(&labels, "labels", "normal", labelHelp())
	fs.StringVar(&terrainSrc, "terrain-source", defaultTerrainSource, "where terrain the store lacks would be fetched from, after asking: a host's address, or a directory of its archives")
	fs.BoolVar(&yes, "yes", false, "fetch what the map and terrain stores lack for the view without asking first")
	fs.BoolVar(&contours, "contours", true, "draw contour lines on the map; --contours=false for the shading alone")
	fs.StringVar(&out, "out", "map3d.png", "file to write the PNG to")
	if _, err := parseArgs(fs, args, stdout); err != nil {
		return err
	}
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return usageErrorf("--lat and --lon say what the camera looks at, and both are needed")
	}
	if radius < 0 {
		return usageErrorf("--radius %g is not a distance", radius)
	}
	if !(distance > 0) {
		return usageErrorf("--distance %g is not a distance", distance)
	}
	if width <= 0 || height <= 0 || width*height > maxRenderPixels {
		return usageErrorf("--width %d --height %d is not an image this command draws", width, height)
	}
	if mapSize < 0 || mapSize > maxMapSide {
		return usageErrorf("--map-size %d is not a map this command draws; at most %d", mapSize, maxMapSide)
	}
	cam := perspective.Camera{Target: render.Coord{Lat: lat, Lon: lon}, Distance: distance, Heading: heading, Pitch: pitch, FOV: fov}

	if store == "" {
		root, err := slice.DefaultRoot()
		if err != nil {
			return err
		}
		store = root
	}
	if terrainAt == "" {
		terrainAt = terrain.Root(store)
	}

	colours, err := paletteNamed(palette)
	if err != nil {
		return err
	}
	if !contours {
		colours.Omitted |= render.Roles(render.RoleContour)
	}
	style, err := labelStyle(render.BasemapStyle(), labels)
	if err != nil {
		return err
	}

	// The map of the ground the camera sees, as the 2D render draws it:
	// what is draped. Its area is what the lens takes in, out into the haze,
	// unless --radius says a square; its size in pixels is about one to an
	// image pixel at the point looked at, unless --map-size says.
	bounds := cam.MapBounds(float64(width) / float64(height))
	if radius > 0 {
		dLat := radius / 111.32
		dLon := radius / (111.32 * math.Cos(lat*math.Pi/180))
		bounds = render.Bounds{West: lon - dLon, South: lat - dLat, East: lon + dLon, North: lat + dLat}
	}
	view := mapView(bounds, cam, height, mapSize)

	// What the stores lack, offered before anything is drawn, as render
	// offers it: the map first, then the terrain, each naming its host.
	chosen, src, err := mapFor(ctx, store, "", view, yes, stderr)
	if err != nil {
		return err
	}
	z, _, err := view.Zoom()
	if err != nil {
		return err
	}
	if s, short := terrain.Measure(terrainAt, viewBounds(view), z); short {
		offerTerrain(ctx, stderr, s, terrainSrc, yes)
	}
	ts, err := openTerrain(terrainAt)
	if err != nil {
		return err
	}
	// Names of places are lifted off the map and stood upright on the
	// picture instead: lying on a slope they are stretched across it.
	o := render.Options{
		Style: style, Palette: colours, Attribution: chosen.Attribution,
		LabelFace: labelFace(), LabelFaceFor: labelFaceFor, LiftPointLabels: true,
	}
	if err := terrainInto(ts, &o, view); err != nil {
		return err
	}
	r, err := render.New(src, o)
	if err != nil {
		return err
	}
	res, err := r.Render(ctx, view)
	if err != nil {
		if errors.Is(err, render.ErrNoCoverage) {
			return fmt.Errorf("%w. The store at %s holds nothing near latitude %s, longitude %s; fetch that area first", err, store, formatCoord(lat), formatCoord(lon))
		}
		return err
	}

	pic, err := perspective.Render(
		perspective.Scene{Map: res.Image, View: view, Heights: ts.Heights(), Exaggeration: exaggerate, Step: meshStep(view)},
		cam,
		perspective.Options{Width: width, Height: height},
	)
	if err != nil {
		return err
	}
	img := pic.Image
	drawUpright(img, pic, res.PointLabels, colours)
	// The credit the map owed, in the picture, as for a 2D render.
	drawCredit(img, res.Attribution)

	tmp := out + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return err
	}

	fmt.Fprintf(stdout, "%-12s %s\n", "file", out)
	fmt.Fprintf(stdout, "%-12s %d x %d pixels, looking %s at %s, %s from %g m, %g° down\n", "image",
		width, height, compass(heading), formatCoord(lat), formatCoord(lon), distance, pitch)
	wKM, hKM := boundsKM(view.Bounds)
	fmt.Fprintf(stdout, "%-12s %.1f by %.1f km, %d x %d pixels draped at zoom %d, %s covered, %s from shallower tiles\n", "map",
		wKM, hKM, view.Width, view.Height, z, percent(res.Covered), percent(res.Overzoomed))
	fmt.Fprintf(stdout, "%-12s %s of the map, from zoom %d\n", "terrain", percent(res.TerrainCovered), res.TerrainZoom)
	// Terrain much coarser than the map is drawn smooth: a mountain from
	// tiles kilometres across is a tilted plane. Said, because it looks like
	// a rendering fault and is a store that holds only the overview.
	if int(res.TerrainZoom)+3 < int(z) {
		fmt.Fprintf(stdout, "%-12s the terrain is from zoom %d and the map from %d, so the ground is drawn far smoother than it is; fetch the terrain for this area to sharpen it\n", "", res.TerrainZoom, z)
	}
	if credit := render.PlainCredit(res.Attribution); credit != "" {
		fmt.Fprintf(stdout, "%-12s %s\n", "credit", credit)
	}
	if res.TerrainNotice != "" {
		fmt.Fprintf(stdout, "\nThe image credits its elevation briefly. Wherever you publish it, give this notice with it:\n%s\n", res.TerrainNotice)
	}
	return nil
}

// compass is a bearing as the nearest of eight compass points.
func compass(deg float64) string {
	points := []string{"north", "north-east", "east", "south-east", "south", "south-west", "west", "north-west"}
	i := int(math.Mod(math.Round(deg/45), 8)+8) % 8
	return points[i]
}

// drawUpright stands the names of places on the picture: each centred on
// where its place appears, upright, with a halo in the colour of the land so
// it reads on any ground, and only where the place is seen -- a name on the
// far side of a hill is not drawn floating over the hill. They come most
// important first, as the map placed them, and one that would overlap a name
// already drawn is left out.
func drawUpright(img *image.RGBA, pic *perspective.Picture, labels []render.PointLabel, p render.Palette) {
	halo := p.Land
	if p.Omits(render.RoleLand) {
		halo = p.Background
	}
	var drawn []image.Rectangle
	for _, l := range labels {
		x, y, seen := pic.Locate(l.At)
		if !seen || l.Face == nil {
			continue
		}
		w := font.MeasureString(l.Face, l.Text).Ceil()
		m := l.Face.Metrics()
		asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
		box := image.Rect(int(x)-w/2-4, int(y)-(asc+desc)/2-4, int(x)+w/2+4, int(y)+(asc+desc)/2+4)
		if !box.In(img.Bounds()) {
			continue
		}
		if slices.ContainsFunc(drawn, box.Overlaps) {
			continue
		}
		drawn = append(drawn, box)
		ink := p.Label
		if l.Minor && p.LabelMinor != (color.RGBA{}) {
			ink = p.LabelMinor
		}
		base := fixed.P(int(x)-w/2, int(y)-(asc+desc)/2+asc)
		d := font.Drawer{Dst: img, Face: l.Face, Src: image.NewUniform(halo)}
		for _, o := range [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}, {-2, 0}, {2, 0}, {0, -2}, {0, 2}} {
			d.Dot = base.Add(fixed.P(o[0], o[1]))
			d.DrawString(l.Text)
		}
		d.Src, d.Dot = image.NewUniform(ink), base
		d.DrawString(l.Text)
	}
}

// maxMapSide is the longest side of the map draped, in pixels.
const maxMapSide = 8192

// mapView is the view of bounds the map is drawn at: its longer side side
// pixels, or when side is 0, about one map pixel to one image pixel at the
// point the camera looks at, no more than maxMapSide.
func mapView(b render.Bounds, cam perspective.Camera, imageHeight, side int) render.View {
	wKM, hKM := boundsKM(b)
	long := math.Max(wKM, hKM) * 1000
	if side == 0 {
		fov := cam.FOV
		if fov == 0 {
			fov = perspective.DefaultFOV
		}
		// Metres one image pixel spans at the point looked at.
		perPixel := 2 * cam.Distance * math.Tan(fov*math.Pi/360) / float64(imageHeight)
		side = int(math.Min(maxMapSide, math.Max(512, long/perPixel)))
	}
	w, h := side, side
	if wKM > hKM {
		h = max(1, int(float64(side)*hKM/wKM))
	} else {
		w = max(1, int(float64(side)*wKM/hKM))
	}
	return render.View{Bounds: b, Width: w, Height: h}
}

// meshStep is how many map pixels a mesh cell spans: the default, or more
// for a large map, so the mesh stays near 512 cells a side however large the
// map draped over it.
func meshStep(v render.View) int {
	return max(perspective.DefaultStep, max(v.Width, v.Height)/512)
}

// boundsKM is the width and height of b, in kilometres.
func boundsKM(b render.Bounds) (w, h float64) {
	mid := (b.North + b.South) / 2
	return (b.East - b.West) * 111.32 * math.Cos(mid*math.Pi/180), (b.North - b.South) * 111.32
}
