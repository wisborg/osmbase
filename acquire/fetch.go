package acquire

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/wisborg/osmbase/pmtiles"
	"github.com/wisborg/osmbase/slice"
)

// Progress is one group's worth of a fetch, reported as it finishes.
//
// It is a callback rather than a writer because where progress goes is the
// application's business: a command puts it on stderr, a library consumer may
// want none of it. Nothing in a fetch depends on it being set.
type Progress struct {
	// Group is 1-based and Groups the total, so a caller can say "3 of 12".
	Group, Groups int
	// Label names the group: "overview", or "cell 3423_1763".
	Label string

	// Requests and Transfer are this group's, Written the tiles it stored.
	Requests int
	Transfer int64
	Written  int

	// DoneTransfer is what the fetch has moved so far and PlanTransfer what
	// the plan said it would move in total. The second is exact, which is what
	// makes a percentage here honest rather than the usual guess.
	DoneTransfer, PlanTransfer int64

	Elapsed time.Duration

	// GroupDone is true on the call made as the group finishes and is
	// stored -- once a group, with Written its own -- and false on those
	// made as each of its ranges arrives, which carry DoneTransfer forward
	// and nothing else.
	GroupDone bool
}

// FetchOptions are the knobs a fetch has beyond the plan.
type FetchOptions struct {
	// Progress, when set, is called as ranges arrive and again as each group
	// finishes (GroupDone). It is called from the goroutine running the
	// fetch, never from two at once, and the groups finish in order, each
	// after a call for every one of its ranges. With several requests in
	// flight, a call made while a group is awaited may be for a range of a
	// group after it: DoneTransfer is all that has arrived, and only climbs.
	//
	// Called as each group finished, as it first was, a fetch made of one
	// group -- a flight's corridor, all of it above the cell zoom -- showed
	// nothing moved until all of it had: nine hundred megabytes at 0 bytes.
	Progress func(Progress)

	// Requests is the most range requests the fetch has in flight at once;
	// zero is DefaultRequests, and one reads a range at a time.
	Requests int
}

// DefaultRequests is how many range requests a fetch keeps in flight: what
// the PMTiles tool's own extract does by default (go-pmtiles,
// --download-threads 4), and what both hosts this project fetches from by
// default, Mapterhorn and Source Cooperative, point their users to for
// taking out an area. Neither states a limit of its own.
//
// One at a time, a fetch over open water was nothing but round trips: a
// cell of sea is a few hundred bytes, fetched one cell after the next, and
// the transfer crawled at under a hundred bytes a second for minutes before
// reaching land and running at megabytes a second. Over land, where ranges
// are large, the requests overlap their transfer too.
const DefaultRequests = 4

// requests is the number of range requests o keeps in flight.
func (o FetchOptions) requests() int {
	if o.Requests <= 0 {
		return DefaultRequests
	}
	return o.Requests
}

// Result is what a fetch actually did.
//
// Written, Held and Absent come from the store rather than from the plan: they
// are what Fill reported, so a disagreement between what was planned and what
// landed is visible instead of assumed away.
type Result struct {
	Written, Held, Absent int

	// Bytes is what was written to disk, Transfer what came over the wire, and
	// Requests how many range requests that took. Transfer is the number the
	// plan promised, and the two being equal is the claim a confirmation
	// prompt rests on.
	Bytes    int64
	Transfer int64
	Requests int

	// Cells is the cells that were completed, in the order they were filled.
	Cells []slice.Cell

	Elapsed time.Duration
}

