package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
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

// archive is an opened PMTiles archive together with what the command needs to
// say about where it came from.
type archive struct {
	*pmtiles.Reader

	// name is the URL or path as the user gave it.
	name string
	// remote says whether reading this archive talks to anyone.
	remote bool

	size    int64
	hasSize bool

	// stats reports requests and bytes for a remote archive, and is nil for a
	// local one -- a file has neither, and reporting "0 requests" for it would
	// invite the reader to think the number meant something.
	stats  func() (int, int64)
	closer io.Closer

	// quiet turns off the per-request trace, and is nil for a local archive.
	//
	// The trace is the right thing for a render, where a handful of requests
	// are the only sign anything is happening. It is the wrong thing under a
	// progress line: the two write to the same stream and the carriage return
	// that redraws the progress lands in the middle of a trace line, so both
	// become unreadable. A caller that draws its own progress turns it off.
	quiet func()

	// bytes is the archive's raw storage, which a fetch needs and a render
	// does not. The reader above answers "where is this tile and what does it
	// decode to"; a fetch asks instead for a span of the file covering several
	// tiles at once, so that eighty-five tiles cost a handful of requests
	// rather than eighty-five. See acquire.Archive.
	bytes io.ReaderAt
}

func (a *archive) Close() error {
	if a.closer == nil {
		return nil
	}
	return a.closer.Close()
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
		source = defaultSource
	}

	switch {
	case strings.HasPrefix(source, "https://"), strings.HasPrefix(source, "http://"):
		return openRemote(source, usingDefault, stderr)
	case strings.Contains(source, "://"):
		scheme, _, _ := strings.Cut(source, "://")
		return nil, usageErrorf("SOURCE %q uses the %q scheme; give an https URL or the path to a local .pmtiles file", source, scheme)
	default:
		return openLocal(source)
	}
}

func openLocal(path string) (*archive, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("there is no file at %s; SOURCE is an https URL or a path to a .pmtiles archive", path)
		}
		return nil, fmt.Errorf("looking at %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; SOURCE is one .pmtiles archive", path)
	}
	r, err := pmtiles.Open(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s for coalesced reads: %w", path, err)
	}
	return &archive{
		Reader: r, name: path, size: info.Size(), hasSize: true,
		bytes: f, closer: multiCloser{r, f},
	}, nil
}

func openRemote(url string, usingDefault bool, stderr io.Writer) (*archive, error) {
	src, err := acquire.NewRangeReader(url)
	if err != nil {
		return nil, err
	}
	// Everything printed from here on uses the reader's own form of the URL
	// rather than the string the user typed. A private mirror behind userinfo
	// and a presigned URL with a signature in its query are both ordinary, and
	// this command's output is meant to be pasted into a bug report.
	shown := src.URL()
	if usingDefault {
		fmt.Fprintf(stderr, "osmbase: no SOURCE given, so reading the default archive over the network:\n")
		fmt.Fprintf(stderr, "osmbase:   %s\n", shown)
		fmt.Fprintf(stderr, "osmbase: this tells that host which part of the map you asked about. Pass a local\n")
		fmt.Fprintf(stderr, "osmbase:   .pmtiles file to avoid it, or run \"osmbase help\" to read why.\n")
	} else {
		fmt.Fprintf(stderr, "osmbase: reading %s over HTTP range requests\n", shown)
	}
	src.Trace = func(off int64, n int, elapsed time.Duration) {
		reqs, _ := src.Stats()
		fmt.Fprintf(stderr, "osmbase: request %d: %s at offset %d in %s\n",
			reqs, humanBytes(int64(n)), off, elapsed.Round(time.Millisecond))
	}

	r, err := pmtiles.NewReader(src)
	if err != nil {
		return nil, err
	}
	a := &archive{Reader: r, name: shown, remote: true, stats: src.Stats, bytes: src}
	a.quiet = func() { src.Trace = nil }
	a.size, a.hasSize = src.Size()
	return a, nil
}

// reportTraffic prints what a remote read cost, and nothing at all for a local
// archive.
//
// The number is the format's whole argument: a few tens of kilobytes out of a
// hundred-gigabyte archive, with no download and no account. It is printed at
// the end of the command rather than per request so that it is the last thing
// on the screen.
func (a *archive) reportTraffic(stderr io.Writer) {
	if a.stats == nil {
		return
	}
	reqs, read := a.stats()
	fmt.Fprintf(stderr, "osmbase: %d range requests, %s fetched", reqs, humanBytes(read))
	if a.hasSize {
		fmt.Fprintf(stderr, " from a %s archive", humanBytes(a.size))
	}
	fmt.Fprintln(stderr)
}

// requireVectorTiles refuses an archive whose tiles are not MVT, before a
// command hands raster bytes to a vector tile decoder.
//
// The decoder would not fail loudly: a PNG is not a valid protobuf message in
// any interesting way, but a protobuf decoder handed arbitrary bytes reports
// something about a field number, and "field 6 is not defined" is a far worse
// explanation than "this archive holds png tiles".
func (a *archive) requireVectorTiles() error {
	if t := a.Header().TileType; t != pmtiles.TileTypeMVT {
		return fmt.Errorf("%s holds %s tiles, and this command decodes vector tiles (mvt)", a.name, t)
	}
	return nil
}

// multiCloser closes several things and reports the first failure.
//
// A local archive is opened twice: once as a tile reader and once as raw bytes
// for coalesced fetching. Two handles on one file is cheap and the alternative
// -- reaching inside the reader for its source -- would make the reader's own
// field part of this package's API.
type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var first error
	for _, c := range m {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
