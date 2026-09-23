package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/osmbasetest"
)

// serveBytes answers every request with the same body, and reports how many
// requests it took.
func serveBody(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// extractBytes is a valid synthetic extract as bytes, for serving.
func extractBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(extractFile(t, t.TempDir(), "src.osm.pbf"))
	if err != nil {
		t.Fatalf("reading the fixture back: %v", err)
	}
	return b
}

// inStore lists what the boundaries directory holds, "" for a directory that
// was never created.
func inStore(t *testing.T, store string) []string {
	t.Helper()
	ents, err := os.ReadDir(boundary.Dir(store))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// A download that goes wrong must leave the store exactly as it found it.
//
// Every case here is one a real mirror produces, and the two that matter most
// are the ones that do NOT look like failures on the wire: a proxy or a
// rewritten 404 answering 200 with an HTML page, and a connection that drops
// after some of the body. Both write bytes into the store before anything
// notices. If either left its file behind, the next run would find an
// extract_REGION.osm.pbf that looks like a download to resume, and
// --keep-extract's promise ("the extract is still at ...") would be made of a
// file that is half a page of HTML.
//
// The assertion is on the DIRECTORY rather than on the output file: a
// temporary from saveThroughTemp left behind is just as much a failure, and
// it is the one an assertion about osm_REGION.osmb alone cannot see.
func TestBoundaries_ADownloadThatFailsLeavesNothingInTheStore(t *testing.T) {
	good := extractBytes(t)

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    string // a fragment the error has to carry
	}{
		{
			name: "the host says no",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", http.StatusNotFound)
			},
			want: "404",
		},
		{
			name: "the host answers with a web page",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, "<html><body>Sign in to continue</body></html>")
			},
			want: "", // any error will do; what matters is that it is one
		},
		{
			name: "the connection drops part way through",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(good)))
				w.Write(good[:len(good)/2])
			},
			want: "unexpected EOF",
		},
		{
			name: "the host offers more than this accepts",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// One byte over the cap, declared rather than sent, which is
				// how a planet file announces itself. maxExtractBytes is
				// otherwise unexercised: raised to zero -- acquire's spelling
				// of "no limit" -- nothing else here would notice.
				w.Header().Set("Content-Length", fmt.Sprint(int64(maxExtractBytes)+1))
				w.Write([]byte("x"))
			},
			want: "accepts at most",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveBody(t, tc.handler)
			store := filepath.Join(t.TempDir(), "store")

			_, stderr, err := runBoundaries(t, "--store", store,
				"--osm", srv.URL+"/hornsby-latest.osm.pbf", "--yes")
			if err == nil {
				t.Fatalf("a broken download was reported as success:\n%s", stderr)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to mention %q", err, tc.want)
			}
			if left := inStore(t, store); len(left) != 0 {
				t.Errorf("the store holds %v after a failed download, want nothing", left)
			}
		})
	}
}

// The extract this command fetched is deleted even when what follows fails --
// and kept even then, if the user asked for it.
//
// The deletion is deferred rather than done after a successful build, and
// that is a deliberate choice the commit message makes a claim about: "a
// failure in the middle clears up as well as a success". Only the success was
// covered, so moving the removal to the end of the happy path -- leaving half
// a gigabyte behind on every failed run -- passed the suite.
//
// The pair is what makes each case discriminating: an implementation that
// always removed would fail the second, one that never removed would fail the
// first, and one that removed only on success would fail the first alone.
func TestBoundaries_AFailureAfterTheFetchStillObeysKeepExtract(t *testing.T) {
	// A body that downloads completely and then cannot be read. An extract
	// holding nothing at the levels asked for is NOT a failure -- it is a
	// fact about how the region is mapped, and the command says so and exits
	// zero -- so it cannot stand in for one here.
	truncated := extractBytes(t)
	truncated = truncated[:len(truncated)/2]
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) { w.Write(truncated) })

	for _, tc := range []struct {
		name string
		args []string
		kept bool
	}{
		{"deleted by default", nil, false},
		{"kept when asked", []string{"--keep-extract"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "store")
			args := append([]string{"--store", store, "--yes",
				"--osm", srv.URL + "/hornsby-latest.osm.pbf"}, tc.args...)
			_, stderr, err := runBoundaries(t, args...)
			if err == nil {
				t.Fatalf("a half-written extract was read as a whole one:\n%s", stderr)
			}

			extract := filepath.Join(boundary.Dir(store), "extract_hornsby.osm.pbf")
			_, statErr := os.Stat(extract)
			if tc.kept && statErr != nil {
				t.Errorf("--keep-extract did not survive the failure: %v", statErr)
			}
			if !tc.kept && statErr == nil {
				t.Errorf("the fetched extract was left behind after a failure; the store holds %v", inStore(t, store))
			}
			// Nothing half-written either way.
			if _, err := os.Stat(filepath.Join(boundary.Dir(store), "osm_hornsby.osmb")); err == nil {
				t.Error("a boundary file was written for a run that failed")
			}
		})
	}
}

