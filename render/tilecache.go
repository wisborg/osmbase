package render

import (
	"container/list"
	"sync"

	"github.com/wisborg/osmbase/mvt"
)

// TileCache keeps the vector tiles renders from one source have decoded, and
// which tiles the source does not hold, so that renders of neighbouring views
// do not each read and decode the same tiles again.
//
// A view reads every tile under it and, where the source lacks one, walks
// up to the shallower tile standing in for it. A flyover draws its pictures
// from thousands of small fixed views, and each decoded, alone, the tiles it
// shared with its neighbours and the parent standing in for all of them:
// decoding was most of what reading the map cost, and most of the memory a
// view allocated. With a cache, a tile is read and decoded once while it is
// in use.
//
// One cache is for one source: a tile is named by its zoom and position
// alone, so a cache shared by two sources would give one's tiles to the
// other. It is safe for concurrent use, and a tile two renders want at once
// is decoded by the first and waited for by the second. The decoded tiles
// are shared, and nothing drawing from one changes it.
type TileCache struct {
	limit int64 // bytes, estimated
	used  int64

	mu    sync.Mutex
	tiles map[tileRef]*list.Element
	lru   *list.List // of *cachedTile, most recently used first
}

type cachedTile struct {
	ref  tileRef
	done chan struct{}
	tile *mvt.Tile
	ok   bool // the source holds the tile
	err  error
	size int64 // estimated, counted against the limit
}

// decodedPerEncoded is roughly how many bytes a tile takes decoded for each
// of its bytes encoded, gzip's included: the estimate a TileCache budgets
// by, since a decoded tile's own size is not cheap to know.
const decodedPerEncoded = 8

// tileOverhead is what a tile takes in the cache before its contents: what
// keeps a cache of tiles of open sea, each a few hundred bytes, from
// growing without bound under a byte budget.
const tileOverhead = 4 << 10

// NewTileCache keeps about budget bytes of decoded tiles, dropping the least
// recently used beyond that; 0 keeps 512 MiB. Budgeted in bytes rather than
// tiles because tiles differ by a thousand times: a city at street detail
// decodes to megabytes, a stretch of open sea to a few hundred bytes.
func NewTileCache(budget int64) *TileCache {
	if budget <= 0 {
		budget = 512 << 20
	}
	return &TileCache{limit: budget, tiles: map[tileRef]*list.Element{}, lru: list.New()}
}

// get is tile ref of src, decoded, and whether src holds it: from the cache,
// or read and decoded now and kept.
func (c *TileCache) get(src TileSource, ref tileRef) (*mvt.Tile, bool, error) {
	c.mu.Lock()
	if el, hit := c.tiles[ref]; hit {
		c.lru.MoveToFront(el)
		e := el.Value.(*cachedTile)
		c.mu.Unlock()
		<-e.done
		return e.tile, e.ok, e.err
	}
	e := &cachedTile{ref: ref, done: make(chan struct{})}
	c.tiles[ref] = c.lru.PushFront(e)
	c.mu.Unlock()

	var encoded int
	e.tile, e.ok, encoded, e.err = readTile(src, ref)
	c.mu.Lock()
	if e.err != nil {
		// Not kept: a failed read is tried again next time.
		if el, ok := c.tiles[ref]; ok && el.Value == e {
			c.lru.Remove(el)
			delete(c.tiles, ref)
		}
	} else if el, ok := c.tiles[ref]; ok && el.Value == e {
		e.size = tileOverhead + int64(encoded)*decodedPerEncoded
		c.used += e.size
		// The oldest go, never the one just read: a tile bigger than the
		// whole budget is still used once.
		for c.used > c.limit && c.lru.Back() != el {
			old := c.lru.Back()
			c.lru.Remove(old)
			o := old.Value.(*cachedTile)
			delete(c.tiles, o.ref)
			c.used -= o.size
		}
	}
	c.mu.Unlock()
	close(e.done)
	return e.tile, e.ok, e.err
}
