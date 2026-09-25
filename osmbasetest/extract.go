package osmbasetest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"math"
	"slices"
)

// Extract builds a synthetic OpenStreetMap PBF extract.
//
// Like the archive builder above, this is an INDEPENDENT implementation of
// the encoding rather than the reader run backwards. A fixture produced by
// inverting the decoder agrees with it by construction, and the failures that
// matter most in this format -- a delta accumulated the wrong way, an id read
// as zigzag when it is two's complement, a dense tag run that drifts one node
// out of step -- are precisely the ones such a fixture cannot see. Everything
// below is written from the format's own schema.
//
// Elements are laid out as a real extract lays them out: nodes first, then
// ways, then relations, each in its own block. The three-pass pipeline
// depends on nothing about that order, but a fixture that got it wrong would
// be testing a file nobody produces.
type Extract struct {
	nodes     []extractNode
	ways      []extractWay
	relations []extractRelation
	layout    ExtractLayout
}

// ExtractLayout is how an Extract's elements are laid out in the file.
//
// The zero value is one block per element kind, in the order a planet dump
// sorts them -- nodes, ways, relations -- at the format's default
// granularity with no offsets. Real files differ on every one of those: a
// country extract is thousands of blocks, some producers put several kinds
// in one block, a file need not be sorted, and a block may scale and shift
// its coordinates. A reader tested only against the zero value has had none
// of that asked of it.
type ExtractLayout struct {
	// PerBlock is the most elements of one kind a block holds; zero is all
	// of them in one.
	PerBlock int
	// Mixed puts a group of each kind in every block, rather than giving
	// each kind blocks of its own.
	Mixed bool
	// Reversed writes relations first and nodes last: a file in no sorted
	// order.
	Reversed bool
	// Granularity is nanodegrees per coordinate unit; zero is the format's
	// default of 100. LatOffset and LonOffset shift every coordinate in a
	// block, in nanodegrees.
	Granularity          int32
	LatOffset, LonOffset int64
}

// Layout sets how the extract is laid out. The elements are unchanged, so an
// extract written two ways must read back the same.
func (e *Extract) Layout(l ExtractLayout) *Extract {
	e.layout = l
	return e
}

type extractNode struct {
	id       int64
	lat, lon float64
	tags     []string
}

type extractWay struct {
	id   int64
	refs []int64
	tags []string
}

// ExtractMember is one member of a relation under construction.
type ExtractMember struct {
	// Type is "node", "way" or "relation".
	Type string
	ID   int64
	Role string
}

type extractRelation struct {
	id      int64
	members []ExtractMember
	tags    []string
}

// NewExtract returns an empty extract.
func NewExtract() *Extract { return &Extract{} }

// Node adds a node. tags are alternating keys and values.
func (e *Extract) Node(id int64, lat, lon float64, tags ...string) *Extract {
	e.nodes = append(e.nodes, extractNode{id: id, lat: lat, lon: lon, tags: tags})
	return e
}

// Way adds a way referencing the given node ids.
func (e *Extract) Way(id int64, refs []int64, tags ...string) *Extract {
	e.ways = append(e.ways, extractWay{id: id, refs: refs, tags: tags})
	return e
}

// Relation adds a relation.
func (e *Extract) Relation(id int64, members []ExtractMember, tags ...string) *Extract {
	e.relations = append(e.relations, extractRelation{id: id, members: members, tags: tags})
	return e
}

// The coordinate scaling every block below uses. 100 nanodegrees per unit is
// what every real producer writes, and a fixture at the default exercises the
// same arithmetic a real file does.
const extractGranularity = 100

