package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"

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
"osmbase fetch --terrain"; nothing reaches the network.

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
		palette, labels  string
		contours         bool
		out              string
	)
	fs := newFlagSet("render3d", render3dUsage)
	fs.Float64Var(&lat, "lat", math.NaN(), "latitude of the point looked at, in degrees (required)")
	fs.Float64Var(&lon, "lon", math.NaN(), "longitude of the point looked at, in degrees (required)")
	fs.Float64Var(&radius, "radius", 5, "how far around the point the map reaches, in kilometres")
	fs.Float64Var(&distance, "distance", 0, "how far the camera is from the point, in metres (default: the radius)")
	fs.Float64Var(&heading, "heading", 0, "the compass bearing the camera looks along, in degrees: 0 looks north")
	fs.Float64Var(&pitch, "pitch", 35, "how far below the horizontal the camera looks, in degrees: 90 is straight down")
	fs.Float64Var(&fov, "fov", perspective.DefaultFOV, "the vertical field of view, in degrees")
	fs.Float64Var(&exaggerate, "exaggeration", 1, "how many times their height the hills are drawn")
	fs.IntVar(&width, "width", defaultWidth, "width of the image in pixels")
	fs.IntVar(&height, "height", defaultHeight, "height of the image in pixels")
	fs.IntVar(&mapSize, "map-size", 2048, "width and height of the map draped over the ground, in pixels; more is sharper and slower")
	fs.StringVar(&store, "store", "", "the map store to draw from (default: osmbase's own)")
	fs.StringVar(&terrainAt, "terrain-store", "", "where the terrain is kept (default: beside the map's store, its name ending -terrain)")
	fs.StringVar(&palette, "palette", "light", "colours to draw the map with: light, dark, or dark-linework")
	fs.StringVar(&labels, "labels", "normal", labelHelp())
	fs.BoolVar(&contours, "contours", true, "draw contour lines on the map; --contours=false for the shading alone")
	fs.StringVar(&out, "out", "map3d.png", "file to write the PNG to")
	if _, err := parseArgs(fs, args, stdout); err != nil {
		return err
	}
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return usageErrorf("--lat and --lon say what the camera looks at, and both are needed")
	}
	if !(radius > 0) {
		return usageErrorf("--radius %g is not a distance", radius)
	}
	if width <= 0 || height <= 0 || width*height > maxRenderPixels {
		return usageErrorf("--width %d --height %d is not an image this command draws", width, height)
	}
	if mapSize <= 0 || mapSize*mapSize > maxRenderPixels {
		return usageErrorf("--map-size %d is not a map this command draws", mapSize)
	}
	if distance == 0 {
		distance = radius * 1000
	}

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
	chosen, src, _, err := openStoreSource(store, "")
	if err != nil {
		return err
	}
	ts, err := openTerrain(terrainAt)
	if err != nil {
		return err
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

	// The map of the ground around the point, square, as the 2D render
	// draws it: what is draped.
	dLat := radius / 111.32
	dLon := radius / (111.32 * math.Cos(lat*math.Pi/180))
	view := render.View{
		Bounds: render.Bounds{West: lon - dLon, South: lat - dLat, East: lon + dLon, North: lat + dLat},
		Width:  mapSize, Height: mapSize,
	}
	o := render.Options{
		Style: style, Palette: colours, Attribution: chosen.Attribution,
		LabelFace: labelFace(), LabelFaceFor: labelFaceFor,
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

	img, err := perspective.Render(
		perspective.Scene{Map: res.Image, View: view, Heights: ts.Heights(), Exaggeration: exaggerate},
		perspective.Camera{Target: render.Coord{Lat: lat, Lon: lon}, Distance: distance, Heading: heading, Pitch: pitch, FOV: fov},
		perspective.Options{Width: width, Height: height},
	)
	if err != nil {
		return err
	}
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
	fmt.Fprintf(stdout, "%-12s %g km around, %d x %d pixels draped, terrain %.0f%%, from zoom %d\n", "map",
		radius, mapSize, mapSize, 100*res.TerrainCovered, res.TerrainZoom)
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
