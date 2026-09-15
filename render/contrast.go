package render

import (
	"fmt"
	"image/color"
	"math"
	"strings"
)

// Overlay is what a consumer draws ON TOP of the map.
//
// The library never draws these. It needs to know them because the whole
// argument for drawing a basemap from the consumer's own palette is that
// separation can then be guaranteed rather than corrected afterwards, and a
// guarantee needs both halves: the colours underneath, which are the Palette,
// and the colours on top, which are these.
//
// In fitdash they are the route line, the covered prefix, the position dot and
// the highlight marks. This package does not know that and must not: the names
// are roles, not features.
type Overlay struct {
	// Foreground is the brightest thing drawn over the map -- the line the
	// viewer is meant to follow.
	Foreground color.RGBA
	// Accent is the single most important mark, drawn small.
	//
	// Small matters: a dot of a few pixels has almost no area with which to
	// win an argument against the texture under it, so it is held to the same
	// threshold as everything else rather than a lower one.
	Accent color.RGBA
	// Highlight marks a stretch of the foreground as special.
	Highlight color.RGBA
	// Dim is the foreground's quieter form -- the part not yet reached, or no
	// longer current.
	//
	// It is usually the binding constraint and it is worth knowing why. A dim
	// ink is mid-luminance by construction, so it is the overlay colour with
	// the least room between itself and the map beneath it. A palette that
	// fails only here is usually telling the consumer that their dim ink is
	// too close to their background, not that the map is wrong.
	Dim color.RGBA
}

func (o Overlay) inks() []namedColour {
	return []namedColour{
		{"Foreground", o.Foreground},
		{"Accent", o.Accent},
		{"Highlight", o.Highlight},
		{"Dim", o.Dim},
	}
}

type namedColour struct {
	name string
	c    color.RGBA
}

func (p Palette) inks() []namedColour {
	return []namedColour{
		{"Background", p.Background},
		{"Land", p.Land},
		{"Water", p.Water},
		{"Green", p.Green},
		{"Built", p.Built},
		{"Road", p.Road},
		{"Ink", p.Ink},
	}
}

// The three thresholds, and why they are these numbers.
//
// They are stated as constants rather than parameters because a threshold a
// caller can lower is a threshold that gets lowered the first time a palette
// fails, which is precisely when it is doing its job.
const (
	// MaxContextRatio bounds how loud a map ink may be against the background.
	//
	// WCAG 2 puts readable body text at 4.5:1. A map ink at or above that is
	// as prominent as text on a page, which is the opposite of context -- so
	// that figure is borrowed as a ceiling rather than a floor. The map is
	// allowed to be seen and not allowed to be read.
	MaxContextRatio = 4.5

	// MinOverlayRatio is how far every overlay ink must stand from every map
	// ink.
	//
	// WCAG 2's non-text minimum, 3:1, for graphical objects and interface
	// components. A route line IS a graphical object, so the analogy is exact
	// rather than borrowed. It is not raised above 3 despite a thin line
	// having less area than a UI control, because a route is drawn with a dot
	// and a casing and is rarely a bare hairline.
	MinOverlayRatio = 3.0

	// MinRoleSeparation is how distinguishable two map roles must be FROM EACH
	// OTHER, in CIE76 delta-E rather than in contrast ratio.
	//
	// The metric changes here on purpose, and the reason is the most useful
	// thing this file records. Contrast ratio is a function of luminance
	// alone, so it cannot see hue at all: a blue bay and a green park at the
	// same luminance have a contrast ratio of 1.00 and are obviously different
	// to anybody looking at them.
	//
	// That is not a curiosity, it is forced. Clearing MinOverlayRatio against
	// a dark theme's overlay inks pins every map ink below a luminance of
	// about 0.05, and the widest contrast ratio available inside that band is
	// about 1.74. So a basemap that satisfies the other two constraints CANNOT
	// separate its roles by luminance, and must separate them by hue. Testing
	// role separation with contrast ratio would demand the impossible and then
	// be relaxed until it demanded nothing.
	//
	// Six is around twice the threshold at which a side-by-side difference is
	// noticeable, which is the right order for two large areas meeting along
	// an edge.
	MinRoleSeparation = 6.0
)

