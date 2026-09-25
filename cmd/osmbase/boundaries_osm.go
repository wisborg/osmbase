package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/boundary/osm"
)

// maxExtractBytes caps an extract download.
//
// A country extract runs from tens of megabytes to about a gigabyte --
// Denmark is 472 MB, Australia 964 MB -- so four leaves room for the largest
// of them to grow and still refuses a planet file, which is eighty and which
// this pipeline is not built for: it holds a country's boundary nodes in
// memory, and the plan says so.
const maxExtractBytes = 4 << 30

// extractTimeout bounds the whole transfer.
//
// acquire's default is ten minutes, which is sized for the tens of megabytes
// the rest of this library fetches; a gigabyte inside that needs a sustained
// thirteen megabits, so an ordinary link would be cut off at ninety per cent
// -- the exact failure that constant's own reasoning exists to avoid.
//
// A whole-transfer deadline is the wrong tool and this is the best one
// available: what a slow download wants is a deadline on making NO progress,
// so that a stalled connection dies quickly and a slow one is left alone.
// acquire has no such hook. Two hours is 4 GiB at five megabits, which is
// generous enough that reaching it means something is wrong rather than slow.
const extractTimeout = 2 * time.Hour

// osmOptions is what the --osm half of the command was asked to do.
//
// A struct rather than eight parameters, two of them adjacent bools. This
// package's own boundary.Layer records the lesson: "a second bool beside the
// first would be a parameter list nobody can read at the call site".
type osmOptions struct {
	ctx    context.Context
	source string
	region string
	levels string
	dir    string
	keep   bool
	yes    bool

	stdin          io.Reader
	stdout, stderr io.Writer
}

// osmBoundaries builds a derived boundary file from an OpenStreetMap extract.
func osmBoundaries(o osmOptions) error {
	levels, err := parseAdminLevels(o.levels)
	if err != nil {
		return err
	}
	region := o.region
	if region == "" {
		if region, err = regionFrom(o.source); err != nil {
			return err
		}
	}
	name, err := boundary.DerivedFile(region)
	if err != nil {
		return usageErrorf("--region %q cannot name a file; it may hold letters, digits, dots, dashes and underscores", region)
	}

	remote, host, err := resolveSource(o.source)
	if err != nil {
		return err
	}
	if !remote {
		// Checked before anything is said or created. A typo otherwise
		// leaves a new empty directory behind and makes the user read a
		// licence obligation for a run that could not start.
		if _, err := os.Stat(o.source); err != nil {
			return fmt.Errorf("reading the extract: %w", err)
		}
	}

	describeOSM(o.stderr, o, region, name, host, remote, levels)
	if !o.yes {
		ok, err := confirmBoundaries(o.stdin, o.stderr)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(o.stdout, "stopped; nothing was downloaded or written.")
			return nil
		}
	}
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", o.dir, err)
	}
	sweepLeftovers(o.dir, o.stderr)

	extract := o.source
	if remote {
		path, n, err := downloadExtract(o.ctx, o.dir, region, o.source)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.stderr, "osmbase: downloaded %s\n", humanBytes(n))
		extract = path
		if !o.keep {
			// Removed whatever happens next: the extract is an input this
			// command chose to fetch, and leaving half a gigabyte behind
			// after a failure is not the command's to decide.
			defer removeExtract(path, o.stderr)
		} else {
			defer fmt.Fprintf(o.stderr, "osmbase: the extract is at %s\n", path)
		}
	}

	set, rep, err := buildFromExtract(o.ctx, extract, o.source, levels, o.stderr)
	if err != nil {
		if errors.Is(err, osm.ErrNoBoundaries) {
			// Not a failure, and the error alone does not say so. It is the
			// common way for a run to come back with nothing, and what the
			// user needs is the reason rather than a message naming a
			// temporary file that has already been deleted.
			explainEmpty(o.stdout, region, levels)
			return nil
		}
		return err
	}

	written, err := saveThroughTemp(o.dir, name, func(w io.Writer) (int64, error) {
		cw := &countingWriter{w: w}
		// Spelled out rather than "return cw.n, WriteDerived(cw, set)": the
		// Go specification does not fix the order of a plain operand read
		// against a call in the same return statement, so the count could be
		// read before anything had been written. The same shape was found in
		// the protobuf reader by review; it is worth not writing twice.
		err := boundary.WriteDerived(cw, set)
		return cw.n, err
	})
	if err != nil {
		return err
	}

	reportOSM(o, filepath.Join(o.dir, name), written, set.Len(), rep)
	return nil
}