// The same guarantee at the last possible moment: the write of the output
// itself fails, after the extract has been fetched and the whole pipeline has
// run.
//
// Contrived by leaving a directory where the output file goes, which is what
// os.Rename cannot overwrite -- but the failure it stands for is not: a full
// disk, a read-only store, a store on a filesystem that will not take the
// rename. This is the one path where a deferred cleanup and a cleanup written
// at the end of the function differ ONLY in a case no other test reaches.
func TestBoundaries_AFailureWritingTheOutputStillDeletesTheFetchedExtract(t *testing.T) {
	good := extractBytes(t)
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })

	store := filepath.Join(t.TempDir(), "store")
	blocked := filepath.Join(boundary.Dir(store), "osm_hornsby.osmb")
	if err := os.MkdirAll(filepath.Join(blocked, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runBoundaries(t, "--store", store,
		"--osm", srv.URL+"/hornsby-latest.osm.pbf", "--yes")
	if err == nil {
		t.Fatalf("a write that could not happen was reported as success:\n%s", stderr)
	}
	if !strings.Contains(err.Error(), "osm_hornsby.osmb") {
		t.Errorf("error %q does not name the file it could not put in place", err)
	}

	if _, err := os.Stat(filepath.Join(boundary.Dir(store), "extract_hornsby.osm.pbf")); err == nil {
		t.Error("the fetched extract survived a failure to write the output")
	}
	// And no temporary is left beside the blocker.
	for _, name := range inStore(t, store) {
		if name != "osm_hornsby.osmb" {
			t.Errorf("the store holds %q after a failed write, want only the blocker", name)
		}
	}
}

// An extract that cannot be read has to say which file, because the two
// reasons -- a typo in the path and a file that is not a .osm.pbf at all --
// are told apart by looking at it.
func TestBoundaries_AnUnreadableLocalExtractSaysWhichFile(t *testing.T) {
	dir := t.TempDir()
	notAnExtract := filepath.Join(dir, "notes.osm.pbf")
	if err := os.WriteFile(notAnExtract, []byte("this is not a pbf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"a path that is not there", filepath.Join(dir, "missing.osm.pbf")},
		{"a file that is not an extract", notAnExtract},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "store")
			_, _, err := runBoundaries(t, "--store", store, "--osm", tc.path, "--yes")
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("error %q does not name %q", err, tc.path)
			}
			if left := inStore(t, store); len(left) != 0 {
				t.Errorf("the store holds %v, want nothing", left)
			}
			// The user's file is theirs, failure or not.
			if tc.path == notAnExtract {
				if _, err := os.Stat(notAnExtract); err != nil {
					t.Errorf("the file this was pointed at is gone: %v", err)
				}
			}
		})
	}
}

// stdinSaying points os.Stdin at a pipe holding an answer, so the one prompt
// this command has can be driven from a test.
//
// The prompt reads os.Stdin directly rather than taking a reader, so this
// swaps the process's. That is why none of these tests may run in parallel.
func stdinSaying(t *testing.T, answer string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	if _, err := io.WriteString(w, answer); err != nil {
		t.Fatalf("writing the answer: %v", err)
	}
	w.Close()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; r.Close() })
}

// Only a yes is a yes -- and a yes really is one.
//
// Silence was pinned already; the other side was not, and that asymmetry is
// the dangerous one. A prompt stuck at "no" refuses every interactive run of
// this command while every test passes, because every test passes --yes.
func TestConfirmBoundaries_OnlyYesMeansYes(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{" y \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false}, // return on its own is the default, and it is N
		{"", false},   // a closed stdin: a pipeline that did not mean to
		{"maybe\n", false},
		{"yesterday\n", false}, // a prefix of "yes" is not "yes"
	} {
		t.Run(strings.TrimSpace(tc.answer), func(t *testing.T) {
			// The reader is a parameter now, so this needs no process-wide
			// stdin and the subtests could run in parallel.
			var out strings.Builder
			got, err := confirmBoundaries(strings.NewReader(tc.answer), &out)
			if err != nil {
				t.Fatalf("confirmBoundaries: %v", err)
			}
			if got != tc.want {
				t.Errorf("answering %q was read as %v, want %v", tc.answer, got, tc.want)
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("the prompt does not say what the default is: %q", out.String())
			}
		})
	}
}

