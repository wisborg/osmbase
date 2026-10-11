package render

import (
	"image"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// politicalTracking is the space added after each letter of a country's or
// a state's name, in the face's height: the open lettering an atlas sets
// them in.
const politicalTracking = 0.18

// trackedFace is a face that sets its letters further apart: every glyph
// advances extra more than its own width. Measuring and drawing both ask
// the face for advances, so a name in it is placed, collided and drawn at
// the width it really takes, wherever it is measured.
type trackedFace struct {
	font.Face
	extra fixed.Int26_6
}

// tracked is face with its letters set apart by tracking times its height.
func tracked(face font.Face, tracking float64) font.Face {
	if face == nil || tracking <= 0 {
		return face
	}
	extra := fixed.Int26_6(float64(face.Metrics().Height) * tracking)
	return trackedFace{Face: face, extra: extra}
}

func (f trackedFace) GlyphAdvance(r rune) (fixed.Int26_6, bool) {
	a, ok := f.Face.GlyphAdvance(r)
	return a + f.extra, ok
}

func (f trackedFace) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	dr, mask, mp, a, ok := f.Face.Glyph(dot, r)
	return dr, mask, mp, a + f.extra, ok
}
