package osm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
)

// from returns an Open over a fixture held in memory.
func from(b []byte) Open {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

// square builds an extract holding one administrative boundary: four nodes,
// two ways that together run round them, and a relation naming both.
//
// Two ways rather than one because that is what a real boundary is -- an
// outline is shared between neighbours and cut at every junction -- and a
// pipeline that only ever saw single-way boundaries would not be exercising
// the part that matters.
func square(t *testing.T, tags ...string) []byte {
	t.Helper()
	e := osmbasetest.NewExtract().
		Node(1, 55.70, 9.50).
		Node(2, 55.70, 9.60).
		Node(3, 55.80, 9.60).
		Node(4, 55.80, 9.50).
		Way(10, []int64{1, 2, 3}).
		Way(11, []int64{3, 4, 1})

	all := append([]string{"boundary", "administrative", "admin_level", "8", "name", "Horsens"}, tags...)
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "way", ID: 11, Role: "outer"},
	}, all...)
	return e.Bytes()
}

func TestABoundaryComesBackWithItsGeometry(t *testing.T) {
	got, err := Read(from(square(t)), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d boundaries, want 1", len(got))
	}
	b := got[0]
	if b.ID != 100 || b.Name != "Horsens" || b.AdminLevel != 8 {
		t.Errorf("boundary = %d %q level %d, want 100 Horsens 8", b.ID, b.Name, b.AdminLevel)
	}
	if len(b.Ways) != 2 {
		t.Fatalf("boundary has %d ways, want 2", len(b.Ways))
	}
	if b.Ways[0].ID != 10 || b.Ways[1].ID != 11 {
		t.Errorf("ways came back as %d,%d, want them in the order the relation names them",
			b.Ways[0].ID, b.Ways[1].ID)
	}
	for _, w := range b.Ways {
		if w.Role != "outer" {
			t.Errorf("way %d has role %q, want outer", w.ID, w.Role)
		}
		if len(w.Points) != 3 {
			t.Errorf("way %d has %d points, want 3", w.ID, len(w.Points))
		}
	}
	// The first way runs 1 -> 2 -> 3, and the coordinates must arrive in that
	// order and at those places.
	// Keyed, so that a reordering of Point's fields fails here rather than
	// compiling and passing with the world transposed.
	want := []Point{{Lat: 55.70, Lon: 9.50}, {Lat: 55.70, Lon: 9.60}, {Lat: 55.80, Lon: 9.60}}
	for i, p := range b.Ways[0].Points {
		if math.Abs(p.Lat-want[i].Lat) > 1e-7 || math.Abs(p.Lon-want[i].Lon) > 1e-7 {
			t.Errorf("point %d = %v, want %v", i, p, want[i])
		}
	}
}

func TestOnlyAdministrativeBoundariesAreKept(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).
		Way(10, []int64{1, 2})
	members := []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}}

	e.Relation(100, members, "boundary", "administrative", "admin_level", "8", "name", "Kept")
	e.Relation(101, members, "boundary", "maritime", "admin_level", "8", "name", "A sea edge")
	e.Relation(102, members, "type", "multipolygon", "name", "A lake")
	e.Relation(103, members, "boundary", "administrative", "name", "No level")
	e.Relation(104, members, "boundary", "administrative", "admin_level", "8")
	e.Relation(105, members, "boundary", "administrative", "admin_level", "8;9", "name", "Two levels")
	e.Relation(106, members, "boundary", "administrative", "admin_level", "", "name", "Empty level")
	// admin_level is free text and carries numbers that are not levels. The
	// scheme runs 1 to 12; anything outside it is a tagging mistake, and
	// keeping it would put a boundary at a level nothing ever asks for.
	e.Relation(107, members, "boundary", "administrative", "admin_level", "0", "name", "Level zero")
	e.Relation(108, members, "boundary", "administrative", "admin_level", "99", "name", "Level ninety-nine")
	e.Relation(109, members, "boundary", "administrative", "admin_level", "-8", "name", "A negative level")

	got, err := Read(from(e.Bytes()), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Kept" {
		var names []string
		for _, b := range got {
			names = append(names, b.Name)
		}
		t.Errorf("kept %v, want only Kept", names)
	}
}

