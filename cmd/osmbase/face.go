package main

import (
	"fmt"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// textFace is the face this command draws map labels and the credit in.
//
// # Why a real font and not the bitmap this used to use
//
// basicfont.Face7x13 is inside golang.org/x/image and cost nothing, which is
// why the credit used it first. It has no glyphs beyond its own small table,
// and that stopped being a footnote the moment this command started drawing
// PLACE NAMES: a map of Denmark rendered "Sonder Lindskov" as "S0nder
// Lindskov" and "Bokkelund" as "B0kkelund". A map that misspells the places
// on it is worse than a map with no names, because the reader cannot tell
// which names are wrong.
//
// So this parses a real font, which is what brings golang.org/x/text into the
// module: opentype imports sfnt, which imports x/text/encoding/charmap. That
// is a second dependency in a library that had exactly one, and it is a
// deliberate, visible edit -- NOTICE names it and the dependency gate admits
// it by name rather than by pattern.
//
// The LIBRARY still ships no font. render takes a face and draws nothing
// without one, so a consumer linking it does not pull the font bytes or the
// parser in unless it asks for labels. This cost is the command's alone.
//
// # Why the Go fonts
//
// They are inside x/image already, so the font data brings no third module
// and no new licence to record. They are also made for screens and legible
// small, which is the only size any of this is drawn at.
const baseTextSize = 13

var parsedFont = sync.OnceValues(func() (*opentype.Font, error) {
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, fmt.Errorf("parsing the built-in label font: %w", err)
	}
	return f, nil
})

// faceAt builds a face at a size, keeping one per size.
//
// Cached because the label pass asks once per RULE and a style has several,
// and because building a face is not free. Keyed on the rounded pixel size,
// so two rules whose scales land on the same size share a face.
var (
	faceMu     sync.Mutex
	faceBySize = map[int]font.Face{}
)

func faceAt(px float64) font.Face {
	if px < 1 {
		px = 1
	}
	key := int(math.Round(px))

	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faceBySize[key]; ok {
		return f
	}
	ttf, err := parsedFont()
	if err != nil {
		return nil
	}
	f, err := opentype.NewFace(ttf, &opentype.FaceOptions{
		Size: float64(key), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	faceBySize[key] = f
	return f
}

// labelFaceFor resolves a label rule's size scale, so that a place name is
// set larger than a street name and a reader can tell which is which.
func labelFaceFor(scale float64) font.Face { return faceAt(baseTextSize * scale) }

// labelFace is the face for map labels, or nil if it could not be built.
//
// A nil face draws no labels rather than failing the render: a map without
// names is still a map, and refusing to draw one because a font would not
// parse would be losing the picture over the caption.
func labelFace() font.Face { return faceAt(baseTextSize) }
