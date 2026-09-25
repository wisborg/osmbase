package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/boundary"
)

// leftIn lists what is in the store's boundaries directory.
func leftIn(t *testing.T, store string) []string {
	t.Helper()
	entries, err := os.ReadDir(boundary.Dir(store))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Ctrl-C part way through downloading an extract stops the download, removes
// the partly written file, and exits 130 saying so. Before, nothing handled a
// signal: the process died where it stood and left the temporary file for the
// NEXT run to sweep.
func TestAnInterruptedDownloadStopsAndLeavesNothing(t *testing.T) {
	started := make(chan struct{})
	srv := serveBody(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000000")
		w.Write(bytes.Repeat([]byte{0}, 64<<10))
		w.(http.Flusher).Flush()
		close(started)
		// Until the client goes away -- or, if it never does because the
		// download ignores the interruption, long enough for the test to
		// fail rather than hang.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	store := filepath.Join(t.TempDir(), "store")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	var stderr bytes.Buffer
	go func() {
		done <- runContext(ctx, []string{"boundaries", "--store", store, "--osm", srv.URL + "/somewhere-latest.osm.pbf", "--yes"},
			&bytes.Buffer{}, &stderr)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the download never started")
	}
	cancel()

	select {
	case code := <-done:
		if code != 130 {
			t.Errorf("exit %d, want 130\n%s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not stop when interrupted")
	}
	if !strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("nothing says it was interrupted:\n%s", stderr.String())
	}
	if left := leftIn(t, store); len(left) != 0 {
		t.Errorf("an interrupted download left %v behind", left)
	}
}

// Ctrl-C while the extract is being read stops the passes at the next read,
// and writes no boundary file.
func TestAnInterruptedBuildStopsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "somewhere.osm.pbf")
	store := filepath.Join(dir, "store")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	code := runContext(ctx, []string{"boundaries", "--store", store, "--osm", src, "--yes"}, &bytes.Buffer{}, &stderr)
	if code != 130 {
		t.Errorf("exit %d, want 130\n%s", code, stderr.String())
	}
	if left := leftIn(t, store); len(left) != 0 {
		t.Errorf("an interrupted build left %v behind", left)
	}
	// And the file the user pointed at is theirs, and still there.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the user's extract is gone: %v", err)
	}
}

// A failure that is not an interruption keeps its own exit status: 130 is
// for Ctrl-C, not for anything that went wrong while a context existed.
func TestAFailureIsNotReportedAsAnInterruption(t *testing.T) {
	var stderr bytes.Buffer
	code := runContext(context.Background(), []string{"boundaries", "--store", t.TempDir(), "--osm", filepath.Join(t.TempDir(), "missing.osm.pbf"), "--yes"},
		&bytes.Buffer{}, &stderr)
	if code == 130 || strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("a missing file was reported as an interruption: exit %d\n%s", code, stderr.String())
	}
}
