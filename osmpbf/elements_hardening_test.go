package osmpbf

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// pbFixed32 and pbFixed64 round out the fixture writer's wire types, so a
// test can put a field of every shape in front of a decoder that must step
// over it.
func pbFixed32(field int, v uint32) []byte {
	return binary.LittleEndian.AppendUint32(pbTag(field, 5), v)
}

func pbFixed64(field int, v uint64) []byte {
	return binary.LittleEndian.AppendUint64(pbTag(field, 1), v)
}

// unknownFields is one field of each wire type, at numbers no schema in this
// format uses. Every one has to be stepped over exactly: a skip that consumed
// the wrong number of bytes would not stop, it would resume reading the rest
// of the element from the middle of a field.
func unknownFields(base int) []byte {
	out := pbVarint(base, 1<<40)
	out = append(out, pbBytes(base+1, []byte("something a later schema added"))...)
	out = append(out, pbFixed32(base+2, 0xdeadbeef)...)
	out = append(out, pbFixed64(base+3, 0xfeedfacecafebeef)...)
	return out
}

// Real extracts carry more per element than this decoder reads. Every one
// produced with metadata has a DenseInfo beside the dense runs and an Info
// beside each way and relation -- versions, timestamps, changesets, user
// ids -- and osmium can add more. A decoder that stopped at an unknown field,
// or stepped over it by the wrong length, would fail or silently misread the
// files people actually download, and every fixture in the suite so far
// carries only the fields the decoder wants.
func TestUnknownFieldsInsideAnElementAreSteppedOver(t *testing.T) {
	strs := []string{"", "name", "Horsens", "highway", "residential", "outer", "type", "boundary"}
	const (
		sName  = 1
		sHors  = 2
		sHwy   = 3
		sResid = 4
		sOuter = 5
		sType  = 6
		sBound = 7
	)
	// A DenseInfo-shaped blob: field 1 is a packed version run there.
	info := packedInt(1, 3, 4, 5)

	t.Run("a dense run", func(t *testing.T) {
		ids := []int64{1_000, 1_002, 999}
		lats := []int64{10, 20, 30}
		lons := []int64{40, 50, 60}

		out := unknownFields(50)
		out = append(out, packedSint(1, ids...)...)
		out = append(out, pbBytes(5, info)...) // denseinfo, which real files carry
		out = append(out, packedSint(8, lats...)...)
		out = append(out, packedSint(9, lons...)...)
		out = append(out, packedInt(10, tagRun([][]int32{{sName, sHors}, nil, nil})...)...)
		out = append(out, unknownFields(60)...)

		b := blockWith(t, strs, pbBytes(2, out))
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
		if !slices.Equal(got[0].tags, [][2]string{{"name", "Horsens"}}) {
			t.Errorf("tags = %v, want name=Horsens", got[0].tags)
		}
	})

	t.Run("a plain node", func(t *testing.T) {
		out := unknownFields(50)
		out = append(out, pbTag(1, protobufWireVarint)...)
		out = binary.AppendUvarint(out, zigzag(42))
		out = append(out, packedInt(2, sName)...)
		out = append(out, pbBytes(4, info)...) // info
		out = append(out, packedInt(3, sHors)...)
		out = append(out, pbTag(8, protobufWireVarint)...)
		out = binary.AppendUvarint(out, zigzag(-557_000_000))
		out = append(out, pbTag(9, protobufWireVarint)...)
		out = binary.AppendUvarint(out, zigzag(98_500_000))
		out = append(out, unknownFields(60)...)

		b := blockWith(t, strs, pbBytes(1, out))
		got := collectNodes(t, b)
		if len(got) != 1 {
			t.Fatalf("decoded %d nodes, want 1", len(got))
		}
		if got[0].id != 42 || got[0].lat != -557_000_000 || got[0].lon != 98_500_000 {
			t.Errorf("node = %+v, want id 42 at -557000000,98500000", got[0])
		}
		if !slices.Equal(got[0].tags, [][2]string{{"name", "Horsens"}}) {
			t.Errorf("tags = %v, want name=Horsens", got[0].tags)
		}
	})

	t.Run("a way", func(t *testing.T) {
		out := unknownFields(50)
		out = append(out, pbVarint(1, 1234)...)
		out = append(out, packedInt(2, sHwy)...)
		out = append(out, pbBytes(4, info)...) // info
		out = append(out, packedInt(3, sResid)...)
		out = append(out, packedSint(8, 500, 700, 690)...)
		out = append(out, unknownFields(60)...)

		b := blockWith(t, strs, pbBytes(3, out))
		var got []Way
		if err := b.EachWay(func(w Way) error {
			got = append(got, Way{ID: w.ID, Refs: slices.Clone(w.Refs)})
			if v, _ := w.Tags.Get("highway"); v != "residential" {
				t.Errorf("highway = %q, want residential", v)
			}
			return nil
		}); err != nil {
			t.Fatalf("EachWay: %v", err)
		}
		if len(got) != 1 || got[0].ID != 1234 {
			t.Fatalf("decoded %+v, want one way with id 1234", got)
		}
		if !slices.Equal(got[0].Refs, []int64{500, 700, 690}) {
			t.Errorf("refs = %v, want [500 700 690]", got[0].Refs)
		}
	})

	t.Run("a relation", func(t *testing.T) {
		out := unknownFields(50)
		out = append(out, pbVarint(1, 77)...)
		out = append(out, packedInt(2, sType)...)
		out = append(out, pbBytes(4, info)...) // info
		out = append(out, packedInt(3, sBound)...)
		out = append(out, packedInt(8, sOuter, sOuter)...)
		out = append(out, packedSint(9, 100, 98)...)
		out = append(out, packedInt(10, int32(MemberWay), int32(MemberWay))...)
		out = append(out, unknownFields(60)...)

		b := blockWith(t, strs, pbBytes(4, out))
		var got []Relation
		if err := b.EachRelation(func(r Relation) error {
			got = append(got, Relation{ID: r.ID, Members: slices.Clone(r.Members)})
			if !r.Tags.Is("type", "boundary") {
				t.Error("Tags.Is(type, boundary) = false")
			}
			return nil
		}); err != nil {
			t.Fatalf("EachRelation: %v", err)
		}
		want := []Member{
			{Type: MemberWay, ID: 100, Role: "outer"},
			{Type: MemberWay, ID: 98, Role: "outer"},
		}
		if len(got) != 1 || got[0].ID != 77 {
			t.Fatalf("decoded %+v, want one relation with id 77", got)
		}
		if !slices.Equal(got[0].Members, want) {
			t.Errorf("members = %+v, want %+v", got[0].Members, want)
		}
	})
}

