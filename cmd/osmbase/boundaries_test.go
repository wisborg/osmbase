package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBoundariesCommand_RefusesADetailItDoesNotKnow covers the validation
// that reaches the network only if it passes.
//
// Cheap to test because it happens before anything else runs: no store, no
// socket, no confirmation. And worth testing because the same flag on the
// locate command went unvalidated, was interpolated into a filename, and let a
// crafted value read a file outside the store.
func TestBoundariesCommand_RefusesADetailItDoesNotKnow(t *testing.T) {
	for _, bad := range []string{"1m", "10", "10M", "../.."} {
		err := boundariesCommand(context.Background(), []string{"--detail", bad, "--store", t.TempDir()}, io.Discard, io.Discard)
		if err == nil {
			t.Errorf("--detail %q was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "110m") {
			t.Errorf("--detail %q was refused without saying what is allowed: %v", bad, err)
		}
	}
}

// TestBoundariesCommand_SilenceIsNotConsent pins the answer to the one prompt
// in this command that sends anything anywhere.
//
// A closed or empty stdin means a pipeline reached this prompt without meaning
// to download, and taking that for a yes would be the one thing this command
// is careful not to do. Testable without a network precisely because the
// refusal comes before the transfer.
func TestBoundariesCommand_SilenceIsNotConsent(t *testing.T) {
	stdin, cleanup := emptyStdin(t)
	defer cleanup()
	_ = stdin

	dir := t.TempDir()
	var out strings.Builder
	if err := boundariesCommand(context.Background(), []string{"--store", dir}, &out, io.Discard); err != nil {
		t.Fatalf("a declined download returned an error: %v", err)
	}
	if !strings.Contains(out.String(), "stopped") {
		t.Errorf("the output does not say nothing was downloaded:\n%s", out.String())
	}
	// And nothing was written, including the directory itself.
	if _, err := os.Stat(filepath.Join(dir, "boundaries")); err == nil {
		t.Error("a declined download created the boundaries directory")
	}
}

// emptyStdin points os.Stdin at a closed pipe, so a prompt reads EOF.
func emptyStdin(t *testing.T) (*os.File, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	w.Close()
	saved := os.Stdin
	os.Stdin = r
	return r, func() { os.Stdin = saved; r.Close() }
}

// TestSaveThroughTemp_LeavesNothingWhenTheWriteFails is why the download goes
// through a temporary name.
//
// A half-written GeoJSON survives the existence check that decides whether a
// store has boundaries at all, and then fails to parse on the next lookup --
// reported against that lookup, with nothing connecting it to the fetch that
// caused it. The function takes the writer rather than doing the fetching so
// that this can be tested at all: the download itself has no seam.
func TestSaveThroughTemp_LeavesNothingWhenTheWriteFails(t *testing.T) {
	dir := t.TempDir()
	const name = "ne_50m_admin_0_countries.geojson"

	boom := errors.New("the connection went away")
	_, err := saveThroughTemp(dir, name, func(w io.Writer) (int64, error) {
		// Some bytes arrive, then the transfer fails -- which is the shape
		// of a real interruption rather than a clean refusal.
		n, _ := w.Write([]byte(`{"type":"FeatureCollection","fea`))
		return int64(n), boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write's own error", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		t.Error("the target file exists after a failed write; a truncated file would be read as a real one")
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d files left behind, want none: %v", len(left), left)
	}
}

// TestSaveThroughTemp_PutsTheFileInPlaceOnSuccess is the other half, so the
// test above cannot pass by the function never writing anything.
func TestSaveThroughTemp_PutsTheFileInPlaceOnSuccess(t *testing.T) {
	dir := t.TempDir()
	const name = "ne_50m_admin_0_countries.geojson"
	const body = `{"type":"FeatureCollection","features":[]}`

	n, err := saveThroughTemp(dir, name, func(w io.Writer) (int64, error) {
		written, err := io.WriteString(w, body)
		return int64(written), err
	})
	if err != nil {
		t.Fatalf("saveThroughTemp: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("wrote %d bytes, want %d", n, len(body))
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("the file is not in place: %v", err)
	}
	if string(got) != body {
		t.Errorf("file holds %q, want %q", got, body)
	}
}
