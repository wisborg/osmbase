package main

import (
	"image"
	"image/color"
	"image/draw"
	"strings"

	"github.com/wisborg/osmbase/render"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
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
// basicfont.Face7x13 is the font because it is already inside
// golang.org/x/image, this module's one admitted dependency. An opentype face
// would look better and would mean either embedding a font file or reaching
// for a second module, and neither is worth it for one line of small print.
func drawCredit(img *image.RGBA, credit string) {
	credit = render.PlainCredit(credit)
	if img == nil || credit == "" {
		return
	}
	face := basicfont.Face7x13
	credit = drawable(face, credit)
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

// drawable replaces any rune the face cannot draw.
//
// basicfont.Face7x13 covers little more than ASCII, and an attribution string
// is full of things that are not: the copyright sign above all, which is how
// every map service writes its credit. A face asked for a rune it lacks draws
// its missing-glyph box, so "(c) OpenStreetMap" came out as a filled square
// followed by the name -- legible enough to miss in a thumbnail and wrong in a
// document that exists to satisfy a licence.
//
// The substitutions are transliterations rather than deletions, because the
// point of the string is to name who is owed credit. "(c)" is the accepted
// ASCII form of the copyright sign and carries the same meaning; a dropped
// character would not. Anything else unrenderable becomes a question mark,
// which is visibly wrong rather than silently absent -- a credit that looks
// damaged gets fixed, and one that quietly lost a word does not.
func drawable(face font.Face, s string) string {
	var b strings.Builder
	for _, r := range s {
		if _, _, _, _, ok := face.Glyph(fixed.Point26_6{}, r); ok {
			b.WriteRune(r)
			continue
		}
		switch r {
		case '\u00a9':
			b.WriteString("(c)")
		case '\u00ae':
			b.WriteString("(r)")
		case '\u2019', '\u2018':
			b.WriteByte('\'')
		case '\u201c', '\u201d':
			b.WriteByte('"')
		case '\u2013', '\u2014':
			b.WriteByte('-')
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// labelFace is the face this command draws map labels in.
//
// basicfont, the same 7x13 bitmap the credit uses, because the library takes
// a face rather than shipping one and this command has no other. It is a real
// limitation and it is confined to this preview tool: the face has no glyphs
// beyond its own small table, so a place name in Danish, Greek or Japanese
// comes out with boxes in it. A consumer that cares -- one rendering a video
// somebody will watch -- passes a scalable face of its own, which is the
// arrangement the option exists for.
//
// Parsing a TTF here instead would mean x/image/font/opentype, which imports
// x/image/font/sfnt, which imports golang.org/x/text: a second module in a
// library whose having exactly one is the property the dependency gate exists
// to hold.
func labelFace() font.Face { return basicfont.Face7x13 }
