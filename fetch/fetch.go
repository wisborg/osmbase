// Package fetch opens a PMTiles archive -- a local file or an https URL --
// and fills a slice store from it.
//
// It is the layer between acquire, which knows how to plan and move bytes,
// and a command, which knows what to print. Both of those existed before this
// did; what did not exist was one answer to the small, fiddly questions in
// between: is this string a path or a URL, does this archive hold vector
// tiles, what is it called in a message, what did reading it cost, and what
// does the store file it under. Every program built on this library needs all
// of them, and the two that existed answered them twice.
//
// That mattered more than ordinary duplication because of the last question.
// A store's default root is shared between programs, so two consumers
// fetching the same archive must agree on the ID it is filed under, or the
// same ground lands on disk twice under two names. Agreement by coincidence
// held while there were two implementations; it would not have survived the
// third.
//
// Nothing here prints. A caller passes Trace to see what crosses the network
// and formats the result itself, because the wording belongs to the program
// the user is running, not to the library underneath it.
package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// ErrNoAttribution is returned by Attribution for an archive whose metadata
// declares no credit.
//
// A sentinel rather than an empty string, and a decision left to the caller
// rather than made here, because the right response differs: a render that
// must discharge an attribution obligation should refuse, and a tool
// inspecting an archive should say so and carry on. What this package will
// not do is hand back "" and let a caller mistake it for a credit.
var ErrNoAttribution = errors.New("fetch: the archive declares no attribution")

// ErrUnsupportedScheme is returned by Open for a source naming a scheme this
// cannot read.
//
// It is distinguishable because it is the one Open error that is certainly
// the USER's typing rather than the world's state: a command exits 2 for a
// usage error and 1 for a failure, and a caller cannot tell those apart from
// an error string without matching on prose.
var ErrUnsupportedScheme = errors.New("fetch: unsupported scheme")

// Options configure Open.
type Options struct {
	// Default is the archive to open when source is empty. There is no
	// built-in one, deliberately: defaulting to somebody's bucket would put a
	// consumer's traffic on a host the consumer never chose. A command may
	// have a default, name it in its own help, and pass it here.
	Default string

	// Trace, when not nil, is called with a one-line description of every
	// range request the archive makes.
	//
	// It exists for the PLANNING phase, which reads the archive's directories
	// and can sit silent for several seconds against a remote host. It must
	// be turned off with Silence before a progress bar is drawn: the two
	// write to the same stream, and a carriage return landing in the middle
	// of a trace line makes both unreadable.
	Trace func(string)

	// RequireVectorTiles refuses an archive whose tiles are not MVT.
	//
	// A flag rather than always-on because not every caller decodes tiles --
	// one that copies them out or reports on the archive does not care. It is
	// checked at Open, well before the first tile, because the failure
	// downstream is unreadable: a PNG handed to a protobuf decoder does not
	// announce itself, it reports something about an undefined field number.
	RequireVectorTiles bool
}

// Archive is an opened PMTiles archive together with what a caller needs to
// say about where it came from.
type Archive struct {
	reader *pmtiles.Reader

	// bytes is the archive's raw storage, which a fetch needs and a render
	// does not. The reader answers "where is this tile and what does it
	// decode to"; a fetch asks instead for a span of the file covering
	// several tiles at once, so that eighty-five tiles cost a handful of
	// requests rather than eighty-five. See acquire.Archive.
	bytes  io.ReaderAt
	closer io.Closer

	name   string
	remote bool

	size    int64
	hasSize bool

	stats func() (int, int64)
	quiet func()
}

// Open opens the archive named by source, or opts.Default when source is
// empty.
func Open(source string, opts Options) (*Archive, error) {
	if source == "" {
		if opts.Default == "" {
			return nil, errors.New("fetch: no archive named, and no default was configured")
		}
		source = opts.Default
	}

	var (
		a   *Archive
		err error
	)
	switch {
	case strings.HasPrefix(source, "https://"), strings.HasPrefix(source, "http://"):
		a, err = openRemote(source, opts.Trace)
	case strings.Contains(source, "://"):
		scheme, _, _ := strings.Cut(source, "://")
		return nil, fmt.Errorf("the archive %q uses the %q scheme; give an https URL or the path to a local .pmtiles file: %w", source, scheme, ErrUnsupportedScheme)
	default:
		a, err = openLocal(source)
	}
	if err != nil {
		return nil, err
	}
	if opts.RequireVectorTiles {
		if t := a.reader.Header().TileType; t != pmtiles.TileTypeMVT {
			a.Close()
			return nil, fmt.Errorf("fetch: the archive %s holds %s tiles, and this reads vector tiles (mvt)", a.name, t)
		}
	}
	return a, nil
}

