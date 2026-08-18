package crdt

import "testing"

// The two indexes, asked the same questions on the same document.
//
// Substituting an index is the change that can corrupt a document without
// saying anything: a wrong answer to "which run holds position 4 000" is a
// character inserted in the wrong place, which converges perfectly and is
// simply not what anybody typed. So the B-tree is built beside the AVL index
// over a real editing history and asked every question the AVL index answers,
// at every position, before either replaces the other.
//
// The trace is 259 778 edits at positions a real person chose. Every insert,
// every deletion, every split — and after all of it, every position in the
// document.
func TestTheTwoIndexesAgree(t *testing.T) {
	shadowIndex = true
	t.Cleanup(func() { shadowIndex = false })

	patches, _ := loadTrace(t)
	d := New(1)
	replay(t, d, patches)

	if d.shadow == nil {
		t.Fatal("the shadow index was not built")
	}
	d.shadow.check(t)

	// The same runs, in the same order.
	var walked []*block
	for b := d.head.next; b != nil; b = b.next {
		walked = append(walked, b)
	}
	got := d.shadow.order()
	// The shadow holds the sentinel too, which the list walk above starts past.
	if len(got) != len(walked)+1 {
		t.Fatalf("the B-tree holds %d runs, the list holds %d", len(got), len(walked)+1)
	}
	for i, b := range walked {
		if got[i+1] != b {
			t.Fatalf("run %d differs between the two", i)
		}
	}

	// The same total, which is the summary every descent is a partial sum of.
	if int(d.shadow.root.vis) != d.Len() {
		t.Fatalf("the B-tree counts %d visible characters, the document holds %d",
			d.shadow.root.vis, d.Len())
	}

	// And the same answer at every position.
	for pos := range d.Len() {
		wantBlock, wantOff := d.seek(pos)
		gotBlock, gotOff := d.shadow.seek(pos)
		if gotBlock != wantBlock || gotOff != wantOff {
			t.Fatalf("position %d: the B-tree found a different run, offset %d against %d",
				pos, gotOff, wantOff)
		}
	}
	// And the UTF-16 descent, which is the other question the index answers:
	// how many of the first pos characters take two code units. A wrong answer
	// here is an editor putting a caret inside an emoji.
	for pos := range d.Len() {
		if want, gotSup := d.supBefore(pos), d.shadow.supBefore(pos); gotSup != want {
			t.Fatalf("position %d: the B-tree counts %d supplementary characters before it, want %d",
				pos, gotSup, want)
		}
	}

	t.Logf("%d runs, %d visible characters, every position agreed on both questions; the B-tree is %d deep",
		len(got), d.Len(), depthOf(d.shadow.root))
}

// The same comparison on documents the trace does not produce: emoji, deletions
// that empty a run, two replicas editing at once, and inserts at positions
// chosen to force splits in the middle rather than at the end.
//
// The trace is one shape — one person, typing mostly forwards, in Latin script.
// An index that agreed only there would be an index that agreed by accident.
func TestTheTwoIndexesAgreeOnAwkwardDocuments(t *testing.T) {
	shadowIndex = true
	t.Cleanup(func() { shadowIndex = false })

	seed := uint64(20260819)
	rnd := func(n int) int {
		if n <= 0 {
			return 0
		}
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	// Characters of one and two UTF-16 units, so the supplementary summaries
	// are exercised rather than left at zero.
	alphabet := []rune{'a', 'b', 'é', '世', '😀', '𝔸', '\n'}

	for round := range 40 {
		a, b := New(1), New(2)
		for step := range 120 {
			// Both replicas edit, and each sends what it did to the other, so
			// the runs are split by concurrent work rather than only appended.
			for _, pair := range [][2]*Doc{{a, b}, {b, a}} {
				from, to := pair[0], pair[1]
				var ops []Op
				var err error
				if from.Len() > 0 && rnd(3) == 0 {
					at := rnd(from.Len())
					ops, err = from.Delete(at, 1+rnd(min(4, from.Len()-at)))
				} else {
					var text []rune
					for range 1 + rnd(5) {
						text = append(text, alphabet[rnd(len(alphabet))])
					}
					ops, err = from.Insert(rnd(from.Len()+1), string(text))
				}
				if err != nil {
					t.Fatalf("round %d step %d: %v", round, step, err)
				}
				if err := to.Apply(ops...); err != nil {
					t.Fatalf("round %d step %d: %v", round, step, err)
				}
			}
		}

		for _, d := range []*Doc{a, b} {
			d.shadow.check(t)
			if int(d.shadow.root.vis) != d.Len() {
				t.Fatalf("round %d: the B-tree counts %d, the document holds %d",
					round, d.shadow.root.vis, d.Len())
			}
			for pos := range d.Len() {
				wantBlock, wantOff := d.seek(pos)
				gotBlock, gotOff := d.shadow.seek(pos)
				if gotBlock != wantBlock || gotOff != wantOff {
					t.Fatalf("round %d, position %d: the two indexes disagree", round, pos)
				}
				if want, got := d.supBefore(pos), d.shadow.supBefore(pos); got != want {
					t.Fatalf("round %d, position %d: %d supplementary against %d",
						round, pos, got, want)
				}
			}
		}
	}
}
