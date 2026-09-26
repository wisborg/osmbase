package fetch

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

// The pieces of offering to fetch what a store lacks, which every program
// drawing from a store needs: osmbase's own render, fitdash's basemap, and
// course's map. Each used to have its own copy, and the copies had learned
// different lessons -- ask before planning, ask only when something can
// answer, measure at the zoom that will be drawn, stop asking past the
// archive's deepest zoom. What is here is those lessons once. What is not
// here is the wording: a program says what it is about to fetch and why in
// its own voice, and this package, as the rest of it, prints nothing a
// caller did not write.

// Answer is how a question about fetching came out.
type Answer int

const (
	// Declined is an explicit no, or an answer that is not a yes.
	Declined Answer = iota
	// Accepted is a yes, typed or given in advance.
	Accepted
	// Unattended is nobody there to answer: stdin is not a terminal, so
	// nothing was read.
	Unattended
	// NoAnswer is stdin ending before an answer arrived.
	NoAnswer
)

// Consent is how a program asks before fetching.
//
// The rule it holds, learned in fitdash and repeated in osmbase before it
// lived here: a fetch tells a third party where somebody is looking, so it is
// never done without a yes -- typed, or given in advance with Yes -- and the
// question is only asked when something can answer it. A program started from
// a script inherits a pipe that may never close, and a read on it blocks for
// ever: a render with no output and no error, the hardest failure there is to
// look at. Nobody there is Unattended, and the caller carries on without.
type Consent struct {
	// Yes answers every question in advance.
	Yes bool
	// In is where an answer is read from; nil is os.Stdin.
	In io.Reader
	// Answerable reports whether In can be answered by a person; nil is
	// StdinIsTerminal, and a test sets it to answer through a pipe.
	Answerable func() bool
}

// Ask writes question to w and reads the answer, unless Yes answered it
// already or nobody can answer.
//
// question is the caller's own words, written as given; a prompt such as
// "Fetch it now? [y/N] " is part of it. Only "y" and "yes", in any case, are a
// yes.
func (c Consent) Ask(w io.Writer, question string) Answer {
	if c.Yes {
		return Accepted
	}
	answerable := c.Answerable
	if answerable == nil {
		answerable = StdinIsTerminal
	}
	if !answerable() {
		return Unattended
	}
	in := c.In
	if in == nil {
		in = os.Stdin
	}
	fmt.Fprint(w, question)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return NoAnswer
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return Accepted
	}
	return Declined
}

// StdinIsTerminal reports whether os.Stdin is a character device -- which a
// terminal is, and a pipe is not.
//
// /dev/null is one too without being a terminal. That is harmless: a read
// from it ends at once, which is NoAnswer, and nothing waits.
func StdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// DrawnZoom is the zoom a store is asked about for a view drawn at zoom: no
// deeper than the archive the store was filled from goes.
//
// Past the archive's deepest zoom, overzoom is the answer and there is
// nothing to fetch; asked at the view's own zoom instead, the store could
// never hold it, and every render would offer the same download for ever.
// A source with no recorded range is asked at the view's zoom.
func DrawnZoom(src *slice.Source, zoom uint8) uint8 {
	if src == nil {
		return zoom
	}
	if sz := src.Manifest().SourceZoom; !sz.Empty() && zoom > sz.Max {
		return sz.Max
	}
	return zoom
}

// Fill fetches an area into the store at root from a: the sequence "osmbase
// fetch" runs, for a program that has already asked and been told yes.
//
// The store is created if it does not exist, the archive registered under
// the credit given, and the depth capped at the archive's deepest zoom --
// asking the planner for more is refused as a zoom the archive does not
// have. The plan is returned before the fetch is made through report, so a
// caller can say what is about to cross the network while it does; progress
// is called as each group lands. Either may be nil. A plan with nothing in it
// fetches nothing and is not an error.
func Fill(ctx context.Context, root string, a *Archive, credit string, req acquire.Request,
	report func(*acquire.Plan), progress func(acquire.Progress)) (acquire.Result, error) {
	st, err := slice.Create(root, slice.Config{})
	if err != nil {
		return acquire.Result{}, fmt.Errorf("fetch: opening the store at %s: %w", root, err)
	}
	src, err := a.AddTo(st, credit)
	if err != nil {
		return acquire.Result{}, err
	}
	if req.MaxZoom != acquire.AutoZoom {
		req.MaxZoom = min(req.MaxZoom, int(a.Reader().Header().MaxZoom))
	}
	req.CellZoom = st.CellZoom()
	plan, err := a.Plan(ctx, src, req)
	if err != nil {
		return acquire.Result{}, err
	}
	if report != nil {
		report(plan)
	}
	if plan.Empty() {
		return acquire.Result{}, nil
	}
	if progress == nil {
		progress = func(acquire.Progress) {}
	}
	return a.Fetch(ctx, plan, src, progress)
}
