package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A world plan fills no cells, so its cell zoom range is empty -- and was
// printed, as "zooms 1 to 0". The zooms a world plan takes are its overview's.
func TestAWorldPlanSaysTheZoomsItTakes(t *testing.T) {
	archive := fixtureArchive(t, 0, 0, 0, worldTile())
	r := runCLI(t, "fetch", archive, "--world", "--max-zoom", "0", "--store", filepath.Join(t.TempDir(), "s"), "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if line, ok := lineContaining(r.stdout, "area"); !ok || !strings.Contains(line, "zooms 0 to 0") {
		t.Errorf("the world plan's zooms are wrong:\n%s", r.stdout)
	}
}
