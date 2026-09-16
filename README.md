# osmbase

A Go library that draws a map for a geographic rectangle from OpenStreetMap data held on
the local disk, and answers what a place is called from the same data.

No API key. No account. **No third-party request at the moment a map is drawn** — data is
acquired once, by a command you run, and every render after that is local.

It renders geometry only and returns place names as data, so the calling program draws all
text in its own font, at its own size, with its own theme. It knows nothing about whatever
that program is for.

## Status

Early, and it draws a map offline. The archive reader, the vector tile decoder, the
projection, the rasterizer that fills, strokes and dashes paths, the renderer that joins
them, the palettes and the contrast check that keeps a route readable over them, and the
on-disk store are all written, along with the synthetic fixtures they are tested against.
A command-line tool reads real archives and writes a PNG.

End to end today: fill a store from a remote archive once -- Sydney Harbour is two cells
and 2.5 MB -- and every render after that reads the disk and nothing else.

Still to come: the fetch planner that coalesces byte ranges and the command that drives
it; place names; and the fitdash adapter.

**[docs/architecture.md](docs/architecture.md) is the design this is being built to.** It
carries the reasoning behind every decision, including the alternatives that were rejected
and why, and the list of things it assumes but has not yet verified.

## Trying it out

```
make build      # or: go build -o osmbase ./cmd/osmbase

./osmbase                                              # the subcommands
./osmbase render --lat -33.8568 --lon 151.2153 --out map.png
./osmbase inspect                                      # an archive's header and shape
./osmbase tile --lat -33.8568 --lon 151.2153 --tags    # what one tile holds
./osmbase geojson --lat -33.8568 --lon 151.2153 --layer roads > roads.geojson
```

`render` writes the PNG and then says what is in it: how much of the image tiles were
found for, how much was drawn from a shallower tile than asked for, and how much is
hatched because there was nothing at all. Overzoomed vector data is sharp, so it looks
complete; the report is the only way to tell.

With no SOURCE those read a Protomaps planet archive over HTTP range requests — a few
hundred kilobytes out of 125 GiB, and nothing stored — which tells that host which few
kilometres of map you asked about. Give a local `.pmtiles` file as SOURCE instead and
nothing leaves the machine. `osmbase help` says the same thing in the terminal.

## Licence

Apache-2.0. See [LICENSE](LICENSE).

Map data rendered through this library is © OpenStreetMap contributors, under the Open
Database License. That obligation attaches to the **images you produce**, not only to this
program, and it travels with them. See [NOTICE](NOTICE).
