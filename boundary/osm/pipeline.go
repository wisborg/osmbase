package osm

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/wisborg/osmbase/osmpbf"
)

// Point is a position in degrees, latitude first.
//
// Its own type rather than one shared with locate.Coord or boundary's
// internal point, because those belong to packages this must not depend on
// and one of them orders its fields the other way round. The order is stated
// because a silent transposition is the kind of mistake that compiles, passes
// a positional composite literal, and puts a Danish municipality in the
// Indian Ocean.
type Point struct{ Lat, Lon float64 }

// Way is one member way of a boundary, with its geometry.
//
// Points are in the order the way lists them, which is the order ring
// assembly needs and not necessarily the order the boundary runs in: a
// relation's ways arrive in no guaranteed direction, and half of them are
// usually reversed. That is part 5's problem, and it is why this stops here.
type Way struct {
	ID   int64
	Role string

	// Nodes are the ids of the points, in the same order and of the same
	// length.
	//
	// Carried because ring assembly joins ways end to end, and with only
	// coordinates the available test for "these two ways meet" is float64
	// equality. That happens to work -- both endpoints come from the same
	// node and so from the same integer -- but it is the wrong rule wearing
	// the right answer's clothes: two genuinely distinct nodes at identical
	// coordinates, which is ordinary where an extract has been cut, would
	// join as one. The id is the join key the file itself uses.
	Nodes  []int64
	Points []Point
}

// Boundary is one administrative area, as the extract describes it.
type Boundary struct {
	ID         int64
	Name       string
	AdminLevel int
	Ways       []Way
}

// Limits bound what a file can make the pipeline hold.
//
// Every one of these is a count the FILE chooses -- how many boundaries it
// declares, how many ways they name, how many nodes those ways reference --
// which makes this the first structure in the library sized by the extract
// rather than by one block of it. They are enforced as each id is recorded,
// not after a pass, because a limit checked afterwards has already spent
// what it is refusing.
type Limits struct {
	Boundaries int
	Ways       int

	// Nodes counts node REFERENCES, not distinct nodes. A boundary way's end
	// node belongs to the way on either side of it, so an extract offers the
	// same id several times over and the cap has to bound what is offered --
	// that is the number that decides how much is allocated.
	Nodes int

	// Points bounds the geometry handed back.
	//
	// It needs its own limit because it is not proportional to any of the
	// above. A way's node list is expanded into coordinates once for every
	// relation that names it, and heavy sharing between adjacent areas is
	// what a real extract looks like -- so a handful of ways named by many
	// relations produces geometry without ever approaching the way cap. A
	// measured example: without this, a 1,495-byte file yielded 1.6 GB.
	Points int
}

// withDefaults fills the fields left at zero.
//
// Per field rather than all-or-nothing, so that raising one limit is the
// natural gesture and works. All-or-nothing was here first, and it meant a
// caller who set only Nodes got a Boundaries of zero and an error saying the
// extract declared more than zero boundaries -- naming no field, and blaming
// the file for the caller's struct literal.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.Boundaries == 0 {
		l.Boundaries = d.Boundaries
	}
	if l.Ways == 0 {
		l.Ways = d.Ways
	}
	if l.Nodes == 0 {
		l.Nodes = d.Nodes
	}
	if l.Points == 0 {
		l.Points = d.Points
	}
	return l
}

// DefaultLimits are sized from what a country extract actually contains.
//
// Denmark has on the order of a few thousand administrative relations and
// their boundaries reference a few million distinct nodes; these leave an
// order of magnitude of headroom on that. A planet file exceeds them,
// deliberately: this pipeline is built for country extracts, and failing at a
// stated limit is a better answer than an unexplained exhaustion of memory.
//
// What they cost if a file actually reaches them, which is worth stating
// because an earlier version of this comment guessed and was out by five
// times: the node id set is 8 bytes a reference, the coordinates 16 a
// reference, the way node lists another 8, and the geometry handed back 24 a
// point. At the numbers below that is on the order of a gigabyte, not a few
// hundred megabytes. It is a ceiling for a hostile file, not a working set:
// a real country extract uses a small fraction of it.
//
// A function rather than a package variable, as in pmtiles, because a
// variable in a public library is one any consumer can change for every other
// consumer in the process.
func DefaultLimits() Limits {
	return Limits{
		Boundaries: 1 << 18,
		Ways:       1 << 22,
		Nodes:      1 << 25,
		Points:     1 << 25,
	}
}

