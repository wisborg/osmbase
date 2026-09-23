package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/osmbasetest"
)

// The derived file is meant to be handed to other people, so what it says
// about where it came from must not be a credential. An archive URL is one of
// the places a credential legitimately lives: userinfo for a private mirror,
// a signature in the query for anything presigned.
func TestAFetchedFileDoesNotRecordTheCredentialsItWasFetchedWith(t *testing.T) {
	good := extractBytes(t)
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })

	// httptest gives http://127.0.0.1:PORT; splice credentials and a signed
	// query into it, as a private mirror or an object store would.
	withSecrets := strings.Replace(srv.URL, "http://", "http://someone:hunter2@", 1) +
		"/hornsby-latest.osm.pbf?X-Amz-Signature=deadbeefcafe"

	store := filepath.Join(t.TempDir(), "store")
	_, stderr, err := runBoundaries(t, "--store", store, "--osm", withSecrets, "--yes")
	if err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}

	f, err := os.Open(filepath.Join(boundary.Dir(store), "osm_hornsby.osmb"))
	if err != nil {
		t.Fatalf("opening what it wrote: %v", err)
	}
	defer f.Close()
	set, err := boundary.ReadDerived(f)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}

	source := set.Provenance().Source
	for _, secret := range []string{"hunter2", "someone", "deadbeefcafe", "X-Amz-Signature"} {
		if strings.Contains(source, secret) {
			t.Errorf("the file records %q, which carries %q", source, secret)
		}
	}
	// And it still says enough to tell two stores' files apart.
	if !strings.Contains(source, "hornsby") {
		t.Errorf("the file records %q, which says nothing about where it came from", source)
	}
	if strings.Contains(stderr, "hunter2") {
		t.Errorf("the run printed the credential:\n%s", stderr)
	}
}

// And the same string reaches stderr through a usage error, which is the
// stream people paste into issue reports.
func TestAUsageErrorDoesNotEchoACredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
	}{
		// The last segment holds a query, so no region can be named from it.
		{"no region can be named", "https://someone:hunter2@host.example/download?region=dk"},
		{"a scheme this does not fetch", "ftp://someone:hunter2@host.example/denmark.osm.pbf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "store")
			_, _, err := runBoundaries(t, "--store", store, "--osm", tc.source, "--yes")
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the error carries the credential: %v", err)
			}
		})
	}
}

