package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/wisborg/osmbase/boundary"
	osmlocate "github.com/wisborg/osmbase/locate"
	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
)

func locateUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase locate --store DIR --lat LAT --lon LON [--lat LAT --lon LON ...]

Say where a coordinate is: country, region, locality, suburb, street, read from
map data already in a store. Nothing reaches the network.

Every answer says HOW it was reached. These tiles carry no named areas -- the
boundaries layer has no names and the landuse polygons have none either -- so a
result is the NEAREST named feature and not the one containing the point. A
locality point is a label anchor near the middle of a town, so "near Horsens"
is what the data supports and "in Horsens" is not. The distance is printed for
exactly that reason.

examples:
  osmbase locate --store ~/Library/Caches/osmbase --lat 55.8623 --lon 9.8451
      one point, as a table

  osmbase locate --store DIR --lat 55.86 --lon 9.84 --lat 55.87 --lon 9.85 --format json
      several points, as JSON

`)
	printFlags(w, fs)
}

// coordList collects repeated --lat and --lon flags, pairwise.
//
// Repeated flags rather than one --coords with a separator: a coordinate pair
// typed as "55.86,9.84" is ambiguous about order in a way lat and lon named
// separately never is, and getting them the wrong way round puts the answer in
// the Gulf of Guinea or the Indian Ocean with no hint that anything is wrong.
type coordList struct {
	lats, lons []float64
}

func (c *coordList) addLat(s string) error { return appendFloat(&c.lats, s, "--lat") }
func (c *coordList) addLon(s string) error { return appendFloat(&c.lons, s, "--lon") }

func appendFloat(dst *[]float64, s, flag string) error {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return usageErrorf("%s %q is not a number", flag, s)
	}
	*dst = append(*dst, v)
	return nil
}

// pairs turns the two lists into coordinates, refusing a mismatch.
//
// Refused rather than zipped to the shorter list: a missing --lon does not
// mean "look up the ones I did pair", it means a coordinate was mistyped, and
// silently answering a different question than the one asked is worse than
// saying so.
func (c *coordList) pairs() ([]osmlocate.Coord, error) {
	if len(c.lats) != len(c.lons) {
		return nil, usageErrorf("%d --lat and %d --lon: each coordinate needs one of each",
			len(c.lats), len(c.lons))
	}
	if len(c.lats) == 0 {
		return nil, usageErrorf("locate needs --lat and --lon")
	}
	out := make([]osmlocate.Coord, len(c.lats))
	for i := range c.lats {
		out[i] = osmlocate.Coord{Lat: c.lats[i], Lon: c.lons[i]}
	}
	return out, nil
}

func runLocate(args []string, stdout, stderr io.Writer) error {
	var (
		coords   coordList
		store    string
		archive  string
		language string
		detail   string
		format   string
	)
	fs := newFlagSet("locate", locateUsage)
	fs.Func("lat", "latitude in degrees, north positive; repeat with --lon for more points", coords.addLat)
	fs.Func("lon", "longitude in degrees, east positive", coords.addLon)
	fs.StringVar(&store, "store", "", "directory holding the map data (default: the osmbase folder under your user cache directory)")
	fs.StringVar(&archive, "archive", "", "which archive in the store to read, by ID or by part of its name; only needed when the store holds more than one")
	fs.StringVar(&language, "language", "", "prefer names in this language where the data has them, as a short code such as \"da\" or \"ja\"; the default takes each name as written locally")
	fs.StringVar(&detail, "detail", "",
		"which boundary outlines to use, if the store has them: "+strings.Join(boundary.Details, ", ")+" (default: "+boundary.DefaultDetail+")")
	fs.StringVar(&format, "format", "text", "how to print the answer: text or json")

	if _, err := parseArgs(fs, args, stdout); err != nil {
		return err
	}
	pts, err := coords.pairs()
	if err != nil {
		return err
	}
	if !boundary.ValidDetail(detail) {
		return usageErrorf("--detail %q is not one this command knows; it has %s",
			detail, strings.Join(boundary.Details, ", "))
	}
	if format != "text" && format != "json" {
		return usageErrorf("--format %q is not one this command knows; it has text and json", format)
	}

	root := store
	if root == "" {
		if root, err = slice.DefaultRoot(); err != nil {
			return fmt.Errorf("finding the default store: %w; pass --store to say where the map data is", err)
		}
	}
	st, err := slice.Open(root)
	if err != nil {
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

	// Boundaries if the store has them, and silently not if it does not: they
	// are an optional download, a store without them is the ordinary case,
	// and the difference is visible in the output anyway -- every answer says
	// "near" instead of "in".
	opts := osmlocate.Options{Language: language}
	if boundary.Available(root, detail) {
		opts.Boundaries = boundary.Open(root, detail)
	}

	places, err := osmlocate.AtEach(context.Background(), src, pts, opts)
	if err != nil {
		return err
	}

	// The credit travels with the answer, for the same reason the render
	// command draws it into the picture rather than printing it beside one: a
	// returned place name is a Produced Work under the ODbL, and the
	// obligation attaches to the thing that gets sent to somebody else. A
	// JSON document pasted into a report carries whatever is inside it and
	// nothing that was on the terminal around it.
	//
	// It names OpenStreetMap only, and an answer can now mix two sources: a
	// Contained match comes from Natural Earth, which is public domain and
	// requires no credit at all. Crediting OSM for the whole answer is not a
	// licence problem -- nothing is owed to Natural Earth -- but it is
	// imprecise, so the line says which levels it covers rather than implying
	// every name came from there.
	//
	// Read from the manifest, never written down here -- see the same argument
	// in the render command. A store refilled from a different archive must
	// change this line without anybody remembering to.
	credit := render.PlainCredit(chosen.Attribution)

	if format == "json" {
		return writeLocateJSON(stdout, places, credit)
	}
	writeLocateText(stdout, places, credit)
	return nil
}

// locateReport is what --format json writes.
//
// An object wrapping the array rather than the bare array it used to be,
// because the credit has to be IN the document. A JSON array of places says
// nothing about whose data named them, and the one place that can carry the
// obligation is the document itself.
type locateReport struct {
	// Credit covers the names taken from the tiles. A contained answer comes
	// from Natural Earth, which is public domain and requires none -- said in
	// its own field rather than folded into the first, so a consumer reading
	// this document can tell which obligation applies to which names.
	Credit          string            `json:"credit,omitempty"`
	ContainedCredit string            `json:"contained_credit,omitempty"`
	Places          []osmlocate.Place `json:"places"`
}

func writeLocateJSON(w io.Writer, places []osmlocate.Place, credit string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	report := locateReport{Credit: credit, Places: places}
	if anyContained(places) {
		report.ContainedCredit = "Natural Earth (public domain)"
	}
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("writing the answer: %w", err)
	}
	return nil
}

// writeLocateText prints one block per coordinate.
//
// The distance is on every line and the word "near" is on every name, because
// the two together are the whole honesty of this command: there is no level at
// which the tiles can say a point is INSIDE anything, and a reader who sees
// "Horsens" with no qualification will believe the point was in Horsens.
func writeLocateText(w io.Writer, places []osmlocate.Place, credit string) {
	for i, p := range places {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s, %s\n", formatCoord(p.Lat), formatCoord(p.Lon))
		if len(p.Matches) == 0 {
			fmt.Fprintf(w, "  nothing within reach -- the store holds no named feature near this point\n")
			continue
		}
		for _, m := range p.Matches {
			kind := m.Kind
			if kind == "" {
				kind = m.Level.String()
			}
			fmt.Fprintf(w, "  %-14s %s %s (%s)\n", m.Level, m.Source, m.Name, kind)
			// No distance for a contained match. It is always zero, and
			// printing "0 m away" invites a reader to think a measurement was
			// taken and came back as nothing, when in fact the question does
			// not apply: the point is inside the area, not near it.
			if m.Source != osmlocate.Contained {
				fmt.Fprintf(w, "  %-14s %s away\n", "", humanDistance(m.DistanceM))
			}
		}
	}
	if credit != "" {
		fmt.Fprintf(w, "\n%s\n", credit)
		if anyContained(places) {
			fmt.Fprintf(w, "Contained answers are from Natural Earth, which is public domain.\n")
		}
	}
}

// anyContained reports whether any answer came from boundary data rather than
// from the tiles, which is what decides whether the second credit line means
// anything.
func anyContained(places []osmlocate.Place) bool {
	for _, p := range places {
		for _, m := range p.Matches {
			if m.Source == osmlocate.Contained {
				return true
			}
		}
	}
	return false
}

// humanDistance prints a distance at a precision the measurement supports.
//
// Metres below a kilometre and kilometres above, with no decimals past ten
// kilometres: "near Horsens, 41.8213 km" states a precision this does not have
// and invites a reader to treat a label anchor as a surveyed point.
func humanDistance(m float64) string {
	switch {
	case m < 1000:
		return strconv.FormatFloat(m, 'f', 0, 64) + " m"
	case m < 10_000:
		return strings.TrimSuffix(strconv.FormatFloat(m/1000, 'f', 1, 64), ".0") + " km"
	default:
		return strconv.FormatFloat(m/1000, 'f', 0, 64) + " km"
	}
}
