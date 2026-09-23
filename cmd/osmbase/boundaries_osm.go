package main

import (
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

// osmBoundaries builds a derived boundary file from an OpenStreetMap extract.
func osmBoundaries(source, region, levelList, dir string, keep, yes bool, stdout, stderr io.Writer) error {
	levels, err := parseAdminLevels(levelList)
	if err != nil {
		return err
	}
	if region == "" {
		if region, err = regionFrom(source); err != nil {
			return err
		}
	}
	name, err := boundary.DerivedFile(region)
	if err != nil {
		return usageErrorf("%v; pass --region to give it one", err)
	}

	remote, err := isRemote(source)
	if err != nil {
		return err
	}

	if err := describeOSM(stderr, source, region, name, dir, levels, remote, keep); err != nil {
		return err
	}
	if !yes {
		ok, err := confirmBoundaries(stdout)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(stdout, "stopped; nothing was downloaded or written.")
			return nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	extract := source
	if remote {
		// Downloaded beside the output rather than into a system temporary
		// directory, because it is half a gigabyte and the store is the
		// place the user has already said may hold that much.
		path, n, err := downloadExtract(dir, region, source)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "osmbase: downloaded %s\n", humanBytes(n))
		extract = path
		if !keep {
			// Removed whatever happens next: the extract is an input this
			// command chose to fetch, and leaving half a gigabyte behind
			// after a failure is not the command's to decide.
			defer os.Remove(path)
		}
	}

	set, rep, err := buildFromExtract(extract, source, levels, stderr)
	if err != nil {
		return err
	}
	out := filepath.Join(dir, name)
	written, err := saveThroughTemp(dir, name, func(w io.Writer) (int64, error) {
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

	reportOSM(stdout, stderr, out, written, set.Len(), rep, keep && remote, extract)
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
func buildFromExtract(path, source string, levels []int, stderr io.Writer) (*boundary.Set, osm.Report, error) {
	open := osm.Open(func() (io.ReadCloser, error) { return os.Open(path) })

	start := time.Now()
	fmt.Fprintf(stderr, "osmbase: reading the extract three times; a country takes about half a minute\n")
	boundaries, err := osm.Read(open, osm.Options{Levels: levels})
	if err != nil {
		return nil, osm.Report{}, fmt.Errorf("reading %s: %w", path, err)
	}

	areas, rep, err := osm.Areas(boundaries)
	if err != nil {
		return nil, rep, err
	}
	fmt.Fprintf(stderr, "osmbase: %d boundaries in %s\n", len(boundaries), time.Since(start).Round(time.Second))

	return boundary.NewSet(osm.Provenance(source, levels), areas), rep, nil
}

// describeOSM says what the command is about to do, including the obligation.
func describeOSM(w io.Writer, source, region, name, dir string, levels []int, remote, keep bool) error {
	if remote {
		u, err := url.Parse(source)
		if err != nil {
			return fmt.Errorf("%q is not a URL: %w", source, err)
		}
		fmt.Fprintf(w, "osmbase: this downloads an OpenStreetMap extract from %s.\n", u.Host)
		fmt.Fprintf(w, "osmbase:   A country extract is hundreds of megabytes. It tells that host which\n")
		fmt.Fprintf(w, "osmbase:   region interests you and nothing about where you have been.\n")
		if !keep {
			fmt.Fprintf(w, "osmbase:   It is deleted once the boundaries are built; pass --keep-extract to keep it.\n")
		}
	} else {
		fmt.Fprintf(w, "osmbase: this reads %s. Nothing is downloaded and the file is left alone.\n", source)
	}
	fmt.Fprintf(w, "osmbase: it writes %s\n", filepath.Join(dir, name))
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
	return nil
}

// reportOSM says what was built, and what the user now holds.
func reportOSM(stdout, stderr io.Writer, out string, written int64, areas int, rep osm.Report, kept bool, extract string) {
	fmt.Fprintf(stderr, "osmbase: %-46s %s\n", filepath.Base(out), humanBytes(written))
	fmt.Fprintf(stderr, "osmbase: %d areas: %d outlines, %d holes\n", areas, rep.Outlines, rep.Holes)

	// The counts that are facts about the EXTRACT rather than about this
	// command, and which the person who chose the extract is the one who can
	// act on.
	if rep.Partial > 0 || rep.Unclosed > 0 {
		fmt.Fprintf(stderr, "osmbase: %d boundaries had gaps and %d produced no outline at all.\n",
			rep.Partial, rep.Unclosed)
		fmt.Fprintf(stderr, "osmbase:   That is normal at the edge of a cut-out extract: a boundary running\n")
		fmt.Fprintf(stderr, "osmbase:   along a border or a coastline continues past it. A wider extract\n")
		fmt.Fprintf(stderr, "osmbase:   closes more of them.\n")
	}
	if rep.OrphanHoles > 0 {
		fmt.Fprintf(stderr, "osmbase: %d holes lie inside no outline this extract holds.\n", rep.OrphanHoles)
	}
	if rep.WrappedRings > 0 {
		fmt.Fprintf(stderr, "osmbase: %d rings cross the antimeridian and were left out; this cannot test\n", rep.WrappedRings)
		fmt.Fprintf(stderr, "osmbase:   a point against one. https://www.openstreetmap.org/copyright\n")
	}
	if kept {
		fmt.Fprintf(stderr, "osmbase: the extract is still at %s\n", extract)
	}

	if areas == 0 {
		fmt.Fprintf(stdout, "\nNo boundaries were found. This extract carries no administrative relations\n")
		fmt.Fprintf(stdout, "at the levels asked for -- which is a property of how the region is mapped,\n")
		fmt.Fprintf(stdout, "not of the extract being wrong. Try without --levels to see what it has.\n")
		return
	}
	fmt.Fprintf(stdout, "\nNow: osmbase locate --lat LAT --lon LON\n")
	fmt.Fprintf(stdout, "Passing that file to anybody else passes the ODbL with it.\n")
}

// downloadExtract fetches an extract into dir and returns where it landed.
func downloadExtract(dir, region, source string) (string, int64, error) {
	name := "extract_" + region + ".osm.pbf"
	written, err := saveThroughTemp(dir, name, func(w io.Writer) (int64, error) {
		return acquire.Download(source, w, maxExtractBytes)
	})
	if err != nil {
		return "", 0, err
	}
	return filepath.Join(dir, name), written, nil
}

// isRemote says whether a source is something to fetch or something to read.
func isRemote(source string) (bool, error) {
	if !strings.Contains(source, "://") {
		return false, nil
	}
	u, err := url.Parse(source)
	if err != nil {
		return false, usageErrorf("--osm %q is neither a path nor a URL: %v", source, err)
	}
	switch u.Scheme {
	case "http", "https":
		return true, nil
	}
	return false, usageErrorf("--osm %q uses the %q scheme; this fetches over http and https, and otherwise reads a local file",
		source, u.Scheme)
}

// regionFrom names the output after the source, which is what a person would
// have called it anyway.
func regionFrom(source string) (string, error) {
	base := source
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".pbf")
	base = strings.TrimSuffix(base, ".osm")
	base = strings.TrimSuffix(base, "-latest")
	if base == "" || !boundary.ValidRegion(base) {
		return "", usageErrorf("cannot name the output after %q; pass --region NAME", source)
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
