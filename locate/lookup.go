package locate

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

// Options configure a lookup. The zero value is usable.
type Options struct {
	// Language prefers a translated name where the data carries one -- "da"
	// reads name:da and falls back to name. Empty takes name as written,
	// which is the local spelling.
	Language string

	// MaxDistanceM caps how far a Near match may be, per level. A level with
	// no entry uses DefaultMaxDistanceM.
	//
	// The cap is what stops this shipping a lie. Without it a point in the
	// outback is "near Alice Springs" from two hundred kilometres away, which
	// is true, useless, and indistinguishable in the output from a match forty
	// metres off.
	MaxDistanceM map[Level]float64

	// Boundaries answers a level by CONTAINMENT rather than by nearest
	// feature, for the levels it covers.
	//
	// nil is a legitimate configuration and the one that needs no download:
	// every answer is then Near, which is what the tiles alone can support.
	// A source that covers a level takes precedence over the tiles for that
	// level, because a statement of fact beats an inference -- and the tiles
	// are not consulted for it at all, which also saves reading the shallow
	// zooms a country lookup would otherwise need.
	Boundaries BoundarySource

	// Levels restricts the lookup. Empty asks for all of them.
	//
	// Worth setting: each level is read at its own zoom, so asking for fewer
	// reads fewer tiles.
	Levels []Level
}

// DefaultMaxDistanceM is how far a level's nearest feature may be before the
// answer is withheld.
//
// Wider for the wider levels, because the features are wider: country labels
// are hundreds of kilometres apart and a street is metres from where you stand.
// These are the distances at which "near X" stops meaning anything useful, and
// they are deliberately generous -- the alternative to an answer here is no
// answer, and a caller can always tighten them or read DistanceM itself.
var DefaultMaxDistanceM = map[Level]float64{
	Country:       1_000_000,
	Region:        500_000,
	Locality:      25_000,
	Macrohood:     5_000,
	Neighbourhood: 3_000,
	Street:        250,
}

// levelSpec says where in the schema a level's data lives.
//
// The zoom is the shallowest at which the producer publishes that level, which
// is also the cheapest tile that can answer: one zoom-4 tile covers a continent
// and answers "which country" for every point in it, where a zoom-14 tile
// answers "which street" for one suburb. Reading each level at its own zoom is
// what keeps a route of thousands of points down to a handful of tile reads.
type levelSpec struct {
	level Level
	layer string
	// kinds selects within the layer; empty takes every kind.
	kinds []string
	zoom  uint8
	// line is true for a level read from line features rather than points.
	line bool
}

// tileSpec is where a level's data lives in the tiles, and whether it is
// there at all.
func tileSpec(l Level) (levelSpec, bool) {
	for _, spec := range levelSpecs {
		if spec.level == l {
			return spec, true
		}
	}
	return levelSpec{}, false
}

var levelSpecs = []levelSpec{
	{Country, "places", []string{"country"}, 4, false},
	{Region, "places", []string{"region"}, 5, false},
	{Locality, "places", []string{"locality"}, 10, false},
	{Macrohood, "places", []string{"macrohood"}, 13, false},
	{Neighbourhood, "places", []string{"neighbourhood"}, 14, false},
	{Street, "roads", nil, 14, true},
}

// Coord is a point to look up.
type Coord struct {
	Lat float64 `json:"latitude"`
	Lon float64 `json:"longitude"`
}

// At answers one coordinate.
//
// A thin wrapper over AtEach, and that is the right way round: the cost of a
// lookup is reading and decoding a tile, so one point and a thousand points in
// the same suburb cost nearly the same. See AtEach.
func At(ctx context.Context, src TileSource, at Coord, opts Options) (Place, error) {
	places, err := AtEach(ctx, src, []Coord{at}, opts)
	if err != nil {
		return Place{}, err
	}
	return places[0], nil
}