func TestLevelsSelectWhatIsKept(t *testing.T) {
	e := osmbasetest.NewExtract().Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).Way(10, []int64{1, 2})
	members := []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}}
	for _, lvl := range []int{2, 4, 7, 8, 10} {
		e.Relation(int64(100+lvl), members,
			"boundary", "administrative", "admin_level", fmt.Sprint(lvl), "name", fmt.Sprintf("Level %d", lvl))
	}
	file := e.Bytes()

	t.Run("a selection", func(t *testing.T) {
		got, err := Read(from(file), Options{Levels: []int{8, 10}})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("kept %d boundaries, want 2", len(got))
		}
		for _, b := range got {
			if b.AdminLevel != 8 && b.AdminLevel != 10 {
				t.Errorf("kept level %d, which was not asked for", b.AdminLevel)
			}
		}
	})

	t.Run("no selection keeps every level", func(t *testing.T) {
		got, err := Read(from(file), Options{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(got) != 5 {
			t.Errorf("kept %d boundaries, want all 5", len(got))
		}
	})
}

func TestTheLanguageIsPreferredWhenTheExtractCarriesIt(t *testing.T) {
	file := square(t, "name:de", "Horsens (deutsch)")

	for _, tc := range []struct {
		language, want string
	}{
		{"", "Horsens"},
		{"de", "Horsens (deutsch)"},
		{"fr", "Horsens"}, // not carried, so the local name stands
	} {
		got, err := Read(from(file), Options{Language: tc.language})
		if err != nil {
			t.Fatalf("Read(%q): %v", tc.language, err)
		}
		if got[0].Name != tc.want {
			t.Errorf("language %q gave name %q, want %q", tc.language, got[0].Name, tc.want)
		}
	}
}

// A boundary relation also names an admin_centre node and sometimes a
// sub-area relation. Neither is part of the outline, and a pipeline that took
// them would ask pass 2 for a way id that is really a node id.
func TestNonWayMembersAreNotPartOfTheOutline(t *testing.T) {
	// The node and relation members deliberately carry the SAME ids as real
	// ways in the extract. Element ids are only unique within their kind, so
	// this is an ordinary thing for an extract to contain -- and a pipeline
	// that ignored the member type would resolve them against the way index
	// and staple unrelated geometry onto the outline. Given distinct ids the
	// mistake is invisible, because the lookup simply finds nothing.
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).
		Node(10, 55.75, 9.55, "place", "town").
		Way(10, []int64{1, 2}).
		Way(11, []int64{2, 1}, "waterway", "river")
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "node", ID: 10, Role: "admin_centre"},
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "relation", ID: 11, Role: "subarea"},
	}, "boundary", "administrative", "admin_level", "8", "name", "Horsens")

	got, err := Read(from(e.Bytes()), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got[0].Ways) != 1 || got[0].Ways[0].ID != 10 || got[0].Ways[0].Role != "outer" {
		t.Errorf("the outline is %+v, want only way 10 as outer", got[0].Ways)
	}
}

func TestAnExtractWithNoBoundariesSaysSo(t *testing.T) {
	e := osmbasetest.NewExtract().Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).Way(10, []int64{1, 2})
	_, err := Read(from(e.Bytes()), Options{})
	if !errors.Is(err, ErrNoBoundaries) {
		t.Errorf("Read: %v, want ErrNoBoundaries", err)
	}
}

// A node a boundary way names and the file does not hold is a hole in the
// outline. Filled with the zero value it would be a point in the Gulf of
// Guinea, and a ring through there encloses most of a hemisphere.
func TestANodeMissingFromTheExtractIsAnError(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).
		Way(10, []int64{1, 2, 3}) // node 3 is not in the file
	e.Relation(100, []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "Horsens")

	_, err := Read(from(e.Bytes()), Options{})
	if err == nil {
		t.Fatal("a boundary with a missing node was assembled anyway")
	}
	if !strings.Contains(err.Error(), "not in the extract") {
		t.Errorf("Read: %v, want an error about the missing node", err)
	}
}