// CheckContrast reports whether this palette can carry that overlay.
//
// It is the deliverable of the styling work rather than a lint: the claim that
// a basemap drawn in the consumer's own colours needs no dimming is only worth
// making if something enforces it. fitdash washes third-party imagery 65%
// toward its background because that imagery is busy, mid-toned and chosen by
// somebody else; a palette that passes here has earned the right not to.
//
// Every failure is reported, not just the first, because a palette usually
// fails in a pattern -- every map ink against one overlay colour, say -- and
// seeing one of those at a time turns tuning into a guessing game.
func (p Palette) CheckContrast(o Overlay) error {
	var bad []string

	// 1. The map is context. Nothing in it may shout.
	for _, ink := range p.inks()[1:] {
		if r := ContrastRatio(ink.c, p.Background); r > MaxContextRatio {
			bad = append(bad, fmt.Sprintf(
				"%s is %.2f against Background, above %.2f: it reads as content rather than as context",
				ink.name, r, MaxContextRatio))
		}
	}

	// 2. Everything drawn on top must read against everything underneath.
	for _, on := range o.inks() {
		for _, under := range p.inks() {
			if r := ContrastRatio(on.c, under.c); r < MinOverlayRatio {
				bad = append(bad, fmt.Sprintf(
					"overlay %s is %.2f against map %s, below %.2f: it would disappear where the map is that colour",
					on.name, r, under.name, MinOverlayRatio))
			}
		}
	}

	// 3. The map must still be legible AS a map.
	inks := p.inks()
	for i, a := range inks {
		for _, b := range inks[i+1:] {
			if d := ColourDistance(a.c, b.c); d < MinRoleSeparation {
				bad = append(bad, fmt.Sprintf(
					"%s and %s differ by only %.1f, below %.1f: the two would not be told apart",
					a.name, b.name, d, MinRoleSeparation))
			}
		}
	}

	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("palette and overlay cannot be separated:\n  %s", strings.Join(bad, "\n  "))
}

// ContrastRatio is WCAG 2's contrast ratio between two opaque colours.
//
// Exported because a consumer choosing its own palette needs the same number
// this package judges it by, and because the alternative is that consumer
// writing a second implementation that rounds differently. fitdash has this
// function already, and the two agree by construction rather than by luck --
// which is the point of naming the standard rather than inventing a measure.
func ContrastRatio(a, b color.RGBA) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance is WCAG 2's formula: linearize each sRGB channel, undoing
// the gamma curve a display applies, then weight them by how much each
// contributes to perceived brightness. Green dominates and blue barely
// registers, which is why the weights are so uneven.
func relativeLuminance(c color.RGBA) float64 {
	return 0.2126*linearizeSRGB(float64(c.R)/255) +
		0.7152*linearizeSRGB(float64(c.G)/255) +
		0.0722*linearizeSRGB(float64(c.B)/255)
}

func linearizeSRGB(c float64) float64 {
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// ColourDistance is CIE76 delta-E: the straight-line distance between two
// colours in CIE L*a*b*, a space built so that equal distances look about
// equally different.
//
// CIE76 rather than one of its successors because this is a threshold test
// over a handful of flat colours, not a tolerance for reproducing a print run.
// The later formulas correct for effects -- chroma and hue weighting near the
// blue axis -- that change the number by a few percent where this file wants a
// factor of two, and each correction is another page of constants that would
// have to be right.
func ColourDistance(a, b color.RGBA) float64 {
	l1, a1, b1 := labOf(a)
	l2, a2, b2 := labOf(b)
	return math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (b1-b2)*(b1-b2))
}

func labOf(c color.RGBA) (l, a, b float64) {
	r := linearizeSRGB(float64(c.R) / 255)
	g := linearizeSRGB(float64(c.G) / 255)
	bl := linearizeSRGB(float64(c.B) / 255)

	// sRGB to CIE XYZ, then XYZ to L*a*b* against the D65 white point, which
	// is the one sRGB is defined for.
	x := 0.4124*r + 0.3576*g + 0.1805*bl
	y := 0.2126*r + 0.7152*g + 0.0722*bl
	z := 0.0193*r + 0.1192*g + 0.9505*bl
	fx, fy, fz := labF(x/0.95047), labF(y/1.0), labF(z/1.08883)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

func labF(t float64) float64 {
	if t > 0.008856 {
		return math.Cbrt(t)
	}
	return 7.787*t + 16.0/116.0
}