// Bytes returns the extract as a PBF file.
//
// Deterministic: the same calls produce byte-identical output, so a test may
// compare bytes and a failure is reproducible.
func (e *Extract) Bytes() []byte {
	out := blobFrame("OSMHeader", pbString(4, "OsmSchema-V0.6"))
	nodes := chunks(e.nodes, e.layout.PerBlock)
	ways := chunks(e.ways, e.layout.PerBlock)
	relations := chunks(e.relations, e.layout.PerBlock)

	type group = func(*stringTable) []byte
	var blocks [][]group
	if e.layout.Mixed {
		for k := range max(len(nodes), len(ways), len(relations)) {
			var gs []group
			if k < len(nodes) {
				gs = append(gs, e.nodeGroup(nodes[k]))
			}
			if k < len(ways) {
				gs = append(gs, wayGroup(ways[k]))
			}
			if k < len(relations) {
				gs = append(gs, relationGroup(relations[k]))
			}
			if e.layout.Reversed {
				slices.Reverse(gs)
			}
			blocks = append(blocks, gs)
		}
	} else {
		var byKind [3][][]group
		for _, c := range nodes {
			byKind[0] = append(byKind[0], []group{e.nodeGroup(c)})
		}
		for _, c := range ways {
			byKind[1] = append(byKind[1], []group{wayGroup(c)})
		}
		for _, c := range relations {
			byKind[2] = append(byKind[2], []group{relationGroup(c)})
		}
		if e.layout.Reversed {
			byKind[0], byKind[2] = byKind[2], byKind[0]
		}
		for _, k := range byKind {
			blocks = append(blocks, k...)
		}
	}
	for _, gs := range blocks {
		out = append(out, e.dataBlock(gs...)...)
	}
	return out
}

// chunks splits a run into pieces of at most n, or leaves it whole for n of
// zero; an empty run is no pieces at all, so a kind with no elements writes
// no block.
func chunks[T any](all []T, n int) [][]T {
	if len(all) == 0 {
		return nil
	}
	if n <= 0 {
		return [][]T{all}
	}
	var out [][]T
	for len(all) > n {
		out = append(out, all[:n])
		all = all[n:]
	}
	return append(out, all)
}

// dataBlock builds one OSMData block around a group, with the string table
// the group's contents need.
func (e *Extract) dataBlock(groups ...func(*stringTable) []byte) []byte {
	st := newStringTable()
	var bodies [][]byte
	for _, g := range groups {
		bodies = append(bodies, g(st))
	}

	block := pbBytes(1, st.encode())
	for _, body := range bodies {
		block = append(block, pbBytes(2, body)...)
	}
	block = append(block, pbVarint(17, uint64(e.granularity()))...)
	if e.layout.LatOffset != 0 {
		// A plain int64 varint, not zigzag: a negative offset is ten bytes
		// of two's complement, which is its own path through a reader.
		block = append(block, pbVarint(19, uint64(e.layout.LatOffset))...)
	}
	if e.layout.LonOffset != 0 {
		block = append(block, pbVarint(20, uint64(e.layout.LonOffset))...)
	}
	return blobFrame("OSMData", block)
}

func (e *Extract) granularity() int64 {
	if e.layout.Granularity > 0 {
		return int64(e.layout.Granularity)
	}
	return extractGranularity
}

// nodeGroup encodes nodes as one dense run, which is how a real extract
// stores them.
func (e *Extract) nodeGroup(nodes []extractNode) func(*stringTable) []byte {
	return func(st *stringTable) []byte { return e.encodeNodes(st, nodes) }
}

func (e *Extract) encodeNodes(st *stringTable, nodes []extractNode) []byte {
	ids := make([]int64, len(nodes))
	lats := make([]int64, len(nodes))
	lons := make([]int64, len(nodes))
	var keysVals []int32
	var tagged bool

	for i, n := range nodes {
		ids[i] = n.id
		lats[i] = e.units(n.lat, e.layout.LatOffset)
		lons[i] = e.units(n.lon, e.layout.LonOffset)
		for j := 0; j+1 < len(n.tags); j += 2 {
			keysVals = append(keysVals, st.index(n.tags[j]), st.index(n.tags[j+1]))
			tagged = true
		}
		keysVals = append(keysVals, 0) // this node's pairs end here
	}

	dense := packedSint(1, ids...)
	dense = append(dense, packedSint(8, lats...)...)
	dense = append(dense, packedSint(9, lons...)...)
	if tagged {
		// Omitted entirely when nothing is tagged, which is what a producer
		// does and what leaves the "no tags at all" path exercised.
		dense = append(dense, packedInt32(10, keysVals...)...)
	}
	return pbBytes(2, dense)
}

func wayGroup(ways []extractWay) func(*stringTable) []byte {
	return func(st *stringTable) []byte { return encodeWays(st, ways) }
}

func encodeWays(st *stringTable, ways []extractWay) []byte {
	var out []byte
	for _, w := range ways {
		body := pbVarint(1, uint64(w.id)) // a way's id is a plain int64
		body = append(body, st.tagFields(w.tags)...)
		body = append(body, packedSint(8, w.refs...)...)
		out = append(out, pbBytes(3, body)...)
	}
	return out
}

