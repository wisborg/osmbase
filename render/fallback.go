package render

import (
	"image"
	"slices"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// Fallback is a face drawn from several: each letter from the first face
// that has it.
//
// A map names places in whatever script they are written in, and no one font
// has them all -- the Go font has Latin, Greek and Cyrillic, and a flight over
// Thailand and India drew its names as rows of boxes. A caller passes its own
// face first and fonts with other scripts after it, and each name is drawn in
// the first that can write each letter. This package still parses no fonts:
// the faces are the caller's, built however it likes.
//
// A letter no face has is drawn in the first face, which writes its
// placeholder, and remembered; Missing says which, so a caller can tell its
// user that the map has names it could not write and what font would help.
//
// Letters are drawn one by one, as every face in this package draws them.
// Scripts whose letters join or reorder -- Arabic, Devanagari -- need a text
// shaper for their proper forms, which this is not; they come out in real
// letters, not always in the right shapes. A Language that has the names in
// another script is the better answer there.
type Fallback struct {
	faces []font.Face

	mu      sync.Mutex
	missing map[rune]bool
}

// NewFallback is a face drawing from faces in order. A nil face is skipped,
// so a caller can pass a font it may have failed to load. With none left it
// returns nil, which draws no labels -- the same as a nil face given to New.
func NewFallback(faces ...font.Face) *Fallback {
	var fs []font.Face
	for _, f := range faces {
		if f != nil {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return nil
	}
	return &Fallback{faces: fs, missing: map[rune]bool{}}
}

// faceFor is the first face with a glyph for r, or the first face when none
// has one.
func (f *Fallback) faceFor(r rune) font.Face {
	for _, face := range f.faces {
		if _, ok := face.GlyphAdvance(r); ok {
			return face
		}
	}
	// Spaces and controls are nobody's glyph in some fonts, and are not
	// missing in any sense a reader would notice.
	if r > ' ' {
		f.mu.Lock()
		f.missing[r] = true
		f.mu.Unlock()
	}
	return f.faces[0]
}

// Missing is every letter asked for that no face had, in order.
func (f *Fallback) Missing() []rune {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]rune, 0, len(f.missing))
	for r := range f.missing {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

func (f *Fallback) Close() error { return nil }

func (f *Fallback) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	return f.faceFor(r).Glyph(dot, r)
}

func (f *Fallback) GlyphBounds(r rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	return f.faceFor(r).GlyphBounds(r)
}

func (f *Fallback) GlyphAdvance(r rune) (fixed.Int26_6, bool) {
	return f.faceFor(r).GlyphAdvance(r)
}

// Kern is the kerning between two letters from the same face; between two
// faces there is none to be had.
func (f *Fallback) Kern(r0, r1 rune) fixed.Int26_6 {
	a, b := f.faceFor(r0), f.faceFor(r1)
	if a != b {
		return 0
	}
	return a.Kern(r0, r1)
}

// Metrics are the first face's: the line a label sits on is the caller's
// own font's, and letters from the others sit on it.
func (f *Fallback) Metrics() font.Metrics { return f.faces[0].Metrics() }
