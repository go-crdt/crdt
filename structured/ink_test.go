package structured

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/go-crdt/crdt"
)

func mustBegin(t *testing.T, i *Ink) StrokeID {
	t.Helper()
	stroke, _, err := i.Begin()
	if err != nil {
		t.Fatal(err)
	}
	return stroke
}

func mustExtend(t *testing.T, i *Ink, stroke StrokeID, pts ...Point) {
	t.Helper()
	if _, err := i.Extend(stroke, pts...); err != nil {
		t.Fatal(err)
	}
}

func at(x, y float32) Point { return Point{X: x, Y: y, Pressure: 1} }

// drawn renders the paths as text, which is the only readable way to say two
// replicas agree about a drawing.
func drawn(i *Ink) string {
	out := ""
	for n, path := range i.Paths() {
		out += fmt.Sprint(n, ":")
		for _, p := range path {
			out += fmt.Sprintf(" (%g,%g,%g)", p.X, p.Y, p.Pressure)
		}
		out += "\n"
	}
	return out
}

// A stroke arrives one piece at a time, which is what it is drawn like, and
// extending it is one operation rather than a redraw.
func TestAStrokeIsDrawnAPieceAtATime(t *testing.T) {
	ink := NewInk(1)
	stroke := mustBegin(t, ink)
	if got := ink.Points(stroke); len(got) != 0 {
		t.Fatalf("a fresh stroke holds %d points", len(got))
	}

	ops, err := ink.Extend(stroke, at(0, 0), at(1, 1), at(2, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.List) != 3 {
		t.Fatalf("three points took %d operations", len(ops.List))
	}
	mustExtend(t, ink, stroke, at(3, 9))

	got := ink.Points(stroke)
	if len(got) != 4 {
		t.Fatalf("the stroke holds %d points, want 4", len(got))
	}
	for n, want := range []Point{at(0, 0), at(1, 1), at(2, 4), at(3, 9)} {
		if got[n] != want {
			t.Fatalf("point %d is %v, want %v", n, got[n], want)
		}
	}
	if paths := ink.Paths(); len(paths) != 1 || len(paths[0]) != 4 {
		t.Fatalf("the drawing is %v", paths)
	}
}

// Two people drawing at the same time. Each stroke keeps its own points, in
// order, however the two streams interleave.
func TestTwoPeopleDrawAtOnce(t *testing.T) {
	a := NewInk(1)
	b, err := LoadInk(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := mustBegin(t, a), mustBegin(t, b)
	// Alternating, without either seeing the other.
	for n := range 5 {
		mustExtend(t, a, mine, at(float32(n), 0))
		mustExtend(t, b, theirs, at(0, float32(n)))
	}
	if err := a.Apply(must(b.OpsSince(a.Version()))...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(must(a.OpsSince(b.Version()))...); err != nil {
		t.Fatal(err)
	}

	if drawn(a) != drawn(b) {
		t.Fatalf("the replicas disagree:\n%s\nand\n%s", drawn(a), drawn(b))
	}
	for who, ink := range map[string]*Ink{"a": a, "b": b} {
		for name, stroke := range map[string]StrokeID{"mine": mine, "theirs": theirs} {
			pts := ink.Points(stroke)
			if len(pts) != 5 {
				t.Fatalf("%s sees %d points of %s, want 5", who, len(pts), name)
			}
			// In the order they were drawn, which is what the sequence keeps.
			for n := range pts {
				want := at(float32(n), 0)
				if stroke == theirs {
					want = at(0, float32(n))
				}
				if pts[n] != want {
					t.Fatalf("%s sees point %d of %s as %v, want %v", who, n, name, pts[n], want)
				}
			}
		}
	}
}

// The order strokes are painted in is the order they were drawn, and a stroke
// can be moved above another without being redrawn.
func TestStrokesArePaintedInOrderAndCanBeReordered(t *testing.T) {
	ink := NewInk(1)
	first, second := mustBegin(t, ink), mustBegin(t, ink)
	mustExtend(t, ink, first, at(1, 1))
	mustExtend(t, ink, second, at(2, 2))

	if got, want := drawn(ink), "0: (1,1,1)\n1: (2,2,1)\n"; got != want {
		t.Fatalf("the drawing is\n%s\nwant\n%s", got, want)
	}
	// The first stroke to the top. It is one write, and no point moves.
	if _, err := ink.Strokes().Move(first, second); err != nil {
		t.Fatal(err)
	}
	if got, want := drawn(ink), "0: (2,2,1)\n1: (1,1,1)\n"; got != want {
		t.Fatalf("after raising the first stroke the drawing is\n%s\nwant\n%s", got, want)
	}
}

// A stroke carries whatever the pen had: a colour, a width.
func TestAStrokeCarriesItsPen(t *testing.T) {
	ink := NewInk(1)
	stroke := mustBegin(t, ink)
	if _, err := ink.Strokes().SetField(stroke, "colour", []byte("#c00")); err != nil {
		t.Fatal(err)
	}
	if got, ok := ink.Strokes().GetField(stroke, "colour"); !ok || string(got) != "#c00" {
		t.Fatalf("the colour reads %q", got)
	}
}

func TestErasingAndSweeping(t *testing.T) {
	ink := NewInk(1)
	keep, drop := mustBegin(t, ink), mustBegin(t, ink)
	mustExtend(t, ink, keep, at(1, 1), at(2, 2))
	mustExtend(t, ink, drop, at(3, 3), at(4, 4), at(5, 5))
	if ink.points.Len() != 5 {
		t.Fatalf("the drawing holds %d points, want 5", ink.points.Len())
	}

	// Erasing is one operation, whatever the stroke cost to draw.
	ops, err := ink.Erase(drop)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.Map) != 1 {
		t.Fatalf("erasing took %d operations", len(ops.Map))
	}
	if got, want := drawn(ink), "0: (1,1,1) (2,2,1)\n"; got != want {
		t.Fatalf("after erasing the drawing is\n%s\nwant\n%s", got, want)
	}
	// The points are still there until they are swept.
	if ink.points.Len() != 5 {
		t.Fatalf("erasing took %d points with it", 5-ink.points.Len())
	}
	if _, err := ink.Sweep(); err != nil {
		t.Fatal(err)
	}
	if ink.points.Len() != 2 {
		t.Fatalf("%d points are left after sweeping, want 2", ink.points.Len())
	}
	if got, want := drawn(ink), "0: (1,1,1) (2,2,1)\n"; got != want {
		t.Fatalf("the sweep changed the drawing to\n%s", got)
	}
	// Nothing left to sweep is not an operation.
	if _, err := ink.Sweep(); !errors.Is(err, ErrNoChange) {
		t.Fatalf("sweeping a swept drawing gave %v, want ErrNoChange", err)
	}
}

// The sweep finds a run of points that is not at the end of the drawing, and
// takes exactly it.
func TestTheSweepDeletesInRuns(t *testing.T) {
	ink := NewInk(1)
	first, middle, last := mustBegin(t, ink), mustBegin(t, ink), mustBegin(t, ink)
	mustExtend(t, ink, first, at(1, 1))
	mustExtend(t, ink, middle, at(2, 2), at(3, 3), at(4, 4))
	mustExtend(t, ink, last, at(5, 5))
	if _, err := ink.Erase(middle); err != nil {
		t.Fatal(err)
	}
	ops, err := ink.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	// A list deletion is one operation per element, so three points are three;
	// what is being checked is that they are the right three.
	if len(ops.List) != 3 {
		t.Fatalf("sweeping three points took %d operations, want 3", len(ops.List))
	}
	if ink.points.Len() != 2 {
		t.Fatalf("%d points are left, want 2", ink.points.Len())
	}
	if got, want := drawn(ink), "0: (1,1,1)\n1: (5,5,1)\n"; got != want {
		t.Fatalf("the drawing is\n%s\nwant\n%s", got, want)
	}
	_ = last
}

// A peer extending a stroke this replica has erased. The points arrive after the
// sweep, are not painted, and the next sweep takes them.
func TestAPeerExtendsAnErasedStroke(t *testing.T) {
	a := NewInk(1)
	stroke := mustBegin(t, a)
	mustExtend(t, a, stroke, at(1, 1))
	b, err := LoadInk(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	late, err := b.Extend(stroke, at(2, 2), at(3, 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Erase(stroke); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sweep(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(late); err != nil {
		t.Fatal(err)
	}
	if got := drawn(a); got != "" {
		t.Fatalf("an erased stroke is painted:\n%s", got)
	}
	if a.points.Len() != 2 {
		t.Fatalf("%d late points arrived, want 2", a.points.Len())
	}
	if _, err := a.Sweep(); err != nil {
		t.Fatal(err)
	}
	if a.points.Len() != 0 {
		t.Fatalf("%d points survived the second sweep", a.points.Len())
	}
}

func TestWhatInkRefuses(t *testing.T) {
	ink := NewInk(1)
	stroke := mustBegin(t, ink)
	gone := StrokeID{Site: 9, Seq: 9}

	if _, err := ink.Extend(gone, at(0, 0)); err == nil {
		t.Fatal("extending a stroke that does not exist was accepted")
	}
	if _, err := ink.Extend(StrokeID{}, at(0, 0)); err == nil {
		t.Fatal("extending nothing was accepted")
	}
	if _, err := ink.Extend(stroke); !errors.Is(err, ErrNoChange) {
		t.Fatalf("extending by no points gave %v, want ErrNoChange", err)
	}
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	for _, p := range []Point{{X: nan}, {Y: nan}, {Pressure: nan}, {X: inf}, {Y: inf}, {Pressure: inf}} {
		if _, err := ink.Extend(stroke, p); err == nil {
			t.Fatalf("a point at %v was accepted", p)
		}
	}
	// And a bad point among good ones takes none of them.
	if _, err := ink.Extend(stroke, at(1, 1), Point{X: nan}); err == nil {
		t.Fatal("a batch with one bad point was accepted")
	}
	if ink.points.Len() != 0 {
		t.Fatalf("%d points were stored by refused calls", ink.points.Len())
	}
	if _, err := ink.Erase(gone); err == nil {
		t.Fatal("erasing a stroke that does not exist was accepted")
	}
	if _, err := LoadInk(1, []byte("not a snapshot")); err == nil {
		t.Fatal("loading rubbish was accepted")
	}
	if ink.Composite() == nil || ink.Site() != 1 || ink.Pending() != 0 {
		t.Fatal("the drawing does not report what it is")
	}
	if doc := crdt.NewComposite(5); InkOf(doc).Site() != 5 {
		t.Fatal("InkOf does not read the composite it was given")
	}
	if got := ink.Points(gone); got != nil {
		t.Fatal("a stroke that does not exist has points")
	}
}

// A peer can write anything into the points list. Whatever it writes, the
// drawing still paints, and it paints the same on a second replica.
func TestRubbishInThePoints(t *testing.T) {
	ink := NewInk(1)
	stroke := mustBegin(t, ink)
	mustExtend(t, ink, stroke, at(1, 1))
	good := drawn(ink)

	for _, rubbish := range [][]byte{
		{0xFF},             // a site that never ends
		{1},                // a site and no sequence
		{1, 0xFF},          // a sequence that never ends
		{1, 0},             // a sequence of zero, which no site issues
		{1, 1},             // no coordinates
		{1, 1, 0, 0, 0, 0}, // not enough of them
		append([]byte{1, 1}, make([]byte, 13)...), // too many
	} {
		if _, err := ink.points.Insert(ink.points.Len(), rubbish); err != nil {
			t.Fatal(err)
		}
		got := drawn(ink)
		if got != good {
			t.Fatalf("%v changed the drawing to\n%s\nwant\n%s", rubbish, got, good)
		}
		other, err := LoadInk(2, ink.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if drawn(other) != got {
			t.Fatalf("after %v two replicas paint differently", rubbish)
		}
		// And the sweep does not choke on it, nor take the real stroke's points.
		if _, err := ink.Sweep(); err != nil && !errors.Is(err, ErrNoChange) {
			t.Fatal(err)
		}
		if got := drawn(ink); got != good {
			t.Fatalf("sweeping with %v in the points changed the drawing", rubbish)
		}
	}
}

func TestInkWithNoClockLeft(t *testing.T) {
	ink := NewInk(1)
	stroke := mustBegin(t, ink)
	mustExtend(t, ink, stroke, at(1, 1))
	spare := mustBegin(t, ink)

	topList := crdt.ListOp{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Value: []byte{1}}
	if err := ink.Apply(crdt.PartOps{Part: pointsPart, List: []crdt.ListOp{topList}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ink.Extend(stroke, at(2, 2)); err == nil {
		t.Fatal("extending with no clock left was accepted")
	}
	// Erasing is in the other part, which still has clock.
	if _, err := ink.Erase(spare); err != nil {
		t.Fatal(err)
	}
	// And the sweep needs the points part, which does not.
	if _, err := ink.Sweep(); err == nil {
		t.Fatal("sweeping with no clock left was accepted")
	}

	topMap := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Key: "seed", Value: []byte("x")}
	if err := ink.Apply(crdt.PartOps{Part: strokesPart, Map: []crdt.MapOp{topMap}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ink.Begin(); err == nil {
		t.Fatal("beginning a stroke with no clock left was accepted")
	}
	if _, err := ink.Erase(stroke); err == nil {
		t.Fatal("erasing with no clock left was accepted")
	}
}

// Many people drawing, delivered in different orders, all painting the same
// drawing.
func TestRandomisedDrawingConverges(t *testing.T) {
	for seed := range uint64(20) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			base := NewInk(1)
			shared := mustBegin(t, base)
			mustExtend(t, base, shared, at(0, 0))
			snapshot := base.Snapshot()

			const replicas = 4
			inks := make([]*Ink, replicas)
			for n := range inks {
				ink, err := LoadInk(crdt.SiteID(n+2), snapshot)
				if err != nil {
					t.Fatal(err)
				}
				inks[n] = ink
			}

			pending := make([][]crdt.PartOps, replicas)
			for n, ink := range inks {
				// Each replica draws its own stroke and extends the shared one,
				// which is where two pens meet on one path.
				own := mustBegin(t, ink)
				for k := range 4 {
					ops, err := ink.Extend(own, at(float32(n), float32(k)))
					if err != nil {
						t.Fatal(err)
					}
					pending[n] = append(pending[n], ops)
					ops, err = ink.Extend(shared, at(float32(k), float32(n)))
					if err != nil {
						t.Fatal(err)
					}
					pending[n] = append(pending[n], ops)
				}
				pending[n] = append(pending[n], must(ink.OpsSince(crdt.CompositeVersion(nil)))...)
			}

			for n, ink := range inks {
				var inbox []crdt.PartOps
				for m, ops := range pending {
					if m != n {
						inbox = append(inbox, ops...)
					}
				}
				rngShuffle(int(seed), inbox)
				if err := ink.Apply(inbox...); err != nil {
					t.Fatal(err)
				}
				if left := ink.Pending(); left != 0 {
					t.Fatalf("replica %d left %d operations parked", n, left)
				}
			}
			want := drawn(inks[0])
			for n, ink := range inks[1:] {
				if got := drawn(ink); got != want {
					t.Fatalf("replica %d paints\n%s\nreplica 0 paints\n%s", n+1, got, want)
				}
			}
			if !equalBytes(inks[0].Snapshot(), inks[1].Snapshot()) {
				t.Fatal("two replicas do not agree byte for byte")
			}
		})
	}
}

// rngShuffle mixes a slice deterministically from a seed, so a failing run can
// be repeated.
func rngShuffle(seed int, batches []crdt.PartOps) {
	state := uint64(seed)*6364136223846793005 + 1442695040888963407
	for i := len(batches) - 1; i > 0; i-- {
		state = state*6364136223846793005 + 1442695040888963407
		j := int((state >> 33) % uint64(i+1))
		batches[i], batches[j] = batches[j], batches[i]
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
