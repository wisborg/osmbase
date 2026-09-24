package boundary

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// An area with a polygon that has no rings keeps the empty bounding box its
// accumulation started from -- west +Inf, east -Inf -- whose area is +Inf.
// It is therefore LARGER than the whole world, wins every outermost-first
// comparison, and loses to nothing in a smallest-first one: one degenerate
// area decides both ends of a ranking.
//
// The same shape as the wrapped-coordinate case this format already refuses,
// with +Inf instead of a large number.
func TestAnAreaWithNoExtentDecidesNothing(t *testing.T) {
	small := NewArea("Horsens Kommune", "7", []Polygon{{Outer: square(0, 0, 1)}})

	// A REAL polygon beside an empty one, which is the shape that matters: an
	// area whose every polygon is empty contains nothing and never reaches a
	// ranking, while this one contains the point through its real polygon and
	// used to bring +Inf to the comparison with it. Built directly, because
	// the format refuses to write one.
	mixed := NewArea("INFBOX", "7", []Polygon{{Outer: square(0, 0, 0.5)}})
	mixed.polygons = append(mixed.polygons, polygon{box: boxOf(nil)})

	set := NewSet(Provenance{}, []Area{mixed, small})

	got := set.Containing(0.25, 0.25)
	if len(got) != 2 {
		t.Fatalf("Containing = %+v, want both areas", got)
	}
	if got[0].Name != small.Name {
		t.Errorf("the outermost area is %q, want %q -- the empty polygon made its area infinite",
			got[0].Name, small.Name)
	}
	if a, ok := set.At(0.25, 0.25); !ok || a.Name != mixed.Name {
		t.Errorf("At = %q (%v), want the genuinely smaller area %q", a.Name, ok, mixed.Name)
	}
}

// And the format refuses to carry one at either end, so it cannot arrive
// from a file somebody was handed.
func TestAPolygonWithNoRingsIsRefusedAtBothEnds(t *testing.T) {
	bad := Area{Name: "n", Kind: "k", polygons: []polygon{{box: boxOf(nil)}}}
	if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, []Area{bad})); err == nil {
		t.Error("an area with a ringless polygon was written")
	}

	// A file declaring one, built by hand.
	file := append([]byte(derivedMagic), derivedVersion, 4, 0, 0, 0, 0)
	file = append(file, 1)      // one area
	file = append(file, 1, 'n') // name
	file = append(file, 1, 'k') // kind
	file = append(file, 1)      // one polygon
	file = append(file, 0)      // of no rings
	if _, err := ReadDerived(bytes.NewReader(file)); !errors.Is(err, ErrDerivedFormat) {
		t.Errorf("ReadDerived: %v, want a ringless polygon refused", err)
	}

	// And a ring with no points, which gives the same empty box.
	file = append([]byte(derivedMagic), derivedVersion, 4, 0, 0, 0, 0)
	file = append(file, 1, 1, 'n', 1, 'k', 1, 1)
	file = binary.AppendUvarint(file, 0) // a ring of no points
	if _, err := ReadDerived(bytes.NewReader(file)); !errors.Is(err, ErrDerivedFormat) {
		t.Errorf("ReadDerived: %v, want a pointless ring refused", err)
	}
}

func TestBoxAreaIgnoresAPartWithNoExtent(t *testing.T) {
	// The empty box really is the infinite one; if that stops being true,
	// the guard is guarding nothing and this test proves nothing.
	if !math.IsInf(boxOf(nil).area(), 1) {
		t.Fatalf("the empty box has area %v, want +Inf", boxOf(nil).area())
	}

	for _, tc := range []struct {
		name string
		a    Area
		want float64
	}{
		{"an ordinary area", NewArea("a", "k", []Polygon{{Outer: square(0, 0, 2)}}), 4},
		{"no polygons at all", Area{Name: "a"}, 0},
		{"one ringless polygon", Area{polygons: []polygon{{box: boxOf(nil)}}}, 0},
		{"a real polygon beside a ringless one",
			Area{polygons: []polygon{{box: boxOf(nil)}, {box: boxOf(square(0, 0, 2))}}}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.boxArea(); got != tc.want {
				t.Errorf("boxArea = %v, want %v", got, tc.want)
			}
		})
	}
}

// A store is a directory nothing checked, holding files the NOTICE
// contemplates being passed between people. Each file is bounded by the
// format, and the directory by a budget its files share. These are the two
// coarse counts kept beside it; TestAStoreHoldsNoMoreThanItsSharedBudget
// measures what the budget actually holds.
func TestADirectoryOfDerivedFilesIsBoundedInAggregate(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("the number of files", func(t *testing.T) {
		root := t.TempDir()
		const files = maxDerivedFiles + 8
		for i := range files {
			writeDerived(t, root, regionName(i), Provenance{},
				NewArea("A", "9", []Polygon{{Outer: square(float64(i), 0, 0.5)}}))
		}
		src := Open(root, DefaultDetail)
		if n := len(src.derived()); n > maxDerivedFiles {
			t.Errorf("the store read %d files, past the %d it holds", n, maxDerivedFiles)
		} else if n == 0 {
			t.Error("the store read nothing at all")
		}
	})

	t.Run("the areas across them", func(t *testing.T) {
		// Few enough files to clear the count cap, holding more areas
		// between them than the store will keep. Each file's own budget is
		// untroubled; it is the total that is not.
		root := t.TempDir()
		const files = 4
		perFile := 1 + maxDerivedAreasTotal/(files-1)
		for i := range files {
			areas := make([]Area, perFile)
			for j := range areas {
				areas[j] = NewArea("A", "9", []Polygon{{Outer: square(float64(i), float64(j)/1000, 0.0005)}})
			}
			writeDerived(t, root, regionName(i), Provenance{}, areas...)
		}

		src := Open(root, DefaultDetail)
		var total int
		for _, set := range src.derived() {
			total += set.Len()
		}
		if total > maxDerivedAreasTotal {
			t.Errorf("the store holds %d areas, past the %d it reads", total, maxDerivedAreasTotal)
		}
		if total == 0 {
			t.Error("the store read nothing at all")
		}
	})
}

// regionName is a distinct, valid region for the nth file.
func regionName(n int) string {
	return fmt.Sprintf("r%03d", n)
}

// A file this cannot read is skipped, and the rest of the directory still
// answers: one bad file must not take a level away from every coordinate.
func TestOneUnreadableFileDoesNotCostTheOthers(t *testing.T) {
	root := t.TempDir()
	writeDerived(t, root, "good", osmProv(), NewArea("Good", "9", []Polygon{{Outer: square(0, 0, 1)}}))

	name, _ := DerivedFile("broken")
	if err := os.WriteFile(filepath.Join(Dir(root), name), []byte("not a derived file"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := Open(root, DefaultDetail)
	got, _, _, ok := inside(src, locate.Locality, 0.5, 0.5)
	if !ok || got != "Good" {
		t.Errorf("got %q (%v), want the readable file's answer", got, ok)
	}
}
