package crdt

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// view is a copy of the text kept up to date only by applying reported changes,
// which is what an editor is. If the changes are wrong, it drifts.
type view []rune

func (v *view) apply(t *testing.T, changes []Change) {
	t.Helper()
	for _, c := range changes {
		if c.Pos < 0 || c.Pos+c.Removed > len(*v) {
			t.Fatalf("change %+v does not fit a text of %d characters", c, len(*v))
		}
		tail := append([]rune(nil), (*v)[c.Pos+c.Removed:]...)
		*v = append(append((*v)[:c.Pos], []rune(c.Text)...), tail...)
	}
}

// The property every editor binding rests on: a copy of the text that only ever
// applies the reported changes, in order, holds what the document holds.
func TestChangesReplayIntoTheSameText(t *testing.T) {
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 23))
		writer, watcher := New(1), New(2)
		var v view

		queued := []Op{}
		for range 14 {
			// The writer edits, sometimes several times before anything is sent.
			for range 1 + rng.IntN(3) {
				if writer.Len() > 0 && rng.IntN(3) == 0 {
					pos := rng.IntN(writer.Len())
					queued = append(queued, remove(t, writer, pos, 1+rng.IntN(writer.Len()-pos))...)
					continue
				}
				text := make([]rune, 1+rng.IntN(5))
				for i := range text {
					text[i] = alphabet[rng.IntN(len(alphabet))]
				}
				queued = append(queued, insert(t, writer, rng.IntN(writer.Len()+1), string(text))...)
			}
			// Some of it is delivered, shuffled, occasionally duplicated.
			n := 1 + rng.IntN(len(queued))
			batch := append([]Op{}, queued[:n]...)
			queued = queued[n:]
			rng.Shuffle(len(batch), func(i, j int) { batch[i], batch[j] = batch[j], batch[i] })
			if rng.IntN(3) == 0 {
				batch = append(batch, batch[rng.IntN(len(batch))])
			}

			changes, err := watcher.ApplyChanges(batch...)
			if err != nil {
				t.Fatalf("seed %d: ApplyChanges: %v", seed, err)
			}
			v.apply(t, changes)
			if got, want := string(v), watcher.String(); got != want {
				t.Fatalf("seed %d: the view holds %q, the document %q", seed, got, want)
			}
		}
		changes, err := watcher.ApplyChanges(queued...)
		if err != nil {
			t.Fatalf("seed %d: ApplyChanges: %v", seed, err)
		}
		v.apply(t, changes)
		if got, want := string(v), writer.String(); got != want {
			t.Fatalf("seed %d: the view holds %q, the writer %q", seed, got, want)
		}
	}
}

// A peer typing a word is one edit to a view, not one per letter.
func TestChangesCoalesceAStretch(t *testing.T) {
	writer, watcher := New(1), New(2)
	typed := insert(t, writer, 0, "hello")

	changes, err := watcher.ApplyChanges(typed...)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	if len(changes) != 1 || changes[0] != (Change{Pos: 0, Text: "hello"}) {
		t.Fatalf("typing five characters reported %+v, want one insertion of \"hello\"", changes)
	}

	// And deleting a stretch is one edit, however many operations it took.
	deleted := remove(t, writer, 1, 3)
	changes, err = watcher.ApplyChanges(deleted...)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	if len(changes) != 1 || changes[0] != (Change{Pos: 1, Removed: 3}) {
		t.Fatalf("deleting three characters reported %+v, want one removal of 3 at 1", changes)
	}
	if got, want := watcher.String(), "ho"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	// Edits that are not adjacent stay separate, and both are reported.
	writer2 := New(3)
	apply(t, writer2, watcher.OpsSince(nil))
	var scattered []Op
	scattered = append(scattered, insert(t, writer2, 0, "A")...)
	scattered = append(scattered, insert(t, writer2, 2, "B")...)
	changes, err = watcher.ApplyChanges(scattered...)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("two edits apart reported %+v, want two changes", changes)
	}
	if got, want := watcher.String(), "AhBo"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// Only what happened is reported: an operation already applied changes nothing,