// A way the relation names and the extract does not hold is the normal case
// at the edge of a cut-out region: a boundary that leaves the extract has
// members beyond it. Not an error, and not a reason to drop the boundary.
func TestAWayBeyondTheExtractIsSkippedNotRefused(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).
		Way(10, []int64{1, 2})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "way", ID: 11, Role: "outer"}, // beyond the cut
	}, "boundary", "administrative", "admin_level", "8", "name", "Horsens")

	got, err := Read(from(e.Bytes()), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || len(got[0].Ways) != 1 || got[0].Ways[0].ID != 10 {
		t.Errorf("got %+v, want the boundary with the one way the extract holds", got)
	}
}

func TestAWayHeldTwiceIsRefused(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).Node(3, 55.9, 9.7).
		Way(10, []int64{1, 2}).
		Way(10, []int64{2, 3}) // the same id again
	e.Relation(100, []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "Horsens")

	_, err := Read(from(e.Bytes()), Options{})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("Read: %v, want a refusal; otherwise the outline is half of each", err)
	}
}

func TestTheLimitsAreEnforced(t *testing.T) {
	e := osmbasetest.NewExtract().Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).Way(10, []int64{1, 2})
	members := []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}}
	for i := 0; i < 4; i++ {
		e.Relation(int64(100+i), members,
			"boundary", "administrative", "admin_level", "8", "name", fmt.Sprintf("Area %d", i))
	}
	file := e.Bytes()

	for _, tc := range []struct {
		name   string
		limits Limits
		want   string
	}{
		{"boundaries", Limits{Boundaries: 2, Ways: 100, Nodes: 100}, "boundaries"},
		{"ways", Limits{Boundaries: 100, Ways: 2, Nodes: 100}, "ids were wanted"},
		{"nodes", Limits{Boundaries: 100, Ways: 100, Nodes: 1}, "ids were wanted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Read(from(file), Options{Limits: tc.limits})
			if err == nil {
				t.Fatalf("a limit of %+v was not enforced", tc.limits)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Read: %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// The zero Limits must mean the defaults, not a pipeline that refuses
// everything.
func TestTheZeroLimitsMeanTheDefaults(t *testing.T) {
	if _, err := Read(from(square(t)), Options{}); err != nil {
		t.Fatalf("Read with zero Limits: %v", err)
	}
}

// Opening the extract fails on the first pass rather than part way through.
func TestAnUnopenableExtractIsReported(t *testing.T) {
	want := errors.New("no such file")
	_, err := Read(func() (io.ReadCloser, error) { return nil, want }, Options{})
	if !errors.Is(err, want) {
		t.Errorf("Read: %v, want the opener's own error", err)
	}
}

// The geometry handed back is not proportional to anything the passes
// recorded: one way's node list is expanded once per relation that names it,
// and boundary ways are shared between neighbours. Measured before this had
// its own limit, a 1,495-byte file yielded 1.6 GB.
func TestTheGeometryHandedBackIsBounded(t *testing.T) {
	const ways, relations, refsPerWay = 4, 40, 200

	e := osmbasetest.NewExtract().Node(1, 55.7, 9.5)
	refs := make([]int64, refsPerWay)
	for i := range refs {
		refs[i] = 1 // every reference to the one node, so the file stays tiny
	}
	members := make([]osmbasetest.ExtractMember, 0, ways)
	for w := int64(10); w < 10+ways; w++ {
		e.Way(w, refs)
		members = append(members, osmbasetest.ExtractMember{Type: "way", ID: w, Role: "outer"})
	}
	// Every relation names every way, which is the shape heavy sharing takes
	// and needs no duplicate members to reach.
	for r := int64(100); r < 100+relations; r++ {
		e.Relation(r, members, "boundary", "administrative", "admin_level", "8", "name", "Shared")
	}
	file := e.Bytes()

	total := ways * relations * refsPerWay
	t.Run("past the limit", func(t *testing.T) {
		_, err := Read(from(file), Options{Limits: Limits{Points: total - 1}})
		if err == nil {
			t.Fatalf("a %d byte file expanded to %d points with a limit of %d", len(file), total, total-1)
		}
		if !strings.Contains(err.Error(), "points") {
			t.Errorf("Read: %v, want an error naming the points", err)
		}
	})

	t.Run("within it", func(t *testing.T) {
		got, err := Read(from(file), Options{Limits: Limits{Points: total}})
		if err != nil {
			t.Fatalf("a file expanding to exactly the limit was refused: %v", err)
		}
		if len(got) != relations {
			t.Errorf("read %d boundaries, want %d", len(got), relations)
		}
	})
}

// A node held twice gets the same answer as a way held twice: the file gives
// two positions and there is no mechanism to make them agree, and this is the
// structure whose values end up as drawn pixels.
func TestANodeHeldTwiceIsRefused(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).
		Node(1, 56.9, 10.9). // the same id, somewhere else
		Node(2, 55.8, 9.6).
		Way(10, []int64{1, 2})
	e.Relation(100, []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "Horsens")

	_, err := Read(from(e.Bytes()), Options{})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("Read: %v, want a refusal naming the duplicate node", err)
	}
}

// A way with no references at all is degenerate but permitted. It must be
// distinguishable from a way the extract does not hold, and a second copy of
// it must still be caught -- which nil-as-sentinel could not do, because
// whether an empty Refs clones to nil depends on where the way sits in the
// file.
func TestAWayWithNoReferencesIsStillAWayThatWasSeen(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.7, 9.5).Node(2, 55.8, 9.6).
		Way(10, nil).           // no references, and first in the file
		Way(10, []int64{1, 2}). // the same id again
		Way(11, []int64{1, 2})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "way", ID: 11, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "8", "name", "Horsens")

	_, err := Read(from(e.Bytes()), Options{})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("Read: %v, want the duplicate caught even though the first copy is empty", err)
	}
}

