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

// Three lists, not one, because the three constraints ask three different
// membership questions and a single slice answered them by position.
//
// It was wrong, and wrong silently: NoData was in none of them, so the hatch --
// which this library paints LAST, over everything -- was measured against
// nothing. Both shipped palettes had their accent, highlight and dim inks
// below the overlay threshold against it, the worst at 1.62, which is a route
// and a position dot going unreadable exactly where a partial render is
// supposed to be at its most honest. And constraint 1 reached its subset with
// a positional [1:], so reordering the slice would have made it skip Land
// rather than Background.

// context is the inks whose job is to stay quiet: everything the map draws
// except the background they are judged against, and except the hatch, whose
// job is the opposite.
func (p Palette) context() []namedColour {
	return []namedColour{
		{"Land", p.Land},
		{"Water", p.Water},
		{"Green", p.Green},
		{"Built", p.Built},
		{"Road", p.Road},
		{"Ink", p.Ink},
	}
}

// underfoot is every colour a consumer's ink can end up lying across a field
// of, which is the background and the context inks.
//
// NoData is deliberately NOT here, and the reason is what the hatch actually
// draws rather than what its name suggests. A gap is filled with the
// BACKGROUND and then struck with diagonal NoData lines a sixth of their own
// spacing wide -- about 12% of the area. So a route crossing a gap lies on
// background for seven eighths of its length and crosses the strokes
// transversely, which is a series of intersections rather than a field to be
// read against. Holding the overlay to 3:1 against the hatch ink would price
// in a solid fill that never happens, and the price is steep: it is
// unsatisfiable, because the hatch must ALSO stand 3:1 clear of the background
// (see MinNoDataRatio) and the overlay inks span the luminance range between
// those two positions.
//
// What the hatch owes is covered by the two constraints that do apply to it:
// it must be recognisable against the background, and it must not be mistaken
// for any map role.
func (p Palette) underfoot() []namedColour {
	return append([]namedColour{{"Background", p.Background}}, p.context()...)
}