// Options select which boundaries to keep.
type Options struct {
	// Levels are the admin_level values wanted. Empty means every level.
	Levels []int

	// Language prefers name:<Language> over name, when the extract carries
	// one. Empty takes whatever name the feature has.
	Language string

	// Limits bound the pipeline. The zero value means DefaultLimits.
	Limits Limits
}

// ErrNoBoundaries reports that the extract held nothing matching.
//
// Distinguishable because the answer differs from a failure: an extract of a
// region that genuinely has no administrative relations at the levels asked
// for is not a corrupt file, and telling somebody to re-download it would
// waste their time.
var ErrNoBoundaries = errors.New("osm: the extract holds no administrative boundaries at the levels asked for")

// Open is a source of readers over one extract.
//
// A factory rather than a reader because the pipeline reads the file three
// times, and rather than an io.Seeker because the caller may be reading a
// file, a decompressing stream, or a fixture in memory, and only the caller
// knows how to start one again.
type Open func() (io.ReadCloser, error)

// Read runs the three passes and returns the boundaries the extract holds.
func Read(open Open, opts Options) ([]Boundary, error) {
	opts.Limits = opts.Limits.withDefaults()

	found, wantedWays, err := readRelations(open, opts)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrNoBoundaries
	}

	wayNodes, wantedNodes, err := readWays(open, wantedWays, opts.Limits)
	if err != nil {
		return nil, err
	}

	places, err := readNodes(open, wantedNodes)
	if err != nil {
		return nil, err
	}

	return assemble(found, wantedWays, wayNodes, wantedNodes, places, opts.Limits.Points)
}

// relation is what pass 1 keeps: everything but the geometry.
type relation struct {
	id     int64
	name   string
	level  int
	wayIDs []int64
	roles  []string
}

// readRelations is pass 1. It keeps the relations that are administrative
// boundaries at a wanted level, and the ids of the ways they name.
func readRelations(open Open, opts Options) ([]relation, *idSet, error) {
	var found []relation
	wanted := newIDSet(opts.Limits.Ways)

	err := eachBlock(open, func(b *osmpbf.PrimitiveBlock) error {
		return b.EachRelation(func(r osmpbf.Relation) error {
			if !r.Tags.Is("boundary", "administrative") {
				return nil
			}
			level, ok := adminLevel(r.Tags)
			if !ok || !wantsLevel(opts.Levels, level) {
				return nil
			}
			name := preferredName(r.Tags, opts.Language)
			if name == "" {
				// A boundary with no name cannot answer the question this
				// pipeline exists for, and an extract carries many of them --
				// maritime edges, statistical areas. Dropped rather than kept
				// nameless, so the derived file holds only what can be
				// reported.
				return nil
			}
			if len(found) >= opts.Limits.Boundaries {
				return fmt.Errorf("osm: the extract declares more than %d boundaries at these levels",
					opts.Limits.Boundaries)
			}

			keep := relation{id: r.ID, name: name, level: level}
			for _, m := range r.Members {
				if m.Type != osmpbf.MemberWay {
					// A boundary relation also names an admin_centre node and
					// sometimes a sub-area relation. Neither is part of the
					// outline.
					continue
				}
				if err := wanted.add(m.ID); err != nil {
					return err
				}
				keep.wayIDs = append(keep.wayIDs, m.ID)
				keep.roles = append(keep.roles, m.Role)
			}
			found = append(found, keep)
			return nil
		})
	})
	if err != nil {
		return nil, nil, err
	}
	wanted.freeze()
	return found, wanted, nil
}