// And the prompt is wired to the --osm path in both directions.
//
// The declining half was covered. Without this half, a confirmation that
// never returns true would pass the whole suite: the fixture runs all pass
// --yes, which skips the prompt entirely.
func TestBoundaries_TheOSMPromptDecidesWhetherAnythingHappens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		written bool
	}{
		{"yes builds it", "y\n", true},
		{"no builds nothing", "n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := extractFile(t, dir, "sydney.osm.pbf")
			store := filepath.Join(dir, "store")
			stdinSaying(t, tc.answer)

			stdout, stderr, err := runBoundaries(t, "--store", store, "--osm", src)
			if err != nil {
				t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
			}
			// The question is on the same stream as the disclosure it is
			// asking about: split, redirecting stderr away leaves a bare
			// prompt with no statement of what attaches to the result.
			if !strings.Contains(stderr, "Continue?") {
				t.Errorf("the run did not ask:\n%s", stderr)
			}
			if !strings.Contains(stderr, "DERIVATIVE DATABASE") {
				t.Errorf("the question was asked without the obligation beside it:\n%s", stderr)
			}

			out := filepath.Join(boundary.Dir(store), "osm_sydney.osmb")
			_, statErr := os.Stat(out)
			if tc.written && statErr != nil {
				t.Fatalf("a confirmed run wrote nothing: %v", statErr)
			}
			if !tc.written {
				if statErr == nil {
					t.Error("a declined run wrote the file anyway")
				}
				if !strings.Contains(stdout, "stopped") {
					t.Errorf("a declined run did not say so:\n%s", stdout)
				}
				// Nothing at all, not even the directory: a store that has
				// grown a boundaries/ is one a later check reads as a store
				// that was fetched into.
				if _, err := os.Stat(boundary.Dir(store)); err == nil {
					t.Error("a declined run created the boundaries directory")
				}
				return
			}
			if set := readDerived(t, out); set.Len() != 1 {
				t.Errorf("the confirmed run wrote %d areas, want 1", set.Len())
			}
		})
	}
}

// A fetched extract is deleted, so the file's provenance has to name the URL
// it came from and not the path it briefly occupied.
//
// Source is what a reader has to go on to re-derive or to check the file, and
// the deleted temporary in the store is no help at all. The local case pins
// Source too, but there source and extract are the same string, so it cannot
// tell the two apart; only a fetch can.
func TestBoundaries_AFetchedFileRecordsTheURLItCameFrom(t *testing.T) {
	good := extractBytes(t)
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
	url := srv.URL + "/hornsby-latest.osm.pbf"

	store := filepath.Join(t.TempDir(), "store")
	if _, stderr, err := runBoundaries(t, "--store", store, "--osm", url, "--yes"); err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}

	set := readDerived(t, filepath.Join(boundary.Dir(store), "osm_hornsby.osmb"))
	if got := set.Provenance().Source; got != url {
		t.Errorf("the file says it came from %q, want the URL %q", got, url)
	}
	if got := set.Provenance().Attribution; !strings.Contains(got, "OpenStreetMap") || !strings.Contains(got, "ODbL") {
		t.Errorf("the fetched file carries the attribution %q", got)
	}
	// The obligation travels in the file because the terminal output does
	// not: this is the copy that survives being handed to somebody else.
	if a, ok := set.At(-33.65, 151.15); !ok || a.Name != "Hornsby" {
		t.Errorf("a point inside the fetched boundary resolved to %q (%v)", a.Name, ok)
	}
}

