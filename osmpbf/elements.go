package osmpbf

import (
	"bytes"
	"fmt"

	"github.com/wisborg/osmbase/internal/protobuf"
)

// Caps on what a single element may hold.
//
// The same reasoning as the string table's: a packed value costs as little as
// one byte on the wire, so a block at the size limit can declare millions of
// them, and each becomes eight bytes in memory. The numbers below are far
// above what OpenStreetMap itself permits -- an editor will not accept a way
// of more than 2,000 nodes, or a relation of more than 32,000 members -- so a
// file that exceeds them was not produced by editing the map.
const (
	MaxWayRefs         = 1 << 17
	MaxRelationMembers = 1 << 20
	MaxDenseNodes      = 1 << 21
	MaxTags            = 1 << 16
)

// MemberType says what kind of element a relation member refers to.
type MemberType int

// The member types, numbered as the format numbers them.
const (
	MemberNode MemberType = iota
	MemberWay
	MemberRelation
)

func (t MemberType) String() string {
	switch t {
	case MemberNode:
		return "node"
	case MemberWay:
		return "way"
	case MemberRelation:
		return "relation"
	}
	return fmt.Sprintf("member type %d", int(t))
}

// Tags are an element's key/value pairs, held as indices into the block's
// string table and resolved on demand.
//
// Not a map, and not resolved eagerly, because the passes over an extract ask
// one or two questions of each element -- is this a boundary, is it
// administrative -- and discard the rest. Building a map per element would
// allocate several times over for every one of a country's millions, to
// answer a question that Get answers without allocating at all.
type Tags struct {
	block      *PrimitiveBlock
	keys, vals []int32
}

// Len is the number of pairs.
func (t Tags) Len() int { return len(t.keys) }

// At returns the ith pair, resolved.
func (t Tags) At(i int) (key, value string) {
	if i < 0 || i >= len(t.keys) {
		return "", ""
	}
	return t.block.StringAt(int(t.keys[i])), t.block.StringAt(int(t.vals[i]))
}

// Get returns the value for a key, and whether it was present.
//
// A tag whose value is the empty string is a real tag, which is why the
// second result is not "is the value non-empty": OpenStreetMap carries
// `name=` on features whose name is deliberately blank, and treating that as
// absent would make them nameless rather than unnamed.
func (t Tags) Get(key string) (string, bool) {
	if i := t.index(key); i >= 0 {
		return t.block.StringAt(int(t.vals[i])), true
	}
	return "", false
}

// Is reports whether the element carries exactly this key and value.
//
// The question the boundary passes actually ask, and it answers without
// allocating: both sides are compared as the bytes they already are, rather
// than through two Go strings built to be thrown away.
func (t Tags) Is(key, value string) bool {
	i := t.index(key)
	return i >= 0 && bytes.Equal(t.block.BytesAt(int(t.vals[i])), []byte(value))
}

// Has reports whether the key is present at all.
func (t Tags) Has(key string) bool { return t.index(key) >= 0 }

// index finds a key without resolving anything it does not have to.
//
// Linear because an element has a handful of tags: a map would cost more to
// build than the scan it replaces, and the format gives no order to binary
// search.
func (t Tags) index(key string) int {
	if t.block == nil {
		return -1
	}
	k := []byte(key)
	for i, ki := range t.keys {
		if bytes.Equal(t.block.BytesAt(int(ki)), k) {
			return i
		}
	}
	return -1
}

// Node is one node, with its coordinates still in the block's integer scale.
//
// Lat and Lon are block-relative: pass them through PrimitiveBlock.Degrees to
// get degrees. They are left that way because the passes compare and index
// far more nodes than they convert, and the conversion is lossy in the sense
// that matters here -- two nodes that share an integer coordinate are the
// same point, and two float64s that round to the same value may not be.
type Node struct {
	ID       int64
	Lat, Lon int64
	Tags     Tags
}

// Way is one way: an ordered list of node ids, with its tags.
//
// Refs are absolute ids, with the file's delta encoding already undone.
type Way struct {
	ID   int64
	Refs []int64
	Tags Tags
}

