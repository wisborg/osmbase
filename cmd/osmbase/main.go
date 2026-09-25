// Command osmbase reads a PMTiles vector-tile archive and draws or describes
// what is in it: the archive's shape, one tile's contents, one tile as GeoJSON,
// or a map of the ground around a coordinate as a PNG.
//
// It is the program the library is tried out through, and render is the point
// of it: everything else hands back geometry for a person to look at
// elsewhere, and that one puts the picture on the screen with no consumer
// involved.
//
// This is an application rather than the library, which is why it is allowed
// to name a default archive to fetch from. The library ships no default source
// on purpose: a library that quietly defaults to somebody's bucket puts its
// consumer's traffic on a host the consumer never chose. See
// docs/architecture.md, "The library ships no default source".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// The first Ctrl-C, or a SIGTERM, cancels the context every command runs
	// under, so a download stops, a build stops reading its extract, and the
	// clean-up already written beside each -- a temporary file removed, a
	// fetched extract deleted -- runs. Before this nothing handled a signal:
	// the process died where it stood, and those files were swept by the NEXT
	// run instead.
	//
	// A second one kills at once, as it always did: stop restores the default
	// behaviour as soon as the first has been seen, so a clean-up that hangs
	// can still be got out of.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole program, with its output injected so that a test can drive
// it exactly as a shell does.
//
// Exit codes: 0 for success, 1 for a failure carrying out the command, 2 for
// being asked for something that is not a command. Keeping the last two apart
// is what lets a script tell "you typed it wrong" from "the archive has no
// tile there".
// run is runContext with nothing to interrupt it, which is what a test
// wants.
func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

// runContext runs one command under ctx and turns its outcome into an exit
// status.
func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	var err error
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return 0
	case "inspect":
		err = inspectCommand(ctx, args[1:], stdout, stderr)
	case "tile":
		err = tileCommand(ctx, args[1:], stdout, stderr)
	case "geojson":
		err = geojsonCommand(ctx, args[1:], stdout, stderr)
	case "render":
		err = renderCommand(ctx, args[1:], stdout, stderr)
	case "fetch":
		err = fetchCommand(ctx, args[1:], stdout, stderr)
	case "locate":
		err = runLocate(ctx, args[1:], stdout, stderr)
	case "boundaries":
		err = boundariesCommand(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "osmbase: there is no %q command\n\n", args[0])
		usage(stderr)
		return 2
	}

	switch {
	case err == nil:
		return 0
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		// Interrupted, which is not the command failing. 130 is what a shell
		// reports for a process ended by Ctrl-C, so a script can tell the
		// two apart.
		fmt.Fprintf(stderr, "osmbase: interrupted; stopped, and removed what was only partly written\n")
		return 130
	case errors.Is(err, flag.ErrHelp):
		// The command printed its own help to stdout. Asking for help is not
		// a failure.
		return 0
	case isUsageError(err):
		fmt.Fprintf(stderr, "osmbase: %v\n", err)
		return 2
	default:
		fmt.Fprintf(stderr, "osmbase: %v\n", err)
		return 1
	}
}

// usage lists the subcommands.
//
// It is printed when the program is run with no arguments at all, because the
// first thing a person does with an unfamiliar command is run it bare, and an
// error message with no list of what to type instead is a dead end.
func usage(w io.Writer) {
	fmt.Fprint(w, `osmbase reads a PMTiles vector-tile archive and draws or describes what is
inside it.

usage:
  osmbase <command> [flags] [SOURCE]

commands:
  render      the map around a coordinate, as a PNG
  inspect     the archive's header, sections and root directory
  tile        what one tile holds: layers, feature counts, geometry types, tags
  geojson     one tile as GeoJSON on stdout, to paste into geojson.io
  fetch       copy an area onto this machine, so rendering needs no network
  locate      say where a coordinate is, from data already on this machine
  boundaries  country and state outlines, so locate can say IN rather than NEAR

Run "osmbase <command> -h" for that command's flags and a worked example.

`)
	fmt.Fprint(w, sourceHelp)
}