// AtEach answers many coordinates, reading each tile once.
//
// This is the primary entry point, not a convenience over At. A route is
// thousands of coordinates over a handful of tiles; grouping them by tile
// before reading anything turns thousands of reads into a handful, and the
// single-point form is the special case.
//
// The result is parallel to pts: one Place per coordinate, in order, including
// for coordinates nothing could be found for.
// src may be nil, and then only the levels Options.Boundaries covers can be
// answered: a store may hold a derived boundary file and no tiles, which is
// what building suburb outlines without fetching a map leaves behind. A level
// that needs tiles is an error in that case rather than a silent absence,
// because the caller asked for it. A covered level is not: a point the
// boundaries hold no data for is left unanswered, the way a point with no
// named feature near it is.
func AtEach(ctx context.Context, src TileSource, pts []Coord, opts Options) ([]Place, error) {
	out := make([]Place, len(pts))
	for i, p := range pts {
		out[i] = Place{Lat: p.Lat, Lon: p.Lon}
	}
	if len(pts) == 0 {
		return out, nil
	}

	all := make([]int, len(pts))
	for i := range all {
		all[i] = i
	}
	for _, level := range Levels {
		if !opts.wants(level) {
			continue
		}
		// Containment first, and exclusively WHERE THE SOURCE KNOWS THE
		// PLACE: a point the source is inside or outside of is answered by
		// it, and the tiles are not consulted for that point. Falling back
		// to a nearest match there would be wrong -- a point in the sea is
		// outside every country, and "near Denmark, 40 km" would turn that
		// correct answer into a guess.
		//
		// A point the source holds no data for is a different thing, and
		// goes to the tiles as though no source were there. A store may hold
		// boundaries for one country and tiles for the world; covering the
		// level everywhere because one file covers it somewhere took three
		// levels away from every coordinate outside that file.
		rest := all
		covered := opts.Boundaries != nil && opts.Boundaries.Covers(level)
		if covered {
			// Checked here as well as on the tile path below. Containment is
			// local file reads rather than network, but a route of thousands
			// of points across several levels is still long enough that a
			// caller cancelling it should be obeyed.
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("locate: looking up %s: %w", level, err)
			}
			rest = nil
			for i, p := range pts {
				name, kind, credit, c := opts.Boundaries.Contains(level, p.Lat, p.Lon)
				switch c {
				case Inside:
					out[i].setMatch(Match{
						Level: level, Name: name, Kind: kind, Attribution: credit,
						Source: Contained,
					})
				case NoData:
					rest = append(rest, i)
				}
			}
			if len(rest) == 0 {
				continue
			}
		}

		// No boundary source for this level, so the tiles answer it -- if
		// they can. Water is the level that cannot be answered any other
		// way: the tiles name rivers and lakes beside you, which is a
		// different question from which sea you are over, so a water level
		// with no boundary data has no answer rather than a misleading one.
		// A level with no tile data at all -- Water -- has no answer here.
		// Removing this guard is an equivalent mutant rather than a bug: the
		// zero spec names no layer, so the loop below finds nothing and the
		// result is the same. It costs a wasted tile read per point, and it
		// costs a reader the knowledge that some levels are answered by
		// boundaries or not at all.
		spec, ok := tileSpec(level)
		if !ok {
			continue
		}
		if src == nil {
			// A caller may hold boundary data and no tiles at all -- a store
			// built by "osmbase boundaries --osm" and nothing else. That is
			// a legitimate configuration for the levels those boundaries
			// cover: a point they hold no data for is left unanswered, as a
			// point with no tile data near it would be, rather than failing
			// a route that is mostly inside them. A level they do not cover
			// at all is where it stops being one.
			if covered {
				continue
			}
			return nil, fmt.Errorf("locate: %s needs map tiles and none were given; ask for fewer levels, or fetch tiles", level)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("locate: looking up %s: %w", level, err)
		}
		// Every point that shares a tile at this level is answered from one
		// read. The grouping is per level because the zooms differ: points a
		// kilometre apart share a country tile and not a street tile.
		byTile := map[tileRef][]int{}
		cap := opts.maxDistance(level)
		for _, i := range rest {
			p := pts[i]
			x, y, err := mercator.TileAt(spec.zoom, p.Lon, p.Lat)
			if err != nil {
				// Not a coordinate. Reported per point by leaving it with no
				// matches rather than failing the whole batch: one bad fix in
				// a track of thousands should not lose the other answers.
				continue
			}
			for _, ref := range tilesNear(spec.zoom, x, y, p, cap) {
				byTile[ref] = append(byTile[ref], i)
			}
		}

		for ref, idx := range byTile {
			feats, err := readLayer(src, ref, spec.layer)
			if err != nil {
				return nil, err
			}
			if len(feats) == 0 {
				continue
			}
			for _, i := range idx {
				m, ok := nearest(feats, spec, ref, pts[i], opts)
				if !ok {
					continue
				}
				// A point may be answered by several tiles once neighbours are
				// read, and the nearest of those answers is the one that is
				// true. Keeping the first would make the result depend on map
				// iteration order, which is randomised.
				if prev, had := out[i].matchAt(level); !had || m.DistanceM < prev.DistanceM {
					out[i].setMatch(m)
				}
			}
		}
	}

	// Widest level first, which is the order a person reads an address in
	// reverse and the order Deepest depends on.
	for i := range out {
		sort.SliceStable(out[i].Matches, func(a, b int) bool {
			return out[i].Matches[a].Level < out[i].Matches[b].Level
		})
	}
	return out, nil
}