func openLocal(path string) (*Archive, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("fetch: there is no file at %s; an archive is an https URL or the path to a .pmtiles file", path)
		}
		return nil, fmt.Errorf("fetch: looking at the archive %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("fetch: %s is a directory; an archive is one .pmtiles file", path)
	}
	r, err := pmtiles.Open(path)
	if err != nil {
		return nil, fmt.Errorf("fetch: opening the archive %s: %w", path, err)
	}
	// Opened twice, as tiles and as raw bytes, because the two questions are
	// different: the reader answers "where is this tile", a fetch asks for a
	// span covering many. Two handles on one file is cheaper than reaching
	// into the reader for its source.
	f, err := os.Open(path)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("fetch: opening the archive %s for coalesced reads: %w", path, err)
	}
	return &Archive{
		reader: r, bytes: f, closer: closers{r, f},
		name: path, size: info.Size(), hasSize: true,
	}, nil
}

func openRemote(url string, trace func(string)) (*Archive, error) {
	src, err := acquire.NewRangeReader(url)
	if err != nil {
		return nil, fmt.Errorf("fetch: reaching the archive: %w", err)
	}
	if trace != nil {
		src.Trace = func(off int64, n int, elapsed time.Duration) {
			reqs, _ := src.Stats()
			trace(fmt.Sprintf("request %d: %d bytes at offset %d in %s",
				reqs, n, off, elapsed.Round(time.Millisecond)))
		}
	}
	r, err := pmtiles.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("fetch: reading the archive %s: %w", src.URL(), err)
	}
	// src.URL rather than the string the caller passed: a private mirror
	// behind userinfo and a presigned URL with a signature in its query are
	// both ordinary, and this name is printed, recorded in the store's
	// manifest and pasted into bug reports.
	a := &Archive{reader: r, bytes: src, name: src.URL(), remote: true, stats: src.Stats}
	a.quiet = func() { src.Trace = nil }
	a.size, a.hasSize = src.Size()
	return a, nil
}

// Reader is the underlying PMTiles reader, for a caller that wants to read
// tiles rather than fetch them.
func (a *Archive) Reader() *pmtiles.Reader { return a.reader }

// Name is the archive's URL or path in the form that is safe to print: a
// remote one is the reader's redacted URL, never the string that was passed.
func (a *Archive) Name() string { return a.name }

// Remote reports whether reading this archive talks to anybody. It is what a
// command branches on to decide whether there is a privacy decision to put in
// front of the user at all.
func (a *Archive) Remote() bool { return a.remote }

// Size is the archive's total size and whether that is known, for saying how
// small a slice of it a fetch takes.
func (a *Archive) Size() (int64, bool) { return a.size, a.hasSize }

// Traffic is how many range requests have been made and how many bytes they
// moved, and false for a local archive -- which has neither, and for which
// reporting "0 requests" would invite the reader to think the number meant
// something.
func (a *Archive) Traffic() (requests int, bytes int64, ok bool) {
	if a.stats == nil {
		return 0, 0, false
	}
	requests, bytes = a.stats()
	return requests, bytes, true
}

// Silence turns off the per-request trace. See Options.Trace.
func (a *Archive) Silence() {
	if a.quiet != nil {
		a.quiet()
	}
}

func (a *Archive) Close() error {
	if a.closer == nil {
		return nil
	}
	return a.closer.Close()
}

