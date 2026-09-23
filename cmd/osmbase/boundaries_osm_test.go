package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/osmbasetest"
)

// extractFile writes a synthetic extract holding one named boundary and
// returns its path.
func extractFile(t *testing.T, dir, name string) string {
	t.Helper()
	e := osmbasetest.NewExtract().
		Node(1, -33.70, 151.10).
		Node(2, -33.70, 151.20).
		Node(3, -33.60, 151.20).
		Node(4, -33.60, 151.10).
		Way(10, []int64{1, 2, 3}).
		Way(11, []int64{3, 4, 1})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "way", ID: 11, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "9", "name", "Hornsby")

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, e.Bytes(), 0o644); err != nil {
		t.Fatalf("writing the extract: %v", err)
	}
	return path
}

func runBoundaries(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = boundariesCommand(args, &out, &errb)
	return out.String(), errb.String(), err
}

// The end of the pipeline: an extract on disk becomes a file that answers
// containment.
func TestBoundariesFromALocalExtract(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	stdout, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--yes")
	if err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}

	// The extract is the user's file and this did not fetch it, so it stays.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the extract this was pointed at is gone: %v", err)
	}

	out := filepath.Join(boundary.Dir(store), "osm_sydney.osmb")
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("opening what it wrote: %v", err)
	}
	defer f.Close()
	set, err := boundary.ReadDerived(f)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("the file holds %d areas, want 1", set.Len())
	}
	a, ok := set.At(-33.65, 151.15)
	if !ok || a.Name != "Hornsby" {
		t.Errorf("a point inside the boundary resolved to %q (%v), want Hornsby", a.Name, ok)
	}

	// The licence obligation travels in the file, not only in the terminal.
	if got := set.Provenance().Attribution; !strings.Contains(got, "ODbL") {
		t.Errorf("the file's attribution is %q", got)
	}
	// The BASE name, not the path it was read from. This file exists to be
	// handed to other people -- that is the whole share-alike argument the
	// command prints -- and the path carries a username and whatever the
	// directory is called. The provenance only has to tell two stores' files
	// apart.
	if got := set.Provenance().Source; got != filepath.Base(src) {
		t.Errorf("the file says it came from %q, want just %q", got, filepath.Base(src))
	}
	if strings.Contains(set.Provenance().Source, string(os.PathSeparator)) {
		t.Errorf("the file carries a path: %q", set.Provenance().Source)
	}

	// And in the terminal, before the file exists rather than after.
	if !strings.Contains(stderr, "DERIVATIVE DATABASE") || !strings.Contains(stderr, "ODbL") {
		t.Errorf("the run did not state the share-alike obligation:\n%s", stderr)
	}
	if !strings.Contains(stdout, "passes the ODbL with it") {
		t.Errorf("the summary did not say what passing the file on means:\n%s", stdout)
	}
}

// A fetched extract is this command's own doing, so it clears up after
// itself -- half a gigabyte left behind is not the command's to decide.
func TestBoundariesDeletesTheExtractItFetched(t *testing.T) {
	dir := t.TempDir()
	body, err := os.ReadFile(extractFile(t, dir, "src.osm.pbf"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	store := filepath.Join(dir, "store")
	for _, tc := range []struct {
		name string
		args []string
		kept bool
	}{
		{"deleted by default", nil, false},
		{"kept when asked", []string{"--keep-extract"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--store", store, "--osm", srv.URL + "/hornsby-latest.osm.pbf", "--yes"}, tc.args...)
			if _, stderr, err := runBoundaries(t, args...); err != nil {
				t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
			}
			extract := filepath.Join(boundary.Dir(store), "extract_hornsby.osm.pbf")
			_, err := os.Stat(extract)
			if tc.kept && err != nil {
				t.Errorf("--keep-extract did not keep it: %v", err)
			}
			if !tc.kept && err == nil {
				t.Error("the fetched extract was left behind")
			}
			// Either way the boundaries were built.
			if _, err := os.Stat(filepath.Join(boundary.Dir(store), "osm_hornsby.osmb")); err != nil {
				t.Errorf("no boundary file: %v", err)
			}
		})
	}
}

// Saying no must leave nothing at all.
func TestBoundariesAsksBeforeFetching(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	// Stdin is not a terminal under test, and confirmBoundaries reads a
	// closed stdin as a no.
	stdout, _, err := runBoundaries(t, "--store", store, "--osm", src)
	if err != nil {
		t.Fatalf("boundaries: %v", err)
	}
	if !strings.Contains(stdout, "stopped") {
		t.Errorf("declining did not say so:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(boundary.Dir(store), "osm_sydney.osmb")); err == nil {
		t.Error("a file was written after the prompt was declined")
	}
}

func TestBoundariesOSMArgumentsAreChecked(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a level that is not one", []string{"--osm", src, "--levels", "99"}, "admin_level"},
		{"a level that is not a number", []string{"--osm", src, "--levels", "suburb"}, "admin_level"},
		{"a scheme this does not fetch", []string{"--osm", "ftp://host/x.pbf"}, "scheme"},
		{"a region that would leave the store", []string{"--osm", src, "--region", "../escape"}, "region"},
		{"--region without --osm", []string{"--region", "x"}, "only means something"},
		{"--levels without --osm", []string{"--levels", "9"}, "only means something"},
		{"--keep-extract without --osm", []string{"--keep-extract"}, "only means something"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runBoundaries(t, append([]string{"--store", store, "--yes"}, tc.args...)...)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An extract carrying nothing at the levels asked for is not a failure: it is
// a fact about how that region is mapped, and the message has to say so or
// the user goes looking for a broken download.
func TestAnExtractWithNoBoundariesAtTheseLevelsExplainsItself(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	stdout, _, err := runBoundaries(t, "--store", store, "--osm", src, "--levels", "4", "--yes")
	if err == nil {
		// Read returns ErrNoBoundaries, which the command surfaces.
		if !strings.Contains(stdout, "how the region is mapped") {
			t.Errorf("no explanation of an empty result:\n%s", stdout)
		}
		return
	}
	if !strings.Contains(err.Error(), "no administrative boundaries") {
		t.Errorf("error %q, want it to say the extract holds none at these levels", err)
	}
}