// distinguishable is every colour that has to be told apart from every other,
// which is the whole palette.
func (p Palette) distinguishable() []namedColour {
	return p.underfoot()
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
	// That is not a curiosity, it is forced -- for a DARK palette, which is
	// the half of the argument an earlier version of this comment left out.
	//
	// Clearing MinOverlayRatio pins every map ink to a luminance of at most
	// (Lo+0.05)/3 - 0.05, where Lo is the dimmest overlay ink. Against
	// DarkOverlay, whose dimmest ink sits at 0.2542, that cap is 0.0514, and
	// the widest contrast ratio available inside [0, 0.0514] is 2.03. So a
	// dark basemap satisfying the other two constraints CANNOT separate its
	// roles by luminance and must separate them by hue.
	//
	// A light palette is the counterexample, and it is worth stating because
	// the general form of the claim is false. LightOverlay's dimmest ink is
	// its near-black Foreground at 0.0103, and the same algebra runs the other
	// way: map inks are pinned ABOVE about 0.131, with ratios up to roughly
	// 5.4 available between them. Luminance separation is possible there.
	//
	// The threshold is one number for both because the dark case is the
	// binding one and a metric that changed with the palette would be no
	// threshold at all.
	//
	// Six is around twice the threshold at which a side-by-side difference is
	// noticeable, which is the right order for two large areas meeting along
	// an edge. It is a CIEDE2000 figure: an earlier version measured CIE76,
	// where the same threshold was 10% to 36% too generous for exactly the
	// pairs that bind here. See ColourDistance.
	MinRoleSeparation = 6.0

	// MaxContextChroma is how saturated a map ink may be, in CIE L*a*b*
	// chroma.
	//
	// It exists because the three constraints above can ALL be satisfied by a
	// map nobody would call context. A consumer deriving a palette from its own
	// theme produced greens and browns at chroma 48 where these built-ins sit
	// at 6 to 15: every luminance rule held, every pair was distinguishable,
	// the check passed, and the map shouted over the route it was drawn under.
	//
	// Saturation is simply a different axis from the two already measured.
	// Contrast ratio sees only luminance, and delta-E measures the distance
	// BETWEEN two colours rather than how far either sits from neutral, so a
	// palette can be uniformly vivid and score perfectly on both.
	//
	// It is a COARSE guard, and saying so is the honest description. Every
	// palette that has actually been looked at in a rendered frame and judged
	// to read correctly falls between 15.5 and 28.4 -- the low end a
	// consumer's derived palette, the high end this package's own light Ink,
	// which is a warm tan at L* 73. The one that read as a diagram rather than
	// a map was 48. Thirty-two sits clear of everything that worked and well
	// below the thing that did not.
	//
	// Two earlier attempts are worth recording because each was wrong in an
	// instructive way. Eighteen, chosen by taste, failed this package's own
	// dark Green at 20.2. Twenty-four, calibrated against the dark palette
	// alone, then failed the light one at 28.4 -- because chroma at L* 73
	// reads as far quieter than the same chroma at L* 15, and a flat ceiling
	// cannot see that. A threshold that scaled with lightness would be the
	// better instrument, and would need its own calibration data to be
	// anything but a guess dressed up as arithmetic.
	//
	// So this is a guard against one observed failure mode, not an aesthetic
	// judgement. It is worth keeping at that strength: the bug it catches
	// passed every other constraint in this file.
	//
	// The hatch is exempt. It is the one colour here whose job is to be
	// noticed.
	MaxContextChroma = 32.0

	// MinNoDataRatio is how far the hatch must stand FROM the background.
	//
	// The only floor in this file, and the reason is that the hatch is the one
	// colour here meant to be noticed. Three is the same WCAG non-text
	// threshold as MinOverlayRatio, borrowed for the same reason: a hatch is a
	// graphical object a viewer has to pick out, and it is doing the job the
	// overlay inks do rather than the job the map does.
	MinNoDataRatio = 3.0
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
	for _, ink := range p.context() {
		if r := ContrastRatio(ink.c, p.Background); r > MaxContextRatio {
			bad = append(bad, fmt.Sprintf(
				"%s is %.2f against Background, above %.2f: it reads as content rather than as context",
				ink.name, r, MaxContextRatio))
		}
	}

	// 2. Everything drawn on top must read against everything underneath.
	for _, on := range o.inks() {
		for _, under := range p.underfoot() {
			if r := ContrastRatio(on.c, under.c); r < MinOverlayRatio {
				bad = append(bad, fmt.Sprintf(
					"overlay %s is %.2f against map %s, below %.2f: it would disappear where the map is that colour",
					on.name, r, under.name, MinOverlayRatio))
			}
		}
	}

	// 3. The map must still be legible AS a map.
	inks := p.distinguishable()
	for i, a := range inks {
		for _, b := range inks[i+1:] {
			if d := ColourDistance(a.c, b.c); d < MinRoleSeparation {
				bad = append(bad, fmt.Sprintf(
					"%s and %s differ by only %.1f, below %.1f: the two would not be told apart",
					a.name, b.name, d, MinRoleSeparation))
			}
		}
	}

	// 4. The map is context in SATURATION as well as in luminance. See
	// MaxContextChroma: without this a palette can pass every rule above and
	// still be a diagram rather than a map.
	for _, ink := range p.context() {
		if c := chromaOf(ink.c); c > MaxContextChroma {
			bad = append(bad, fmt.Sprintf(
				"%s has chroma %.1f, above %.1f: it is too saturated to sit behind anything",
				ink.name, c, MaxContextChroma))
		}
	}

	// 5. The hatch has the opposite job to everything else here, so it gets
	// the opposite constraint: a FLOOR against the background rather than a
	// ceiling. Its whole purpose is to be unmistakable for map ink -- a gap
	// painted in something background-ish is indistinguishable from ocean and
	// from a crash, which is the thing it exists to prevent. Nothing else in
	// this file expresses a floor, so without this the hatch could drift to
	// invisible and every test would still pass.
	if r := ContrastRatio(p.NoData, p.Background); r < MinNoDataRatio {
		bad = append(bad, fmt.Sprintf(
			"NoData is %.2f against Background, below %.2f: a gap would not be recognisable as a gap",
			r, MinNoDataRatio))
	}

	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("palette and overlay cannot be separated:\n  %s", strings.Join(bad, "\n  "))
}

