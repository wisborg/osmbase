package render

import "testing"

// Text with no right-to-left letter is untouched.
func TestVisualLeavesLeftToRightTextAlone(t *testing.T) {
	for _, s := range []string{"Sydney", "Москва", "กรุงเทพ", "नेपाल", "", "12 Main St"} {
		if got := Visual(s); got != s {
			t.Errorf("%q became %q", s, got)
		}
	}
}

// Hebrew is turned round; numbers and Latin in it keep reading left to right.
func TestVisualTurnsHebrewRound(t *testing.T) {
	for in, want := range map[string]string{
		"ישראל":       "לארשי",
		"תל אביב":     "ביבא לת",
		"כביש 90":     "90 שיבכ",
		"Tel Aviv תל": "Tel Aviv לת",
		// Two Hebrew words in a left-to-right label are one run, turned
		// round as one: the space between them is theirs.
		"Tel Aviv תל אביב": "Tel Aviv ביבא לת",
		"חיפה (Haifa)":     "(Haifa) הפיח",
	} {
		if got := Visual(in); got != want {
			t.Errorf("%q drawn as %q, want %q", in, got, want)
		}
	}
}

// Arabic letters take the form their neighbours call for, and the word is
// turned round. سوريا, Syria: sin initial, waw final (it joins back only),
// reh isolated (waw does not join forward), yeh initial, alef final.
func TestVisualJoinsArabic(t *testing.T) {
	want := string([]rune{0xFE8E, 0xFEF3, 0xFEAD, 0xFEEE, 0xFEB3}) // drawn left to right
	if got := Visual("سوريا"); got != want {
		t.Errorf("سوريا drawn as %U, want %U", []rune(got), []rune(want))
	}
	// بيت, a house: beh initial, yeh medial, teh final.
	if got := Visual("بيت"); got != string([]rune{0xFE96, 0xFEF4, 0xFE91}) {
		t.Errorf("بيت drawn as %U", []rune(got))
	}
	// Lam and alef are one glyph: لا alone is the isolated ligature.
	if got := Visual("لا"); got != string(rune(0xFEFB)) {
		t.Errorf("لا drawn as %U", []rune(got))
	}
	// A mark rides on its letter across the turn: بَ is beh isolated, then
	// its fatha after it.
	if got := Visual("بَ"); got != string([]rune{0xFE8F, 0x064E}) {
		t.Errorf("بَ drawn as %U", []rune(got))
	}
}
