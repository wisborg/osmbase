package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/pmtiles"
)

// defaultSource is the archive used when the user names none.
//
// # Why there is one here at all
//
// The LIBRARY ships no default source, deliberately: defaulting to somebody's
// bucket would put a consumer's traffic on a host the consumer never chose.
// A command is the other case. Someone trying this out has no archive yet, and
// "first download a hundred gigabytes" is not a first step. The rule the
// architecture states is that the default belongs in the application, where a
// flag can name it and help text can explain it -- so it is named here, said
// out loud in sourceHelp, and avoidable by giving a path.
//
// # Why this URL and not the daily build
//
// The obvious choice is Protomaps' daily build at
// build.protomaps.com/YYYYMMDD.pmtiles, and it is the wrong one for a default.
// Those files are dated and expire: builds older than about a week are gone,
// and today's is not there until it has been made. A default built from
// today's date is therefore a 404 for part of every day and a hardcoded date
// rots within the week -- both of which fail in the hands of the person least
// able to tell what went wrong, someone running this for the first time.
//
// This URL is Protomaps' planet basemap hosted on Source Cooperative, which is
// a stable path over a stable snapshot, supports range requests, and is the
// host the architecture already examined: it states that hosted data is public
// and may be accessed by anyone, and documents no consumer obligations.
//
// It is still a third party and it may still change. When it does, the answer
// is the one beside it in the help text: name an archive, or point at a file.
const defaultSource = "https://data.source.coop/protomaps/openstreetmap/v4.pmtiles"

// sourceHelp is printed at the end of every command's help, because what
// SOURCE means and what using the default costs are the same question in every
// one of them.
const sourceHelp = `SOURCE is an https URL or the path to a local .pmtiles file, and it may come
either before or after the flags. With no SOURCE, this archive is used:

  ` + defaultSource + `

Reading it sends HTTP range requests to a third party, Source Cooperative,
which tells that host which few-kilometre squares of the map you asked about
and when. Only a few tens of kilobytes are fetched out of a 125 GiB archive,
and nothing is stored. A local .pmtiles file avoids the request entirely, and
a dated daily build can be named explicitly instead:

  https://build.protomaps.com/20260912.pmtiles
`

// archive is fetch.Archive with the announcements this command makes about
// it.
//
// The opening, the path-or-URL question, the vector tile check and the
// accounting all live in the library now; what is left here is the part that
// is genuinely this program's -- the wording, the "osmbase:" prefix, and the
// decision to say anything at all. That split is the reason the library takes
// a Trace callback rather than an io.Writer: a second consumer prints
// different words in a different place, and used to reimplement the whole
// opener to get them.
type archive struct {
	*fetch.Archive
}

// openArchive opens the archive named by source, or the default when source is
// empty, announcing on stderr anything that reaches the network.
//
// The announcement is not decoration. A command that silently contacts a host
// on the user's behalf is the thing this whole project exists to stop being
// normal, and a fetch of a hundred kilobytes from the other side of the world
// is slow enough that a blank terminal looks like a hang.
func openArchive(source string, stderr io.Writer) (*archive, error) {
	usingDefault := source == ""
	if usingDefault {
		// Announced BEFORE the open, because the open is what contacts the
		// host: a notice printed afterwards tells the user about a request
		// that has already gone.
		fmt.Fprintf(stderr, "osmbase: no SOURCE given, so reading the default archive over the network:\n")
		fmt.Fprintf(stderr, "osmbase:   %s\n", defaultSource)
		fmt.Fprintf(stderr, "osmbase: this tells that host which part of the map you asked about. Pass a local\n")
		fmt.Fprintf(stderr, "osmbase:   .pmtiles file to avoid it, or run \"osmbase help\" to read why.\n")
	}

	a, err := fetch.Open(source, fetch.Options{
		Default: defaultSource,
		Trace: func(line string) {
			fmt.Fprintf(stderr, "osmbase: %s\n", line)
		},
	})
	if err != nil {
		// A scheme this cannot read is the user's typing, not the world's
		// state, and the two exit with different codes. Matched on the
		// sentinel rather than on the message, which is the library's to
		// reword.
		if errors.Is(err, fetch.ErrUnsupportedScheme) {
			return nil, usageErrorf("%s", err)
		}
		return nil, err
	}
	if a.Remote() && !usingDefault {
		fmt.Fprintf(stderr, "osmbase: reading %s over HTTP range requests\n", a.Name())
	}
	return &archive{Archive: a}, nil
}

// reportTraffic prints what a remote read cost, and nothing at all for a local
// archive.
//
// The number is the format's whole argument: a few tens of kilobytes out of a
// hundred-gigabyte archive, with no download and no account. It is printed at
// the end of the command rather than per request so that it is the last thing
// on the screen.
func (a *archive) reportTraffic(stderr io.Writer) {
	reqs, read, ok := a.Traffic()
	if !ok {
		return
	}
	fmt.Fprintf(stderr, "osmbase: %d range requests, %s fetched", reqs, humanBytes(read))
	if n, known := a.Size(); known {
		fmt.Fprintf(stderr, " from a %s archive", humanBytes(n))
	}
	fmt.Fprintln(stderr)
}

// requireVectorTiles refuses an archive whose tiles are not MVT, before a
// command hands raster bytes to a vector tile decoder.
//
// Checked here rather than through fetch.Options because not every command
// decodes tiles: "osmbase inspect" reports on an archive of any tile type,
// and refusing at open would leave the one command that can explain a png
// archive unable to look at one.
func (a *archive) requireVectorTiles() error {
	if t := a.Reader().Header().TileType; t != pmtiles.TileTypeMVT {
		return fmt.Errorf("%s holds %s tiles, and this command decodes vector tiles (mvt)", a.Name(), t)
	}
	return nil
}
