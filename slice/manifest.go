package slice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FormatVersion is the on-disk layout this package writes and reads.
//
// It is in store.json so that a future change of layout -- a different
// directory shape, a different cell file -- is refused with a message rather
// than half-read. A cache is the one place where "just delete it and refetch"
// is an acceptable remedy, and saying so requires knowing that the thing on
// disk is from another version.
const FormatVersion = 1

// The permissions the store is written with.
//
// Narrower than a cache directory usually gets, deliberately. A slice is a
// record of which few-kilometre squares of the world somebody was interested
// in, which is the same information the third-party tile services this library
// exists to replace would have been told. It belongs to the user who fetched
// it and to nobody else on the machine.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

const (
	storeFileName    = "store.json"
	manifestFileName = "manifest.json"
	cellFileName     = "cell.json"
)

// storeFile is the root manifest: what every source under this root agrees on.
type storeFile struct {
	Format int `json:"format"`
	// CellZoom is the authority for how this store is keyed, for its whole
	// life. Nothing in this package defaults it after creation: a store
	// written at cell zoom 13 is read at cell zoom 13 even by a build whose
	// DefaultCellZoom has moved on, because the alternative is a lookup that
	// finds nothing and a fetch that writes a second copy beside the first.
	CellZoom uint8 `json:"cell_zoom"`
}

// Manifest describes one source's slice: where the data came from, what it is,
// and how much of the zoom range it holds.
//
// The attribution is DATA and not a constant. A rendered map is a Produced
// Work under the ODbL and the credit has to appear wherever the image is
// shown; writing it here, from whatever source the bytes actually came from,
// is what stops a change of source leaving the old credit spelled into the
// program. See docs/architecture.md, "Attribution".
type Manifest struct {
	// ID is the directory name under the store root: a hash of Source and
	// Build. A new planet build is a new source rather than an update of the
	// old one, because a tile fetched from each would be two different
	// pictures of the same ground.
	ID string `json:"id"`

	// Source is the archive URL or path the slice was filled from, and Build
	// identifies which build of it. Both are free text from the caller; they
	// are recorded so a user can be told what they are looking at and so a
	// staleness report has something to say.
	Source string `json:"source"`
	Build  string `json:"build,omitempty"`

	// Schema names the tile schema, because it is somebody else's and has
	// changed shape before. A style written against one version renders a
	// blank map against another rather than failing, and a blank map with a
	// schema string beside it is diagnosable.
	Schema string `json:"schema,omitempty"`

	// Attribution is the credit the source requires.
	Attribution string `json:"attribution,omitempty"`

	// TileType is what the tile blobs are ("mvt"), and TileCompression how
	// they are encoded on disk. Tiles are stored exactly as fetched, so the
	// compression is the source's, and this is what says how to undo it.
	TileType        string      `json:"tile_type,omitempty"`
	TileCompression Compression `json:"tile_compression"`

	// SourceZoom is the zoom range the SOURCE archive offers. It is not the
	// same fact as Zoom and the difference is the one trap T4 is about: public
	// builds stop at zoom 15, so a tighter view overzooms sharp geometry and
	// there is nothing deeper to fetch. A caller that wants to say so needs
	// the source's own ceiling, not the slice's.
	SourceZoom ZoomRange `json:"source_zoom"`

	// Zoom is the range this slice actually holds, widened as fetches land. It
	// is recorded rather than compiled in because a slice is a zoom range over
	// an area and both ends are the caller's: 12 to 15 over a suburb, 0 to 5
	// over the planet.
	Zoom ZoomRange `json:"zoom"`

	// Created is when the source directory was first written and Updated when
	// a fetch last added to it. Neither is an expiry. There must not be one --
	// this is offline data the user deliberately acquired, and expiring it
	// breaks a render on a plane, which is the case it exists for. Staleness
	// is reported, never enforced. See docs/architecture.md, trap T3.
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// SourceDesc is what a caller says about a source when adding it to a store.
//
// It is the caller's job and not this package's: the store never opens an
// archive, so everything here -- the URL, the build, the compression, the
// credit -- comes from whoever did.
type SourceDesc struct {
	Source string
	Build  string
	Schema string

	Attribution string

	TileType        string
	TileCompression Compression

	// SourceZoom is the archive's own zoom range, from its header.
	SourceZoom ZoomRange
}

// SourceID is the directory name a source gets under the store root.
//
// It hashes the URL and the build together because those two are what make the
// bytes what they are, and it hashes rather than sanitises because a source is
// a URL: slashes, dots, query strings and occasionally a credential, none of
// which belongs in a path. Sixteen hex characters is eight bytes of SHA-256,
// which is short enough to read in a directory listing and long enough that
// two sources on one machine will not collide.
//
// It is exported so that the acquisition step can name a source's directory
// before it has opened anything.
func SourceID(source, build string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + build))
	return hex.EncodeToString(sum[:8])
}

// CellInfo is what cell.json records: the account of one fetch.
//
// The FILE's existence is the more important half. It is written LAST, after
// every tile of the cell is on disk, so a fetch killed part way through leaves
// a directory of real tiles and no cell.json -- which is read back as
// incomplete rather than served as a complete cell with holes in it.
type CellInfo struct {
	Cell Cell `json:"-"`

	// Zoom is the range this cell was fetched over, which need not be the
	// whole slice's: a cell taken at 12 to 13 is complete at 12 to 13, and
	// saying so is the only way a later fetch can tell there is more to get.
	Zoom ZoomRange `json:"zoom"`

	// Tiles and Bytes are what the fetch wrote, as a record of that fetch.
	// They are not a live measurement of the directory -- Store.Bytes walks
	// the files for that -- because a count that has to be kept in step with
	// the disk is a count that will one day disagree with it.
	Tiles int   `json:"tiles"`
	Bytes int64 `json:"bytes"`

	Fetched time.Time `json:"fetched"`

	// LastUsed is the eviction clock, and it moves once per RENDER rather than
	// once per tile. A render reads eighty-five files from a cell and that is
	// one use of it, not eighty-five.
	LastUsed time.Time `json:"last_used"`

	// Complete is true in every cell.json this package writes. It is a field
	// rather than an implication so that a file damaged into existence -- an
	// empty file, a truncated one -- does not read back as a finished fetch.
	Complete bool `json:"complete"`
}

// readJSON reads and decodes a JSON file, distinguishing "not there" from
// "unreadable" by returning the os error unwrapped enough for errors.Is.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("slice: reading %s: %w", path, err)
	}
	return nil
}

// writeJSON writes a JSON file atomically.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("slice: encoding %s: %w", path, err)
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// writeFileAtomic writes a file under a temporary name in the same directory
// and renames it into place.
//
// The rename is the whole mechanism behind "a killed fetch leaves a cell that
// is recognised as incomplete". A fetch is interrupted by impatience as often
// as by anything else, and a half-written tile under its final name is a file
// that exists, passes every existence check the store makes, and decodes into
// a protobuf error at render time. The rename is atomic within a directory, so
// a file is either absent or whole.
//
// Same directory rather than the system temporary one, because a rename across
// filesystems is not a rename and would silently become a copy that can be
// interrupted in the middle.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("slice: making %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".partial")
	if err != nil {
		return fmt.Errorf("slice: creating a temporary file beside %s: %w", path, err)
	}
	tmp := f.Name()
	if err := f.Chmod(filePerm); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("slice: setting permissions on %s: %w", tmp, err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("slice: writing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("slice: closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("slice: renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}
