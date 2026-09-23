package osm

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Ring is a closed loop of coordinates.
//
// The first point is NOT repeated at the end. A ring of n corners holds n
// points and the closing edge is implied, which is what boundary's own
// point-in-ring test already assumes -- it walks the edges with a wraparound
// rather than looking for a repeated vertex. Anything writing GeoJSON has to
// repeat the first point itself, because that format requires it.
type Ring []Point

// Rings is what a boundary's ways assemble into.
type Rings struct {
	// Outer and Inner are closed rings: the areas enclosed, and the holes in
	// them. Outer rings wind counterclockwise and inner rings clockwise, as
	// RFC 7946 specifies for GeoJSON.
	Outer []Ring
	Inner []Ring

	// Open are the chains that did not close.
	//
	// A field rather than an error because an unclosed chain is ORDINARY
	// input, not a failure: 56% of the ways Denmark's boundary relations name
	// lie outside a Denmark extract, because its boundaries run along
	// borders and coastlines that continue past the cut. A caller drawing a
	// map may want to draw them; a caller testing containment must not.
	// Either way the decision is theirs, and dropping them silently would
	// take it away.
	Open []Chain
}

// Chain is a run of ways that joined to each other but not into a loop.
type Chain struct {
	Role   string
	Points []Point

	// From and To are the node ids at the two loose ends, which is what says
	// WHERE the outline is broken -- and usually that the way carrying it was
	// outside the extract.
	From, To int64

	// Ways is how many ways joined before the chain ran out.
	Ways int
}

// Assemble joins a boundary's ways end to end into rings.
//
// This is not tidying up. Not one of the 46 ways making up Hornsby's outline
// is closed on its own, and the same holds across a Sydney extract: a
// relation's members are fragments, cut at every junction with a neighbour,
// arriving in no particular order and in no particular direction. Assembly is
// the only thing that turns them into a polygon.
//
// Ways are joined on NODE ID rather than on coordinates. Two distinct nodes
// at identical coordinates are ordinary where an extract has been cut, and
// joining those would weld together outlines that merely touch.
func Assemble(ways []Way) (Rings, error) {
	var out Rings
	for _, w := range ways {
		if len(w.Nodes) != len(w.Points) {
			return Rings{}, fmt.Errorf("osm: way %d has %d node ids and %d points; they must correspond",
				w.ID, len(w.Nodes), len(w.Points))
		}
	}

	// An empty role means outer. That is the convention on boundary
	// relations and it is common -- an editor writing an outline does not
	// always spell it out -- so treating it as its own kind would leave most
	// of a country's outlines in neither bucket.
	outer, inner := split(ways)

	var openOuter, openInner []Chain
	out.Outer, openOuter = assembleRole(outer, "outer")
	out.Inner, openInner = assembleRole(inner, "inner")
	out.Open = append(openOuter, openInner...)

	for i, r := range out.Outer {
		out.Outer[i] = orient(r, counterclockwise)
	}
	for i, r := range out.Inner {
		out.Inner[i] = orient(r, clockwise)
	}
	return out, nil
}

// split sorts ways into the two buckets by role.
//
// Lowercased, and the legacy spellings handled: a boundary relation may use
// enclave for a hole and exclave for an outline, which predate inner/outer
// and still appear. Getting one of those backwards is not a cosmetic error --
// a hole assembled as an outline means every point inside it answers "inside
// the area", which is the wrong way round.
//
// Anything else becomes outer, including the empty role, which is the
// convention on boundary relations. Measured, it is also rare: every one of
// the 5,211 members in Denmark's admin relations and 15,542 in Sydney's names
// its role explicitly. The rule is for the files that do not, and for roles a
// later schema adds -- treating an unrecognised one as its own kind would
// leave those outlines in neither bucket.
func split(ways []Way) (outer, inner []Way) {
	for _, w := range ways {
		switch strings.ToLower(strings.TrimSpace(w.Role)) {
		case "inner", "enclave":
			inner = append(inner, w)
		default:
			outer = append(outer, w)
		}
	}
	return outer, inner
}

