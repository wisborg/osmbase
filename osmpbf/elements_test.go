package osmpbf

import (
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
)

// blockWith builds a decoded block from a string table and one group.
func blockWith(t *testing.T, strs []string, group ...[]byte) *PrimitiveBlock {
	t.Helper()
	var g []byte
	for _, x := range group {
		g = append(g, x...)
	}
	// The group fixtures already carry their PrimitiveGroup field tags, so
	// they are concatenated into one group rather than wrapped again.
	data := pbBytes(1, stringTable(strs...))
	data = append(data, pbBytes(2, g)...)

	b, err := DecodePrimitiveBlock(data)
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	return &b
}

// collectNodes copies what the callback is handed, because the Node it
// receives aliases buffers the next element overwrites.
type node struct {
	id       int64
	lat, lon int64
	tags     [][2]string
}

func collectNodes(t *testing.T, b *PrimitiveBlock) []node {
	t.Helper()
	var out []node
	err := b.EachNode(func(n Node) error {
		c := node{id: n.ID, lat: n.Lat, lon: n.Lon}
		for i := 0; i < n.Tags.Len(); i++ {
			k, v := n.Tags.At(i)
			c.tags = append(c.tags, [2]string{k, v})
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		t.Fatalf("EachNode: %v", err)
	}
	return out
}

func TestDenseNodesAreUndeltaed(t *testing.T) {
	ids := []int64{1_000_000, 1_000_003, 999_999, 5_000_000_000}
	lats := []int64{557_000_000, 557_000_100, 556_999_000, -338_680_000}
	lons := []int64{98_500_000, 98_500_050, 98_499_000, 1_512_090_000}

	b := blockWith(t, []string{""}, denseNodes(ids, lats, lons, nil))
	got := collectNodes(t, b)

	if len(got) != len(ids) {
		t.Fatalf("decoded %d nodes, want %d", len(got), len(ids))
	}
	for i := range ids {
		if got[i].id != ids[i] || got[i].lat != lats[i] || got[i].lon != lons[i] {
			t.Errorf("node %d = id %d at %d,%d; want id %d at %d,%d",
				i, got[i].id, got[i].lat, got[i].lon, ids[i], lats[i], lons[i])
		}
	}
}

// The deltas go backwards as well as forwards, and the ids are not sorted in
// the fixture above for that reason: a decoder that accumulated the absolute
// value, or dropped the sign, would pass on a monotonic run.
func TestDenseNodeDeltasCarrySign(t *testing.T) {
	ids := []int64{100, 50, 200, 1}
	b := blockWith(t, []string{""}, denseNodes(ids, []int64{0, 0, 0, 0}, []int64{0, 0, 0, 0}, nil))
	got := collectNodes(t, b)
	for i := range ids {
		if got[i].id != ids[i] {
			t.Errorf("node %d has id %d, want %d", i, got[i].id, ids[i])
		}
	}
}

// Each node's tags are terminated by a zero in one flat run, so a node with
// no tags is a bare terminator. Nodes with differing counts, including empty
// ones in the middle, are what makes a decoder drift out of step -- and the
// symptom is tags landing on the wrong node, not an error.
func TestDenseNodeTagsStayInStep(t *testing.T) {
	strs := []string{"", "name", "Horsens", "place", "town", "wikidata", "Q12345"}
	const (
		sName = 1
		sHors = 2
		sPlac = 3
		sTown = 4
		sWiki = 5
		sQ    = 6
	)
	kv := tagRun([][]int32{
		{sName, sHors, sPlac, sTown}, // two tags
		nil,                          // none
		{sWiki, sQ},                  // one
		nil,                          // none
		{sPlac, sTown},               // one
	})

	ids := []int64{1, 2, 3, 4, 5}
	zeros := []int64{0, 0, 0, 0, 0}
	b := blockWith(t, strs, denseNodes(ids, zeros, zeros, kv))
	got := collectNodes(t, b)

	want := [][][2]string{
		{{"name", "Horsens"}, {"place", "town"}},
		nil,
		{{"wikidata", "Q12345"}},
		nil,
		{{"place", "town"}},
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d nodes, want %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i].tags, want[i]) {
			t.Errorf("node %d (id %d) has tags %v, want %v", i, got[i].id, got[i].tags, want[i])
		}
	}
}

