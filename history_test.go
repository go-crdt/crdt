package crdt

import "testing"

// replayChanges applies the changes to the text they were computed against, which is
// how a caller uses them and therefore the only check worth making: the report
// has to turn the old text into the new one.
func replayChanges(old string, changes []Change) string {
	r := []rune(old)
	for _, c := range changes {
		out := append([]rune{}, r[:c.Pos]...)
		out = append(out, []rune(c.Text)...)
		out = append(out, r[c.Pos+c.Removed:]...)
		r = out
	}
	return string(r)
}

func TestTextAtEachStepOfItsOwnHistory(t *testing.T) {
	d := New(1)
	var marks []VersionVector
	var texts []string
	note := func() {
		marks = append(marks, d.Version())
		texts = append(texts, d.String())
	}

	note() // the empty document
	if _, err := d.Insert(0, "hello world"); err != nil {
		t.Fatal(err)
	}
	note()
	if _, err := d.Delete(5, 6); err != nil { // " world"
		t.Fatal(err)
	}
	note()
	if _, err := d.Insert(5, ", there"); err != nil {
		t.Fatal(err)
	}
	note()
	if _, err := d.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	note()

	for i, v := range marks {
		if got := d.TextAt(v); got != texts[i] {
			t.Errorf("at mark %d the document read %q, want %q", i, got, texts[i])
		}
		if got := d.LenAt(v); got != len([]rune(texts[i])) {
			t.Errorf("at mark %d the length was %d, want %d", i, got, len([]rune(texts[i])))
		}
		// And what has happened since that mark turns that text into this one.
		if got := replayChanges(texts[i], d.ChangesSince(v)); got != d.String() {
			t.Errorf("replaying from mark %d gave %q, want %q", i, got, d.String())
		}
	}
	// The current version is the document itself, and nothing has happened since.
	if got := d.TextAt(d.Version()); got != d.String() {
		t.Fatalf("at the current version: %q", got)
	}
	if got := d.ChangesSince(d.Version()); len(got) != 0 {
		t.Fatalf("since the current version: %v", got)
	}
}

// Text written and taken away between the mark and now was in neither text, so
// it is reported in neither.
func TestWhatCameAndWentSinceIsInNeither(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "keep"); err != nil {
		t.Fatal(err)
	}
	mark := d.Version()

	if _, err := d.Insert(4, " temporary"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Delete(4, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Insert(4, "!"); err != nil {
		t.Fatal(err)
	}

	if got := d.TextAt(mark); got != "keep" {
		t.Fatalf("the mark reads %q", got)
	}
	changes := d.ChangesSince(mark)
	if got := replayChanges("keep", changes); got != "keep!" {
		t.Fatalf("replaying gave %q, want %q", got, "keep!")
	}
	// One edit, not three: what came and went is not in the report at all.
	if len(changes) != 1 || changes[0].Removed != 0 || changes[0].Text != "!" {
		t.Fatalf("the report is %v, want one insertion of \"!\"", changes)
	}
}

// A mark taken on one replica, read on another: the history is the operations,
// so it does not matter which replica keeps it.
func TestAMarkFromOneReplicaReadOnAnother(t *testing.T) {
	a, b := New(1), New(2)
	ops, err := a.Insert(0, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	mark := b.Version()

	// Both edit, and each hears the other.
	fromA, err := a.Insert(6, " by one")
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.Insert(0, "the ")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(fromB...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(fromA...); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatalf("the replicas disagree: %q and %q", a.String(), b.String())
	}

	// The mark reads the same on both, and so does what has happened since.
	if a.TextAt(mark) != "shared" || b.TextAt(mark) != "shared" {
		t.Fatalf("the mark reads %q on a and %q on b", a.TextAt(mark), b.TextAt(mark))
	}
	if got := replayChanges("shared", a.ChangesSince(mark)); got != a.String() {
		t.Fatalf("replaying on a gave %q, want %q", got, a.String())
	}
	if got := replayChanges("shared", b.ChangesSince(mark)); got != b.String() {
		t.Fatalf("replaying on b gave %q, want %q", got, b.String())
	}
}

// A version holding operations this replica has not seen is answered with what
// the two have in common, rather than refused.
func TestAVersionFromTheFuture(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "here"); err != nil {
		t.Fatal(err)
	}
	ahead := d.Version()
	ahead[SiteID(9)] = 100 // a site this document has never heard of

	if got := d.TextAt(ahead); got != "here" {
		t.Fatalf("a version from the future read %q", got)
	}
	if got := d.ChangesSince(ahead); len(got) != 0 {
		t.Fatalf("since a version from the future: %v", got)
	}
}

// Whatever the edits, replaying the report on the old text gives the new one.
func TestReplayingTheReportAlwaysArrives(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "the quick brown fox jumps over the lazy dog"); err != nil {
		t.Fatal(err)
	}
	seed := uint64(20260823)
	next := func(n int) int {
		if n <= 0 {
			return 0
		}
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	for round := range 120 {
		mark := d.Version()
		was := d.String()
		for range 3 {
			if d.Len() > 2 && next(2) == 0 {
				at := next(d.Len() - 1)
				if _, err := d.Delete(at, 1+next(min(4, d.Len()-at))); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if _, err := d.Insert(next(d.Len()+1), string(rune('a'+next(26)))); err != nil {
				t.Fatal(err)
			}
		}
		if got := d.TextAt(mark); got != was {
			t.Fatalf("round %d: the mark reads %q, want %q", round, got, was)
		}
		if got := replayChanges(was, d.ChangesSince(mark)); got != d.String() {
			t.Fatalf("round %d: replaying gave %q, want %q", round, got, d.String())
		}
	}
}

// A list is its own history too, by the same reasoning and the same walk.
func TestValuesAtEachStepOfItsOwnHistory(t *testing.T) {
	l := NewList(1)
	var marks []VersionVector
	var want [][]string
	note := func() {
		marks = append(marks, l.Version())
		var s []string
		for _, v := range l.Values() {
			s = append(s, string(v))
		}
		want = append(want, s)
	}

	note()
	if _, err := l.Insert(0, []byte("a"), []byte("b"), []byte("c")); err != nil {
		t.Fatal(err)
	}
	note()
	if _, err := l.Delete(1, 1); err != nil { // "b"
		t.Fatal(err)
	}
	note()
	if _, err := l.Insert(1, []byte("x")); err != nil {
		t.Fatal(err)
	}
	note()

	for i, v := range marks {
		var got []string
		for _, b := range l.ValuesAt(v) {
			got = append(got, string(b))
		}
		if len(got) != len(want[i]) {
			t.Fatalf("at mark %d the list held %v, want %v", i, got, want[i])
		}
		for k := range got {
			if got[k] != want[i][k] {
				t.Fatalf("at mark %d the list held %v, want %v", i, got, want[i])
			}
		}
		if n := l.LenAt(v); n != len(want[i]) {
			t.Fatalf("at mark %d the length was %d, want %d", i, n, len(want[i]))
		}
	}
	// An element the caller gets back is its own: changing it does not reach
	// into the list, as Values already promises.
	at := l.ValuesAt(marks[1])
	at[0][0] = 'Z'
	if got := string(l.ValuesAt(marks[1])[0]); got != "a" {
		t.Fatalf("the list handed out its own storage: %q", got)
	}
}