// wants reports whether a level was asked for.
func (o Options) wants(l Level) bool {
	if len(o.Levels) == 0 {
		return true
	}
	for _, w := range o.Levels {
		if w == l {
			return true
		}
	}
	return false
}

// maxDistance is the cap for a level.
func (o Options) maxDistance(l Level) float64 {
	if d, ok := o.MaxDistanceM[l]; ok {
		return d
	}
	return DefaultMaxDistanceM[l]
}

// BoundarySource answers which named area contains a coordinate.
//
// An interface rather than a concrete type because the first implementation is
// not the last one. Natural Earth is public domain and reaches country and
// region; suburb needs OpenStreetMap's administrative relations, which is a
// different licence regime, a different acquisition story and a much larger
// pipeline. Both answer this one question.
type BoundarySource interface {
	// Covers reports whether this source can answer a level at all. A source
	// that says no leaves the level to the tiles rather than reporting it
	// unknown.
	Covers(Level) bool

	// Contains reports the area holding a coordinate at a level, and which
	// of the three things a source can say about that place it is saying.
	// name, kind and credit are set only when c is Inside.
	//
	// credit is the attribution that answer owes, empty when it owes none.
	// It is returned PER ANSWER rather than per source because one source
	// may hold data under several licences -- a store may hold a file
	// derived from OpenStreetMap beside one that is public domain -- and
	// crediting the wrong one is a claim about somebody else's work.
	Contains(l Level, lat, lon float64) (name, kind, credit string, c Containment)
}

// Containment is what a boundary source can say about one coordinate.
//
// Three states rather than a boolean, because "no area holds this point" is
// two different statements depending on whether the source knows the place.
// Natural Earth covers the world, so outside every country is a fact about
// the point: it is at sea. A derived file covers one extract, so outside
// every area in it is usually a fact about the FILE -- a Sydney file says
// nothing about Horsens -- and treating that as "nothing is there" took the
// tiles' "near Horsens" away and put nothing in its place. Only the source
// can tell the two apart, so it says which.
type Containment uint8

const (
	// NoData: the source holds nothing about this place, and the tiles
	// answer it as though the source were not there. The zero value, so a
	// source that says nothing claims nothing.
	NoData Containment = iota
	// Outside: the source knows this place and no area it holds is the
	// answer at this level. That is an answer, and the tiles are not asked.
	Outside
	// Inside: an area holds the point, and it is the answer.
	Inside
)

type tileRef struct {
	z    uint8
	x, y uint32
}

// readLayer decodes one layer of one tile.
//
// A tile the store does not hold is not an error: most coordinates on earth
// have no tile in any given store, and a store fetched for one city holds
// nothing for the next. The caller sees no match, which is the truth.
func readLayer(src TileSource, ref tileRef, layer string) ([]mvt.Feature, error) {
	raw, ok, err := src.Tile(ref.z, ref.x, ref.y)
	if err != nil {
		return nil, fmt.Errorf("locate: reading tile %d/%d/%d: %w", ref.z, ref.x, ref.y, err)
	}
	if !ok {
		return nil, nil
	}
	tile, err := mvt.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("locate: decoding tile %d/%d/%d: %w", ref.z, ref.x, ref.y, err)
	}
	l, ok := tile.Layer(layer)
	if !ok {
		return nil, nil
	}
	return l.Features, nil
}