// assembleRole joins one role's ways into rings, and reports what did not.
func assembleRole(ways []Way, role string) ([]Ring, []Chain) {
	var rings []Ring
	var open []Chain

	// A way that is already a closed loop is a ring in its own right, and it
	// is taken out BEFORE the endpoint index is built rather than joined like
	// the rest. That is the multipolygon rule, and it is not a shortcut: left
	// in the index, a closed way is reachable through its own seam node, so a
	// second way touching that node either gets welded onto it -- two islands
	// coming out as one figure of eight -- or, if the open way is met first,
	// swallows the ring into a chain that never closes and vanishes from
	// every containment answer. Denmark's extract holds 1,823 such ways
	// across 70 relations, so this is the common case, not an exotic one.
	fragments := make([]Way, 0, len(ways))
	for _, w := range ways {
		switch {
		case len(w.Nodes) < 2:
			// A way of one node joins to nothing, and a way of none has no
			// ends to report -- which Read can legitimately produce, because
			// the format permits a way with no references at all. Reported
			// rather than dropped: it is malformed data, and discarding it
			// silently would leave an outline short with nothing to say why.
			open = append(open, degenerate(w, role))
		case isRing(w):
			r := Ring(slices.Clone(w.Points[:len(w.Points)-1]))
			if r.winding() == 0 {
				open = append(open, degenerate(w, role))
				continue
			}
			rings = append(rings, r)
		default:
			fragments = append(fragments, w)
		}
	}

	// Every endpoint, to the fragments that have it. A boundary node joins
	// exactly two ways, so this is a short list per entry -- but it is a list
	// because a file may say otherwise, and finding out by overwriting would
	// be silent.
	ends := map[int64]*endList{}
	at := func(node int64) *endList {
		e := ends[node]
		if e == nil {
			e = &endList{}
			ends[node] = e
		}
		return e
	}
	for i, w := range fragments {
		first, last := w.Nodes[0], w.Nodes[len(w.Nodes)-1]
		e := at(first)
		e.ways = append(e.ways, i)
		if last != first {
			// Only once when both ends are the same node, which a spur that
			// runs out and back is. Listing it twice makes one candidate
			// look like two, and a junction with one way on it then reads as
			// an ambiguous fork.
			e := at(last)
			e.ways = append(e.ways, i)
		}
	}

	used := make([]bool, len(fragments))
	for i := range fragments {
		if used[i] {
			continue
		}
		used[i] = true

		c := start(fragments[i])
		c.extend(fragments, ends, used)
		if !c.closed() {
			// The run may have started in the middle of the outline, so the
			// other direction is still open. Reversing and extending again
			// costs one more walk and finds it.
			before := c.ways
			c.reverse()
			c.extend(fragments, ends, used)
			if c.ways == before {
				// Nothing was behind it either, so put it back the way the
				// file had it. Handing back a reversed copy of an untouched
				// way would be a difference a caller can see and nothing can
				// explain.
				c.reverse()
			}
		}

		if c.closed() {
			// A loop that encloses nothing is not a ring. The clearest way
			// to make one is to name the same way twice: out along it and
			// back, ending where it started with zero area. Reported as a
			// gap, because a degenerate polygon in the output is worse than
			// an outline that says it did not close.
			if r := c.ring(); r.winding() != 0 {
				rings = append(rings, r)
				continue
			}
		}
		open = append(open, c.chain(role))
	}
	return rings, open
}

// isRing reports whether a way closes on itself and encloses something.
//
// More than three points, because a way that runs out and back -- 1,2,1 --
// returns to where it started without enclosing anything. That is a spur, and
// it joins to other ways like any other fragment.
func isRing(w Way) bool {
	return len(w.Nodes) > 3 && w.Nodes[0] == w.Nodes[len(w.Nodes)-1]
}

// degenerate reports a way too short to join to anything.
func degenerate(w Way, role string) Chain {
	c := Chain{Role: role, Points: slices.Clone(w.Points), Ways: 1}
	if len(w.Nodes) > 0 {
		c.From, c.To = w.Nodes[0], w.Nodes[len(w.Nodes)-1]
	}
	return c
}