// A dense run with no keys_vals at all is every node untagged, which is the
// common case in any extract.
func TestDenseNodesMayCarryNoTags(t *testing.T) {
	b := blockWith(t, []string{""}, denseNodes([]int64{1, 2}, []int64{0, 0}, []int64{0, 0}, nil))
	for _, n := range collectNodes(t, b) {
		if len(n.tags) != 0 {
			t.Errorf("node %d has tags %v, want none", n.id, n.tags)
		}
	}
}

// The three runs are one table in three columns. Reading the shortest would
// hand back nodes at coordinates belonging to different nodes.
func TestDenseRunsOfDifferentLengthsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name            string
		ids, lats, lons []int64
	}{
		{"fewer latitudes", []int64{1, 2, 3}, []int64{0, 0}, []int64{0, 0, 0}},
		{"fewer longitudes", []int64{1, 2, 3}, []int64{0, 0, 0}, []int64{0, 0}},
		{"more latitudes", []int64{1, 2}, []int64{0, 0, 0}, []int64{0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := blockWith(t, []string{""}, denseNodes(tc.ids, tc.lats, tc.lons, nil))
			err := b.EachNode(func(Node) error { return nil })
			if err == nil {
				t.Fatal("runs of different lengths were accepted")
			}
			if !strings.Contains(err.Error(), "must agree") {
				t.Errorf("EachNode: %v, want an error saying the runs disagree", err)
			}
		})
	}
}

// A tag run that stops early leaves the remaining nodes untagged, which looks
// exactly like nodes that carry no tags -- most of them, in any extract. So
// it has to be an error rather than a shorter answer.
func TestATagRunThatEndsEarlyIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		kv   []int32
	}{
		{"no terminator for the last node", []int32{1, 2, 0, 1, 2}},
		{"a key with no value", []int32{1, 2, 0, 1}},
		{"nothing at all for the second node", []int32{1, 2, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := blockWith(t, []string{"", "name", "Horsens"},
				denseNodes([]int64{1, 2}, []int64{0, 0}, []int64{0, 0}, tc.kv))
			err := b.EachNode(func(Node) error { return nil })
			if err == nil {
				t.Fatal("a truncated tag run was accepted, leaving nodes silently untagged")
			}
		})
	}
}

func TestPlainNodesAreDecoded(t *testing.T) {
	b := blockWith(t, []string{"", "name", "Horsens"},
		plainNode(42, 557_000_000, 98_500_000, []int32{1}, []int32{2}))
	got := collectNodes(t, b)
	if len(got) != 1 {
		t.Fatalf("decoded %d nodes, want 1", len(got))
	}
	if got[0].id != 42 || got[0].lat != 557_000_000 || got[0].lon != 98_500_000 {
		t.Errorf("node = %+v, want id 42 at 557000000,98500000", got[0])
	}
	if !slices.Equal(got[0].tags, [][2]string{{"name", "Horsens"}}) {
		t.Errorf("tags = %v, want name=Horsens", got[0].tags)
	}
}

