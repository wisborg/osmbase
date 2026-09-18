package main

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/wisborg/osmbase/render"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// drawCredit puts the attribution into the picture.
//
// It has to be in the PIXELS rather than only in the report, and that is the
// whole reason this file exists. A PNG is a Produced Work under the ODbL, the
// obligation attaches to the image, and the image is the thing that gets sent
// to somebody else -- a line printed in a terminal does not travel with it.
// Until this existed, "osmbase render" wrote a file that owed a credit it did
// not carry.
//
// The library deliberately draws no text: it renders geometry and hands place
// names back as data, so a consumer can label in its own font at its own size.
// That is right for a consumer and leaves this command with nothing to write
// with, which is why the drawing is here rather than in render/. fitdash, the
// other consumer, has its own text stack and draws the same string itself.
//
// The face is the one the map labels use -- see textFace. It used to be
// basicfont, which has no copyright sign, so this drew "(c) OpenStreetMap"
// through a transliteration step. With a real font the credit is the string
// the data actually asks for.
func drawCredit(img *image.RGBA, credit string) {
	credit = render.PlainCredit(credit)
	if img == nil || credit == "" {
		return
	}
	face := labelFace()
	if face == nil {
		return
	}
	b := img.Bounds()

	const pad = 4
	w := font.MeasureString(face, credit).Ceil()
	h := face.Metrics().Height.Ceil()

	// Bottom right, which is where every map service asks for it and where a
	// reader looks for it. Clamped to the image so a credit wider than a
	// narrow picture is truncated at the left rather than drawn off the edge:
	// a partly visible credit is a bug to fix, an invisible one is a licence
	// breach nobody notices.
	x1, y1 := b.Max.X-pad, b.Max.Y-pad
	x0, y0 := x1-w-pad, y1-h-pad
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}

	// A plate behind it, then the text. The map underneath is arbitrary --
	// this program has no idea whether the pixels there are near-black water
	// or near-white paper -- so the credit gets guaranteed contrast rather
	// than a colour that usually works. The plate is near-opaque white and the
	// text near-black, which reads on any imagery because it is not the
	// imagery.
	plate := image.Rect(x0, y0, x1+pad, y1+pad).Intersect(b)
	// color.NRGBA, not color.RGBA. image/color's RGBA is ALPHA-PREMULTIPLIED,
	// so white at 87% alpha is {0xff,0xff,0xff,0xdd} -- a colour whose
	// channels exceed its own alpha, which is not a valid premultiplied value
	// and composites as something near black. The plate came out dark on the
	// first render for exactly that reason, which is the same trap that made
	// this project's contrast check disagree with its consumer's.
	draw.Draw(img, plate, &image.Uniform{C: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xdd}},
		image.Point{}, draw.Over)

	d := font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}),
		Face: face,
		Dot: fixed.Point26_6{
			X: fixed.I(x0 + pad),
			Y: fixed.I(y0 + pad + face.Metrics().Ascent.Ceil()),
		},
	}
	d.DrawString(credit)
}
