// Package slice is the on-disk store: cells, manifests, coverage and
// eviction.
//
// It is the piece that makes this library's central claim true. Data is
// acquired once, deliberately, by a command somebody typed; every render after
// that reads it from here. Nothing in this package opens a socket, imports the
// package that does, or has any way to ask for a tile it has not got.
//
// # A miss never becomes a fetch
//
// Tile returns ok=false for a tile the store does not hold, and that is the
// end of it. Working offline at render time is the whole product claim, and a
// render that quietly reached the network because a cell had been evicted
// would be exactly the data exfiltration this library exists to remove,
// happening at the moment the user least expected it. The renderer's answer to
// a miss is to walk up the pyramid to a shallower tile and, failing that, to
// hatch the gap and report it. See docs/architecture.md, "A cache miss at
// render time".
//
// That is structural rather than a rule anyone has to remember: this package
// imports neither acquire nor render, and there is nothing in its vocabulary
// to fetch with. imports_test.go checks it.
//
// # Two units, and conflating them is how this goes wrong
//
// The STORAGE unit is one source tile, keyed on the source and the tile
// coordinates and nothing else -- not the view, not the pixel size, not the
// style. Re-rendering one route at 1080p and at 4K reads the same files, and
// changing the theme reads the same files.
//
// The FETCH and EVICTION unit is a cell: one tile at the store's cell zoom
// plus its whole sub-pyramid, plus the shallower ancestors, which are stored
// once per source and shared between cells. A cell is one directory, so
// evicting one is one removal and resuming a half-finished fetch is reading
// which files are already there.
//
// # How a store gets filled
//
// The store never opens an archive. A caller opens one -- a PMTiles reader
// over a local file, or the same reader over HTTP range requests in the
// acquisition step -- and hands it to Source.Fill, which asks it for raw tiles
// and writes them down. *pmtiles.Reader satisfies the Archive interface as it
// stands, with no adapter:
//
//	r, err := pmtiles.Open("planet.pmtiles")
//	st, err := slice.Create(root, slice.Config{})
//	src, err := st.AddSource(slice.SourceDesc{
//		Source:          "planet.pmtiles",
//		Build:           "20260912",
//		Attribution:     attribution,
//		TileType:        r.Header().TileType.String(),
//		TileCompression: slice.Compression(r.Header().TileCompression.String()),
//		SourceZoom:      slice.ZoomRange{Min: r.Header().MinZoom, Max: r.Header().MaxZoom},
//	})
//	cells, err := st.CellsFor(area)
//	for _, c := range cells {
//		_, err := src.Fill(ctx, r, c, slice.ZoomRange{Min: 12, Max: 15})
//	}
//
// and afterwards src is a render.TileSource, which is the only thing the
// renderer wants.
package slice

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultBudget is the disk a store is expected to keep to, and it is a
// suggestion to Evict rather than anything this package enforces on its own.
//
// Nothing is ever evicted except by a call to Evict, and that call is the
// caller's to place: at the end of an acquisition and at the start of a
// render, never during one.
const DefaultBudget int64 = 1 << 30

// Config is what a new store is created with. Everything in it is written to
// store.json once and read from there forever after.
type Config struct {
	// CellZoom is the zoom cells are keyed at. Zero means DefaultCellZoom.
	//
	// It is ignored when the store already exists: the store on disk is the
	// authority for how it is keyed, because a store read at a cell zoom other
	// than the one it was written at finds nothing and fetches a second copy
	// beside the first.
	CellZoom uint8
}

// Store is one store root, holding one or more sources.
//
// The root is application-neutral by design -- see DefaultRoot -- so two
// programs on one machine share one slice: download a city once and both of
// them render from it.
//
// A Store is safe for concurrent use. It holds no open files between calls;
// every method that reads a tile opens and closes one, which is what lets a
// second process read the same store while this one is rendering.
type Store struct {
	root     string
	cellZoom uint8

	mu      sync.Mutex
	sources map[string]*Source
	// held counts the in-process holds on each cell. See Source.Hold.
	held map[heldKey]int
}