// countingWriter counts what passes through it, so saveThroughTemp can report
// a size for a writer that does not return one.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// buildFromExtract runs the three passes, the assembly and the conversion.
//
// source rather than path in the errors and the provenance: path may be a
// working copy this command is about to delete, and naming a file the user
// cannot go and look at is worse than naming nothing.
func buildFromExtract(ctx context.Context, path, source string, levels []int, stderr io.Writer) (*boundary.Set, osm.Report, error) {
	// Read through ctx, so Ctrl-C stops the three passes at the next block
	// rather than after them: 25 seconds for Denmark, and a planet would be
	// hours. The osm package needs no context of its own for this -- a read
	// that fails is a read that fails, and it stops there.
	open := osm.Open(func() (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return ctxReader{ctx: ctx, ReadCloser: f}, nil
	})

	start := time.Now()
	fmt.Fprintf(stderr, "osmbase: reading the extract three times; a country takes about half a minute\n")
	boundaries, err := osm.Read(open, osm.Options{Levels: levels})
	if err != nil {
		return nil, osm.Report{}, fmt.Errorf("reading %s: %w", acquire.Redact(source), err)
	}

	areas, rep, err := osm.Areas(boundaries)
	if err != nil {
		return nil, rep, err
	}
	fmt.Fprintf(stderr, "osmbase: %d boundaries in %s\n", len(boundaries), time.Since(start).Round(time.Second))

	// The source is recorded in a file this command tells the user to hand to
	// other people, so it is redacted the way acquire redacts a URL in an
	// error -- a private mirror's userinfo and a presigned query are exactly
	// the things that legitimately live in an archive URL -- and a local path
	// is reduced to its base name, which is all the provenance claim needs
	// and does not carry somebody's home directory into a shared artefact.
	return boundary.NewSet(osm.Provenance(provenanceSource(source), levels), areas), rep, nil
}

// provenanceSource is what the file should say it came from.
func provenanceSource(source string) string {
	if strings.Contains(source, "://") {
		return acquire.Redact(source)
	}
	return filepath.Base(source)
}

// describeOSM says what the command is about to do, including the obligation.
//
// On stderr, with the prompt, because a disclosure the user is being asked to
// agree to has to be on the stream the question is on: with them split,
// redirecting stderr away leaves a bare "Continue? [y/N]" and no statement of
// what is about to be contacted or what attaches to the result.
func describeOSM(w io.Writer, o osmOptions, region, name, host string, remote bool, levels []int) {
	if remote {
		fmt.Fprintf(w, "osmbase: this downloads an OpenStreetMap extract from %s.\n", host)
		fmt.Fprintf(w, "osmbase:   A country extract is hundreds of megabytes. It tells that host which\n")
		fmt.Fprintf(w, "osmbase:   region interests you and nothing about where you have been.\n")
		if !o.keep {
			fmt.Fprintf(w, "osmbase:   It is deleted when the build finishes; pass --keep-extract to keep it.\n")
		}
	} else {
		fmt.Fprintf(w, "osmbase: this reads %s. Nothing is downloaded and the file is left alone.\n", o.source)
	}

	out := filepath.Join(o.dir, name)
	if _, err := os.Stat(out); err == nil {
		fmt.Fprintf(w, "osmbase: it REPLACES %s\n", out)
	} else {
		fmt.Fprintf(w, "osmbase: it writes %s\n", out)
	}
	if len(levels) == 0 {
		fmt.Fprintf(w, "osmbase:   every admin_level the extract holds, as %q\n", region)
	} else {
		fmt.Fprintf(w, "osmbase:   admin_level %s, as %q\n", joinLevels(levels), region)
	}

	// The obligation, before the file exists rather than after. It is the
	// project's first share-alike term and it attaches to the output, not to
	// this program.
	fmt.Fprintf(w, "osmbase:\n")
	fmt.Fprintf(w, "osmbase: The file it writes is a DERIVATIVE DATABASE under the ODbL, because it is\n")
	fmt.Fprintf(w, "osmbase:   OpenStreetMap's data in another shape rather than a picture made from it.\n")
	fmt.Fprintf(w, "osmbase:   Share-alike attaches: if you pass that file to anybody, they must receive\n")
	fmt.Fprintf(w, "osmbase:   it under the ODbL. https://www.openstreetmap.org/copyright\n")
	fmt.Fprintf(w, "osmbase:   Answering where a coordinate is FROM the file is a Produced Work and needs\n")
	fmt.Fprintf(w, "osmbase:   the credit only: %s\n", osm.Attribution)
	fmt.Fprintf(w, "osmbase:\n")
}