func relationGroup(relations []extractRelation) func(*stringTable) []byte {
	return func(st *stringTable) []byte { return encodeRelations(st, relations) }
}

func encodeRelations(st *stringTable, relations []extractRelation) []byte {
	var out []byte
	for _, r := range relations {
		body := pbVarint(1, uint64(r.id))
		body = append(body, st.tagFields(r.tags)...)

		roles := make([]int32, len(r.members))
		ids := make([]int64, len(r.members))
		types := make([]int32, len(r.members))
		for i, m := range r.members {
			roles[i] = st.index(m.Role)
			ids[i] = m.ID
			types[i] = memberType(m.Type)
		}
		body = append(body, packedInt32(8, roles...)...)
		body = append(body, packedSint(9, ids...)...)
		body = append(body, packedInt32(10, types...)...)
		out = append(out, pbBytes(4, body)...)
	}
	return out
}

func memberType(s string) int32 {
	switch s {
	case "way":
		return 1
	case "relation":
		return 2
	}
	return 0 // node
}

// units converts degrees to the block's integer scale, after its offset.
//
// Rounded rather than truncated: a coordinate that truncates loses up to a
// unit in one direction only, which biases a whole fixture south and west and
// would make an exact round-trip assertion impossible to write.
func (e *Extract) units(d float64, offset int64) int64 {
	return int64(math.Round((d*1e9 - float64(offset)) / float64(e.granularity())))
}

// stringTable accumulates the strings a block's elements refer to.
//
// Index 0 is the empty string, because the format reserves it to mean "no
// string" -- a table that put a real string there would be testing a file no
// producer writes.
type stringTable struct {
	strs []string
	at   map[string]int32
}

func newStringTable() *stringTable {
	return &stringTable{strs: []string{""}, at: map[string]int32{"": 0}}
}

func (t *stringTable) index(s string) int32 {
	if i, ok := t.at[s]; ok {
		return i
	}
	i := int32(len(t.strs))
	t.strs = append(t.strs, s)
	t.at[s] = i
	return i
}

func (t *stringTable) encode() []byte {
	var out []byte
	for _, s := range t.strs {
		out = append(out, pbString(1, s)...)
	}
	return out
}

// tagFields encodes an element's keys and values as the two parallel packed
// runs the per-element messages use.
func (t *stringTable) tagFields(tags []string) []byte {
	if len(tags) < 2 {
		return nil
	}
	var keys, vals []int32
	for i := 0; i+1 < len(tags); i += 2 {
		keys = append(keys, t.index(tags[i]))
		vals = append(vals, t.index(tags[i+1]))
	}
	out := packedInt32(2, keys...)
	return append(out, packedInt32(3, vals...)...)
}

// The protobuf and PBF encoding, from the schema.

// These build on appendTagged and appendBytes in tile.go rather than
// restating them. The vector tile builder and this one are deliberately
// independent of the DECODERS; that is an argument against deriving a fixture
// from the reader, and none at all for two copies of the protobuf field
// encoding in one package.
func pbTag(field, wire int) []byte { return appendTagged(nil, field, wire) }

func pbVarint(field int, v uint64) []byte {
	return binary.AppendUvarint(pbTag(field, 0), v)
}

func pbBytes(field int, b []byte) []byte {
	return appendBytes(pbTag(field, 2), b)
}

func pbString(field int, s string) []byte { return pbBytes(field, []byte(s)) }

// packedSint encodes a delta-coded run of zigzag signed values, which is how
// the format stores every list of ids it repeats.
func packedSint(field int, vs ...int64) []byte {
	var payload []byte
	var prev int64
	for _, v := range vs {
		payload = binary.AppendUvarint(payload, zigzag64(v-prev))
		prev = v
	}
	return pbBytes(field, payload)
}

// packedInt32 encodes a run of plain int32 values: not zigzag, not delta.
func packedInt32(field int, vs ...int32) []byte {
	var payload []byte
	for _, v := range vs {
		payload = binary.AppendUvarint(payload, uint64(int64(v)))
	}
	return pbBytes(field, payload)
}

// blobFrame wraps a block in the compressed blob and length-prefixed header
// the file format puts before every one.
func blobFrame(kind string, payload []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}

	blob := pbVarint(2, uint64(len(payload))) // raw_size
	blob = append(blob, pbBytes(3, buf.Bytes())...)

	header := append(pbString(1, kind), pbVarint(3, uint64(len(blob)))...)
	out := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	out = append(out, header...)
	return append(out, blob...)
}