type heldKey struct {
	source string
	cell   Cell
}

// DefaultRoot is where a store lives when the application names no other
// place: <UserCacheDir>/osmbase.
//
// It is application-neutral on purpose. A slice under an application's own
// cache directory would be downloaded once per application, and the sharing is
// the strongest single argument that this is a library rather than a package
// inside one of them.
func DefaultRoot() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("slice: finding the user cache directory: %w", err)
	}
	return filepath.Join(dir, "osmbase"), nil
}

// Open opens an existing store and reads nothing else.
//
// It does not create one. A render opening a store that is not there wants to
// hear so -- the honest answer is that there is no basemap, and the consumer
// decides what that means for its layout -- rather than to find an empty
// directory it will now never be able to fill, because filling is not
// something a render can do. The error satisfies errors.Is(err,
// fs.ErrNotExist) so that case can be told from a damaged store.
func Open(root string) (*Store, error) {
	var sf storeFile
	path := filepath.Join(root, storeFileName)
	if err := readJSON(path, &sf); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("slice: there is no store at %s: %w", root, err)
		}
		return nil, err
	}
	return newStore(root, sf)
}

// Create opens the store at root, making it if it is not there.
//
// An existing store is returned as it is, with the cell zoom it was written
// with; cfg is only consulted for a store this call brings into being. A
// caller that cares which it got asks CellZoom.
func Create(root string, cfg Config) (*Store, error) {
	st, err := Open(root)
	if err == nil {
		return st, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	zoom := cfg.CellZoom
	if zoom == 0 {
		zoom = DefaultCellZoom
	}
	if err := checkCellZoom(zoom); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("slice: making the store at %s: %w", root, err)
	}
	sf := storeFile{Format: FormatVersion, CellZoom: zoom}
	if err := writeJSON(filepath.Join(root, storeFileName), sf); err != nil {
		return nil, err
	}
	return newStore(root, sf)
}

func newStore(root string, sf storeFile) (*Store, error) {
	if sf.Format != FormatVersion {
		return nil, fmt.Errorf("slice: the store at %s is layout version %d and this is version %d; it was written by another build and the remedy is to delete it and fetch again", root, sf.Format, FormatVersion)
	}
	if sf.CellZoom == 0 || sf.CellZoom > MaxCellZoom {
		return nil, fmt.Errorf("slice: the store at %s records cell zoom %d, which is not a zoom cells can be keyed at", root, sf.CellZoom)
	}
	return &Store{
		root:     root,
		cellZoom: sf.CellZoom,
		sources:  make(map[string]*Source),
		held:     make(map[heldKey]int),
	}, nil
}

// Root is the directory the store lives in.
func (s *Store) Root() string { return s.root }

// CellZoom is the zoom this store's cells are keyed at, as recorded in
// store.json when it was created. It is the authority, not DefaultCellZoom.
func (s *Store) CellZoom() uint8 { return s.cellZoom }

// CellsFor returns every cell of this store that the rectangle touches,
// rounded outward, in row-major order.
func (s *Store) CellsFor(b Bounds) ([]Cell, error) { return cellsFor(b, s.cellZoom) }

// Sources lists the manifests of every source in the store, ordered by ID.
//
// A directory with no readable manifest is skipped rather than reported as an
// error: a fetch interrupted before it wrote one leaves exactly that, and a
// store that could not be listed because of it would be a store no command
// could report on.
func (s *Store) Sources() ([]Manifest, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("slice: listing the store at %s: %w", s.root, err)
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var m Manifest
		if err := readJSON(filepath.Join(s.root, e.Name(), manifestFileName), &m); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Source opens one source of the store by ID.
//
// The same *Source is returned for the same ID for the life of the Store, so
// that two callers cannot hold two manifests for one directory and write them
// over each other, and so that a hold taken by one is visible to an eviction
// asked for by the other.
func (s *Store) Source(id string) (*Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if src, ok := s.sources[id]; ok {
		return src, nil
	}
	var m Manifest
	if err := readJSON(filepath.Join(s.root, id, manifestFileName), &m); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("slice: the store at %s holds no source %s: %w", s.root, id, err)
		}
		return nil, err
	}
	return s.attach(m)
}

