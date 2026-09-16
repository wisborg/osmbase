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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole program, with its output injected so that a test can drive
// it exactly as a shell does.
//
// Exit codes: 0 for success, 1 for a failure carrying out the command, 2 for
// being asked for something that is not a command. Keeping the last two apart
// is what lets a script tell "you typed it wrong" from "the archive has no
// tile there".
func run(args []string, stdout, stderr io.Writer) int {
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
		err = inspectCommand(args[1:], stdout, stderr)
	case "tile":
		err = tileCommand(args[1:], stdout, stderr)
	case "geojson":
		err = geojsonCommand(args[1:], stdout, stderr)
	case "render":
		err = renderCommand(args[1:], stdout, stderr)
	case "fetch":
		err = fetchCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "osmbase: there is no %q command\n\n", args[0])
		usage(stderr)
		return 2
	}

	switch {
	case err == nil:
		return 0
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
  render    the map around a coordinate, as a PNG
  inspect   the archive's header, sections and root directory
  tile      what one tile holds: layers, feature counts, geometry types, tags
  geojson   one tile as GeoJSON on stdout, to paste into geojson.io
  fetch     copy an area onto this machine, so rendering needs no network

Run "osmbase <command> -h" for that command's flags and a worked example.

`)
	fmt.Fprint(w, sourceHelp)
}
