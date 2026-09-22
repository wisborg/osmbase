package osmpbf

import (
	"slices"
	"testing"
)

// The scratch contract.
//
// The element structs alias buffers the block reuses between elements, which
// is why the decoding takes a pointer receiver and why the callback must copy
// anything it keeps. Two things follow, and they fail in opposite directions:
// a buffer that is NOT reset hands the next element the previous one's data,
// and a buffer that is reset but not aliased puts an allocation back on a
// path walked a few million times per extract. The tests here pin both.
//
// The existing suite reached the first only for a way's references and tags.
// A relation's members, a plain node's tags and a dense run's three columns
// are reset by lines nothing else runs twice.

// blockWithGroups builds a block whose elements are spread over several
// PrimitiveGroups, which blockWith cannot express: it concatenates everything
// into one. The format has the groups repeated, and a decoder that read only
// the first would return a fraction of a real file with nothing to say so.
func blockWithGroups(t *testing.T, strs []string, groups ...[]byte) *PrimitiveBlock {
	t.Helper()
	data := pbBytes(1, stringTable(strs...))
	for _, g := range groups {
		data = append(data, pbBytes(2, g)...)
	}
	b, err := DecodePrimitiveBlock(data)
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	return &b
}

// A relation's members are appended to a buffer shared with the relation
// before it. Unreset, the second relation comes back carrying the first's
// ways as well as its own -- and the parallel-run length check cannot see it,
// because that compares the id, role and type runs, all three of which are
// reset. For a boundary that is a ring with somebody else's coastline in it.
func TestASecondRelationDoesNotInheritTheFirstsMembers(t *testing.T) {
	strs := []string{"", "outer", "inner"}
	const sOuter, sInner = 1, 2
	b := blockWith(t, strs,
		relation(10, []int32{sOuter, sInner, sOuter}, []int64{100, 101, 102},
			[]int32{int32(MemberWay), int32(MemberWay), int32(MemberWay)}, nil, nil),
		relation(11, []int32{sInner}, []int64{200}, []int32{int32(MemberRelation)}, nil, nil),
	)

	var got []Relation
	if err := b.EachRelation(func(r Relation) error {
		got = append(got, Relation{ID: r.ID, Members: slices.Clone(r.Members)})
		return nil
	}); err != nil {
		t.Fatalf("EachRelation: %v", err)
	}

	want := [][]Member{
		{
			{Type: MemberWay, ID: 100, Role: "outer"},
			{Type: MemberWay, ID: 101, Role: "inner"},
			{Type: MemberWay, ID: 102, Role: "outer"},
		},
		{{Type: MemberRelation, ID: 200, Role: "inner"}},
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d relations, want %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i].Members, want[i]) {
			t.Errorf("relation %d has members %+v, want %+v", got[i].ID, got[i].Members, want[i])
		}
	}
}

// The same for a plain node's tags, which come off the two parallel lists a
// way's do -- but through a reset of their own, in the node decoder. Unreset,
// the second node carries the first's tags and its own, and pairedTags is
// happy because both lists grew together.
func TestASecondPlainNodeDoesNotInheritTheFirstsTags(t *testing.T) {
	strs := []string{"", "name", "Horsens", "place", "town", "ele"}
	const sName, sHors, sPlace, sTown, sEle = 1, 2, 3, 4, 5
	b := blockWith(t, strs,
		plainNode(1, 10, 20, []int32{sName, sPlace}, []int32{sHors, sTown}),
		plainNode(2, 30, 40, []int32{sEle}, []int32{sTown}),
		plainNode(3, 50, 60, nil, nil),
	)

	got := collectNodes(t, b)
	want := [][][2]string{
		{{"name", "Horsens"}, {"place", "town"}},
		{{"ele", "town"}},
		nil,
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d nodes, want %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i].tags, want[i]) {
			t.Errorf("node %d has tags %v, want %v", got[i].id, got[i].tags, want[i])
		}
	}
}

// Groups are repeated in the format. A block's elements are spread over them,
// and this is also where a dense run's three columns are reset for a second
// time -- unreset, the second group's run replays the first group's nodes at
// accumulated ids, which look like perfectly good ids for other nodes.
func TestEveryGroupInABlockIsWalked(t *testing.T) {
	first := []int64{1_000, 1_001, 999}
	second := []int64{7_000, 6_500}
	firstLats := []int64{10, 20, 30}
	secondLats := []int64{40, 50}

	b := blockWithGroups(t, []string{"", "highway", "residential"},
		denseNodes(first, firstLats, firstLats, nil),
		append(denseNodes(second, secondLats, secondLats, nil),
			way(5, []int64{1_000, 999}, []int32{1}, []int32{2})...),
	)

	got := collectNodes(t, b)
	wantIDs := append(slices.Clone(first), second...)
	wantLats := append(slices.Clone(firstLats), secondLats...)
	if len(got) != len(wantIDs) {
		t.Fatalf("decoded %d nodes, want %d; the groups after the first were not walked", len(got), len(wantIDs))
	}
	for i := range wantIDs {
		if got[i].id != wantIDs[i] || got[i].lat != wantLats[i] {
			t.Errorf("node %d = id %d at lat %d, want id %d at lat %d",
				i, got[i].id, got[i].lat, wantIDs[i], wantLats[i])
		}
	}

	var ways int
	if err := b.EachWay(func(w Way) error { ways++; return nil }); err != nil {
		t.Fatalf("EachWay: %v", err)
	}
	if ways != 1 {
		t.Errorf("saw %d ways, want the one in the second group", ways)
	}
}