// requireColour rejects a nil color.Color with a message that says what
// happened.
//
// These two functions took color.RGBA until the premultiplication bug forced
// them onto the interface, and an interface can be nil where a struct cannot.
// Without this the failure is a bare "invalid memory address" from three
// frames inside image/color, which tells a caller nothing about which of their
// own colours they forgot to set -- and that is exactly how it first appeared,
// out of a consumer that passed a zero-valued struct of interface fields.
//
// It panics rather than returning a sentinel because there is no honest number
// to return. Treating nil as black would be worse than the crash: a palette
// check against a dark theme would then PASS, and the consumer would ship a
// missing ink instead of learning about it. A ratio has no zero value that
// means "there was no colour here".
func requireColour(c color.Color, fn string) {
	if c == nil {
		panic("osmbase/render: " + fn + " was given a nil colour; a color.Color field was left unset")
	}
}

// ContrastRatio is WCAG 2's contrast ratio between two colours.
//
// Exported because a consumer choosing its own palette needs the same number
// this package judges it by, and because the alternative is that consumer
// writing a second implementation that rounds differently. fitdash has this
// function already and the two agree by construction -- same standard, same
// un-premultiplying conversion -- rather than by luck.
//
// The duplication is deliberate and temporary in one direction: osmbase cannot
// depend on fitdash, and its own tests need this. When the fitdash adapter
// lands, fitdash should drop its copy and call this one, or one binary will
// hold two implementations of one formula and they will drift.
func ContrastRatio(a, b color.Color) float64 {
	requireColour(a, "ContrastRatio")
	requireColour(b, "ContrastRatio")
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
// relativeLuminance is WCAG 2's formula: linearize each sRGB channel, undoing
// the gamma curve a display applies, then weight them by how much each
// contributes to perceived brightness. Green dominates and blue barely
// registers, which is why the weights are so uneven.
//
// It converts through NRGBAModel first, and that is not a formality. Go's
// color.RGBA is ALPHA-PREMULTIPLIED by definition, so a consumer expressing a
// half-transparent ink the obvious way -- color.RGBAModel.Convert of its own
// theme colour -- hands over channels already scaled by the alpha. Reading
// those directly measured a mid grey at 50% alpha as 1.97 against the dark
// background where the colour itself is 5.46: the check would pass and the ink
// the viewer actually sees would fail.
//
// Converting here is also what makes the claim above true rather than
// aspirational, since fitdash's copy of this function does exactly the same
// conversion.
func relativeLuminance(c color.Color) float64 {
	nc := color.NRGBAModel.Convert(c).(color.NRGBA)
	return 0.2126*linearizeSRGB(float64(nc.R)/255) +
		0.7152*linearizeSRGB(float64(nc.G)/255) +
		0.0722*linearizeSRGB(float64(nc.B)/255)
}

func linearizeSRGB(c float64) float64 {
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// ColourDistance is CIEDE2000: the perceptual difference between two colours,
// as the CIE defined it in 2000.
//
// An earlier version used CIE76, the straight Euclidean distance in L*a*b*,
// with a comment claiming the later formulas "change the number by a few
// percent where this file wants a factor of two". That was measured and it is
// false. Against the palettes this package ships, CIE76 reads 10% to 36% high,
// and every one of those deviations is in the unsafe direction for the pairs
// nearest the threshold -- dark Background against Land, which is sea against
// land, scored 6.18 under CIE76 and 3.97 here.
//
// The cause is not the blue-region weakness CIEDE2000 is usually cited for;
// these colours have almost no chroma. It is S_L, the lightness weighting,
// which reaches about 1.6 at the extremes of L*. And the other two constraints
// FORCE both palettes to those extremes: clearing MinOverlayRatio against a
// dark theme pushes every map ink to L* 5-20, and against a light theme to
// L* 78-97. So the metric's blind spot and the band the model pins the palette
// into are the same region, which is the worst possible coincidence and the
// reason the extra arithmetic is worth carrying.
//
// It is forty lines of constants that must all be right, so it is checked
// against the Sharma-Wu-Dalal reference pairs -- the dataset published for
// exactly this, containing the cases where a naive implementation gets the hue
// rotation or the angle wraparound wrong.
func ColourDistance(a, b color.Color) float64 {
	requireColour(a, "ColourDistance")
	requireColour(b, "ColourDistance")
	l1, a1, b1 := labOf(a)
	l2, a2, b2 := labOf(b)
	return ciede2000(l1, a1, b1, l2, a2, b2)
}

// ciede2000 is the formula itself, taking L*a*b* directly.
//
// Split from ColourDistance so it can be checked against the reference data,
// which is published as L*a*b* pairs rather than as colours. Forty lines of
// constants with no external check would be a worse bet than the CIE76 it
// replaced -- the failure mode of a hand-rolled CIEDE2000 is not a crash, it is
// numbers that look reasonable everywhere except the cases the reference pairs
// are chosen to expose.
func ciede2000(l1, a1, b1, l2, a2, b2 float64) float64 {
	const pow25_7 = 6103515625.0 // 25^7

	lBar := (l1 + l2) / 2
	c1 := math.Hypot(a1, b1)
	c2 := math.Hypot(a2, b2)
	cBar := (c1 + c2) / 2

	// G expands the a* axis for low-chroma colours, which is what stops two
	// near-neutrals being called more similar than they look.
	cBar7 := math.Pow(cBar, 7)
	g := 0.5 * (1 - math.Sqrt(cBar7/(cBar7+pow25_7)))
	a1p, a2p := (1+g)*a1, (1+g)*a2

	c1p, c2p := math.Hypot(a1p, b1), math.Hypot(a2p, b2)
	cBarP := (c1p + c2p) / 2

	h1p, h2p := hueAngle(b1, a1p), hueAngle(b2, a2p)

	dLp := l2 - l1
	dCp := c2p - c1p

	// The hue difference, taking the short way round the circle. Getting this
	// wrap wrong is the classic CIEDE2000 bug and it is what the reference
	// pairs are chosen to expose.
	var dhp float64
	switch {
	case c1p*c2p == 0:
		dhp = 0
	case math.Abs(h2p-h1p) <= 180:
		dhp = h2p - h1p
	case h2p-h1p > 180:
		dhp = h2p - h1p - 360
	default:
		dhp = h2p - h1p + 360
	}
	dHp := 2 * math.Sqrt(c1p*c2p) * math.Sin(rad(dhp)/2)

	var hBarP float64
	switch {
	case c1p*c2p == 0:
		hBarP = h1p + h2p
	case math.Abs(h1p-h2p) <= 180:
		hBarP = (h1p + h2p) / 2
	case h1p+h2p < 360:
		hBarP = (h1p + h2p + 360) / 2
	default:
		hBarP = (h1p + h2p - 360) / 2
	}

	t := 1 -
		0.17*math.Cos(rad(hBarP-30)) +
		0.24*math.Cos(rad(2*hBarP)) +
		0.32*math.Cos(rad(3*hBarP+6)) -
		0.20*math.Cos(rad(4*hBarP-63))

	lBar50 := (lBar - 50) * (lBar - 50)
	sL := 1 + (0.015*lBar50)/math.Sqrt(20+lBar50)
	sC := 1 + 0.045*cBarP
	sH := 1 + 0.015*cBarP*t

	dTheta := 30 * math.Exp(-((hBarP-275)/25)*((hBarP-275)/25))
	cBarP7 := math.Pow(cBarP, 7)
	rC := 2 * math.Sqrt(cBarP7/(cBarP7+pow25_7))
	rT := -rC * math.Sin(rad(2*dTheta))

	lTerm := dLp / sL
	cTerm := dCp / sC
	hTerm := dHp / sH
	return math.Sqrt(lTerm*lTerm + cTerm*cTerm + hTerm*hTerm + rT*cTerm*hTerm)
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }

// hueAngle is atan2 in degrees on [0, 360), with the convention CIEDE2000
// requires that a colour with no chroma at all has a hue of zero rather than
// whatever atan2(0, 0) happens to return.
func hueAngle(b, ap float64) float64 {
	if b == 0 && ap == 0 {
		return 0
	}
	h := math.Atan2(b, ap) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return h
}

// chromaOf is how far a colour sits from neutral grey in L*a*b*.
//
// Separate from ColourDistance because the question is different: that one asks
// how far two colours are from EACH OTHER, and this asks how far one is from
// having no hue at all. A palette can score well on the first while every
// member of it fails the second.
func chromaOf(c color.Color) float64 {
	_, a, b := labOf(c)
	return math.Hypot(a, b)
}

func labOf(c color.Color) (l, a, b float64) {
	nc := color.NRGBAModel.Convert(c).(color.NRGBA)
	r := linearizeSRGB(float64(nc.R) / 255)
	g := linearizeSRGB(float64(nc.G) / 255)
	bl := linearizeSRGB(float64(nc.B) / 255)

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