// readWays is pass 2. For the ways pass 1 asked about, it records the nodes
// each one references.
//
// The node lists are held in a slice parallel to the frozen way set rather
// than in a map keyed by way id. That is the whole reason the set is a sorted
// slice: a binary search gives the position, and the position indexes
// everything else, so nothing pays a map's per-entry overhead.
func readWays(open Open, wantedWays *idSet, limits Limits) ([][]int64, *idSet, error) {
	nodes := make([][]int64, wantedWays.len())
	// Presence tracked separately rather than by nodes[i] != nil. A way may
	// legitimately carry no references at all, and slices.Clone of an empty
	// Refs is nil or not depending on whether an earlier way in the same
	// block happened to grow the decoder's scratch buffer -- so nil as a
	// sentinel makes "have I seen this way" depend on where the way sits in
	// the file, and lets a second copy of an empty way through unnoticed.
	seen := make([]bool, wantedWays.len())
	wanted := newIDSet(limits.Nodes)

	err := eachBlock(open, func(b *osmpbf.PrimitiveBlock) error {
		return b.EachWay(func(w osmpbf.Way) error {
			i, ok := wantedWays.find(w.ID)
			if !ok {
				return nil
			}
			if seen[i] {
				// Two ways with one id. The rest of the pipeline would take
				// the first silently and draw a boundary from half of one way
				// and half of another.
				return fmt.Errorf("osm: the extract holds way %d twice", w.ID)
			}
			// Cloned because Refs points into a buffer the next way
			// overwrites.
			nodes[i] = slices.Clone(w.Refs)
			seen[i] = true
			for _, id := range w.Refs {
				if err := wanted.add(id); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, nil, err
	}
	wanted.freeze()
	return nodes, wanted, nil
}

// readNodes is pass 3. For the nodes pass 2 asked about, it records where
// they are.
//
// This is the pass the memory analysis is about. The extract holds every
// building, address and traffic signal in the country; this keeps only the
// nodes an administrative boundary actually runs through, in an array
// parallel to the frozen set -- 24 bytes a node, against the 50 or more a
// map[int64]Point would spend per entry before storing anything.
func readNodes(open Open, wantedNodes *idSet) ([]Point, error) {
	points := make([]Point, wantedNodes.len())
	seen := make([]bool, wantedNodes.len())

	err := eachBlock(open, func(b *osmpbf.PrimitiveBlock) error {
		return b.EachNode(func(n osmpbf.Node) error {
			i, ok := wantedNodes.find(n.ID)
			if !ok {
				return nil
			}
			if seen[i] {
				// Refused for the same reason a way held twice is: the file
				// gives two answers and there is no mechanism to make them
				// agree. Last-one-wins would move the boundary to whichever
				// block came later, in the structure whose values end up as
				// drawn pixels.
				return fmt.Errorf("osm: the extract holds node %d twice", n.ID)
			}
			lat, lon := b.Degrees(n.Lat, n.Lon)
			points[i] = Point{Lat: lat, Lon: lon}
			seen[i] = true
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	// A node a boundary way references and the file does not contain is a
	// hole in the outline. Reported rather than filled with 0,0 -- which is a
	// real place in the Gulf of Guinea, and a ring through it would enclose
	// most of a hemisphere.
	if missing := countFalse(seen); missing > 0 {
		return nil, fmt.Errorf("osm: %d of the %d nodes the boundary ways reference are not in the extract",
			missing, len(seen))
	}
	return points, nil
}

func countFalse(bs []bool) int {
	var n int
	for _, b := range bs {
		if !b {
			n++
		}
	}
	return n
}

// assemble joins the three passes back together.
//
// maxPoints bounds the geometry produced, which is not bounded by anything
// the passes recorded: a way's node list is expanded once per relation that
// names it, and boundary ways are shared between neighbours.
func assemble(found []relation, wantedWays *idSet, wayNodes [][]int64, wantedNodes *idSet, points []Point, maxPoints int) ([]Boundary, error) {
	out := make([]Boundary, 0, len(found))
	var total int

	for _, r := range found {
		b := Boundary{ID: r.id, Name: r.name, AdminLevel: r.level}
		for j, id := range r.wayIDs {
			i, ok := wantedWays.find(id)
			if !ok {
				// Every id in wayIDs was added to the set in pass 1, and a
				// failed add aborts the read, so this cannot happen. Raised
				// rather than skipped because reaching it means the passes
				// have got out of step with each other, and the quiet version
				// of that is an outline missing a piece.
				return nil, fmt.Errorf("osm: way %d is named by relation %d and was never asked for; the passes disagree", id, r.id)
			}
			if wayNodes[i] == nil {
				// A way the relation names and the extract does not hold.
				// Normal at the edge of a cut-out extract: a boundary that
				// leaves the region has members beyond it. The ring assembly
				// in part 5 decides what an incomplete outline means; here it
				// is simply not geometry we have.
				continue
			}

			refs := wayNodes[i]
			if total+len(refs) > maxPoints {
				return nil, fmt.Errorf("osm: the boundaries expand to more than %d points", maxPoints)
			}
			total += len(refs)

			w := Way{
				ID:     id,
				Role:   r.roles[j],
				Nodes:  make([]int64, 0, len(refs)),
				Points: make([]Point, 0, len(refs)),
			}
			for _, nodeID := range refs {
				k, ok := wantedNodes.find(nodeID)
				if !ok {
					// As above: pass 2 added every one of these. Skipping
					// would drop a coordinate from the middle of an outline,
					// which is exactly what readNodes refuses loudly a few
					// lines earlier.
					return nil, fmt.Errorf("osm: node %d is referenced by way %d and was never asked for; the passes disagree", nodeID, id)
				}
				w.Nodes = append(w.Nodes, nodeID)
				w.Points = append(w.Points, points[k])
			}
			b.Ways = append(b.Ways, w)
		}
		out = append(out, b)
	}
	return out, nil
}

// eachBlock opens the extract and hands every OSMData block to walk.
func eachBlock(open Open, walk func(*osmpbf.PrimitiveBlock) error) error {
	rc, err := open()
	if err != nil {
		return fmt.Errorf("osm: opening the extract: %w", err)
	}
	defer rc.Close()

	d := osmpbf.NewReader(rc)
	for {
		block, err := d.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if block.Type != osmpbf.TypeData {
			continue
		}
		pb, err := osmpbf.DecodePrimitiveBlock(block.Data)
		if err != nil {
			return err
		}
		if err := walk(&pb); err != nil {
			return err
		}
	}
}

// adminLevel reads the admin_level tag as a number.
func adminLevel(tags osmpbf.Tags) (int, bool) {
	v, ok := tags.Get("admin_level")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 12 {
		// The tag is free text and carries things like "8;9" and "". A value
		// that is not a level is not a level.
		return 0, false
	}
	return n, true
}

func wantsLevel(levels []int, level int) bool {
	return len(levels) == 0 || slices.Contains(levels, level)
}

// preferredName picks the name to report.
//
// name:<language> first, because an extract of a bilingual region carries
// both and the caller asked for one; then name, which is whatever the place
// calls itself locally. An empty translation is treated as absent: name:de=""
// is as ordinary a tag as admin_level="", and taking it would leave the
// boundary nameless, which drops it from the output entirely.
//
// locate/feature.go has the same policy over the vector tiles' own tag
// container, and the two cannot share code without an accessor function
// between them. They must not drift: locate reports a suburb answered by
// containment through this rule and one answered by nearest through that one,
// and a user comparing the two would see one Place disagreeing with itself
// about which language it is in.
func preferredName(tags osmpbf.Tags, language string) string {
	if language != "" {
		if v, ok := tags.Get("name:" + language); ok && v != "" {
			return v
		}
	}
	v, _ := tags.Get("name")
	return v
}
