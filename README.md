# osmbase

A Go library that draws a map for a geographic rectangle from OpenStreetMap data held on
the local disk, and answers what a place is called from the same data.

No API key. No account. **No third-party request at the moment a map is drawn** — data is
acquired once, by a command you run, and every render after that is local.

It renders geometry only and returns place names as data, so the calling program draws all
text in its own font, at its own size, with its own theme. It knows nothing about whatever
that program is for.

## Status

Early. The archive reader, the vector tile decoder, the rasterizer that fills, strokes
and dashes paths, and the synthetic fixtures they are tested against are written, and a
command-line tool reads real archives with them. Nothing joins the two halves into a map
yet.

**[docs/architecture.md](docs/architecture.md) is the design this is being built to.** It
carries the reasoning behind every decision, including the alternatives that were rejected
and why, and the list of things it assumes but has not yet verified.

## Trying it out

The rasterizer draws what it is given, but nothing yet turns tiles into paths for it, so
there is no map to look at. There is a command that reads real tiles and says what is in
them, and writes one tile as GeoJSON you can paste straight into
[geojson.io](https://geojson.io):

```
go build -o osmbase ./cmd/osmbase

./osmbase                                              # the subcommands
./osmbase inspect                                      # an archive's header and shape
./osmbase tile --lat -33.8568 --lon 151.2153 --tags    # what one tile holds
./osmbase geojson --lat -33.8568 --lon 151.2153 --layer roads > roads.geojson
```

With no SOURCE those read a Protomaps planet archive over HTTP range requests — a few
hundred kilobytes out of 125 GiB, and nothing stored — which tells that host which few
kilometres of map you asked about. Give a local `.pmtiles` file as SOURCE instead and
nothing leaves the machine. `osmbase help` says the same thing in the terminal.

## Licence

Apache-2.0. See [LICENSE](LICENSE).

Map data rendered through this library is © OpenStreetMap contributors, under the Open
Database License. That obligation attaches to the **images you produce**, not only to this
program, and it travels with them. See [NOTICE](NOTICE).