// An extract whose boundaries do not close is NOT an extract with no
// boundaries, and the report must not tell the user it is.
//
// This is the only way to reach the "No boundaries were found" branch at all.
// osm.Read returns ErrNoBoundaries -- an error, which the command surfaces --
// whenever no relation matched, so by the time reportOSM sees zero areas the
// extract is known to have carried administrative relations at exactly the
// levels asked for. Every sentence it then prints is false:
//
//   - "This extract carries no administrative relations at the levels asked
//     for" contradicts the stderr line printed two lines earlier, which says
//     how many boundaries were found and that they produced no outline.
//   - "which is a property of how the region is mapped, not of the extract
//     being wrong" is the opposite of the truth: an extract cut through its
//     own boundaries is exactly the case, and a wider extract fixes it.
//   - "Try without --levels to see what it has" is unactionable advice when
//     --levels was never passed, which is the default and the case below.
//
// The test asserts the report does not contradict itself. It says nothing
// about the wording, because the wording is the author's to choose.
func TestBoundaries_AnExtractWhoseBoundariesDoNotCloseIsNotAnEmptyExtract(t *testing.T) {
	dir := t.TempDir()

	// One named boundary at admin_level 9 whose single way is an open chain:
	// three points and no way back. This is what the edge of a cut-out
	// extract does to a boundary that runs past it.
	e := osmbasetest.NewExtract().
		Node(1, -33.70, 151.10).
		Node(2, -33.70, 151.20).
		Node(3, -33.60, 151.20).
		Way(10, []int64{1, 2, 3})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "9", "name", "Hornsby")

	src := filepath.Join(dir, "sydney.osm.pbf")
	if err := os.WriteFile(src, e.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "store")

	// No --levels: the default, and the case in which advice to drop --levels
	// cannot be followed.
	stdout, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--yes")
	if err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}

	// What is true, and is said: one boundary was found and it produced no
	// outline. This half also stops the test passing by the extract simply
	// holding nothing.
	if !strings.Contains(stderr, "produced no outline") {
		t.Fatalf("the run did not report the unclosed boundary:\n%s", stderr)
	}

	if strings.Contains(stdout, "carries no administrative relations") {
		t.Errorf("the summary says the extract carries no relations at these levels, "+
			"while the run itself reported finding one and failing to close it:\n--- stdout ---\n%s\n--- stderr ---\n%s",
			stdout, stderr)
	}
	if strings.Contains(stdout, "without --levels") {
		t.Errorf("the summary advises running without --levels, which was not passed:\n%s", stdout)
	}
}

// What a fetch says before it happens, on the path where it matters.
//
// The share-alike notice was pinned only for a LOCAL extract. Skipping it for
// a remote one passed the whole suite -- and remote is the case where the
// notice is doing the work, because that is the run in which somebody
// acquires OpenStreetMap data for the first time and the obligation attaches
// to what they get. The commit calls this "the project's first share-alike
// obligation and the one thing a person cannot discover by reading the output
// later".
//
// The host is part of it too: this is the only command that opens a socket for
// a file a person named, and where the request goes is theirs to know before
// it goes.
func TestBoundaries_AFetchSaysWhereItGoesAndWhatAttachesToTheResult(t *testing.T) {
	good := extractBytes(t)
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
	host := strings.TrimPrefix(srv.URL, "http://")

	store := filepath.Join(t.TempDir(), "store")
	_, stderr, err := runBoundaries(t, "--store", store,
		"--osm", srv.URL+"/hornsby-latest.osm.pbf", "--yes")
	if err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}

	for _, want := range []string{
		host,                  // where the request goes
		"DERIVATIVE DATABASE", // what the output is
		"ODbL",                // under which licence
		"Share-alike attaches",
		"openstreetmap.org/copyright",
		"Produced Work", // and what a lookup from it is instead
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("a fetch did not say %q:\n%s", want, stderr)
		}
	}
	// The notice comes before the transfer, not after it: a person reading
	// it has to still be able to stop.
	if i, j := strings.Index(stderr, "DERIVATIVE DATABASE"), strings.Index(stderr, "downloaded"); i > j {
		t.Errorf("the obligation was stated after the download:\n%s", stderr)
	}
}

// A downloaded extract is transient, and which of the two things that means
// has to be said: it will be deleted, or it is still there and here is where.
//
// Neither line was asserted anywhere. A user who passes --keep-extract to
// avoid re-downloading half a gigabyte, and is never told the path, has been
// given the file and not the file's name.
func TestBoundaries_AFetchSaysWhatBecomesOfTheExtract(t *testing.T) {
	good := extractBytes(t)
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })

	t.Run("deleted, and said so beforehand", func(t *testing.T) {
		store := filepath.Join(t.TempDir(), "store")
		_, stderr, err := runBoundaries(t, "--store", store,
			"--osm", srv.URL+"/hornsby-latest.osm.pbf", "--yes")
		if err != nil {
			t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "--keep-extract") {
			t.Errorf("a fetch that will delete the extract did not say so, or did not "+
				"say how to stop it:\n%s", stderr)
		}
	})

	t.Run("kept, and said where", func(t *testing.T) {
		store := filepath.Join(t.TempDir(), "store")
		_, stderr, err := runBoundaries(t, "--store", store,
			"--osm", srv.URL+"/hornsby-latest.osm.pbf", "--keep-extract", "--yes")
		if err != nil {
			t.Fatalf("boundaries --osm --keep-extract: %v\n%s", err, stderr)
		}
		extract := filepath.Join(boundary.Dir(store), "extract_hornsby.osm.pbf")
		if _, err := os.Stat(extract); err != nil {
			t.Fatalf("--keep-extract kept nothing: %v", err)
		}
		if !strings.Contains(stderr, extract) {
			t.Errorf("the kept extract's path was never printed, so the user has the "+
				"file and not its name:\n%s", stderr)
		}
	})
}
