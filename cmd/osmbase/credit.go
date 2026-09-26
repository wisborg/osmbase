package main

import (
	"image"

	"github.com/wisborg/osmbase/render"
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
// The drawing is render.DrawCredit's, shared with every program that writes a
// map; what is here is the face.
//
// The face is the one the map labels use -- see textFace. It used to be
// basicfont, which has no copyright sign, so this drew "(c) OpenStreetMap"
// through a transliteration step. With a real font the credit is the string
// the data actually asks for.
func drawCredit(img *image.RGBA, credit string) {
	render.DrawCredit(img, credit, labelFace())
}