// Member is one entry in a relation.
type Member struct {
	Type MemberType
	ID   int64
	Role string
}

// Relation is one relation: an ordered list of members, with its tags.
//
// This is the element the boundary pipeline is looking for: an administrative
// boundary is a relation whose members are the ways making up its outline.
type Relation struct {
	ID      int64
	Members []Member
	Tags    Tags
}

// EachNode calls f for every node in the block, dense or plain.
//
// The Node handed to f -- and the Tags on it -- is only valid for the
// duration of the call: its slices point into buffers this reuses for the
// next element, and the string table it resolves against points into the
// reader's. A caller keeping anything must copy it.
//
// Stopping early is what returning an error from f is for; the error comes
// back unchanged.
func (b *PrimitiveBlock) EachNode(f func(Node) error) error {
	for _, g := range b.Groups {
		if err := b.eachInGroup(g, 1, 2, f, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// EachWay calls f for every way in the block. The same aliasing rules as
// EachNode apply.
func (b *PrimitiveBlock) EachWay(f func(Way) error) error {
	for _, g := range b.Groups {
		if err := b.eachInGroup(g, 3, 0, nil, f, nil); err != nil {
			return err
		}
	}
	return nil
}

// EachRelation calls f for every relation in the block. The same aliasing
// rules as EachNode apply.
func (b *PrimitiveBlock) EachRelation(f func(Relation) error) error {
	for _, g := range b.Groups {
		if err := b.eachInGroup(g, 4, 0, nil, nil, f); err != nil {
			return err
		}
	}
	return nil
}

// eachInGroup walks one PrimitiveGroup, decoding only the field the caller
// asked for and skipping the rest.
//
// Skipping rather than decoding everything is the point: the boundary
// pipeline reads the file three times and wants a different element kind each
// time, so decoding all of them on every pass would do three times the work
// to discard most of it.
func (b *PrimitiveBlock) eachInGroup(
	group []byte, want, dense int,
	onNode func(Node) error, onWay func(Way) error, onRelation func(Relation) error,
) error {
	r := protobuf.New(group, "osmpbf", "a primitive group")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return err
		}
		if wire != protobuf.WireBytes || (field != want && field != dense) {
			if err := r.Skip(field, wire); err != nil {
				return err
			}
			continue
		}
		payload, err := r.Bytes("an element")
		if err != nil {
			return err
		}
		switch field {
		case dense:
			err = b.eachDenseNode(payload, onNode)
		case 1:
			err = b.eachPlainNode(payload, onNode)
		case 3:
			err = b.decodeWay(payload, onWay)
		case 4:
			err = b.decodeRelation(payload, onRelation)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// eachDenseNode decodes a DenseNodes message.
//
// Dense nodes are how an extract stores almost all of its nodes, and nothing
// about them is laid out per node: the ids, latitudes and longitudes are
// three parallel delta-coded runs, and the tags are one flat run of string
// table indices in which each node's pairs are terminated by a zero. So the
// decoding is a walk across four lists at once, and the ways it can go wrong
// are all of the silent kind -- a delta accumulated into the wrong variable,
// a tag run that drifts one node out of step.
func (b *PrimitiveBlock) eachDenseNode(data []byte, f func(Node) error) error {
	if f == nil {
		return nil
	}
	s := &b.scratch
	s.ids, s.lats, s.lons, s.keysVals = s.ids[:0], s.lats[:0], s.lons[:0], s.keysVals[:0]

	r := protobuf.New(data, "osmpbf", "a dense node run")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return err
		}
		switch field {
		case 1: // id, delta coded
			s.ids, err = r.PackedSint64(s.ids, "the node ids", wire)
		case 8: // lat, delta coded
			s.lats, err = r.PackedSint64(s.lats, "the latitudes", wire)
		case 9: // lon, delta coded
			s.lons, err = r.PackedSint64(s.lons, "the longitudes", wire)
		case 10: // keys_vals, a flat run with zero terminators
			s.keysVals, err = r.PackedInt32(s.keysVals, "the tags", wire)
		default:
			err = r.Skip(field, wire)
		}
		if err != nil {
			return err
		}
		if len(s.ids) > MaxDenseNodes {
			return fmt.Errorf("osmpbf: a dense run holds more than %d nodes", MaxDenseNodes)
		}
	}

	// The three runs are one table written as three columns. Different
	// lengths mean the rows do not line up, and reading the shortest would
	// hand back nodes at coordinates belonging to different nodes.
	if len(s.lats) != len(s.ids) || len(s.lons) != len(s.ids) {
		return fmt.Errorf("osmpbf: a dense run has %d ids, %d latitudes and %d longitudes, and they must agree",
			len(s.ids), len(s.lats), len(s.lons))
	}

	var id, lat, lon int64
	var kv int
	for i := range s.ids {
		id += s.ids[i]
		lat += s.lats[i]
		lon += s.lons[i]

		node := Node{ID: id, Lat: lat, Lon: lon}
		if len(s.keysVals) > 0 {
			var err error
			if node.Tags, kv, err = b.denseTags(kv); err != nil {
				return err
			}
		}
		if err := f(node); err != nil {
			return err
		}
	}
	return nil
}

// denseTags takes one node's pairs off the flat keys_vals run, returning
// where the next node's begin.
func (b *PrimitiveBlock) denseTags(at int) (Tags, int, error) {
	s := &b.scratch
	s.keys, s.vals = s.keys[:0], s.vals[:0]
	for {
		if at >= len(s.keysVals) {
			// Running out mid-table is malformed, and it is the failure that
			// would otherwise be invisible: the remaining nodes would come
			// back untagged, which looks exactly like nodes that carry no
			// tags -- most of them, in any extract.
			return Tags{}, at, fmt.Errorf(
				"osmpbf: a dense run's tags end part way through, at index %d of %d", at, len(s.keysVals))
		}
		k := s.keysVals[at]
		at++
		if k == 0 { // this node's pairs end here
			return Tags{block: b, keys: s.keys, vals: s.vals}, at, nil
		}
		if at >= len(s.keysVals) {
			return Tags{}, at, fmt.Errorf(
				"osmpbf: a dense run's tags end after a key with no value, at index %d", at)
		}
		s.keys = append(s.keys, k)
		s.vals = append(s.vals, s.keysVals[at])
		at++
		if len(s.keys) > MaxTags {
			return Tags{}, at, fmt.Errorf("osmpbf: an element holds more than %d tags", MaxTags)
		}
	}
}

// eachPlainNode decodes a Node message -- the per-node encoding, which real
// extracts use only for nodes a dense run cannot hold. Decoded anyway,
// because the format permits it and a file that used it would otherwise come
// back with those nodes silently missing.
func (b *PrimitiveBlock) eachPlainNode(data []byte, f func(Node) error) error {
	if f == nil {
		return nil
	}
	s := &b.scratch
	s.keys, s.vals = s.keys[:0], s.vals[:0]

	var node Node
	r := protobuf.New(data, "osmpbf", "a node")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return err
		}
		switch field {
		case 1: // id
			node.ID, err = r.Sint64("a node id")
		case 2: // keys
			s.keys, err = r.PackedInt32(s.keys, "the tag keys", wire)
		case 3: // vals
			s.vals, err = r.PackedInt32(s.vals, "the tag values", wire)
		case 8: // lat
			node.Lat, err = r.Sint64("a latitude")
		case 9: // lon
			node.Lon, err = r.Sint64("a longitude")
		default:
			err = r.Skip(field, wire)
		}
		if err != nil {
			return err
		}
	}
	tags, err := b.pairedTags()
	if err != nil {
		return err
	}
	node.Tags = tags
	return f(node)
}

