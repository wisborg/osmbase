package perspective

import (
	"context"
	"math"
	"slices"

	"github.com/wisborg/osmbase/render"
)

// planScale is how much smaller than its frame each way a plan's picture is
// drawn: a sixteenth of the pixels, which is still several to a name.
const planScale = 4

// NamePlan is how strongly each name of a flight is shown in each of its
// frames, worked out for every frame before any is drawn, and smoothed over
// time.
//
// DrawNames works out each picture's names from that picture alone, and
// however continuously it does, a name's strength can still change faster
// from one frame to the next than the eye can follow -- a crest crossing
// it, a stronger name sliding over it -- and in motion that reads as a
// flicker. Knowing every frame first, each name's strength is averaged over
// the frames either side, so none changes faster than the eye can follow.
// It is worked out before any frame is drawn, from the whole flight, so a
// frame draws the same however it is reached: in order, or alone.
type NamePlan struct {
	names map[nameID]*plannedName
	order []nameID

	// before and after are the mean change of a name's strength from one
	// frame to the next, over the frames it is shown in, before and after
	// smoothing; see Steadiness.
	before, after float64
}

type plannedName struct {
	name
	first int       // the frame alpha[0] is for
	alpha []float32 // strength in each frame from first on
}

// PlanFrame is frame i of a flight: the scene, the camera and the options
// it is drawn with. It is called for many frames at once, from as many
// goroutines, so it must be safe for that.
type PlanFrame func(i int) (Scene, Camera, Options, error)

// PlanNames plans the names of a flight of the given number of frames, each
// drawn as frame says, smoothing each name's strength over smooth frames
// either side; progress, when not nil, is told how many frames are planned
// of how many as each is -- a long flight's plan takes minutes, and a
// program with nothing to show for them looks as if it has hung. The frames
// are planned many at once (frame is called concurrently; progress one call
// at a time), and the plan is the one they would have made in turn. Each
// frame is drawn small, without colour -- enough to know
// where its names fall, which zoom the ground there is drawn from, and what
// a hill hides -- and its names' strengths worked out as DrawNames works
// them out for a picture of the full size. The tiles the frames are drawn
// from are drawn as they are needed, as they would be by the frames
// themselves.
func PlanNames(ctx context.Context, frames int, frame PlanFrame, smooth int, progress func(done, total int)) (*NamePlan, error) {
	plan := &NamePlan{names: map[nameID]*plannedName{}}
	// Each frame's names worked out on its own, many frames at once, and
	// put together in frame order below; see eachFrame.
	shownIn := make([][]shownName, frames)
	err := eachFrame(ctx, frames, func(i int) error {
		s, c, o, err := frame(i)
		if err != nil {
			return err
		}
		small := o
		small.Width = max(1, int(math.Round(float64(o.Width)/planScale)))
		small.Height = max(1, int(math.Round(float64(o.Height)/planScale)))
		scale := float64(o.Width) / float64(small.Width)
		pic, err := renderScene(ctx, s, c, small, &planning{scale: scale})
		if err != nil {
			return err
		}
		namesMu.Lock()
		shownIn[i] = pic.shownNames(scale)
		namesMu.Unlock()
		return nil
	}, progress)
	if err != nil {
		return nil, err
	}
	for i, shown := range shownIn {
		for _, sn := range shown {
			pn := plan.names[sn.id]
			if pn == nil {
				pn = &plannedName{name: sn.name, first: i}
				plan.names[sn.id] = pn
			}
			for len(pn.alpha) <= i-pn.first {
				pn.alpha = append(pn.alpha, 0)
			}
			pn.alpha[i-pn.first] = float32(sn.alpha)
		}
	}
	// Both totals over the frames the names were shown in before
	// smoothing, which spreads each over a few more.
	var changeBefore, changeAfter, steps float64
	for id, pn := range plan.names {
		plan.order = append(plan.order, id)
		changeBefore += totalChange(pn.alpha)
		steps += float64(len(pn.alpha) + 1)
		pn.first, pn.alpha = pn.first-smooth, smoothed(pn.alpha, smooth)
		changeAfter += totalChange(pn.alpha)
	}
	if steps > 0 {
		plan.before, plan.after = changeBefore/steps, changeAfter/steps
	}
	slices.SortFunc(plan.order, func(a, b nameID) int { return a.compare(b) })
	return plan, nil
}

// smoothed is v, with smooth frames of nothing either side, averaged over
// smooth frames either side of each.
func smoothed(v []float32, smooth int) []float32 {
	if smooth <= 0 {
		return v
	}
	padded := make([]float64, len(v)+2*smooth)
	for i, a := range v {
		padded[smooth+i] = float64(a)
	}
	sum := make([]float64, len(padded)+1)
	for i, a := range padded {
		sum[i+1] = sum[i] + a
	}
	out := make([]float32, len(padded))
	for i := range padded {
		lo, hi := max(0, i-smooth), min(len(padded)-1, i+smooth)
		out[i] = float32((sum[hi+1] - sum[lo]) / float64(2*smooth+1))
	}
	return out
}

// totalChange is the change from each value of v to the next, added up,
// counting the steps on from nothing and back to it.
func totalChange(v []float32) float64 {
	prev := float32(0)
	var total float64
	for _, a := range v {
		total += math.Abs(float64(a - prev))
		prev = a
	}
	return total + math.Abs(float64(prev))
}

// Steadiness is the mean change of a name's strength from one frame to the
// next over the frames it is shown in, before smoothing and after: how much
// the names would have flickered, and how much they will.
func (p *NamePlan) Steadiness() (before, after float64) { return p.before, p.after }

// strength is how strongly the name id is shown in frame i.
func (p *NamePlan) strength(id nameID, i int) float64 {
	pn := p.names[id]
	if pn == nil || i < pn.first || i >= pn.first+len(pn.alpha) {
		return 0
	}
	return float64(pn.alpha[i-pn.first])
}

// DrawNamesPlanned stands the names of frame i of a planned flight on the
// picture -- the picture of that frame -- each as strongly as the plan
// says: where it falls in this picture, at the angle its line runs here,
// faded as the plan has it. See NamePlan and DrawNames.
func (p *Picture) DrawNamesPlanned(pal render.Palette, plan *NamePlan, i int) {
	if plan == nil {
		return
	}
	namesMu.Lock()
	defer namesMu.Unlock()
	for _, id := range plan.order {
		alpha := plan.strength(id, i)
		if alpha < 1.0/64 {
			continue
		}
		n := plan.names[id]
		if n.label.Face == nil {
			continue
		}
		sx, sy, _, ok := p.onFrame(n.label.At)
		if !ok {
			continue
		}
		angle := 0.0
		if !n.place {
			if angle, ok = p.screenAngle(n.label.At, n.label.Angle); !ok {
				continue
			}
		}
		p.drawName(pal, n.label, sx/supersample, sy/supersample, angle, alpha)
	}
}