// chain is a run of ways joined end to end, under construction.
//
// It keeps the two end node ids rather than the whole list of them. Only the
// ends are ever read -- the tail to find what joins next, the head to know
// when the loop has come round -- so carrying every id alongside every point
// doubled the memory for nothing, and the doubling as it grew was the larger
// half of that.
type chain struct {
	head, tail int64
	points     []Point
	ways       int
}

func start(w Way) *chain {
	return &chain{
		head:   w.Nodes[0],
		tail:   w.Nodes[len(w.Nodes)-1],
		points: slices.Clone(w.Points),
		ways:   1,
	}
}

// closed reports whether the chain has come back to where it started.
//
// Four points minimum, because a run out and back along itself returns to its
// own start without enclosing anything: a way 1,2,1 would otherwise read as a
// ring, and a degenerate polygon in the output is worse than a reported gap.
func (c *chain) closed() bool {
	return len(c.points) > 3 && c.head == c.tail
}

func (c *chain) reverse() {
	c.head, c.tail = c.tail, c.head
	slices.Reverse(c.points)
}

// extend appends ways onto the tail for as long as exactly one fits.
func (c *chain) extend(ways []Way, ends map[int64]*endList, used []bool) {
	for {
		// Checked before looking rather than after appending. A chain that
		// has already come round is finished, and looking again would let it
		// leave through the node it arrived at -- which is how a ring and a
		// way touching its seam become one self-touching loop.
		if c.closed() {
			return
		}
		i, reversed, ok := c.next(ways, ends, used)
		if !ok {
			return
		}
		used[i] = true
		c.append(ways[i], reversed)
		c.ways++
	}
}

// endList is the ways that have one node as an endpoint.
//
// start is how far into ways the consumed ones reach. It only ever moves
// forward, which is what keeps the walk linear: the list is built once and
// never rebuilt, so without a cursor every step rescans the ways already
// taken. A node that is an endpoint of M ways then costs O(M) a step and
// O(M squared) a relation -- and a relation naming one two-node way a
// quarter of a million times is a few kilobytes of packed varints that
// compress to nothing, so the file is free to write and the walk is not.
type endList struct {
	ways  []int
	start int
}

// maxForks bounds how many continuations are considered at one junction.
//
// A boundary node joins exactly two ways, so a real junction offers one
// continuation and a node where two outlines touch offers three. Anything
// past a handful is not an outline, and scanning it is the other half of the
// quadratic cost -- so beyond this the chain is left open, which is what an
// unreadable junction deserves.
const maxForks = 8

// next finds the way that continues the chain, and says whether it has to be
// reversed to run away from the tail.
//
// A junction where more than one unused way is available is NOT guessed at.
// Where two outlines of the same relation touch at a node, taking the wrong
// fork walks out of one lobe into the other and closes a figure of eight
// around both -- a ring enclosing the wrong area, with no gap reported and
// nothing to tell a caller it happened. So the fork that closes the current
// loop is preferred, and if none of them closes it the chain is left open,
// which is the answer this package gives everywhere else when the data does
// not say.
func (c *chain) next(ways []Way, ends map[int64]*endList, used []bool) (i int, reversed, ok bool) {
	e := ends[c.tail]
	if e == nil {
		return 0, false, false
	}
	// Past the consumed prefix. start only advances, so this is free across
	// the walk as a whole however often it is called.
	for e.start < len(e.ways) && used[e.ways[e.start]] {
		e.start++
	}

	var live [maxForks]int
	var n int
	for _, j := range e.ways[e.start:] {
		if used[j] {
			continue
		}
		live[n] = j
		n++
		if n == maxForks {
			break
		}
	}

	switch n {
	case 0:
		return 0, false, false
	case 1:
		j := live[0]
		return j, ways[j].Nodes[len(ways[j].Nodes)-1] == c.tail, true
	}

	for _, j := range live[:n] {
		nodes := ways[j].Nodes
		// The closure it offers has to be a real ring. A relation naming the
		// same way twice puts its own twin at the junction, and that twin
		// closes the loop -- out along the way and back, three points,
		// enclosing nothing. Preferred without this check, the chain takes
		// that fork, fails to close on it, and walks on poisoned, so a
		// duplicated member costs the whole outline rather than just itself.
		if len(c.points)+len(nodes)-1 <= 3 {
			continue
		}
		switch {
		case nodes[0] == c.tail && nodes[len(nodes)-1] == c.head:
			return j, false, true
		case nodes[len(nodes)-1] == c.tail && nodes[0] == c.head:
			return j, true, true
		}
	}
	return 0, false, false
}