// Ring assembly joins ways by the node they share, so the ids have to survive
// the pipeline alongside the coordinates.
func TestAWayCarriesTheNodeIdsBesideItsPoints(t *testing.T) {
	got, err := Read(from(square(t)), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	w := got[0].Ways[0]
	if len(w.Nodes) != len(w.Points) {
		t.Fatalf("the way has %d node ids and %d points; they must correspond", len(w.Nodes), len(w.Points))
	}
	want := []int64{1, 2, 3}
	for i, id := range want {
		if w.Nodes[i] != id {
			t.Errorf("node id %d = %d, want %d", i, w.Nodes[i], id)
		}
	}
	// The two ways of the square meet at node 3, and that is the join part 5
	// makes. Asserted here because it is the property the ids exist for.
	last := got[0].Ways[0].Nodes[len(got[0].Ways[0].Nodes)-1]
	if first := got[0].Ways[1].Nodes[0]; first != last {
		t.Errorf("the ways meet at ids %d and %d, want the same node", last, first)
	}
}

// Raising one limit must not silently zero the others. All-or-nothing
// defaulting meant a caller who set only Nodes got an error saying the
// extract declared more than zero boundaries -- naming no field, and blaming
// the file for the caller's struct literal.
func TestOneLimitMayBeRaisedWithoutSettingTheRest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits Limits
	}{
		{"only boundaries", Limits{Boundaries: 10}},
		{"only ways", Limits{Ways: 10}},
		{"only nodes", Limits{Nodes: 10}},
		{"only points", Limits{Points: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Read(from(square(t)), Options{Limits: tc.limits}); err != nil {
				t.Errorf("Read with %+v: %v", tc.limits, err)
			}
		})
	}
}