// Every per-element cap, from both sides.
//
// One side alone cannot tell a cap that is enforced from one enforced a
// value early: a check written >= instead of > refuses a file the cap was
// written to admit, and no test that only ever exceeds the limit can see it.
// The block-level limits in this package are already tested this way --
// a blob of exactly its declared size, a granularity of exactly the bound --
// and these are the four that were not.
//
// The counts are the constants themselves rather than numbers copied from
// them, so a cap that moves takes its test with it.
func TestEveryElementCapAdmitsItsLimitAndRefusesOneMore(t *testing.T) {
	refsWay := func(n int) []byte {
		refs := make([]int64, n)
		for i := range refs {
			refs[i] = int64(i * 2)
		}
		return way(1, refs, nil, nil)
	}
	pairedTagWay := func(n int) []byte {
		keys := make([]int32, n)
		vals := make([]int32, n)
		for i := range keys {
			keys[i], vals[i] = 1, 2
		}
		return way(1, nil, keys, vals)
	}
	denseTagNode := func(n int) []byte {
		pairs := make([]int32, 0, 2*n)
		for i := 0; i < n; i++ {
			pairs = append(pairs, 1, 2)
		}
		return denseNodes([]int64{1}, []int64{0}, []int64{0}, tagRun([][]int32{pairs}))
	}
	denseRun := func(n int) []byte {
		ids := make([]int64, n)
		zeros := make([]int64, n)
		for i := range ids {
			ids[i] = int64(i)
		}
		return denseNodes(ids, zeros, zeros, nil)
	}
	members := func(n int) []byte {
		ids := make([]int64, n)
		roles := make([]int32, n)
		types := make([]int32, n)
		for i := range ids {
			ids[i] = int64(i)
		}
		return relation(1, roles, ids, types, nil, nil)
	}

	countWays := func(b *PrimitiveBlock) (int, error) {
		var n int
		err := b.EachWay(func(w Way) error { n = max(n, max(len(w.Refs), w.Tags.Len())); return nil })
		return n, err
	}
	countNodeTags := func(b *PrimitiveBlock) (int, error) {
		var n int
		err := b.EachNode(func(nd Node) error { n = max(n, nd.Tags.Len()); return nil })
		return n, err
	}
	countNodes := func(b *PrimitiveBlock) (int, error) {
		var n int
		err := b.EachNode(func(Node) error { n++; return nil })
		return n, err
	}
	countMembers := func(b *PrimitiveBlock) (int, error) {
		var n int
		err := b.EachRelation(func(r Relation) error { n = len(r.Members); return nil })
		return n, err
	}

	for _, tc := range []struct {
		name  string
		limit int
		strs  []string
		build func(int) []byte
		count func(*PrimitiveBlock) (int, error)
	}{
		{"a way's references", MaxWayRefs, []string{""}, refsWay, countWays},
		{"an element's tags", MaxTags, []string{"", "k", "v"}, pairedTagWay, countWays},
		{"a dense node's tags", MaxTags, []string{"", "k", "v"}, denseTagNode, countNodeTags},
		{"a dense run's nodes", MaxDenseNodes, []string{""}, denseRun, countNodes},
		{"a relation's members", MaxRelationMembers, []string{"", "outer"}, members, countMembers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := blockWith(t, tc.strs, tc.build(tc.limit))
			got, err := tc.count(b)
			if err != nil {
				t.Errorf("exactly %d was refused: %v", tc.limit, err)
			} else if got != tc.limit {
				t.Errorf("decoded %d, want exactly the limit of %d", got, tc.limit)
			}

			b = blockWith(t, tc.strs, tc.build(tc.limit+1))
			if _, err := tc.count(b); err == nil {
				t.Errorf("%d was accepted, one past the cap of %d", tc.limit+1, tc.limit)
			}
		})
	}
}