// Nothing in this program handles a signal, so an interrupted download skips
// the deferred cleanup -- and the temporary name carries a random suffix, so
// nothing ever overwrites it. Repeating an interrupted fetch is how a cache
// directory quietly acquires several gigabytes.
func TestAnInterruptedRunsLeftoversAreSweptUp(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")
	bdir := boundary.Dir(store)
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		t.Fatal(err)
	}

	leftovers := []string{
		"extract_denmark.osm.pbf.3820194471",
		"osm_denmark.osmb.117",
	}
	keep := []string{
		"ne_10m_admin_0_countries.geojson", // the Natural Earth outlines
		"osm_hornsby.osmb",                 // a finished file
		"extract_kept.osm.pbf",             // one --keep-extract left on purpose
		// Ends in a dot and digits, like a temporary, but is not one of
		// this program's names. The sweep runs in a directory the user may
		// have put things in, so it is scoped to what this writes.
		"field-notes.txt.42",
	}
	for _, n := range append(append([]string{}, leftovers...), keep...) {
		if err := os.WriteFile(filepath.Join(bdir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--yes"); err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}

	for _, n := range leftovers {
		if _, err := os.Stat(filepath.Join(bdir, n)); err == nil {
			t.Errorf("%s survived; an interrupted run's debris is never swept", n)
		}
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(bdir, n)); err != nil {
			t.Errorf("%s was removed, and it is not a leftover: %v", n, err)
		}
	}
}

// Replacing a file the user already has is worth saying before it happens.
func TestReplacingAnExistingFileIsAnnounced(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	_, first, err := runBoundaries(t, "--store", store, "--osm", src, "--yes")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if strings.Contains(first, "REPLACES") {
		t.Errorf("the first run claimed to replace something:\n%s", first)
	}

	_, second, err := runBoundaries(t, "--store", store, "--osm", src, "--yes")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(second, "REPLACES") {
		t.Errorf("the second run did not say it was replacing the first:\n%s", second)
	}
}

// A typo must not be discovered after the licence notice, the prompt and a
// new directory.
func TestAMissingLocalExtractIsFoundBeforeAnythingHappens(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	_, stderr, err := runBoundaries(t, "--store", store, "--osm", "/no/such/extract.osm.pbf", "--yes")
	if err == nil {
		t.Fatal("a missing extract was accepted")
	}
	if strings.Contains(stderr, "DERIVATIVE DATABASE") {
		t.Errorf("an obligation was stated for a run that could not start:\n%s", stderr)
	}
	if _, err := os.Stat(boundary.Dir(store)); err == nil {
		t.Error("a store directory was created for a run that could not start")
	}
}

// --detail belongs to the Natural Earth outlines. Accepting and ignoring it
// is worse than refusing it: the user asked for something and got silence,
// and this is the one flag in the command with a path-traversal history.
func TestDetailIsRefusedWithOSM(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	_, _, err := runBoundaries(t, "--store", filepath.Join(dir, "store"),
		"--osm", src, "--detail", "50m", "--yes")
	if err == nil {
		t.Fatal("--detail was accepted with --osm")
	}
	if !strings.Contains(err.Error(), "detail") {
		t.Errorf("error %q does not name the flag", err)
	}
}

// An extract carrying no administrative relations at the levels asked for is
// not a failure. It is the common way for a run to come back with nothing,
// and what the user needs is the reason -- not an error naming a temporary
// file that has already been deleted.
func TestNoRelationsAtTheseLevelsIsExplainedNotReportedAsAFailure(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf") // carries admin_level 9
	store := filepath.Join(dir, "store")

	stdout, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--levels", "4", "--yes")
	if err != nil {
		t.Fatalf("an extract with nothing at level 4 was reported as a failure: %v\n%s", err, stderr)
	}
	for _, want := range []string{"No administrative boundaries", "admin_level 4", "how the region is mapped", "without --levels"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the explanation does not mention %q:\n%s", want, stdout)
		}
	}
	// And nothing was written, so a later run does not find an empty file
	// and read it as "this region has no suburbs".
	if _, err := os.Stat(filepath.Join(boundary.Dir(store), "osm_sydney.osmb")); err == nil {
		t.Error("a file was written for a run that found nothing")
	}
}

// And when levels were never passed, the advice to drop them is not given.
func TestTheEmptyExplanationDoesNotAdviseDroppingAFlagNobodyPassed(t *testing.T) {
	dir := t.TempDir()
	e := extractWithNoRelations(t, dir)
	store := filepath.Join(dir, "store")

	stdout, _, err := runBoundaries(t, "--store", store, "--osm", e, "--yes")
	if err != nil {
		t.Fatalf("boundaries --osm: %v", err)
	}
	if !strings.Contains(stdout, "No administrative boundaries") {
		t.Errorf("no explanation:\n%s", stdout)
	}
	if strings.Contains(stdout, "without --levels") {
		t.Errorf("the summary advises dropping a flag that was not passed:\n%s", stdout)
	}
}

// extractWithNoRelations writes an extract holding ways and nodes but no
// administrative relation at all.
func extractWithNoRelations(t *testing.T, dir string) string {
	t.Helper()
	e := osmbasetest.NewExtract().
		Node(1, -33.70, 151.10).
		Node(2, -33.60, 151.20).
		Way(10, []int64{1, 2}, "highway", "residential")
	path := filepath.Join(dir, "plain.osm.pbf")
	if err := os.WriteFile(path, e.Bytes(), 0o644); err != nil {
		t.Fatalf("writing the extract: %v", err)
	}
	return path
}
