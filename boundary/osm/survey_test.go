package osm

import (
	"io"
	"os"
	"sort"
	"testing"

	"github.com/wisborg/osmbase/osmpbf"
)

// TestSurveyARealExtract reports what administrative data an extract actually
// holds, without filtering it through the pipeline's own assumptions.
//
//	go test ./boundary/osm/ -run TestSurveyARealExtract -v -extract /path/to.osm.pbf
//
// It exists because the pipeline's premise is a claim about the DATA, not
// about the code: docs/locate.md says "levels 8 to 10 are suburb" and builds
// everything on it. A run against Denmark found that claim does not hold
// there -- 108 relations at level 7, 21 at level 8, one each at 9 and 10 --
// and no amount of testing the decoder would have shown it, because the
// decoder was right. This is the check to run first against any new country.
//
// It asserts almost nothing on purpose. What counts as enough coverage is a
// judgement about a place, and printing the distribution lets a person make
// it; a threshold here would only encode one country's tagging habits.
func TestSurveyARealExtract(t *testing.T) {
	if *extractPath == "" {
		t.Skip("no -extract given; this surveys a real country extract, which is not committed")
	}
	open := Open(func() (io.ReadCloser, error) { return os.Open(*extractPath) })

	var (
		relAdmin  = map[string]int{}
		relPlace  = map[string]int{}
		wayAdmin  = map[string]int{}
		wayPlace  = map[string]int{}
		nodePlace = map[string]int{}
		relations int
	)

	err := eachBlock(open, func(b *osmpbf.PrimitiveBlock) error {
		if err := b.EachRelation(func(r osmpbf.Relation) error {
			relations++
			if v, ok := r.Tags.Get("admin_level"); ok && r.Tags.Is("boundary", "administrative") {
				relAdmin[v]++
			}
			if v, ok := r.Tags.Get("place"); ok {
				relPlace[v]++
			}
			return nil
		}); err != nil {
			return err
		}
		if err := b.EachWay(func(w osmpbf.Way) error {
			// A boundary may be a single closed way rather than a relation,
			// and this pipeline reads only relations. Counted so that the
			// gap is visible rather than assumed away.
			if v, ok := w.Tags.Get("admin_level"); ok && w.Tags.Is("boundary", "administrative") {
				wayAdmin[v]++
			}
			if v, ok := w.Tags.Get("place"); ok {
				wayPlace[v]++
			}
			return nil
		}); err != nil {
			return err
		}
		return b.EachNode(func(n osmpbf.Node) error {
			if v, ok := n.Tags.Get("place"); ok {
				nodePlace[v]++
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("survey: %v", err)
	}

	t.Logf("%s: %d relations", *extractPath, relations)
	report(t, "relation boundary=administrative, admin_level", relAdmin)
	report(t, "relation place", relPlace)
	report(t, "way boundary=administrative, admin_level", wayAdmin)
	report(t, "way place (a polygon this pipeline does not read)", wayPlace)
	report(t, "node place (a point, so containment is impossible)", nodePlace)
}

func report(t *testing.T, label string, m map[string]int) {
	t.Helper()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 0 {
		t.Logf("%s: none", label)
		return
	}
	t.Logf("%s:", label)
	for i, k := range keys {
		if i >= 12 {
			t.Logf("    (%d more)", len(keys)-i)
			break
		}
		t.Logf("    %-20q %6d", k, m[k])
	}
}