// nearest finds the closest named feature of a level to a point.
func nearest(feats []mvt.Feature, spec levelSpec, ref tileRef, at Coord, opts Options) (Match, bool) {
	best := math.Inf(1)
	var bestName, bestKind string

	for i := range feats {
		f := &feats[i]
		if !kindMatches(f, spec.kinds) {
			continue
		}
		name, ok := preferredName(f, opts.Language)
		if !ok {
			continue
		}
		d, ok := distanceTo(f, spec.line, at, spec.zoom, ref)
		if !ok || d >= best {
			continue
		}
		best, bestName = d, name
		bestKind, _ = textTag(f, "kind_detail")
	}

	if bestName == "" || best > opts.maxDistance(spec.level) {
		return Match{}, false
	}
	return Match{
		Level: spec.level, Name: bestName, Kind: bestKind,
		Source: Near, DistanceM: math.Round(best*10) / 10,
	}, true
}

// tilesNear is the tiles that could hold a feature within cap metres of a
// point: the one containing it, and any neighbour whose edge is closer than
// that.
//
// Reading only the containing tile is the obvious implementation and it is
// wrong at the edges. A street two hundred metres away across a tile boundary
// is a street this is meant to find, and at zoom 14 a tile is about two and a
// half kilometres wide, so a good fraction of every tile is within the street
// cap of an edge. Vector tiles carry a buffer of features from their
// neighbours, which hides this most of the time and therefore makes it worse:
// the bug appears only for the features that fall outside the buffer, which is
// exactly the ones nothing else will find.
//
// Bounded to the eight immediate neighbours. A cap wide enough to need more
// than that -- more than a whole tile at the level's own zoom -- is asking
// about something too far away to be an answer, and the levels' default caps
// are chosen against their zooms so this does not bite.
func tilesNear(z uint8, x, y uint32, at Coord, capM float64) []tileRef {
	refs := []tileRef{{z, x, y}}
	if capM <= 0 {
		return refs
	}
	n := float64(uint32(1) << z)

	// The point's position within its own tile, and how much of a tile the cap
	// reaches.
	//
	// Both axes use the SAME ground size, and that is the correction this
	// comment used to get wrong. It said longitude shortens toward the poles
	// and latitude does not, which is true of equirectangular degrees and
	// false of what is being measured here: Web Mercator is conformal, so a
	// tile's ground HEIGHT shrinks with the cosine of the latitude in exact
	// lockstep with its width. Measured against TileBounds at zoom 14, a tile
	// is 2443 m square at the equator and 1370 m square at Horsens -- not
	// 1370 by 2446. Treating the height as constant made the north and south
	// thresholds about half what they should be at Danish latitudes, so a
	// street within the cap of a tile's north or south edge was missed about
	// twice as often as intended -- which is the bug this function exists to
	// prevent, reintroduced on one axis.
	px, py := mercator.Project(at.Lon, at.Lat)
	fx := math.Mod(px*n, 1)
	fy := math.Mod(py*n, 1)

	tileM := earthCircumferenceM * math.Cos(at.Lat*math.Pi/180) / n
	near := 1.0
	if tileM > 0 {
		near = capM / tileM
	}
	nearX, nearY := near, near

	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			if dx == 0 && dy == 0 {
				continue
			}
			if dx < 0 && fx > nearX {
				continue
			}
			if dx > 0 && 1-fx > nearX {
				continue
			}
			if dy < 0 && fy > nearY {
				continue
			}
			if dy > 0 && 1-fy > nearY {
				continue
			}
			// Longitude wraps and latitude does not: a tile east of the last
			// column is the first column, and there is nothing north of the
			// top row.
			nx := (int64(x) + int64(dx) + int64(n)) % int64(n)
			ny := int64(y) + int64(dy)
			if ny < 0 || ny >= int64(n) {
				continue
			}
			refs = append(refs, tileRef{z, uint32(nx), uint32(ny)})
		}
	}
	return refs
}

// matchAt is the answer already recorded for a level, if any.
func (p *Place) matchAt(l Level) (Match, bool) {
	for _, m := range p.Matches {
		if m.Level == l {
			return m, true
		}
	}
	return Match{}, false
}

// setMatch records a level's answer, replacing any already there.
func (p *Place) setMatch(m Match) {
	for i := range p.Matches {
		if p.Matches[i].Level == m.Level {
			p.Matches[i] = m
			return
		}
	}
	p.Matches = append(p.Matches, m)
}