// A relation member with no role is the ordinary case, not the exception --
// most members of most relations carry one. The format says so with index 0,
// and index 0 is the empty string by decree rather than by what the file put
// there, which is why the table below starts with a real string.
func TestAMemberWithNoRoleGetsTheEmptyStringWhateverTheFileHolds(t *testing.T) {
	// A producer that did not reserve index 0. StringAt must still answer "".
	strs := []string{"outer", "inner"}
	b := blockWith(t, strs, relation(1,
		[]int32{0, 1}, []int64{10, 11},
		[]int32{int32(MemberWay), int32(MemberWay)}, nil, nil))

	var got []Member
	if err := b.EachRelation(func(r Relation) error {
		got = slices.Clone(r.Members)
		return nil
	}); err != nil {
		t.Fatalf("EachRelation: %v", err)
	}
	want := []Member{
		{Type: MemberWay, ID: 10, Role: ""},
		{Type: MemberWay, ID: 11, Role: "inner"}, // table index 1, whatever index 0 held
	}
	if !slices.Equal(got, want) {
		t.Errorf("members = %+v, want %+v", got, want)
	}
}

// A string index the table does not have resolves to nothing rather than
// panicking or picking up a neighbour. The indices are int32 on the wire, so
// a file can put a negative one there, and a downloaded extract should not
// be able to stop a pass over several million elements.
func TestAStringIndexOutsideTheTableResolvesToNothing(t *testing.T) {
	b := blockWith(t, []string{"", "name"}, way(1, nil, []int32{-1, 1, 9999}, []int32{2, 9999, -7}))

	if err := b.EachWay(func(w Way) error {
		if w.Tags.Len() != 3 {
			t.Fatalf("Len = %d, want 3", w.Tags.Len())
		}
		for i, want := range [][2]string{{"", ""}, {"name", ""}, {"", ""}} {
			if k, v := w.Tags.At(i); k != want[0] || v != want[1] {
				t.Errorf("At(%d) = %q=%q, want %q=%q", i, k, v, want[0], want[1])
			}
		}
		if _, ok := w.Tags.Get("name"); !ok {
			t.Error("Get(name) did not find the one resolvable key")
		}
		return nil
	}); err != nil {
		t.Fatalf("EachWay: %v", err)
	}
}