// The aliasing itself, stated rather than left to the doc comment. Two ways
// of the same shape hand the callback the SAME backing array, which is what
// makes "the callback must copy anything it keeps" a rule rather than advice.
//
// If this ever fails because the decoder began cloning per element, the
// decoder is not wrong -- but the contract in EachNode's doc comment, the
// pointer receiver and the allocation test below are all then describing
// something that no longer happens, and they should go together.
func TestTheCallbackIsHandedBuffersTheNextElementOverwrites(t *testing.T) {
	b := blockWith(t, []string{"", "highway", "residential", "track"},
		way(1, []int64{10, 11, 12}, []int32{1}, []int32{2}),
		way(2, []int64{20, 21, 22}, []int32{1}, []int32{3}),
	)

	var refs [][]int64
	var tags []Tags
	if err := b.EachWay(func(w Way) error {
		refs = append(refs, w.Refs)
		tags = append(tags, w.Tags)
		return nil
	}); err != nil {
		t.Fatalf("EachWay: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("decoded %d ways, want 2", len(refs))
	}
	if &refs[0][0] != &refs[1][0] {
		t.Error("the two ways' references are in different arrays; the decoder no longer reuses its scratch")
	}
	// And the consequence a caller has to know about: what the first
	// callback was handed now reads as the second way's.
	if !slices.Equal(refs[0], []int64{20, 21, 22}) {
		t.Errorf("the retained references read %v, want the second way's %v", refs[0], []int64{20, 21, 22})
	}
	if v, _ := tags[0].Get("highway"); v != "track" {
		t.Errorf("the retained tags read highway=%s, want the second way's track", v)
	}
}

// The reason for all of it: what a block costs to walk must not grow with
// what the elements hold. A decoder that cloned per element, or built a map
// of tags per element, would allocate in proportion to the file -- millions
// of times over a country extract, to answer one question per element.
//
// The assertion is that the count does not change with the size, not what
// the count is: pinning the constant would turn any future rearrangement of
// the reader into a failure that looks like a spec.
//
// It is half of a pair. Counting allocations cannot see a clone of a fixed
// number of elements, however large each clone is -- that is what the
// aliasing test above catches, by the two elements sharing one array.
func TestWalkingABlockDoesNotAllocateInProportionToIt(t *testing.T) {
	denseBlock := func(n int) *PrimitiveBlock {
		ids := make([]int64, n)
		lats := make([]int64, n)
		lons := make([]int64, n)
		for i := range ids {
			ids[i], lats[i], lons[i] = int64(i*7), int64(i*3), int64(i*5)
		}
		return blockWith(t, []string{""}, denseNodes(ids, lats, lons, nil))
	}

	small, large := denseBlock(100), denseBlock(10_000)
	walk := func(b *PrimitiveBlock) float64 {
		_ = b.EachNode(func(Node) error { return nil }) // grow the scratch first
		return testing.AllocsPerRun(5, func() {
			_ = b.EachNode(func(Node) error { return nil })
		})
	}
	if s, l := walk(small), walk(large); l > s {
		t.Errorf("walking 10000 nodes allocates %v times against %v for 100; the cost is growing with the file", l, s)
	}

	// The same for a way's references, which land in a buffer of their own.
	wayBlock := func(refsEach int) *PrimitiveBlock {
		refs := make([]int64, refsEach)
		for i := range refs {
			refs[i] = int64(i * 2)
		}
		var gs [][]byte
		for i := 0; i < 20; i++ {
			gs = append(gs, way(int64(i+1), refs, []int32{1}, []int32{2}))
		}
		return blockWith(t, []string{"", "highway", "residential"}, gs...)
	}
	short, long := wayBlock(4), wayBlock(2_000)
	walkWays := func(b *PrimitiveBlock) float64 {
		_ = b.EachWay(func(Way) error { return nil })
		return testing.AllocsPerRun(5, func() {
			_ = b.EachWay(func(Way) error { return nil })
		})
	}
	if s, l := walkWays(short), walkWays(long); l > s {
		t.Errorf("twenty 2000-node ways allocate %v times against %v for twenty 4-node ways", l, s)
	}
}