// explainEmpty says why a run found nothing, which an error cannot.
func explainEmpty(w io.Writer, region string, levels []int) {
	fmt.Fprintf(w, "\nNo administrative boundaries in %s", region)
	if len(levels) > 0 {
		fmt.Fprintf(w, " at admin_level %s", joinLevels(levels))
	}
	fmt.Fprintf(w, ".\n")
	fmt.Fprintf(w, "That is a property of how the region is mapped rather than of the extract\n")
	fmt.Fprintf(w, "being wrong: admin_level means different things in different countries, and\n")
	fmt.Fprintf(w, "some map their suburbs as points, which carry no outline to test against.\n")
	if len(levels) > 0 {
		fmt.Fprintf(w, "Run again without --levels to see which levels this extract does carry.\n")
	}
	fmt.Fprintf(w, "Nothing was written.\n")
}

// reportOSM says what was built, and what the user now holds.
func reportOSM(o osmOptions, out string, written int64, areas int, rep osm.Report) {
	fmt.Fprintf(o.stderr, "osmbase: %-46s %s\n", filepath.Base(out), humanBytes(written))
	fmt.Fprintf(o.stderr, "osmbase: %d areas: %d outlines, %d holes\n", areas, rep.Outlines, rep.Holes)

	// The counts that are facts about the EXTRACT rather than about this
	// command, and which the person who chose the extract is the one who can
	// act on. Complete is printed with them so the three that are documented
	// to sum to the total are all visible.
	fmt.Fprintf(o.stderr, "osmbase: %d boundaries closed completely, %d had gaps, %d produced no outline.\n",
		rep.Complete, rep.Partial, rep.Unclosed)

	// Unclosed counts two different things and osm.Report's own doc says this
	// command should say so: a boundary whose ways never met, and one whose
	// outlines closed and then had to be left out for crossing the
	// antimeridian. Only the first is what a wider extract fixes.
	if rep.Partial > 0 || rep.Unclosed > rep.WrappedRings {
		fmt.Fprintf(o.stderr, "osmbase:   A gap is normal at the edge of a cut-out extract: a boundary running\n")
		fmt.Fprintf(o.stderr, "osmbase:   along a border or a coastline continues past it, and a wider extract\n")
		fmt.Fprintf(o.stderr, "osmbase:   closes more of them.\n")
	}
	if rep.WrappedRings > 0 {
		fmt.Fprintf(o.stderr, "osmbase:   %d of them cross the antimeridian, which this cannot test a point\n", rep.WrappedRings)
		fmt.Fprintf(o.stderr, "osmbase:   against; a wider extract does not change those.\n")
	}
	if rep.OrphanHoles > 0 {
		fmt.Fprintf(o.stderr, "osmbase: %d holes lie inside no outline this extract holds.\n", rep.OrphanHoles)
	}

	if areas == 0 {
		// Reachable only when boundaries were found and not one of them
		// produced a usable outline -- which is the extract's edge, not the
		// region's mapping. The no-relations case exits through
		// explainEmpty and never arrives here.
		fmt.Fprintf(o.stdout, "\nBoundaries were found, but none of them closed into an outline, so the file\n")
		fmt.Fprintf(o.stdout, "holds nothing to test a point against. A wider extract is what fixes that.\n")
		return
	}
	fmt.Fprintf(o.stdout, "\nNow: osmbase locate --lat LAT --lon LON\n")
	fmt.Fprintf(o.stdout, "Passing that file to anybody else passes the ODbL with it.\n")
}

// removeExtract deletes the working copy, saying so if it cannot.
//
// The command promised it would be deleted, so a failure to keep that promise
// is the user's business -- half a gigabyte staying behind silently is how a
// cache directory fills up with nothing to explain it.
func removeExtract(path string, stderr io.Writer) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "osmbase: could not delete %s: %v\n", path, err)
	}
}