// At is the only way a caller walks the pairs, and it must hold at both ends
// of the range. A negative index reaches it from a caller counting down.
func TestTagsAtHoldsAtBothEndsOfItsRange(t *testing.T) {
	b := blockWith(t, []string{"", "name", "Horsens"}, way(1, nil, []int32{1}, []int32{2}))

	if err := b.EachWay(func(w Way) error {
		for _, i := range []int{-1, 1, 1 << 20} {
			if k, v := w.Tags.At(i); k != "" || v != "" {
				t.Errorf("At(%d) = %q=%q on a single pair, want empty", i, k, v)
			}
		}
		if k, v := w.Tags.At(0); k != "name" || v != "Horsens" {
			t.Errorf("At(0) = %q=%q, want name=Horsens; the guard must not shift the pairs", k, v)
		}
		return nil
	}); err != nil {
		t.Fatalf("EachWay: %v", err)
	}
}

// The zero Tags -- what an element decoded with no tags at all carries, and
// what the error paths return -- answers every question with "absent" rather
// than panicking on its nil block.
func TestTheZeroTagsIsEmptyNotBroken(t *testing.T) {
	var tags Tags
	if tags.Len() != 0 {
		t.Errorf("Len = %d, want 0", tags.Len())
	}
	if k, v := tags.At(0); k != "" || v != "" {
		t.Errorf("At(0) = %q=%q, want empty", k, v)
	}
	if v, ok := tags.Get("name"); ok || v != "" {
		t.Errorf("Get(name) = %q, %v; want absent", v, ok)
	}
	if tags.Has("name") || tags.Is("name", "") {
		t.Error("the zero Tags claims to carry a tag")
	}
}

// A member type is a number in the file and a word in everything a person
// reads. Nothing in the decoder's own paths calls String, so a swapped pair
// here would show up only in an error message or a dump -- where it says a
// boundary is made of relations when it is made of ways.
//
// The numbering is the format's: 0 node, 1 way, 2 relation.
func TestMemberTypeNamesItself(t *testing.T) {
	for _, tc := range []struct {
		t    MemberType
		want string
	}{
		{MemberNode, "node"},
		{MemberWay, "way"},
		{MemberRelation, "relation"},
	} {
		if got := tc.t.String(); got != tc.want {
			t.Errorf("MemberType(%d) = %q, want %q", int(tc.t), got, tc.want)
		}
	}
	// Not reachable through the decoder, which refuses an undefined type --
	// but String is exported and must not answer with one of the real names.
	for _, n := range []MemberType{-1, 3, 99} {
		got := n.String()
		if got == "node" || got == "way" || got == "relation" {
			t.Errorf("MemberType(%d) = %q, which is a defined type's name", int(n), got)
		}
		if !strings.Contains(got, "3") && n == 3 {
			t.Errorf("MemberType(3) = %q, want the number said", got)
		}
	}
}

