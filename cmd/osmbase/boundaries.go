package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/slice"
)

// boundarySourceURL is where the outlines come from.
//
// The natural-earth-vector repository, which is Natural Earth's own GeoJSON
// distribution -- the project publishes shapefiles from its site and this
// repository alongside them, maintained by one of its two authors. GeoJSON
// rather than the shapefile because it is read with encoding/json and a
// shapefile would mean a DBF parser for the attributes, which is a decoder
// written for one file format used once.
//
// Public domain, so there is nothing to record in NOTICE and nothing that
// propagates to what a consumer does with the answers. That is the whole
// reason this is the first boundary source: see docs/locate.md.
// The ref is a released TAG and not master, which the first version used.
// An unpinned branch makes the confirmation prompt's own claim -- that the
// files are the same for everybody -- untrue the moment upstream changes,
// and it means two runs of this command a week apart can disagree with
// nothing to say they did.
const boundarySourceURL = "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/v5.1.2/geojson/"

// maxBoundaryBytes caps a single boundary download.
//
// The largest file this asks for is the 10m state outlines at about 41 MB, so
// 128 is generous room for upstream growth and still refuses a response that
// has stopped being a GeoJSON file and started being a way to fill somebody's
// disk. See acquire.Download.
const maxBoundaryBytes = 128 << 20

func boundariesUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase boundaries --store DIR [--detail 50m] [--yes]

Download the country and state outlines that let "osmbase locate" say a
coordinate is IN a country rather than near one.

Without them every answer is the nearest named point, which for a country is
a label anchor that may be hundreds of kilometres away. With them, country and
region become statements of fact and the rest stays as it was -- there are no
suburb outlines in this data, and there is no honest way to invent them.

The data is Natural Earth, which is public domain: no attribution is required
and nothing about using it attaches to what you do with the answers.

The default is the finest set, about 54 MB, because it is the only one whose
state outlines cover the world: the 50m set has subdivisions for nine countries
and none for Denmark, Germany, France or the United Kingdom.

examples:
  osmbase boundaries --store ~/Library/Caches/osmbase
      about 54 MB, and a coordinate can then be placed in a country and a state
      anywhere in the world

  osmbase boundaries --store DIR --detail 50m
      about 5 MB. Countries are covered in full; states are covered for nine
      countries only, so region is usually unanswered

`)
	printFlags(w, fs)
}

func boundariesCommand(args []string, stdout, stderr io.Writer) error {
	var (
		store  string
		detail string
		yes    bool
	)
	fs := newFlagSet("boundaries", boundariesUsage)
	fs.StringVar(&store, "store", "", "directory to keep the outlines in (default: the osmbase folder under your user cache directory)")
	fs.StringVar(&detail, "detail", boundary.DefaultDetail,
		"how fine the outlines are: "+strings.Join(boundary.Details, ", ")+". Finer is more accurate near a border and slower to read on every lookup")
	fs.BoolVar(&yes, "yes", false, "do not ask before downloading")

	if _, err := parseArgs(fs, args, stdout); err != nil {
		return err
	}
	if !slices.Contains(boundary.Details, detail) {
		return usageErrorf("--detail %q is not one this command knows; it has %s",
			detail, strings.Join(boundary.Details, ", "))
	}

	root := store
	if root == "" {
		var err error
		if root, err = slice.DefaultRoot(); err != nil {
			return fmt.Errorf("finding the default store: %w; pass --store to say where to keep the outlines", err)
		}
	}
	dir := boundary.Dir(root)

	files := []string{boundary.File(detail, false), boundary.File(detail, true)}
	fmt.Fprintf(stderr, "osmbase: this downloads two files from raw.githubusercontent.com:\n")
	for _, f := range files {
		fmt.Fprintf(stderr, "osmbase:   %s\n", f)
	}
	fmt.Fprintf(stderr, "osmbase: it tells that host you are fetching world outlines and nothing about\n")
	fmt.Fprintf(stderr, "osmbase:   where you have been -- the files are the same for everybody.\n")
	fmt.Fprintf(stderr, "osmbase: they go in %s\n", dir)

	if !yes {
		ok, err := confirmBoundaries(stdout)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(stdout, "stopped; nothing was downloaded.")
			return nil
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	for _, name := range files {
		n, err := downloadBoundary(dir, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "osmbase: %-46s %s\n", name, humanBytes(n))
	}
	fmt.Fprintf(stdout, "\nNow: osmbase locate --lat LAT --lon LON\n")
	fmt.Fprintf(stdout, "Country and region are answered by containment; the rest stays nearest-feature.\n")
	return nil
}

// downloadBoundary fetches one file into dir.
//
// The transfer goes through acquire, which is the only package in this library
// that opens a socket -- a property the architecture states as something a
// reader can verify from the import list. The first version of this did its
// own http.Get from here, which worked and quietly gave up everything acquire
// centralises: the User-Agent that says who is calling, the redirect policy
// that refuses a host change or a scheme downgrade, and any bound at all on
// what a response may write to the disk.
func downloadBoundary(dir, name string) (int64, error) {
	return saveThroughTemp(dir, name, func(w io.Writer) (int64, error) {
		return acquire.Download(boundarySourceURL+name, w, maxBoundaryBytes)
	})
}

// saveThroughTemp writes under a temporary name and renames into place.
//
// So that an interrupted or refused download leaves NOTHING rather than a
// truncated file. A half-written GeoJSON survives the existence check that
// decides whether a store has boundaries at all, and then fails to parse on
// the next lookup -- reported against that lookup, with nothing to connect it
// to the fetch that caused it.
//
// Separated from the fetching so it can be tested without a network: the
// caller hands in whatever writes the bytes, including something that fails
// part way through.
func saveThroughTemp(dir, name string, write func(io.Writer) (int64, error)) (int64, error) {
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return 0, fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	n, err := write(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return 0, fmt.Errorf("putting %s in place: %w", name, err)
	}
	return n, nil
}

// confirmBoundaries asks before the one network access this command makes.
//
// Its own function rather than the fetch command's confirm, which reads a plan
// it would have to be handed nil -- and which asks a different question. A
// tile fetch tells a host which few-kilometre squares interest you; this tells
// it only that somebody wanted world outlines, which is the same request every
// user of this command makes.
func confirmBoundaries(w io.Writer) (bool, error) {
	fmt.Fprintf(w, "Continue? [y/N] ")
	var answer string
	if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil {
		// A closed or empty stdin is a no rather than an error, for the same
		// reason it is in the fetch command: a pipeline that reached this
		// prompt did not mean to download anything.
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