// Fetch carries out a plan: it reads the coalesced ranges and fills the store.
//
// # Why the store does the writing
//
// Every tile goes into the store through slice.Source.Fill and
// slice.Source.FillOverview rather than being written here. That keeps one set
// of rules about what a cell on disk means -- a tile written under a temporary
// name and renamed, cell.json written LAST so a killed fetch leaves a
// directory the store reads back as incomplete, the manifest's zoom range
// widened as tiles land -- in the package that owns those rules. A fetch that
// wrote files itself would be a second implementation of them, and the first
// time the two disagreed the symptom would be a cell that claims to be
// finished with a hole in it, invisible until somebody rendered that square of
// the world.
//
// So what this does is put the bytes where the store can reach them: it fetches
// one group's ranges into memory and hands Fill an Archive backed by that
// buffer. Fill then asks for the tiles it wants, skipping the ones already on
// disk, exactly as it would from a real archive.
//
// # Memory
//
// The groups are fetched ahead of the store at most opt.Requests at a time,
// so the buffers are bounded by that many cells -- 8.9 MB for the densest
// measured cell -- rather than by the whole fetch.
//
// # Concurrency
//
// The range requests run several at a time (see DefaultRequests), across
// the groups ahead and within each, but the store is filled from this
// goroutine alone, one group after the next in the plan's order: the rules
// in the paragraph above are the store's, and they were written for one
// writer. A fetch stopped part way leaves what it left before -- cells
// stored whole, in order, and the rest not begun.
func Fetch(ctx context.Context, p *Plan, a Archive, dst *slice.Source, opt FetchOptions) (Result, error) {
	if p == nil {
		return Result{}, fmt.Errorf("acquire: there is no plan to fetch")
	}
	if err := a.validate(); err != nil {
		return Result{}, err
	}
	if dst == nil {
		return Result{}, fmt.Errorf("acquire: fetching %s: there is no store to fill", a.Name)
	}

	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// Cancelled before it is waited for: a fetch that fails leaves no
	// request to be started after it returns.
	defer func() {
		cancel()
		wg.Wait()
	}()

	n := opt.requests()
	slots := make(chan struct{}, n) // one per request in flight
	// Each range's size as it arrives, room for every range of the plan, so
	// a request never waits on the progress to finish.
	ranges := 0
	for _, g := range p.Groups {
		ranges += len(g.Ranges)
	}
	arrivals := make(chan int64, max(ranges, 1))
	var done int64 // what the progress has been told has arrived
	type fetched struct {
		buf *bufferArchive
		err error
	}
	results := make([]chan fetched, len(p.Groups))
	began := make([]time.Time, len(p.Groups))
	launch := func(i int) {
		results[i], began[i] = make(chan fetched, 1), time.Now()
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf, err := fetchRanges(ctx, a, p.Groups[i], slots, func(b int64) { arrivals <- b })
			results[i] <- fetched{buf, err}
		}()
	}
	next := 0
	for ; next < min(n, len(p.Groups)); next++ {
		launch(next)
	}

	var res Result
	for i, g := range p.Groups {
		arrive := func(b int64) {
			done += b
			if opt.Progress != nil {
				opt.Progress(Progress{
					Group: i + 1, Groups: len(p.Groups), Label: g.Label(),
					DoneTransfer: done, PlanTransfer: p.Transfer,
					Elapsed: time.Since(began[i]),
				})
			}
		}
		var got fetched
	wait:
		for {
			select {
			case b := <-arrivals:
				arrive(b)
			case got = <-results[i]:
				// Every range of the group was sent before the group was,
				// so what is waiting now includes all of its own: told
				// before the group is done, the progress counts them.
				for {
					select {
					case b := <-arrivals:
						arrive(b)
					default:
						break wait
					}
				}
			}
		}
		if got.err != nil {
			return res, got.err
		}
		if next < len(p.Groups) {
			launch(next)
			next++
		}
		buf := got.buf
		res.Transfer += g.Transfer
		res.Requests += len(g.Ranges)

		var f slice.Fill
		var err error
		if g.Overview {
			f, err = dst.FillOverview(ctx, buf, g.Refs)
		} else {
			z := p.Zoom
			if g.Depth != 0 {
				z.Max = g.Depth
			}
			f, err = dst.Fill(ctx, buf, g.Cell, z)
		}
		if err != nil {
			return res, fmt.Errorf("acquire: storing %s of %s: %w", g.Label(), a.Name, err)
		}
		res.Written += f.Written
		res.Held += f.Skipped
		res.Absent += f.Absent
		res.Bytes += f.Bytes
		if !g.Overview {
			res.Cells = append(res.Cells, g.Cell)
		}

		if opt.Progress != nil {
			opt.Progress(Progress{
				Group: i + 1, Groups: len(p.Groups), Label: g.Label(),
				Requests: len(g.Ranges), Transfer: g.Transfer, Written: f.Written,
				DoneTransfer: done, PlanTransfer: p.Transfer,
				Elapsed: time.Since(began[i]), GroupDone: true,
			})
		}
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// fetchRanges reads one group's coalesced ranges and returns an archive over
// them, telling arrived the size of each range as it is read. Each range is
// read in a goroutine of its own once it has one of slots, the fetch's
// requests in flight; arrived is called from those goroutines.
func fetchRanges(ctx context.Context, a Archive, g Group, slots chan struct{}, arrived func(int64)) (*bufferArchive, error) {
	buf := &bufferArchive{
		archive: a.Name,
		label:   g.Label(),
		present: make(map[slice.TileRef][]byte, len(g.Tiles)),
		absent:  make(map[slice.TileRef]bool),
	}
	for _, ref := range g.Refs {
		buf.wanted++
		_ = ref
	}
	data := make([][]byte, len(g.Ranges))
	errs := make([]error, len(g.Ranges))
	var wg sync.WaitGroup
	for i, r := range g.Ranges {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			errs[i] = fmt.Errorf("acquire: fetching %s of %s: %w", g.Label(), a.Name, ctx.Err())
		}
		if errs[i] != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if err := ctx.Err(); err != nil {
				errs[i] = fmt.Errorf("acquire: fetching %s of %s: %w", g.Label(), a.Name, err)
				return
			}
			b := make([]byte, r.Length)
			// ReadAt's contract is exactly what is wanted here: it reads all of p
			// or says why it could not. A short read at the end of an archive is
			// io.EOF, and for a range taken from the archive's own directories
			// that means the archive is shorter than its index claims -- which is
			// a damaged or truncated file, not an ordinary end, so it is an error
			// rather than a partial buffer.
			if _, err := a.Bytes.ReadAt(b, r.Offset); err != nil {
				if err == io.EOF {
					errs[i] = fmt.Errorf("acquire: %s ends before byte %d, which its own directories say holds tiles of %s; the archive is truncated or it changed while it was being read", a.Name, r.End(), g.Label())
				} else {
					errs[i] = fmt.Errorf("acquire: reading bytes %d to %d of %s for %s: %w", r.Offset, r.End()-1, a.Name, g.Label(), err)
				}
				return
			}
			data[i] = b
			if arrived != nil {
				arrived(r.Length)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	for _, t := range g.Tiles {
		b, ok := sliceOut(g.Ranges, data, t.At)
		if !ok {
			// The ranges are built from these very locations, so this cannot
			// happen without coalesce having dropped one. Reported rather than
			// panicked, and reported as the store-filling failure it would
			// otherwise become: a tile the buffer cannot serve is recorded by
			// Fill as absent, and a cell finished with an absent tile in it is
			// marked complete with a hole.
			return nil, fmt.Errorf("acquire: %s of %s planned tile %s at bytes %d to %d and no fetched range holds it", g.Label(), a.Name, t.Ref, t.At.Offset, t.At.End()-1)
		}
		buf.present[t.Ref] = b
	}
	for _, ref := range g.Refs {
		if _, ok := buf.present[ref]; !ok {
			buf.absent[ref] = true
		}
	}
	return buf, nil
}

// sliceOut finds the fetched range holding a location and returns the tile's
// bytes inside it.
func sliceOut(ranges []Range, data [][]byte, at pmtiles.Location) ([]byte, bool) {
	for i, r := range ranges {
		if !r.contains(at) {
			continue
		}
		start := at.Offset - r.Offset
		return data[i][start : start+at.Length], true
	}
	return nil, false
}

// bufferArchive serves one group's tiles out of the bytes just fetched, so the
// store can fill itself from them exactly as it would from a real archive.
//
// It satisfies slice.Archive, which is one method: have you got the tile at
// z/x/y, and what are its stored bytes. That interface is declared by the
// store rather than exported by an archive reader, which is what makes this
// possible at all -- a store that knew about PMTiles could not be filled from
// a byte buffer.
type bufferArchive struct {
	archive string
	label   string
	wanted  int
	present map[slice.TileRef][]byte
	absent  map[slice.TileRef]bool
}

// RawTile returns a tile's stored bytes, still compressed, as the archive held
// them.
//
// A tile the plan did not cover is an ERROR rather than an absence, and the
// distinction is the whole reason this type exists instead of a plain map. The
// plan skipped the tiles the store already had; if one of those has since been
// removed -- another process evicting, a user clearing a directory -- Fill
// would ask for it here. Answering "not in the archive" would have Fill record
// it as absent and then write a cell.json saying the cell is complete, leaving
// a hole nobody sees until that square of the world is drawn. Saying so out
// loud costs a failed fetch that a re-run fixes.
func (b *bufferArchive) RawTile(z uint8, x, y uint32) ([]byte, bool, error) {
	ref := slice.TileRef{Z: z, X: x, Y: y}
	if data, ok := b.present[ref]; ok {
		return data, true, nil
	}
	if b.absent[ref] {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("acquire: the store asked for tile %s of %s, which the plan for %s did not cover; the store changed while the fetch was running, so run it again",
		ref, b.label, b.archive)
}