// An overlong varint inside an element -- more bytes than a 64-bit value can
// have -- must come back as an error. The reader's own tests pin the refusal;
// this pins that the element decoding is standing behind it, because the
// failure mode without it is a panic in a library walking a downloaded file.
func TestAnOverlongVarintInAnElementIsAnErrorNotAPanic(t *testing.T) {
	overlong := append(bytes.Repeat([]byte{0x80}, 10), 0x01)

	t.Run("a way id", func(t *testing.T) {
		data := append(pbTag(1, protobufWireVarint), overlong...)
		data = append(data, packedSint(8, 1, 2)...)
		b := blockWith(t, []string{""}, pbBytes(3, data))
		if err := b.EachWay(func(Way) error { return nil }); err == nil {
			t.Error("a way whose id cannot be a varint decoded cleanly")
		}
	})

	t.Run("inside a packed run of node ids", func(t *testing.T) {
		payload := append([]byte{0x02}, overlong...)
		b := blockWith(t, []string{""}, pbBytes(2, pbBytes(1, payload)))
		if err := b.EachNode(func(Node) error { return nil }); err == nil {
			t.Error("a dense run holding a value too wide for a varint decoded cleanly")
		}
	})
}

// Every prefix of an element is a file somebody's download ended in the
// middle of. Each must come back as an error or as a decode, never as a
// panic -- the error branches inside the four element decoders are one per
// field read, and no single malformed fixture reaches more than one of them.
//
// The element's declared length is recomputed for each prefix, so what is
// truncated is the message rather than the framing: this is the decoder
// running off the end of a field, not the block reader catching a bad length.
func TestEveryTruncationOfAnElementIsAnErrorNotAPanic(t *testing.T) {
	strs := []string{"", "name", "Horsens", "outer"}
	const sName, sHors, sOuter = 1, 2, 3

	denseBody := packedSint(1, 1_000, 1_001)
	denseBody = append(denseBody, packedSint(8, 10, 20)...)
	denseBody = append(denseBody, packedSint(9, 30, 40)...)
	denseBody = append(denseBody, packedInt(10, tagRun([][]int32{{sName, sHors}, nil})...)...)

	nodeBody := append(pbTag(1, protobufWireVarint), binary.AppendUvarint(nil, zigzag(42))...)
	nodeBody = append(nodeBody, packedInt(2, sName)...)
	nodeBody = append(nodeBody, packedInt(3, sHors)...)
	nodeBody = append(nodeBody, append(pbTag(8, protobufWireVarint), binary.AppendUvarint(nil, zigzag(-557_000_000))...)...)

	wayBody := pbVarint(1, 1234)
	wayBody = append(wayBody, packedInt(2, sName)...)
	wayBody = append(wayBody, packedInt(3, sHors)...)
	wayBody = append(wayBody, packedSint(8, 500, 700, 690)...)

	relBody := pbVarint(1, 77)
	relBody = append(relBody, packedInt(8, sOuter, sOuter)...)
	relBody = append(relBody, packedSint(9, 100, 98)...)
	relBody = append(relBody, packedInt(10, int32(MemberWay), int32(MemberWay))...)

	for _, tc := range []struct {
		name  string
		field int
		body  []byte
		walk  func(*PrimitiveBlock) error
	}{
		{"a dense run", 2, denseBody, func(b *PrimitiveBlock) error { return b.EachNode(func(Node) error { return nil }) }},
		{"a plain node", 1, nodeBody, func(b *PrimitiveBlock) error { return b.EachNode(func(Node) error { return nil }) }},
		{"a way", 3, wayBody, func(b *PrimitiveBlock) error { return b.EachWay(func(Way) error { return nil }) }},
		{"a relation", 4, relBody, func(b *PrimitiveBlock) error { return b.EachRelation(func(Relation) error { return nil }) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for n := 0; n <= len(tc.body); n++ {
				data := pbBytes(1, stringTable(strs...))
				data = append(data, pbBytes(2, pbBytes(tc.field, tc.body[:n]))...)
				b, err := DecodePrimitiveBlock(data)
				if err != nil {
					t.Fatalf("the block framing was refused at %d of %d bytes: %v", n, len(tc.body), err)
				}
				// Either answer is correct. A panic, or an error that named
				// another format, would not be.
				if err := tc.walk(&b); err != nil && !strings.HasPrefix(err.Error(), "osmpbf: ") {
					t.Fatalf("truncated at %d of %d bytes: %v, want the format named first",
						n, len(tc.body), err)
				}
			}
			// The whole thing must still decode, or the loop above proved
			// only that everything fails.
			data := pbBytes(1, stringTable(strs...))
			data = append(data, pbBytes(2, pbBytes(tc.field, tc.body))...)
			b, err := DecodePrimitiveBlock(data)
			if err != nil {
				t.Fatalf("DecodePrimitiveBlock: %v", err)
			}
			if err := tc.walk(&b); err != nil {
				t.Fatalf("the untruncated element was refused: %v", err)
			}
		})
	}
}

