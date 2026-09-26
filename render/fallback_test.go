package render

import (
	"image"
	"slices"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/mvt"
)

// only is a face that has a glyph for its own letters and nothing else, and
// says which letter it was asked to draw.
type only struct {
	letters string
	advance fixed.Int26_6
	drew    *[]rune
}

func (o only) has(r rune) bool { return slices.Contains([]rune(o.letters), r) }
func (o only) Close() error    { return nil }
func (o only) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	*o.drew = append(*o.drew, r)
	return image.Rectangle{}, nil, image.Point{}, o.advance, true
}
func (o only) GlyphBounds(r rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	return fixed.Rectangle26_6{}, o.advance, o.has(r)
}
func (o only) GlyphAdvance(r rune) (fixed.Int26_6, bool) { return o.advance, o.has(r) }
func (o only) Kern(r0, r1 rune) fixed.Int26_6            { return 1 }
func (o only) Metrics() font.Metrics                     { return font.Metrics{Height: o.advance} }

// Each letter comes from the first face that has it; a letter none has is
// drawn in the first, and remembered.
func TestFallbackTakesEachLetterFromTheFirstFaceWithIt(t *testing.T) {
	var fromLatin, fromThai []rune
	latin := only{letters: "abc", advance: 10, drew: &fromLatin}
	thai := only{letters: "กขa", advance: 20, drew: &fromThai}
	f := NewFallback(nil, latin, thai)

	for _, r := range "aกz" {
		f.Glyph(fixed.Point26_6{}, r)
	}
	if string(fromLatin) != "az" || string(fromThai) != "ก" {
		t.Errorf("latin drew %q and thai %q; want a and the placeholder for z from latin, ก from thai", string(fromLatin), string(fromThai))
	}
	if adv, ok := f.GlyphAdvance('ข'); !ok || adv != 20 {
		t.Errorf("advance of ข is %v %v, want the thai face's", adv, ok)
	}
	if got := f.Missing(); len(got) != 1 || got[0] != 'z' {
		t.Errorf("missing %q, want z", string(got))
	}
	if f.Kern('a', 'b') != 1 || f.Kern('a', 'ก') != 0 {
		t.Error("kerning across two faces is not zero, or within one face not the face's")
	}
	if f.Metrics().Height != 10 {
		t.Error("metrics are not the first face's")
	}
	if NewFallback(nil, nil) != nil {
		t.Error("a fallback of no faces is not nil")
	}
}

// A real face in the chain: basicfont has ASCII and nothing else, so a Thai
// letter is missing with it alone and found with a face that has it.
func TestFallbackWithARealFace(t *testing.T) {
	var drew []rune
	f := NewFallback(basicfont.Face7x13, only{letters: "ก", advance: 5, drew: &drew})
	if _, ok := f.GlyphAdvance('A'); !ok {
		t.Error("basicfont's own letter is not found")
	}
	if adv, ok := f.GlyphAdvance('ก'); !ok || adv != 5 {
		t.Error("the Thai letter did not come from the second face")
	}
	if len(f.Missing()) != 0 {
		t.Errorf("missing %q", string(f.Missing()))
	}
}

// A label is written in the language asked for where the data has it, and in
// the place's own name where it does not.
func TestLabelTextPrefersTheLanguage(t *testing.T) {
	f := &mvt.Feature{Tags: map[string]mvt.Value{
		"name": mvt.StringValue("กรุงเทพ"), "name:en": mvt.StringValue("Bangkok"), "name:da": mvt.StringValue(""),
	}}
	for lang, want := range map[string]string{"": "กรุงเทพ", "en": "Bangkok", "da": "กรุงเทพ", "fr": "กรุงเทพ"} {
		if got, ok := labelText(f, "name", lang); !ok || got != want {
			t.Errorf("language %q: %q, want %q", lang, got, want)
		}
	}
}
