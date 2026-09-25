package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/slice"
)

// stdinAnswerable reports whether something can answer a question on stdin.
//
// Only a terminal can be relied on to. A render started from a script or a job
// runner inherits a pipe that may stay open for the life of the parent, and a
// read on it blocks for ever: a render that produces no output and no error,
// which is the hardest failure there is to look at. fitdash found that out by
// running it; this does not need to find it again.
//
// A character device is the test, and /dev/null passes it without being a
// terminal. That is harmless: a read from it ends at once, which is a no, and
// nothing waits. The hazard is a pipe, and a pipe fails it.
//
// A variable so a test can say yes and then answer through a pipe.
var stdinAnswerable = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// shortfall is what a store lacks for one view.
type shortfall struct {
	root   string
	source string // the archive the store was filled from; empty for the default
	bounds slice.Bounds
	zoom   uint8
	held   int
	wanted int
	empty  bool // no store, or a store with no map in it
}

// offerToFill asks whether to fetch what a view lacks, and fetches it if the
// answer is yes. It reports whether anything was fetched.
//
// # Why it asks
//
// Because of what a fetch is. Contacting a map service tells a third party
// which part of the map somebody is looking at, and a render from a store
// exists precisely so that drawing contacts nobody. A render that downloaded
// on its own would be that rule broken by convenience. So the network keeps
// its consent, and what goes away is only having to know which command to
// type. --yes answers in advance.
//
// # Why it asks before anything is read
//
// Planning a fetch reads the archive's directories, which already tells the
// host the area. Asking afterwards would be asking permission for something
// done. So the shortfall is measured from the disk alone, the question names
// the host before a socket is opened, and the exact cost is shown by the plan
// once it is made -- one question, not two.
//
// A no, a pipe, or a failed download is not an error. The render carries on
// with what the store holds -- overzoomed or hatched, and its own report says
// which -- because somebody who declined a download still asked for a map.
func offerToFill(w io.Writer, s shortfall, yes bool) bool {
	host := defaultSource
	if s.source != "" {
		host = s.source
	}
	if s.empty {
		fmt.Fprintf(w, "osmbase: %s holds no map yet, so this render would be all hatching.\n", s.root)
	} else {
		fmt.Fprintf(w, "osmbase: this view needs %d tiles at zoom %d and %s holds %d of them;\n", s.wanted, s.zoom, s.root, s.held)
		fmt.Fprintf(w, "osmbase:   the rest would be drawn from shallower tiles, with less detail, or hatched.\n")
	}
	fmt.Fprintf(w, "osmbase: fetching it contacts %s, which learns which part\n", hostOf(host))
	fmt.Fprintf(w, "osmbase:   of the map you asked about. Afterwards, rendering it contacts nobody.\n")

	later := fmt.Sprintf("osmbase fetch --store %s --bbox %s --max-zoom %d", s.root, bboxString(s.bounds), s.zoom)
	if !yes {
		if !stdinAnswerable() {
			fmt.Fprintf(w, "osmbase: nothing is attached to answer, so nothing was fetched; pass --yes, or run\n")
			fmt.Fprintf(w, "osmbase:   %s\n", later)
			return false
		}
		fmt.Fprintf(w, "Fetch it now? [y/N] ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintf(w, "\nosmbase: no answer, so nothing was fetched\n")
			return false
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintf(w, "osmbase: not fetched; when you want it:\nosmbase:   %s\n", later)
			return false
		}
	}

	if err := fillFor(w, s); err != nil {
		// Reported, not returned: a download that failed is a reason to draw
		// a worse map, not a reason to draw none.
		fmt.Fprintf(w, "osmbase: %v\nosmbase: carrying on with what the store holds\n", err)
		return false
	}
	return true
}

// fillFor carries out the fetch offerToFill got consent for: the same
// sequence "osmbase fetch" runs, with the area and depth taken from the view
// rather than from flags, and the question already asked.
func fillFor(w io.Writer, s shortfall) error {
	a, err := openArchive(s.source, w)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.requireVectorTiles(); err != nil {
		return err
	}
	st, err := slice.Create(s.root, slice.Config{})
	if err != nil {
		return fmt.Errorf("opening the store at %s: %w", s.root, err)
	}
	src, err := a.AddTo(st, attributionOf(a, w))
	if err != nil {
		return err
	}
	// No deeper than the archive goes: a render at zoom 17 overzooms from
	// 15, and asking the planner for 17 is refused as a zoom it cannot have.
	zoom := min(s.zoom, a.Reader().Header().MaxZoom)
	plan, err := a.Plan(context.Background(), src, acquire.Request{
		Bounds: s.bounds, MaxZoom: int(zoom), CellZoom: st.CellZoom(),
	})
	if err != nil {
		return err
	}
	writePlan(w, plan, s.root)
	if plan.Empty() {
		fmt.Fprintln(w, "osmbase: nothing to fetch after all: the archive holds no more of this view than the store does")
		return nil
	}
	a.Silence()
	res, err := a.Fetch(context.Background(), plan, src, progressTo(w))
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%-12s %d tiles in %d requests, %s\n", "fetched", res.Written, res.Requests, humanBytes(res.Transfer))
	return nil
}

func bboxString(b slice.Bounds) string {
	return fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", b.West, b.South, b.East, b.North)
}