// A packed field that declares more bytes than the element holds. The length
// is read inside the packed accessor, one level below the element decoder's
// own reads, and it is the length a hostile file has most to gain from: the
// run is where every id and coordinate lives.
func TestAPackedRunThatOverrunsItsElementIsRefused(t *testing.T) {
	// Field 8 of a Way, declaring 200 bytes of references and supplying two.
	body := pbVarint(1, 1234)
	body = append(body, pbTag(8, protobufWireBytes)...)
	body = binary.AppendUvarint(body, 200)
	body = append(body, 0x01, 0x02)

	b := blockWith(t, []string{""}, pbBytes(3, body))
	err := b.EachWay(func(Way) error { return nil })
	if err == nil {
		t.Fatal("a packed run declaring more bytes than the way holds was accepted")
	}
	if !strings.Contains(err.Error(), "node references") {
		t.Errorf("%v, want the field named", err)
	}
}

// The group is where a malformed file lands before any element decoder sees
// it, and every element decoder repeats the same three reads. A field tag
// that runs off the end, a length that overruns what is left, and a field
// this decoder skips whose length overruns: each is a separate branch, and
// each must come back named rather than panicking or quietly stopping the
// pass with a partial answer.
func TestAMalformedGroupOrElementIsRefused(t *testing.T) {
	// A one-byte tag varint with its continuation bit set: the field number
	// is never finished. Field numbers of 16 and up take two bytes, so this
	// is what a truncation in the middle of one looks like.
	unfinishedTag := []byte{0x80}
	// Field 7, length-delimited, declaring 200 bytes and supplying none.
	overrunning := append(pbTag(7, protobufWireBytes), binary.AppendUvarint(nil, 200)...)

	for _, tc := range []struct {
		name  string
		group []byte
	}{
		{"a group whose field tag is unfinished", unfinishedTag},
		{"a group holding a field number of zero", []byte{0x00}},
		{"a group whose element length overruns it",
			append(pbTag(3, protobufWireBytes), binary.AppendUvarint(nil, 200)...)},
		{"a group whose skipped field overruns it", overrunning},
		{"a dense run whose field tag is unfinished", pbBytes(2, unfinishedTag)},
		{"a dense run whose skipped field overruns it", pbBytes(2, overrunning)},
		{"a plain node whose field tag is unfinished", pbBytes(1, unfinishedTag)},
		{"a plain node whose skipped field overruns it", pbBytes(1, overrunning)},
		{"a way whose field tag is unfinished", pbBytes(3, unfinishedTag)},
		{"a way whose skipped field overruns it", pbBytes(3, overrunning)},
		{"a relation whose field tag is unfinished", pbBytes(4, unfinishedTag)},
		{"a relation whose skipped field overruns it", pbBytes(4, overrunning)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := blockWith(t, []string{""}, tc.group)
			var errs []error
			errs = append(errs, b.EachNode(func(Node) error { return nil }))
			errs = append(errs, b.EachWay(func(Way) error { return nil }))
			errs = append(errs, b.EachRelation(func(Relation) error { return nil }))

			var named int
			for _, err := range errs {
				if err == nil {
					continue
				}
				named++
				if !strings.HasPrefix(err.Error(), "osmpbf: ") {
					t.Errorf("%v, want the format named first", err)
				}
			}
			if named == 0 {
				t.Error("every pass accepted it; a malformed group must stop the pass that reads it")
			}
		})
	}
}