// decodeWay decodes a Way message.
func (b *PrimitiveBlock) decodeWay(data []byte, f func(Way) error) error {
	if f == nil {
		return nil
	}
	s := &b.scratch
	s.keys, s.vals, s.refs = s.keys[:0], s.vals[:0], s.refs[:0]

	var way Way
	r := protobuf.New(data, "osmpbf", "a way")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return err
		}
		switch field {
		case 1: // id, NOT zigzag -- a way's id is a plain int64
			way.ID, err = r.Int64("a way id")
		case 2:
			s.keys, err = r.PackedInt32(s.keys, "the tag keys", wire)
		case 3:
			s.vals, err = r.PackedInt32(s.vals, "the tag values", wire)
		case 8: // refs, delta coded
			s.refs, err = r.PackedSint64(s.refs, "the node references", wire)
			if err == nil && len(s.refs) > MaxWayRefs {
				err = fmt.Errorf("osmpbf: a way holds more than %d node references", MaxWayRefs)
			}
		default:
			err = r.Skip(field, wire)
		}
		if err != nil {
			return err
		}
	}

	undelta(s.refs)
	way.Refs = s.refs
	tags, err := b.pairedTags()
	if err != nil {
		return err
	}
	way.Tags = tags
	return f(way)
}

// decodeRelation decodes a Relation message.
func (b *PrimitiveBlock) decodeRelation(data []byte, f func(Relation) error) error {
	if f == nil {
		return nil
	}
	s := &b.scratch
	s.keys, s.vals = s.keys[:0], s.vals[:0]
	s.roles, s.types, s.refs, s.members = s.roles[:0], s.types[:0], s.refs[:0], s.members[:0]

	var rel Relation
	r := protobuf.New(data, "osmpbf", "a relation")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return err
		}
		switch field {
		case 1: // id, a plain int64
			rel.ID, err = r.Int64("a relation id")
		case 2:
			s.keys, err = r.PackedInt32(s.keys, "the tag keys", wire)
		case 3:
			s.vals, err = r.PackedInt32(s.vals, "the tag values", wire)
		case 8: // roles_sid
			s.roles, err = r.PackedInt32(s.roles, "the member roles", wire)
		case 9: // memids, delta coded
			s.refs, err = r.PackedSint64(s.refs, "the member ids", wire)
		case 10: // types
			s.types, err = r.PackedInt32(s.types, "the member types", wire)
		default:
			err = r.Skip(field, wire)
		}
		if err != nil {
			return err
		}
		if len(s.refs) > MaxRelationMembers {
			return fmt.Errorf("osmpbf: a relation holds more than %d members", MaxRelationMembers)
		}
	}

	// Three parallel runs again, and the same failure if they disagree: a
	// member would take its role or its type from a different member.
	if len(s.roles) != len(s.refs) || len(s.types) != len(s.refs) {
		return fmt.Errorf("osmpbf: a relation has %d member ids, %d roles and %d types, and they must agree",
			len(s.refs), len(s.roles), len(s.types))
	}

	undelta(s.refs)
	for i, id := range s.refs {
		t := MemberType(s.types[i])
		if t != MemberNode && t != MemberWay && t != MemberRelation {
			return fmt.Errorf("osmpbf: a relation member has type %d, and the format defines 0, 1 and 2", s.types[i])
		}
		s.members = append(s.members, Member{Type: t, ID: id, Role: b.StringAt(int(s.roles[i]))})
	}
	rel.Members = s.members
	tags, err := b.pairedTags()
	if err != nil {
		return err
	}
	rel.Tags = tags
	return f(rel)
}

// pairedTags builds the Tags for an element that carries its keys and values
// as two parallel lists.
func (b *PrimitiveBlock) pairedTags() (Tags, error) {
	s := &b.scratch
	if len(s.keys) != len(s.vals) {
		return Tags{}, fmt.Errorf("osmpbf: an element has %d tag keys and %d values, and they must agree",
			len(s.keys), len(s.vals))
	}
	if len(s.keys) > MaxTags {
		return Tags{}, fmt.Errorf("osmpbf: an element holds more than %d tags", MaxTags)
	}
	return Tags{block: b, keys: s.keys, vals: s.vals}, nil
}

// undelta turns a run of deltas into absolute values, in place.
//
// The format delta-codes every id it repeats -- node ids in a dense run, a
// way's node references, a relation's member ids -- because consecutive ids
// in an extract are usually close together, and the difference fits in a byte
// where the id itself needs five. Forgetting to undo it does not fail: it
// yields small numbers that look like perfectly good ids belonging to
// entirely different elements.
func undelta(xs []int64) {
	var acc int64
	for i, d := range xs {
		acc += d
		xs[i] = acc
	}
}