// sweepLeftovers removes the working copies an interrupted run left behind.
//
// saveThroughTemp writes under a temporary name and removes it on the way out,
// but nothing in this program handles a signal, so an interrupted download
// skips that -- and the name carries a random suffix, so nothing ever
// overwrites it either. Repeating an interrupted fetch of a country extract is
// how a cache directory quietly acquires several gigabytes.
func sweepLeftovers(dir string, stderr io.Writer) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isLeftover(name) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			fmt.Fprintf(stderr, "osmbase: removed %s, left by an interrupted run\n", name)
		}
	}
}

// isLeftover recognises saveThroughTemp's temporary names, which are the
// intended name followed by a dot and digits.
func isLeftover(name string) bool {
	dot := strings.LastIndex(name, ".")
	if dot < 0 || dot == len(name)-1 {
		return false
	}
	for _, r := range name[dot+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	stem := name[:dot]
	return strings.HasSuffix(stem, ".osm.pbf") || strings.HasSuffix(stem, boundary.DerivedExt)
}

// downloadExtract fetches an extract into dir and returns where it landed.
func downloadExtract(ctx context.Context, dir, region, source string) (string, int64, error) {
	name, err := boundary.ExtractFile(region)
	if err != nil {
		return "", 0, err
	}
	written, err := saveThroughTemp(dir, name, func(w io.Writer) (int64, error) {
		return acquire.DownloadContext(ctx, source, w, maxExtractBytes, extractTimeout)
	})
	if err != nil {
		return "", 0, err
	}
	return filepath.Join(dir, name), written, nil
}

// resolveSource says whether a source is something to fetch, and from where.
//
// The host comes back with the answer rather than being parsed again by
// whatever prints it: the string was parsed here, and a second parse is a
// second chance to disagree about what it says.
func resolveSource(source string) (remote bool, host string, err error) {
	if !strings.Contains(source, "://") {
		return false, "", nil
	}
	u, parseErr := url.Parse(source)
	if parseErr != nil {
		return false, "", usageErrorf("--osm %s is neither a path nor a URL: %v", acquire.Redact(source), parseErr)
	}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return false, "", usageErrorf("--osm %s names no host to fetch from", acquire.Redact(source))
		}
		return true, u.Host, nil
	}
	return false, "", usageErrorf("--osm %s uses the %q scheme; this fetches over http and https, and otherwise reads a local file",
		acquire.Redact(source), u.Scheme)
}

// regionFrom names the output after the source, which is what a person would
// have called it anyway.
func regionFrom(source string) (string, error) {
	base := source
	// A URL is parsed rather than sliced, so that the region comes from the
	// PATH. Sliced, a presigned link's signature and a fragment end up in
	// what would be the filename and the command refuses a URL whose path
	// names the region perfectly well -- and a presigned link is exactly
	// where a credential lives, so that refusal lands in the one case that
	// most wants to work.
	if u, err := url.Parse(source); err == nil && u.Scheme != "" && u.Path != "" {
		base = u.Path
	}
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".pbf")
	base = strings.TrimSuffix(base, ".osm")
	base = strings.TrimSuffix(base, "-latest")
	if base == "" || !boundary.ValidRegion(base) {
		return "", usageErrorf("cannot name the output after %s; pass --region NAME", acquire.Redact(source))
	}
	return base, nil
}

// parseAdminLevels reads the --levels list of OSM admin_level numbers.
//
// Not the locate command's parseLevels, which reads the NAMES of this
// library's own levels -- country, locality, street. These are OpenStreetMap's
// numbers, whose meaning differs by country, which is why they stay numbers.
func parseAdminLevels(list string) ([]int, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []int
	for _, f := range strings.Split(list, ",") {
		f = strings.TrimSpace(f)
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > 12 {
			return nil, usageErrorf("--levels %q: %q is not an admin_level; they run from 1 to 12", list, f)
		}
		out = append(out, n)
	}
	return out, nil
}

func joinLevels(levels []int) string {
	parts := make([]string, len(levels))
	for i, l := range levels {
		parts[i] = strconv.Itoa(l)
	}
	return strings.Join(parts, ", ")
}

// ctxReader is a reader that fails once ctx is done, with ctx's error, so an
// interruption surfaces through whatever is reading as that error.
type ctxReader struct {
	ctx context.Context
	io.ReadCloser
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}