// AddSource records a source in the store, or returns the one already there.
//
// Adding a source that exists does not overwrite it; it widens it. The
// attribution, the schema and the source's own zoom range are refreshed from
// the description -- they are facts about the archive and the newer reading is
// the better one -- and the slice's held zoom range is only ever widened, by
// Fill, as tiles land.
//
// A compression this store cannot decode is refused HERE rather than at the
// first read, so that a caller finds out before downloading a cell it would
// never be able to draw.
func (s *Store) AddSource(d SourceDesc) (*Source, error) {
	if d.Source == "" {
		return nil, fmt.Errorf("slice: a source needs a name -- the URL or path its tiles came from -- so that a store can say what it holds")
	}
	if d.TileCompression == "" {
		return nil, fmt.Errorf("slice: source %q does not say how its tiles are compressed; tiles are stored exactly as fetched, so the encoding has to be recorded with them", d.Source)
	}
	if !d.TileCompression.supported() {
		return nil, fmt.Errorf("slice: source %q stores tiles as %q, which this store cannot decode; re-encode the archive with gzip or with no compression", d.Source, string(d.TileCompression))
	}
	if err := d.SourceZoom.validate(); err != nil {
		return nil, fmt.Errorf("slice: source %q: %w", d.Source, err)
	}

	id := SourceID(d.Source, d.Build)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	m := Manifest{ID: id, Created: now}
	existing := true
	if err := readJSON(filepath.Join(s.root, id, manifestFileName), &m); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		existing = false
		m = Manifest{ID: id, Created: now, Zoom: emptyZoom}
	}
	if existing && m.TileCompression != d.TileCompression {
		return nil, fmt.Errorf("slice: the store already holds source %s with %q tiles and this one says %q; tiles are stored as fetched, so one source cannot hold both", id, string(m.TileCompression), string(d.TileCompression))
	}
	m.Source, m.Build, m.Schema = d.Source, d.Build, d.Schema
	m.Attribution = d.Attribution
	m.TileType, m.TileCompression = d.TileType, d.TileCompression
	m.SourceZoom = d.SourceZoom
	m.Updated = now

	if err := writeJSON(filepath.Join(s.root, id, manifestFileName), m); err != nil {
		return nil, err
	}
	return s.attach(m)
}

// attach builds and caches the Source for a manifest. The caller holds s.mu.
func (s *Store) attach(m Manifest) (*Source, error) {
	if src, ok := s.sources[m.ID]; ok {
		src.mu.Lock()
		src.manifest = m
		src.mu.Unlock()
		return src, nil
	}
	src := &Source{store: s, id: m.ID, compression: m.TileCompression, manifest: m}
	s.sources[m.ID] = src
	return src, nil
}

// Bytes is how much disk the whole store occupies, measured by walking it.
//
// It is measured rather than accumulated. A running total kept in a manifest
// is a number that has to stay in step with the filesystem through every
// interrupted fetch, every eviction and every user who deleted a directory to
// free space, and the first time it does not, a store silently believes it is
// over or under budget. A store is thousands of files, not millions, and the
// walk costs a few milliseconds.
func (s *Store) Bytes() (int64, error) {
	total, err := dirBytes(s.root)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// dirBytes sums the sizes of every regular file under dir. A directory that is
// not there is zero bytes rather than an error: an empty store, a source with
// no cells yet and an evicted cell are all ordinary.
func dirBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("slice: measuring %s: %w", dir, err)
	}
	return total, nil
}
