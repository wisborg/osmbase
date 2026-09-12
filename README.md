# osmbase

A Go library that draws a map for a geographic rectangle from OpenStreetMap data held on
the local disk, and answers what a place is called from the same data.

No API key. No account. **No third-party request at the moment a map is drawn** — data is
acquired once, by a command you run, and every render after that is local.

It renders geometry only and returns place names as data, so the calling program draws all
text in its own font, at its own size, with its own theme. It knows nothing about whatever
that program is for.

## Status

Design only. Nothing is implemented yet.

**[docs/architecture.md](docs/architecture.md) is the design this is being built to.** It
carries the reasoning behind every decision, including the alternatives that were rejected
and why, and the list of things it assumes but has not yet verified.

## Licence

Apache-2.0. See [LICENSE](LICENSE).

Map data rendered through this library is © OpenStreetMap contributors, under the Open
Database License. That obligation attaches to the **images you produce**, not only to this
program, and it travels with them. See [NOTICE](NOTICE).
