package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

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
		levels   string
		detail   string
		format   string
	)
	fs := newFlagSet("locate", locateUsage)
	fs.Func("lat", "latitude in degrees, north positive; repeat with --lon for more points", coords.addLat)
	fs.Func("lon", "longitude in degrees, east positive", coords.addLon)
	fs.StringVar(&store, "store", "", "directory holding the map data (default: the osmbase folder under your user cache directory)")
	fs.StringVar(&archive, "archive", "", "which archive in the store to read, by ID or by part of its name; only needed when the store holds more than one")
	fs.StringVar(&language, "language", "", "prefer names in this language where the data has them, as a short code such as \"da\" or \"ja\"; the default takes each name as written locally")
	fs.StringVar(&levels, "levels", "",
		"which levels to answer, comma separated, from "+levelNames()+
			". The default asks for all of them. Worth setting: each level is read at its own zoom, so asking "+
			"for fewer reads fewer tiles -- and a track that was not on the ground wants the fine ones left out, "+
			"since a street 250 m below an aircraft is a true answer to a question nobody asked")
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
	// Levels and boundaries first, because whether the tiles are needed at
	// all depends on them. A store built by "osmbase boundaries --osm" and
	// nothing else holds no tiles, and asking only for the levels those
	// boundaries cover is a legitimate way to use it -- so opening the tile
	// store cannot be the first thing that happens.
	wanted, err := parseLevels(levels)
	if err != nil {
		return err
	}
	opts := osmlocate.Options{Language: language, Levels: wanted}
	var bounds *boundary.Source
	if boundary.Available(root, detail) {
		bounds = boundary.Open(root, detail)
		opts.Boundaries = bounds
	}

	var src osmlocate.TileSource
	var chosen slice.Manifest
	if need := tileLevels(wanted, bounds); len(need) > 0 {
		st, err := slice.Open(root)
		if err != nil {
			if bounds != nil {
				// The store holds boundaries and no map. Naming the levels
				// that need one turns "there is no store" into something the
				// user can act on: ask for fewer levels, or fetch tiles.
				verb := "need"
				if len(need) == 1 {
					verb = "needs"
				}
				return fmt.Errorf("%s %s map tiles, and %s holds boundaries but no map: ask for fewer levels, or run \"osmbase fetch\"",
					joinLevelNames(need), verb, root)
			}
			return fmt.Errorf("opening the store at %s: %w", root, err)
		}
		sources, err := st.Sources()
		if err != nil {
			return err
		}
		if chosen, err = chooseSource(root, sources, archive); err != nil {
			return err
		}
		if src, err = st.Source(chosen.ID); err != nil {
			return err
		}
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
	// It names the TILE archive only. A contained answer comes from a
	// boundary file, and what that owes depends on which file -- Natural
	// Earth is public domain, a file derived from OpenStreetMap is not --
	// so those are reported separately, from the answers themselves.
	//
	// Read from the manifest, never written down here -- see the same argument
	// in the render command. A store refilled from a different archive must
	// change this line without anybody remembering to.
	// Empty when no tiles were read, which is a store holding boundaries and
	// nothing else: there is no archive to credit, and the contained credit
	// below is the whole obligation.
	credit := render.PlainCredit(chosen.Attribution)

	// And which credit a CONTAINED answer owes is no longer a constant. It
	// was Natural Earth, which is public domain and owes nothing; a derived
	// boundary file is OpenStreetMap, which is not, so an answer taken from
	// one is a Produced Work that owes the credit. Which of them answered is
	// something only the source knows, so it is asked rather than assumed.
	contained := containedCredits(places)

	if format == "json" {
		return writeLocateJSON(stdout, places, credit, contained)
	}
	writeLocateText(stdout, places, credit, contained)
	return nil
}

// tileLevels are the levels asked for that have to come from the tiles.
//
// Boundaries if the store has them, and silently not if it does not: they are
// an optional download, a store without them is the ordinary case, and the
// difference is visible in the output anyway -- every answer says "near"
// instead of "in". What is new is that the reverse is also possible, so the
// tiles have to be optional in the same way -- and when they are missing, the
// levels that wanted them are what the message has to name.
func tileLevels(wanted []osmlocate.Level, bounds *boundary.Source) []osmlocate.Level {
	levels := wanted
	if len(levels) == 0 {
		levels = osmlocate.Levels
	}
	var need []osmlocate.Level
	for _, l := range levels {
		if bounds == nil || !bounds.Covers(l) {
			need = append(need, l)
		}
	}
	return need
}