// append adds a way to the tail, dropping its first point because the chain
// already ends on that node.
func (c *chain) append(w Way, reversed bool) {
	points := w.Points
	if reversed {
		points = slices.Clone(points)
		slices.Reverse(points)
		c.tail = w.Nodes[0]
	} else {
		c.tail = w.Nodes[len(w.Nodes)-1]
	}
	c.points = append(c.points, points[1:]...)
}

// ring returns the closed loop, dropping the repeated closing vertex.
func (c *chain) ring() Ring {
	return Ring(slices.Clone(c.points[:len(c.points)-1]))
}

func (c *chain) chain(role string) Chain {
	return Chain{
		Role:   role,
		Points: slices.Clone(c.points),
		From:   c.head,
		To:     c.tail,
		Ways:   c.ways,
	}
}

// Winding directions, as RFC 7946 uses them.
const (
	counterclockwise = 1
	clockwise        = -1
)

// orient returns the ring wound the way asked for, reversing it if it is not.
//
// mvt's equivalent also returns a ring of no area untouched, and this has no
// such case because it cannot reach one: a loop whose winding is zero is
// refused as a ring before it gets here and reported as a gap instead. The
// guard would be unreachable and untestable, which is worse than absent.
func orient(r Ring, want int) Ring {
	if r.winding() == want {
		return r
	}
	slices.Reverse(r)
	return r
}

// winding is +1 for counterclockwise, -1 for clockwise, 0 for a ring that
// encloses nothing.
//
// The shoelace sum over longitude and latitude as though they were plane
// coordinates. That is not the area of anything -- a degree of longitude is
// not a degree of latitude anywhere but the equator -- but the SIGN is what
// is wanted, and scaling one axis cannot change it. An administrative
// boundary that spanned the antimeridian would still break this, and none
// does: OpenStreetMap cuts them there.
//
// Every term is taken RELATIVE to the ring's first point. Without that, each
// product is of the order of the coordinates themselves -- up to 1.6e4 near
// Sydney -- while the answer for a small ring is of the order of 1e-12, and
// the sum loses the sign to cancellation: a square of a ten-millionth of a
// degree reads clockwise when it is not. Translating costs one subtraction a
// term and makes the numbers the ring's own size rather than its distance
// from the origin, which is the same argument mvt's integer version makes for
// the same reason.
func (r Ring) winding() int {
	if len(r) < 3 {
		return 0
	}
	ox, oy := r[0].Lon, r[0].Lat
	var sum, extent float64
	for i := range r {
		j := (i + 1) % len(r)
		xi, yi := r[i].Lon-ox, r[i].Lat-oy
		xj, yj := r[j].Lon-ox, r[j].Lat-oy
		sum += xi*yj - xj*yi
		extent = max(extent, math.Abs(xi), math.Abs(yi))
	}

	// Zero is judged against the ring's own size, not against nothing. A
	// ring whose points are collinear -- which is what a chain that retraces
	// itself produces, and the plainest way to get one is a relation naming
	// the same way twice -- has a true sum of zero and a computed one of
	// whatever the rounding left behind. Comparing that to exact zero calls
	// it wound, and a degenerate polygon then reaches the output as a real
	// one. A real ring's sum is of the order of its extent squared, so
	// anything vanishingly smaller than that encloses nothing.
	if math.Abs(sum) <= 1e-12*extent*extent {
		return 0
	}
	if sum > 0 {
		return counterclockwise
	}
	return clockwise
}
