package osmpbf

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

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
	MaxRelationMembers = 1 << 18
	MaxDenseNodes      = 1 << 18
	MaxTags            = 1 << 16

	// MaxDenseTagEntries bounds the flat keys_vals run, which holds two
	// entries per tag plus a terminator per node. Generous enough for a full
	// dense run carrying a couple of tags each, and it is a bound on the run
	// as a whole -- the per-element MaxTags still applies inside it.
	MaxDenseTagEntries = 1 << 20
)

// ErrStop is the error a callback returns to end a pass early without it
// being a failure.
//
// Defined here rather than left to each caller because the three passes over
// an extract all want it, and three private sentinels would be three
// spellings of one rule -- each with its own errors.Is at the call site. Each*
// returns a callback's error unchanged, so a caller may use its own instead;
// this exists so that the common case has one name.
var ErrStop = errors.New("osmpbf: stop")

// The coordinate system's extent, in nanodegrees. A node outside it is not
// on Earth, and the format has no way to mean one.
//
// Checked rather than tolerated because the scaled coordinate is multiplied
// by the block's granularity in int64, where Go wraps silently: an
// unconstrained coordinate does not fail, it produces a confident position
// somewhere else entirely. Bounding the input is what makes Degrees total.
const (
	maxLatUnits = 90_000_000_000
	maxLonUnits = 180_000_000_000
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

// The indices are int32 although the schema types an element's keys and vals
// as uint32, because a dense block's keys_vals is genuinely int32 and Tags
// holds both. Nothing is lost: the string table is capped well inside 31
// bits, so a value that would not fit is one the table could not hold
// anyway, and a negative index resolves to the empty string through the same
// guard as an out-of-range one.

// Clone returns a copy that outlives the callback it came from.
//
// Necessary because a plain struct copy is not one: the index slices point
// into buffers the next element overwrites, so two copies of a Tags taken
// from two elements end up viewing the same data, and the earlier one
// silently acquires the later one's tags. That is a name landing on the wrong
// place, which is the failure this whole decoder is written against, arriving
// from the caller's side. The string table is not copied -- it belongs to the
// block, which outlives the pass.
func (t Tags) Clone() Tags {
	if t.block == nil {
		return Tags{}
	}
	return Tags{block: t.block, keys: slices.Clone(t.keys), vals: slices.Clone(t.vals)}
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
// Refs are absolute ids, with the file's delta encoding already undone. They
// point into a buffer the next way overwrites: pass 2's whole job is to keep
// them, so it must clone them. A slice field reads as ownership and this one
// is not, which is why it is said here and not only on EachWay.
type Way struct {
	ID   int64
	Refs []int64
	Tags Tags
}

// Member is one entry in a relation.
//
// Role is resolved to a string rather than left as an index, unlike a tag.
// The asymmetry is deliberate: a relation has one role per member where an
// element has a handful of tags, the passes compare the role of every member
// they keep, and Member is already a value the caller copies out. Tags exist
// to answer a question and be discarded; members exist to be kept.
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
// The callback must not call another Each method on the same block: they all
// decode through one set of reused buffers, so re-entering clobbers the
// element being walked. That is a tempting thing to write -- the ways a
// relation names are often in the same block -- so it is worth saying rather
// than leaving to be discovered.
func (b *PrimitiveBlock) EachNode(f func(Node) error) error {
	return b.each(func(g []byte) error { return b.nodesIn(g, f) })
}

// each walks every group in the block.
func (b *PrimitiveBlock) each(walk func([]byte) error) error {
	for _, g := range b.Groups {
		if err := walk(g); err != nil {
			return err
		}
	}
	return nil
}

// EachWay calls f for every way in the block. The same aliasing rules as
// EachNode apply.
func (b *PrimitiveBlock) EachWay(f func(Way) error) error {
	return b.each(func(g []byte) error {
		return b.elementsIn(g, 3, func(payload []byte) error { return b.decodeWay(payload, f) })
	})
}

// EachRelation calls f for every relation in the block. The same aliasing
// rules as EachNode apply.
func (b *PrimitiveBlock) EachRelation(f func(Relation) error) error {
	return b.each(func(g []byte) error {
		return b.elementsIn(g, 4, func(payload []byte) error { return b.decodeRelation(payload, f) })
	})
}

// nodesIn walks a group's nodes, which arrive in either of two encodings.
func (b *PrimitiveBlock) nodesIn(group []byte, f func(Node) error) error {
	return b.fieldsIn(group, func(field int, payload []byte) error {
		switch field {
		case 1:
			return b.decodePlainNode(payload, f)
		case 2:
			return b.eachDenseNode(payload, f)
		}
		return nil
	}, 1, 2)
}

// elementsIn walks a group's elements of one field number.
func (b *PrimitiveBlock) elementsIn(group []byte, want int, decode func([]byte) error) error {
	return b.fieldsIn(group, func(_ int, payload []byte) error { return decode(payload) }, want)
}

// fieldsIn walks a PrimitiveGroup, handing decode the payload of every field
// the caller named and skipping the rest.
//
// Skipping rather than decoding everything is the point: the boundary
// pipeline reads the file three times and wants a different element kind each
// time, so decoding all of them on every pass would do three times the work
// to discard most of it.
func (b *PrimitiveBlock) fieldsIn(group []byte, decode func(field int, payload []byte) error, want ...int) error {
	r := protobuf.New(group, "osmpbf", "a primitive group")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return err
		}
		if wire != protobuf.WireBytes || !slices.Contains(want, field) {
			if err := r.Skip(field, wire); err != nil {
				return err
			}
			continue
		}
		payload, err := r.Bytes("an element")
		if err != nil {
			return err
		}
		if err := decode(field, payload); err != nil {
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
			s.ids, err = r.PackedSint64(s.ids, "the node ids", wire, MaxDenseNodes)
		case 8: // lat, delta coded
			s.lats, err = r.PackedSint64(s.lats, "the latitudes", wire, MaxDenseNodes)
		case 9: // lon, delta coded
			s.lons, err = r.PackedSint64(s.lons, "the longitudes", wire, MaxDenseNodes)
		case 10: // keys_vals, a flat run with zero terminators
			s.keysVals, err = r.PackedInt32(s.keysVals, "the tags", wire, MaxDenseTagEntries)
		default:
			err = r.Skip(field, wire)
		}
		if err != nil {
			return err
		}
	}

	// The three runs are one table written as three columns. Different
	// lengths mean the rows do not line up, and reading the shortest would
	// hand back nodes at coordinates belonging to different nodes.
	if len(s.lats) != len(s.ids) || len(s.lons) != len(s.ids) {
		return fmt.Errorf("osmpbf: a dense run has %d ids, %d latitudes and %d longitudes, and they must agree",
			len(s.ids), len(s.lats), len(s.lons))
	}

	// One spelling of the delta rule, rather than three accumulators advanced
	// together in the loop below -- which is exactly where "a delta
	// accumulated into the wrong variable" happens.
	undelta(s.ids)
	undelta(s.lats)
	undelta(s.lons)

	var kv int
	for i := range s.ids {
		if err := b.onEarth(s.lats[i], s.lons[i]); err != nil {
			return err
		}
		node := Node{ID: s.ids[i], Lat: s.lats[i], Lon: s.lons[i]}
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

	// Entries left over mean the tag column was longer than the id column,
	// which is the same "the rows do not line up" evidence the three
	// coordinate runs are checked for -- and the shape a producer drifting
	// one node out of step also leaves behind.
	if kv != len(s.keysVals) {
		return fmt.Errorf("osmpbf: a dense run has %d tag entries left over after its %d nodes",
			len(s.keysVals)-kv, len(s.ids))
	}
	return nil
}

// onEarth refuses a coordinate the format cannot mean.
//
// The bound is on the value AFTER the block's granularity is applied, which
// is why it is a method: a coordinate is in units of a nanodegree scaled by
// the granularity, so what counts as 90 degrees depends on the block. The
// multiply happens in int64, where Go wraps silently, so an unbounded
// coordinate does not fail -- it produces a confident position somewhere
// else. Dividing the limit by the granularity rather than multiplying the
// coordinate is what keeps this check itself from overflowing.
func (b *PrimitiveBlock) onEarth(lat, lon int64) error {
	g := int64(b.Granularity)
	// The scaled value first, so neither multiply can overflow: each side is
	// at most the axis limit, which is 1.8e11 at the outside.
	if lat < -maxLatUnits/g || lat > maxLatUnits/g || lon < -maxLonUnits/g || lon > maxLonUnits/g {
		return fmt.Errorf("osmpbf: a node is at %d,%d in units of %d nanodegrees, which is off the Earth",
			lat, lon, g)
	}
	// Then the origin, which Degrees adds. Bounding the two separately is not
	// enough: each may be within its axis and the sum outside it, and the
	// result is a node at a latitude the Earth does not have.
	if absLat, absLon := b.LatOffset+g*lat, b.LonOffset+g*lon; absLat < -maxLatUnits ||
		absLat > maxLatUnits || absLon < -maxLonUnits || absLon > maxLonUnits {
		return fmt.Errorf("osmpbf: a node is at %d,%d nanodegrees once the block's origin is applied, which is off the Earth",
			absLat, absLon)
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
func (b *PrimitiveBlock) decodePlainNode(data []byte, f func(Node) error) error {
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
			s.keys, err = r.PackedInt32(s.keys, "the tag keys", wire, MaxTags)
		case 3: // vals
			s.vals, err = r.PackedInt32(s.vals, "the tag values", wire, MaxTags)
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
	if err := b.onEarth(node.Lat, node.Lon); err != nil {
		return err
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
			s.keys, err = r.PackedInt32(s.keys, "the tag keys", wire, MaxTags)
		case 3:
			s.vals, err = r.PackedInt32(s.vals, "the tag values", wire, MaxTags)
		case 8: // refs, delta coded
			s.refs, err = r.PackedSint64(s.refs, "the node references", wire, MaxWayRefs)
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
			s.keys, err = r.PackedInt32(s.keys, "the tag keys", wire, MaxTags)
		case 3:
			s.vals, err = r.PackedInt32(s.vals, "the tag values", wire, MaxTags)
		case 8: // roles_sid
			s.roles, err = r.PackedInt32(s.roles, "the member roles", wire, MaxRelationMembers)
		case 9: // memids, delta coded
			s.refs, err = r.PackedSint64(s.refs, "the member ids", wire, MaxRelationMembers)
		case 10: // types
			s.types, err = r.PackedInt32(s.types, "the member types", wire, MaxRelationMembers)
		default:
			err = r.Skip(field, wire)
		}
		if err != nil {
			return err
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