func joinLevelNames(levels []osmlocate.Level) string {
	names := make([]string, len(levels))
	for i, l := range levels {
		names[i] = l.String()
	}
	return strings.Join(names, ", ")
}

// containedCredits names the sources behind the contained answers, and only
// those: a lookup that used no boundary file, or whose boundary file answered
// nothing, owes nothing here.
//
// Read off the ANSWERS rather than asked of the source per level. A store may
// hold a file derived from OpenStreetMap beside one that is public domain,
// and a credit taken from the store as a whole attributes an answer to
// whichever file did not give it.
func containedCredits(places []osmlocate.Place) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range places {
		for _, m := range p.Matches {
			if m.Source != osmlocate.Contained || m.Attribution == "" {
				continue
			}
			// Through the same cleaner the tile credit uses, so an
			// attribution carrying markup prints as text either way.
			c := safeForTerminal(render.PlainCredit(m.Attribution))
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

// safeForTerminal strips what a name or a credit has no business carrying.
//
// Both come out of a file, and a derived boundary file is meant to be passed
// between people -- the NOTICE says so. A name holding an ANSI escape colours
// somebody's terminal, and one holding a newline forges a line of output that
// looks like another answer. Neither is a licence problem and both are the
// program lying on behalf of a file it read.
//
// JSON needs none of this: encoding/json escapes control characters already.
// It is the text path that prints what it is given.
func safeForTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
}

// locateReport is what --format json writes.
//
// An object wrapping the array rather than the bare array it used to be,
// because the credit has to be IN the document. A JSON array of places says
// nothing about whose data named them, and the one place that can carry the
// obligation is the document itself.
type locateReport struct {
	// Credit covers the names taken from the tiles. A contained answer comes
	// from a boundary file, and which obligation that carries depends on
	// which file: Natural Earth is public domain and owes nothing, while a
	// file derived from OpenStreetMap owes the credit. Said in its own field
	// rather than folded into the first, so a consumer reading this document
	// can tell which obligation applies to which names.
	Credit          string            `json:"credit,omitempty"`
	ContainedCredit string            `json:"contained_credit,omitempty"`
	Places          []osmlocate.Place `json:"places"`
}

func writeLocateJSON(w io.Writer, places []osmlocate.Place, credit string, contained []string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	report := locateReport{Credit: credit, Places: places}
	if len(contained) > 0 {
		report.ContainedCredit = strings.Join(contained, "; ")
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
func writeLocateText(w io.Writer, places []osmlocate.Place, credit string, contained []string) {
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
			fmt.Fprintf(w, "  %-14s %s %s (%s)\n", m.Level, m.Source, safeForTerminal(m.Name), safeForTerminal(kind))
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
	}
	if len(contained) > 0 {
		fmt.Fprintf(w, "Contained answers are from %s.\n", strings.Join(contained, "; "))
	}
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

// levelNames lists the levels for the flag's help.
func levelNames() string { return joinLevelNames(osmlocate.Levels) }

// parseLevels turns the --levels flag into the levels to ask for.
//
// An empty flag means all of them, which is what Options.Levels already means
// -- so the two agree without this having to enumerate anything. An unknown
// name is refused rather than ignored: silently dropping a level the caller
// asked for would give a shorter answer with nothing to say why, which is
// exactly the failure this command is otherwise careful to avoid.
func parseLevels(flag string) ([]osmlocate.Level, error) {
	if strings.TrimSpace(flag) == "" {
		return nil, nil
	}
	var out []osmlocate.Level
	for _, name := range strings.Split(flag, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		l, ok := osmlocate.ParseLevel(name)
		if !ok {
			return nil, usageErrorf("--levels %q is not one this command knows; it has %s", name, levelNames())
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, usageErrorf("--levels was given nothing; leave it out to ask for all of %s", levelNames())
	}
	return out, nil
}
