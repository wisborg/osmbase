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

`osmbase fetch ... --terrain` also copies the shape of the ground for the same area --
elevation -- from [Mapterhorn](https://mapterhorn.com/), a second host that learns the same
cells, and `osmbase render ... --terrain` then shades the hills under the map from it and
draws contour lines, every fifth one labelled with its height (`--contours=false` leaves the
lines out and keeps the shading). Like
the map, terrain a view lacks is offered before it is fetched -- the question names the host
first, and `--yes` answers it in advance -- and once it is held, rendering contacts nobody. It is kept in a store of its own beside the map's
(`osmbase-terrain` in your user cache directory), so nothing that draws from the map's store
has to choose between them. `--terrain-source DIR` reads a directory of Mapterhorn's archives
already on disk instead, and asks nobody. The choice of source is in
[docs/elevation.md](docs/elevation.md).

`osmbase render3d --lat ... --lon ... --heading DEG --pitch DEG` draws the same map draped
over the shape of the ground and seen from a camera in the sky -- the still image a flyover
will be made of. It reads the map and terrain stores only.

A shaded map owes its elevation sources a credit as well as OpenStreetMap, and which ones
depends on where it is: Mapterhorn, the national survey whose data covers the view
(Geoscience Australia around Sydney, Klimadatastyrelsen in Denmark, under CC BY 4.0), and
Copernicus GLO-30 nearly everywhere, whose licence dictates its own sentence. The image
carries a short credit, as Mapterhorn's own map does -- "Elevation: © Mapterhorn,
mapterhorn.com/attribution", where every source is listed -- and `render` prints the full
notice for the sources in that view, worked out from Mapterhorn's coverage data. **If you
publish a shaded map, give that notice with it**: in the caption, the description, the
credits. The licences ask for it to be given to whoever sees the data, and a picture
cannot carry a sentence that long in its corner.

## Licence

Apache-2.0. See [LICENSE](LICENSE).

Map data rendered through this library is © OpenStreetMap contributors, under the Open
Database License. That obligation attaches to the **images you produce**, not only to this
program, and it travels with them. See [NOTICE](NOTICE).

Hillshading is drawn from elevation data under its own licences, listed in [NOTICE](NOTICE),
and credited in the image in the same way. Where it uses Copernicus GLO-30, its licence asks
every notice covering distribution to say: *The organisations in charge of the Copernicus
programme by law or by delegation do not incur any liability for any use of the Copernicus
WorldDEM-30.* Nothing here implies any endorsement by Copernicus, ESA or Airbus.