// Attribution is the credit the archive's own metadata declares, RAW -- as
// the archive wrote it, markup and all.
//
// Deliberately not converted to plain text here. What goes into a store's
// manifest is a faithful record of what the source said, so that stores
// written by two different programs hold the same bytes and stay
// interchangeable. The conversion belongs at the point of display, where
// render.PlainCredit does it, and where a store somebody else filled gets it
// too.
//
// An archive declaring none returns ErrNoAttribution.
func (a *Archive) Attribution() (string, error) {
	raw, err := a.reader.Metadata()
	if err != nil {
		return "", fmt.Errorf("fetch: reading the metadata of the archive %s: %w", a.name, err)
	}
	var meta struct {
		Attribution string `json:"attribution"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return "", fmt.Errorf("fetch: the metadata of the archive %s is not JSON this can read: %w", a.name, err)
		}
	}
	if strings.TrimSpace(meta.Attribution) == "" {
		return "", fmt.Errorf("%s: %w", a.name, ErrNoAttribution)
	}
	return meta.Attribution, nil
}

// SourceID is the name a store files this archive's tiles under.
//
// Asked of slice rather than computed here, and computed from the archive's
// printable name with no build, which is what makes a store shareable: the
// same archive fetched by two different programs lands in one directory
// rather than two copies of the same ground.
func (a *Archive) SourceID() string { return slice.SourceID(a.name, "") }

// AddTo registers this archive as a source of store and returns the source to
// fetch into. It WRITES: a caller that has not yet decided to download
// anything must not call it.
//
// attribution is passed in rather than read here so that the caller keeps the
// policy decision about an archive that declares none -- see Attribution.
func (a *Archive) AddTo(store *slice.Store, attribution string) (*slice.Source, error) {
	h := a.reader.Header()
	src, err := store.AddSource(slice.SourceDesc{
		Source:          a.name,
		Attribution:     attribution,
		TileType:        h.TileType.String(),
		TileCompression: slice.Compression(h.TileCompression.String()),
		SourceZoom:      slice.ZoomRange{Min: h.MinZoom, Max: h.MaxZoom},
	})
	if err != nil {
		return nil, fmt.Errorf("fetch: recording %s as a source of the store: %w", a.name, err)
	}
	return src, nil
}

// Plan works out exactly which tiles req asks for, where they are and what
// they cost, WITHOUT reading a byte of tile data.
//
// dst may be nil, which is what a store that does not exist yet holds:
// nothing. That is what lets a dry run cost an area on a machine that has
// never fetched, without creating a directory first.
//
// req.SourceZoom is filled in from the archive's own header, overwriting
// whatever the caller set. It is the one field in the request that is a fact
// about the archive rather than a choice about the fetch, and having every
// caller copy it out of the header was how two of them came to spell the same
// fact separately.
func (a *Archive) Plan(ctx context.Context, dst *slice.Source, req acquire.Request) (*acquire.Plan, error) {
	h := a.reader.Header()
	req.SourceZoom = slice.ZoomRange{Min: h.MinZoom, Max: h.MaxZoom}
	p, err := acquire.PlanFor(ctx, a.acquireArchive(), dst, req)
	if err != nil {
		return nil, fmt.Errorf("fetch: planning a fetch of %s from %s: %w", BoundsText(req.Bounds), a.name, err)
	}
	return p, nil
}

// Fetch carries out a plan, filling dst.
//
// progress may be nil. When it is not, the per-request trace must already
// have been turned off with Silence, or the two will overwrite each other on
// the same stream.
func (a *Archive) Fetch(ctx context.Context, p *acquire.Plan, dst *slice.Source, progress func(acquire.Progress)) (acquire.Result, error) {
	res, err := acquire.Fetch(ctx, p, a.acquireArchive(), dst, acquire.FetchOptions{Progress: progress})
	if err != nil {
		return acquire.Result{}, fmt.Errorf("fetch: fetching %d tiles from %s: %w", p.Tiles, a.name, err)
	}
	return res, nil
}

// acquireArchive is this archive in the shape the acquisition package takes
// it. Bound together in one place so that a plan made against one archive
// cannot be fetched from another.
func (a *Archive) acquireArchive() acquire.Archive {
	return acquire.Archive{Index: a.reader, Bytes: a.bytes, Name: a.name}
}

// BoundsText spells an area for a message, in the order the flags and the
// manifest use.
func BoundsText(b slice.Bounds) string {
	return fmt.Sprintf("west %.4f, south %.4f, east %.4f, north %.4f", b.West, b.South, b.East, b.North)
}

// closers closes several things and reports the first failure, for the local
// archive's two handles on one file.
type closers []io.Closer

func (cs closers) Close() error {
	var first error
	for _, c := range cs {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