// A way's id is a plain int64 and its refs are zigzag deltas. The asymmetry
// is in the format, and getting it wrong does not fail -- it yields an id
// half the size, belonging to a different way.
func TestWayIdIsNotZigzagAndRefsAre(t *testing.T) {
	refs := []int64{1_000_000, 1_000_001, 999_998, 1_000_002}
	b := blockWith(t, []string{"", "highway", "residential"},
		way(4_294_967_296, refs, []int32{1}, []int32{2}))

	var got []Way
	err := b.EachWay(func(w Way) error {
		got = append(got, Way{ID: w.ID, Refs: slices.Clone(w.Refs)})
		return nil
	})
	if err != nil {
		t.Fatalf("EachWay: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("decoded %d ways, want 1", len(got))
	}
	if got[0].ID != 4_294_967_296 {
		t.Errorf("way id = %d, want 4294967296; a zigzag read would halve it", got[0].ID)
	}
	if !slices.Equal(got[0].Refs, refs) {
		t.Errorf("refs = %v, want %v", got[0].Refs, refs)
	}
}

func TestRelationMembersCarryRoleAndType(t *testing.T) {
	strs := []string{"", "outer", "inner", "type", "boundary", "boundary", "administrative"}
	const (
		sOuter = 1
		sInner = 2
		sType  = 3
		sBound = 4
		sBdKey = 5
		sAdmin = 6
	)
	memids := []int64{100, 102, 101}
	b := blockWith(t, strs, relation(
		7,
		[]int32{sOuter, sInner, sOuter},
		memids,
		[]int32{int32(MemberWay), int32(MemberWay), int32(MemberNode)},
		[]int32{sType, sBdKey}, []int32{sBound, sAdmin},
	))

	var got []Relation
	err := b.EachRelation(func(r Relation) error {
		got = append(got, Relation{ID: r.ID, Members: slices.Clone(r.Members)})
		if !r.Tags.Is("boundary", "administrative") {
			t.Errorf("Tags.Is(boundary, administrative) = false on the relation this pipeline exists to find")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("EachRelation: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("decoded %v, want one relation with id 7", got)
	}
	want := []Member{
		{Type: MemberWay, ID: 100, Role: "outer"},
		{Type: MemberWay, ID: 102, Role: "inner"},
		{Type: MemberNode, ID: 101, Role: "outer"},
	}
	if !slices.Equal(got[0].Members, want) {
		t.Errorf("members = %+v, want %+v", got[0].Members, want)
	}
}

// Every direction of the disagreement, because the check is two comparisons
// and one of them can be dropped with the other still catching the case
// above it. A relation with fewer ROLES than members is the one that bites:
// the member loop indexes the role run by the member's position, so a check
// that only compared the types would read past the end of it.
func TestRelationRunsOfDifferentLengthsAreRefused(t *testing.T) {
	const sOuter = 1
	for _, tc := range []struct {
		name   string
		roles  []int32
		memids []int64
		types  []int32
	}{
		{"fewer roles", []int32{sOuter}, []int64{100, 101}, []int32{1, 1}},
		{"more roles", []int32{sOuter, sOuter, sOuter}, []int64{100, 101}, []int32{1, 1}},
		{"fewer types", []int32{sOuter, sOuter}, []int64{100, 101}, []int32{1}},
		{"more types", []int32{sOuter, sOuter}, []int64{100, 101}, []int32{1, 1, 1}},
		{"fewer member ids", []int32{sOuter, sOuter}, []int64{100}, []int32{1, 1}},
		{"roles and types but no ids at all", []int32{sOuter}, nil, []int32{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := pbVarint(1, 7)
			data = append(data, packedInt(8, tc.roles...)...)
			data = append(data, packedSint(9, tc.memids...)...)
			data = append(data, packedInt(10, tc.types...)...)
			b := blockWith(t, []string{"", "outer"}, pbBytes(4, data))

			err := b.EachRelation(func(Relation) error { return nil })
			if err == nil {
				t.Fatal("runs of different lengths were accepted; a member would take its role or type from another member")
			}
			if !strings.Contains(err.Error(), "must agree") {
				t.Errorf("EachRelation: %v, want an error saying the runs disagree", err)
			}
		})
	}
}

func TestAnUndefinedMemberTypeIsRefused(t *testing.T) {
	b := blockWith(t, []string{"", "outer"},
		relation(7, []int32{1}, []int64{100}, []int32{3}, nil, nil))
	err := b.EachRelation(func(Relation) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "0, 1 and 2") {
		t.Fatalf("EachRelation: %v, want the defined types named", err)
	}
}

// Each pass wants one element kind, and must not be handed the others.
func TestEachPassSeesOnlyItsOwnKind(t *testing.T) {
	b := blockWith(t, []string{"", "outer"},
		denseNodes([]int64{1}, []int64{0}, []int64{0}, nil),
		plainNode(2, 0, 0, nil, nil),
		way(3, []int64{1, 2}, nil, nil),
		relation(4, []int32{1}, []int64{3}, []int32{1}, nil, nil),
	)

	var nodes, ways, relations int
	if err := b.EachNode(func(Node) error { nodes++; return nil }); err != nil {
		t.Fatalf("EachNode: %v", err)
	}
	if err := b.EachWay(func(Way) error { ways++; return nil }); err != nil {
		t.Fatalf("EachWay: %v", err)
	}
	if err := b.EachRelation(func(Relation) error { relations++; return nil }); err != nil {
		t.Fatalf("EachRelation: %v", err)
	}
	if nodes != 2 || ways != 1 || relations != 1 {
		t.Errorf("saw %d nodes, %d ways, %d relations; want 2, 1, 1", nodes, ways, relations)
	}
}

// The buffers are reused between elements, so a second element must not
// inherit the first's tags or references.
func TestScratchDoesNotLeakBetweenElements(t *testing.T) {
	b := blockWith(t, []string{"", "highway", "residential"},
		way(1, []int64{10, 11, 12}, []int32{1}, []int32{2}),
		way(2, nil, nil, nil),
	)

	var got []Way
	err := b.EachWay(func(w Way) error {
		got = append(got, Way{ID: w.ID, Refs: slices.Clone(w.Refs), Tags: w.Tags})
		if w.ID == 2 {
			if w.Tags.Len() != 0 {
				k, v := w.Tags.At(0)
				t.Errorf("the second way carries %s=%s from the first", k, v)
			}
			if len(w.Refs) != 0 {
				t.Errorf("the second way carries refs %v from the first", w.Refs)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("EachWay: %v", err)
	}
	if len(got) != 2 || len(got[0].Refs) != 3 {
		t.Fatalf("decoded %+v, want two ways the first with three refs", got)
	}
}

// Returning an error from the callback is how a pass stops early, and the
// error must come back unchanged so the caller can recognise its own.
func TestAnErrorFromTheCallbackStopsThePass(t *testing.T) {
	b := blockWith(t, []string{""},
		denseNodes([]int64{1, 2, 3, 4}, []int64{0, 0, 0, 0}, []int64{0, 0, 0, 0}, nil))

	stop := errors.New("enough")
	var seen int
	err := b.EachNode(func(Node) error {
		seen++
		if seen == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Errorf("EachNode returned %v, want the callback's own error", err)
	}
	if seen != 2 {
		t.Errorf("the callback ran %d times, want 2; the pass did not stop", seen)
	}
}

func TestTagsAnswerWithoutResolvingEverything(t *testing.T) {
	strs := []string{"", "name", "", "boundary", "administrative", "admin_level", "8"}
	b := blockWith(t, strs, way(1, nil, []int32{1, 3, 5}, []int32{2, 4, 6}))

	err := b.EachWay(func(w Way) error {
		// A tag whose value is empty is present. OpenStreetMap carries
		// `name=` on features deliberately left blank, and reporting that as
		// absent would make them nameless rather than unnamed.
		if v, ok := w.Tags.Get("name"); !ok || v != "" {
			t.Errorf("Get(name) = %q, %v; want the empty value reported as present", v, ok)
		}
		if !w.Tags.Has("name") {
			t.Error("Has(name) = false for a tag with an empty value")
		}
		if v, ok := w.Tags.Get("admin_level"); !ok || v != "8" {
			t.Errorf("Get(admin_level) = %q, %v; want 8", v, ok)
		}
		if _, ok := w.Tags.Get("absent"); ok {
			t.Error("Get(absent) reported a tag that is not there")
		}
		if !w.Tags.Is("boundary", "administrative") {
			t.Error("Is(boundary, administrative) = false")
		}
		if w.Tags.Is("boundary", "maritime") {
			t.Error("Is(boundary, maritime) = true for a boundary that is administrative")
		}
		if w.Tags.Len() != 3 {
			t.Errorf("Len = %d, want 3", w.Tags.Len())
		}
		k, v := w.Tags.At(1)
		if k != "boundary" || v != "administrative" {
			t.Errorf("At(1) = %s=%s, want boundary=administrative", k, v)
		}
		if k, v := w.Tags.At(9); k != "" || v != "" {
			t.Errorf("At(9) = %s=%s, want empty for an index out of range", k, v)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("EachWay: %v", err)
	}
}

func TestElementCountsAreBounded(t *testing.T) {
	t.Run("a way's references", func(t *testing.T) {
		refs := make([]int64, MaxWayRefs+1)
		for i := range refs {
			refs[i] = int64(i)
		}
		b := blockWith(t, []string{""}, way(1, refs, nil, nil))
		if err := b.EachWay(func(Way) error { return nil }); err == nil {
			t.Errorf("a way of %d references was accepted", len(refs))
		}
	})

	t.Run("an element's tags", func(t *testing.T) {
		keys := make([]int32, MaxTags+1)
		vals := make([]int32, MaxTags+1)
		for i := range keys {
			keys[i], vals[i] = 1, 1
		}
		b := blockWith(t, []string{"", "k"}, way(1, nil, keys, vals))
		if err := b.EachWay(func(Way) error { return nil }); err == nil {
			t.Errorf("an element of %d tags was accepted", len(keys))
		}
	})
}

// A way with a realistic number of references must not be refused; the caps
// are there for what an editor cannot produce.
func TestRealisticElementsAreAccepted(t *testing.T) {
	refs := make([]int64, 2000) // OpenStreetMap's own per-way limit
	for i := range refs {
		refs[i] = int64(i * 3)
	}
	b := blockWith(t, []string{""}, way(1, refs, nil, nil))
	var n int
	if err := b.EachWay(func(w Way) error { n = len(w.Refs); return nil }); err != nil {
		t.Fatalf("a 2000-node way was refused: %v", err)
	}
	if n != 2000 {
		t.Errorf("decoded %d references, want 2000", n)
	}
}

// Keys and values are two parallel lists, so a mismatch means every pair
// after the gap is a key married to the wrong value -- a boundary that reads
// as admin_level=Horsens rather than an error.
func TestTagKeysAndValuesOfDifferentLengthsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name       string
		keys, vals []int32
	}{
		{"more keys than values", []int32{1, 3}, []int32{2}},
		{"more values than keys", []int32{1}, []int32{2, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// All three element kinds that carry two parallel lists, because
			// each builds them through a reset of its own and only reaches
			// the shared check afterwards.
			for _, kind := range []struct {
				name  string
				field int
				body  []byte
				walk  func(*PrimitiveBlock) error
			}{
				{"a way", 3, pbVarint(1, 1),
					func(b *PrimitiveBlock) error { return b.EachWay(func(Way) error { return nil }) }},
				{"a plain node", 1, append(pbTag(1, protobufWireVarint), binary.AppendUvarint(nil, zigzag(1))...),
					func(b *PrimitiveBlock) error { return b.EachNode(func(Node) error { return nil }) }},
				{"a relation", 4, append(pbVarint(1, 1), append(append(packedInt(8, 1), packedSint(9, 5)...), packedInt(10, 1)...)...),
					func(b *PrimitiveBlock) error { return b.EachRelation(func(Relation) error { return nil }) }},
			} {
				t.Run(kind.name, func(t *testing.T) {
					data := append([]byte(nil), kind.body...)
					data = append(data, packedInt(2, tc.keys...)...)
					data = append(data, packedInt(3, tc.vals...)...)
					b := blockWith(t, []string{"", "a", "b", "c", "d"}, pbBytes(kind.field, data))

					err := kind.walk(b)
					if err == nil {
						t.Fatal("mismatched key and value runs were accepted")
					}
					if !strings.Contains(err.Error(), "must agree") {
						t.Errorf("%v, want an error saying the runs disagree", err)
					}
				})
			}
		})
	}
}

// The tag cap applies on the dense path too, which reaches it by its own
// route: the pairs come off one flat run rather than two parallel ones.
func TestADenseNodesTagsAreBounded(t *testing.T) {
	pairs := make([]int32, 0, 2*(MaxTags+1))
	for i := 0; i <= MaxTags; i++ {
		pairs = append(pairs, 1, 2)
	}
	b := blockWith(t, []string{"", "k", "v"},
		denseNodes([]int64{1}, []int64{0}, []int64{0}, tagRun([][]int32{pairs})))

	if err := b.EachNode(func(Node) error { return nil }); err == nil {
		t.Errorf("a dense node carrying %d tags was accepted", MaxTags+1)
	}
}

// Undoing the delta coding is what turns the file's small numbers into ids.
// Forgetting does not fail: it yields ids that look perfectly plausible and
// belong to entirely different elements.
func TestReferencesAreAbsoluteNotDeltas(t *testing.T) {
	refs := []int64{500, 700, 690}
	b := blockWith(t, []string{""}, way(1, refs, nil, nil))

	var got []int64
	if err := b.EachWay(func(w Way) error { got = slices.Clone(w.Refs); return nil }); err != nil {
		t.Fatalf("EachWay: %v", err)
	}
	// The deltas would be 500, 200, -10 -- all different from the values, and
	// the first deliberately not, so a decoder that returned the raw run
	// fails on the second rather than passing by coincidence.
	if !slices.Equal(got, refs) {
		t.Errorf("refs = %v, want %v; these look like ids either way, which is the point", got, refs)
	}
}

// A struct copy of Tags is not a copy: the index slices point into buffers
// the next element overwrites, so two Tags kept from two elements end up
// viewing the same data and the earlier one silently acquires the later
// one's tags. Clone is the copy, and this is why it has to exist.
func TestTagsMustBeClonedToOutliveTheCallback(t *testing.T) {
	b := blockWith(t, []string{"", "name", "Alpha", "Beta"},
		denseNodes([]int64{1, 2}, []int64{0, 0}, []int64{0, 0},
			tagRun([][]int32{{1, 2}, {1, 3}})))

	var shallow, cloned []Tags
	var duringPass []string
	err := b.EachNode(func(n Node) error {
		v, _ := n.Tags.Get("name")
		duringPass = append(duringPass, v)
		shallow = append(shallow, n.Tags)
		cloned = append(cloned, n.Tags.Clone())
		return nil
	})
	if err != nil {
		t.Fatalf("EachNode: %v", err)
	}

	if len(duringPass) != 2 || duringPass[0] != "Alpha" || duringPass[1] != "Beta" {
		t.Fatalf("during the pass the nodes reported %v, want Alpha then Beta", duringPass)
	}
	for i, want := range []string{"Alpha", "Beta"} {
		if got, _ := cloned[i].Get("name"); got != want {
			t.Errorf("the cloned tags of node %d give %q, want %q", i, got, want)
		}
	}
	// The shallow copies are expected to have gone wrong. Asserted, rather
	// than left implicit, because if they ever stop aliasing then Clone is
	// no longer load-bearing and this whole contract can be simplified.
	if got, _ := shallow[0].Get("name"); got == "Alpha" {
		t.Skip("a struct copy of Tags no longer aliases; Clone and its documentation can go")
	}
}

func TestCloningTheZeroTagsIsSafe(t *testing.T) {
	var zero Tags
	c := zero.Clone()
	if c.Len() != 0 {
		t.Errorf("the zero Tags cloned to %d pairs", c.Len())
	}
	if _, ok := c.Get("name"); ok {
		t.Error("the zero Tags reported a tag")
	}
}

// ErrStop is the common spelling of "end this pass", and it must come back
// from Each* unchanged so a caller can tell it from a decoding failure.
func TestErrStopEndsAPassAndComesBackRecognisable(t *testing.T) {
	b := blockWith(t, []string{""},
		denseNodes([]int64{1, 2, 3}, []int64{0, 0, 0}, []int64{0, 0, 0}, nil))

	var seen int
	err := b.EachNode(func(Node) error {
		seen++
		return ErrStop
	})
	if !errors.Is(err, ErrStop) {
		t.Errorf("EachNode returned %v, want ErrStop", err)
	}
	if seen != 1 {
		t.Errorf("the callback ran %d times, want 1", seen)
	}
}
