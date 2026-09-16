package main

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// TestDrawCredit_PutsTheObligationInThePixels is the point of the whole file.
//
// A PNG is a Produced Work under the ODbL and the obligation attaches to the
// image, which is the thing that gets sent to somebody else. Until this
// existed the command printed the credit to the terminal and wrote a file that
// carried none, so every picture this program produced owed a credit it did
// not have.
func TestDrawCredit_PutsTheObligationInThePixels(t *testing.T) {
	blank := func() *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 400, 120))
		for i := range img.Pix {
			img.Pix[i] = 0x40 // a flat mid grey, so any plate or text differs
		}
		return img
	}

	before := blank()
	after := blank()
	drawCredit(after, "(c) OpenStreetMap")

	changed := 0
	for i := range after.Pix {
		if after.Pix[i] != before.Pix[i] {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("drawing a credit changed no pixels; the file would carry none")
	}

	// It must be in the bottom right, which is where every service asks for it
	// and where a reader looks. The top left must be untouched.
	if after.RGBAAt(4, 4) != before.RGBAAt(4, 4) {
		t.Error("the credit reached the top left corner of the image")
	}
	if after.RGBAAt(380, 110) == before.RGBAAt(380, 110) {
		t.Error("the bottom right corner is unchanged; the credit is not where it belongs")
	}
}

// TestDrawCredit_PlateIsLightNotBlack pins a bug that reached a rendered
// image.
//
// image.RGBA is ALPHA-PREMULTIPLIED, so a near-opaque white written as
// color.RGBA{0xff,0xff,0xff,0xdd} has channels exceeding its own alpha. That
// is not a valid premultiplied colour and composites as something near black:
// the first version of this drew a black plate with black text on it, which is
// an invisible credit rather than a legible one.
func TestDrawCredit_PlateIsLightNotBlack(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 120))
	for i := range img.Pix {
		img.Pix[i] = 0x40
	}
	drawCredit(img, "(c) OpenStreetMap")

	// A pixel inside the plate but away from the glyphs.
	c := img.RGBAAt(398, 118)
	if c.R < 0x80 || c.G < 0x80 || c.B < 0x80 {
		t.Errorf("the plate is %v, darker than the grey it was drawn over; "+
			"a premultiplied colour was probably built as a straight one", c)
	}
}

// TestDrawCredit_TransliteratesRunesTheFontCannotDraw covers the copyright
// sign, which is how every map service writes its credit and which the bitmap
// font does not have.
//
// A face asked for a rune it lacks draws its missing-glyph box, so the credit
// rendered as a filled square followed by the name. The substitution is a
// transliteration rather than a deletion because the string exists to name who
// is owed credit, and "(c)" carries that where a dropped character would not.
func TestDrawCredit_TransliteratesRunesTheFontCannotDraw(t *testing.T) {
	face := basicfont.Face7x13
	got := drawable(face, "© OpenStreetMap contributors")
	if strings.ContainsRune(got, '©') {
		t.Errorf("the copyright sign survived into %q, and the font cannot draw it", got)
	}
	if !strings.HasPrefix(got, "(c)") {
		t.Errorf("got %q, want the copyright sign transliterated to (c)", got)
	}
	if !strings.Contains(got, "OpenStreetMap contributors") {
		t.Errorf("got %q, want the name of who is owed credit intact", got)
	}
	// Every rune that survives must actually be drawable, or the box comes back.
	for _, r := range got {
		if _, _, _, _, ok := face.Glyph(fixedZero(), r); !ok {
			t.Errorf("%q survived transliteration and the font cannot draw it", r)
		}
	}
}

// TestDrawCredit_DoesNothingWithoutACredit keeps an archive that names nobody
// from getting a blank plate.
func TestDrawCredit_DoesNothingWithoutACredit(t *testing.T) {
	for _, s := range []string{"", "   ", "\n\t "} {
		img := image.NewRGBA(image.Rect(0, 0, 60, 30))
		drawCredit(img, s)
		for _, p := range img.Pix {
			if p != 0 {
				t.Fatalf("an empty credit (%q) drew something", s)
			}
		}
	}
	// And a nil image must not panic: a render that failed has no picture.
	drawCredit(nil, "(c) OpenStreetMap")
	_ = color.RGBA{}
}

func fixedZero() fixed.Point26_6 { return fixed.Point26_6{} }
