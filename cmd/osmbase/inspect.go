package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/wisborg/osmbase/pmtiles"
)

func inspectUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase inspect [SOURCE]

Print an archive's header and shape: version, tile type, how the directories
and the tiles are compressed, the zoom range it covers, where each section
lives, and a summary of the root directory. Reading it costs two range
requests however large the archive is.

This is the output to paste into a bug report about an archive.

examples:
  osmbase inspect ./sydney.pmtiles
  osmbase inspect                     # the default archive, over the network
  osmbase inspect https://build.protomaps.com/20260912.pmtiles

`)
	printFlags(w, fs)
	fmt.Fprint(w, "\n"+sourceHelp)
}

func inspectCommand(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("inspect", inspectUsage)
	source, err := parseArgs(fs, args, stdout)
	if err != nil {
		return err
	}

	a, err := openArchive(source, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	defer a.reportTraffic(stderr)

	writeArchiveReport(stdout, a)
	return nil
}

// writeArchiveReport prints everything the header and the root directory say.
//
// It is one aligned two-column table rather than sections, because its purpose
// is to be copied whole into a bug report: a fixed shape is greppable, and
// every field is present even when it is zero. A field the format defines as
// "0 means unknown" is labelled as such rather than printed as a count, since
// a reader cannot otherwise tell an empty archive from an archive whose writer
// declined to count.
func writeArchiveReport(w io.Writer, a *archive) {
	h := a.Header()
	var t table
	row := func(name, format string, args ...any) {
		t.row(name, fmt.Sprintf(format, args...))
	}

	row("source", "%s", a.name)
	if a.hasSize {
		row("archive size", "%s", bytesExact(a.size))
	} else {
		row("archive size", "not reported by the host")
	}
	row("format version", "%d", h.Version)
	row("tile type", "%s", h.TileType)
	row("internal compression", "%s  (root directory, leaf directories, metadata)", h.InternalCompression)
	row("tile compression", "%s", h.TileCompression)
	row("clustered", "%t", h.Clustered)
	row("zoom levels", "%d to %d", h.MinZoom, h.MaxZoom)
	row("bounds", "west %s  south %s  east %s  north %s",
		formatCoord(h.MinLon), formatCoord(h.MinLat), formatCoord(h.MaxLon), formatCoord(h.MaxLat))
	row("centre", "lon %s  lat %s  zoom %d", formatCoord(h.CenterLon), formatCoord(h.CenterLat), h.CenterZoom)
	row("addressed tiles", "%s", countOrUnknown(h.AddressedTiles))
	row("tile entries", "%s", countOrUnknown(h.TileEntries))
	row("tile contents", "%s", countOrUnknown(h.TileContents))

	t.blank()
	row("root directory", "%s at offset %d", bytesExact(int64(h.RootLength)), h.RootOffset)
	row("metadata", "%s at offset %d", bytesExact(int64(h.MetadataLength)), h.MetadataOffset)
	row("leaf directories", "%s at offset %d", bytesExact(int64(h.LeafLength)), h.LeafOffset)
	row("tile data", "%s at offset %d", bytesExact(int64(h.TileDataLength)), h.TileDataOffset)

	t.blank()
	entries := a.RootEntries()
	leaves, runs, tiles := summariseEntries(entries)
	row("root entries", "%d  (%d leaf pointers, %d tile runs covering %d tiles)", len(entries), leaves, runs, tiles)
	if len(entries) > 0 {
		first, last := entries[0], entries[len(entries)-1]
		row("first root entry", "%s", describeEntry(first))
		row("last root entry", "%s", describeEntry(last))
	}
	t.write(w)
}

// summariseEntries counts what a directory is made of. A leaf pointer has run
// length zero -- that is how the format distinguishes the two -- and a tile
// run stands for as many tiles as its run length.
func summariseEntries(entries []pmtiles.Entry) (leaves, runs int, tiles uint64) {
	for _, e := range entries {
		if e.IsLeaf() {
			leaves++
			continue
		}
		runs++
		tiles += uint64(e.RunLength)
	}
	return leaves, runs, tiles
}

// describeEntry renders one directory entry, with the tile coordinates its ID
// stands for.
//
// The coordinates are what make the entry readable: an ID of 19078479 says
// nothing, and 12/3423/1763 says which corner of the world the archive's
// directory starts at.
func describeEntry(e pmtiles.Entry) string {
	where := fmt.Sprintf("tile ID %d", e.TileID)
	if z, x, y, err := pmtiles.IDToZxy(e.TileID); err == nil {
		where = fmt.Sprintf("tile ID %d (%d/%d/%d)", e.TileID, z, x, y)
	}
	if e.IsLeaf() {
		return fmt.Sprintf("%s, a leaf directory of %s at offset %d", where, humanBytes(int64(e.Length)), e.Offset)
	}
	return fmt.Sprintf("%s, a run of %d tiles, %s at offset %d", where, e.RunLength, humanBytes(int64(e.Length)), e.Offset)
}

// countOrUnknown prints one of the header's three counts.
//
// The spec defines 0 as "the writer did not record this", not as "none", so
// printing a bare 0 would report an empty archive for one that simply declined
// to count. See pmtiles.Header.
func countOrUnknown(n uint64) string {
	if n == 0 {
		return "not recorded by the writer"
	}
	return fmt.Sprintf("%d", n)
}