// and one still waiting for its dependencies says nothing until it lands.
func TestChangesReportOnlyWhatHappened(t *testing.T) {
	writer, watcher := New(1), New(2)
	ops := insert(t, writer, 0, "abc")

	if changes, err := watcher.ApplyChanges(ops...); err != nil || len(changes) != 1 {
		t.Fatalf("ApplyChanges = %+v, %v; want one change", changes, err)
	}
	changes, err := watcher.ApplyChanges(ops...)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("re-applying reported %+v, want nothing", changes)
	}

	// An operation whose predecessor is missing waits, silently, and is reported
	// when the predecessor arrives.
	later := insert(t, writer, 3, "de")
	if changes, err := watcher.ApplyChanges(later[1]); err != nil || len(changes) != 0 {
		t.Fatalf("an operation that cannot be applied reported %+v, %v; want nothing", changes, err)
	}
	if got := watcher.Pending(); got != 1 {
		t.Fatalf("Pending() = %d, want 1", got)
	}
	changes, err = watcher.ApplyChanges(later[0])
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	if len(changes) != 1 || changes[0] != (Change{Pos: 3, Text: "de"}) {
		t.Fatalf("the waiting operation landed as %+v, want one insertion of \"de\" at 3", changes)
	}
	if got, want := watcher.String(), "abcde"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// Apply reports nothing, and a malformed batch changes nothing either way.
func TestApplyReportsNothing(t *testing.T) {
	writer, watcher := New(1), New(2)
	ops := insert(t, writer, 0, "quiet")
	if err := watcher.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := watcher.String(), "quiet"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	changes, err := watcher.ApplyChanges(Op{Kind: OpInsert, ID: ID{Seq: 9}, Clock: 0})
	if err == nil {
		t.Fatal("ApplyChanges accepted a malformed operation")
	}
	if changes != nil {
		t.Fatalf("a rejected batch reported %+v, want nothing", changes)
	}
}

func TestChangesFrom(t *testing.T) {
	tests := []struct {
		name       string
		from, want string
		change     []Change
	}{
		{"nothing to do", "same", "same", nil},
		{"both empty", "", "", nil},
		{"inserted in the middle", "ac", "abc", []Change{{Pos: 1, Text: "b"}}},
		{"removed from the middle", "abc", "ac", []Change{{Pos: 1, Removed: 1}}},
		{"replaced", "the quick fox", "the slow fox", []Change{{Pos: 4, Removed: 5, Text: "slow"}}},
		{"emptied", "gone", "", []Change{{Pos: 0, Removed: 4}}},
		{"filled", "", "new", []Change{{Pos: 0, Text: "new"}}},
		{"multibyte", "héllo 世界", "héllo 世", []Change{{Pos: 7, Removed: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChangesFrom(tt.from, tt.want)
			if len(got) != len(tt.change) {
				t.Fatalf("ChangesFrom(%q, %q) = %+v, want %+v", tt.from, tt.want, got, tt.change)
			}
			for i := range got {
				if got[i] != tt.change[i] {
					t.Fatalf("ChangesFrom(%q, %q) = %+v, want %+v", tt.from, tt.want, got, tt.change)
				}
			}
			// Whatever it returns has to actually turn one into the other.
			v := view([]rune(tt.from))
			v.apply(t, got)
			if string(v) != tt.want {
				t.Fatalf("applying %+v to %q gave %q, want %q", got, tt.from, string(v), tt.want)
			}
		})
	}

	// And on something longer than a hand-written case.
	rng := rand.New(rand.NewPCG(5, 5))
	for range 500 {
		from := randomText(rng, 40)
		to := randomText(rng, 40)
		v := view([]rune(from))
		v.apply(t, ChangesFrom(from, to))
		if string(v) != to {
			t.Fatalf("ChangesFrom(%q, %q) did not produce it: %q", from, to, string(v))
		}
	}
}

func randomText(rng *rand.Rand, max int) string {
	var b strings.Builder
	for range rng.IntN(max) {
		b.WriteRune(alphabet[rng.IntN(len(alphabet))])
	}
	return b.String()
}
