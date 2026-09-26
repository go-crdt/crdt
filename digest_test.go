package crdt

import (
	"strings"
	"testing"
)

// writeTo is the fixture's way of putting something in every kind of part, so a
// composite digest is exercised over all three rather than over text alone.
// Each part gets its OWN argument, so that a case can change exactly one of
// them. An earlier version passed `value` to both the list and the map, which
// made the "different list" case change the map too -- and a break check that
// removed the list's values from the digest stayed green, because the map was
// carrying the case.
func writeTo(t *testing.T, c *Composite, text, key, mapValue, listValue string) {
	t.Helper()
	doc, err := c.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Insert(0, text); err != nil {
		t.Fatal(err)
	}
	list, err := c.List("items")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := list.Insert(0, []byte(listValue)); err != nil {
		t.Fatal(err)
	}
	m, err := c.Map("meta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set(key, []byte(mapValue)); err != nil {
		t.Fatal(err)
	}
}

// TestAVersionVectorCanAgreeWhereADigestDoesNot is why this exists.
//
// Two replicas, one site name, two different histories under it. Their version
// vectors are IDENTICAL, because a version vector counts operations per site and
// both sites made the same number. So each concludes it is completely caught up
// with the other and neither will ever ask for anything again — which is the
// half of the failure that has no repair, and the half a signature would not
// have touched.
//
// The control is the second case, and without it the first proves nothing: any
// two different objects have different digests. What has to be shown is that
// digests agree exactly when the documents do.
func TestAVersionVectorCanAgreeWhereADigestDoesNot(t *testing.T) {
	// One case per kind of part, each differing in THAT KIND ALONE.
	//
	// The first version of this differed in all three at once, and passed with a
	// digest that hashed identities and no characters at all -- because the map
	// values still differed. A case that can be satisfied by a neighbouring part
	// is not a witness for the part it names.
	for _, tc := range []struct {
		kind             string
		mineArgs, theirs [4]string // text, key, map value, list value
	}{
		{"text", [4]string{"GENUINE", "who", "v", "same"}, [4]string{"FORGERY", "who", "v", "same"}},
		{"list", [4]string{"same", "who", "v", "ours"}, [4]string{"same", "who", "v", "eves"}},
		{"map", [4]string{"same", "who", "ours", "same"}, [4]string{"same", "who", "eves", "same"}},
	} {
		t.Run("same vector, different "+tc.kind, func(t *testing.T) {
			mine, theirs := NewComposite(7), NewComposite(7)
			writeTo(t, mine, tc.mineArgs[0], tc.mineArgs[1], tc.mineArgs[2], tc.mineArgs[3])
			writeTo(t, theirs, tc.theirs[0], tc.theirs[1], tc.theirs[2], tc.theirs[3])

			if !versionsMatch(mine.Version(), theirs.Version()) {
				t.Fatalf("the fixture does not reproduce the failure: versions differ\n %v\n %v",
					mine.Version(), theirs.Version())
			}
			if mine.Digest() == theirs.Digest() {
				t.Errorf("two documents differing in their %s share a digest: %v", tc.kind, mine.Digest())
			}
		})
	}

	t.Run("control: replicas that really converged agree", func(t *testing.T) {
		mine, theirs := NewComposite(1), NewComposite(2)
		writeTo(t, mine, "hers", "a", "1", "1")
		writeTo(t, theirs, "his", "b", "2", "2")

		if err := mine.Apply(theirs.OpsSince(mine.Version())...); err != nil {
			t.Fatal(err)
		}
		if err := theirs.Apply(mine.OpsSince(theirs.Version())...); err != nil {
			t.Fatal(err)
		}
		if got, want := mine.Digest(), theirs.Digest(); got != want {
			t.Errorf("replicas that converged disagree:\n %v\n %v", got, want)
		}
	})
}

// TestADigestDoesNotDependOnTheOrDerOperationsArrivedIn is the property that
// rules out the obvious implementation.
//
// Summarising subtrees of the index would be the natural way to build this, and
// it cannot be used: the AVL shape follows the order operations arrived in, so
// two replicas holding the same document need not hold the same tree. Walking
// the LIST is what makes this independent of that, and this is the test that
// would fail if somebody moved it onto the tree.
func TestADigestDoesNotDependOnTheOrderOperationsArrivedIn(t *testing.T) {
	// Three writers, so there is something to interleave.
	sources := make([][]PartOps, 0, 3)
	for i := range 3 {
		c := NewComposite(SiteID(i + 1))
		writeTo(t, c, strings.Repeat(string(rune('a'+i)), 4), string(rune('k'+i)), "v", "w")
		sources = append(sources, c.OpsSince(nil))
	}

	forwards, backwards := NewComposite(99), NewComposite(99)
	for _, batch := range sources {
		if err := forwards.Apply(batch...); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(sources) - 1; i >= 0; i-- {
		if err := backwards.Apply(sources[i]...); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := backwards.Digest(), forwards.Digest(); got != want {
		t.Errorf("the digest followed the arrival order:\n forwards  %v\n backwards %v", want, got)
	}
	// And the fixture has to have produced one document, or the above compares
	// two empty things.
	if forwards.Parts() == nil {
		t.Fatal("the fixture built nothing")
	}
}

// TestADigestSurvivesPurgeAndCollect pins what makes a digest of state possible
// at all: both ways of discarding take only what a reader cannot see.
func TestADigestSurvivesPurgeAndCollect(t *testing.T) {
	c := NewComposite(1)
	doc, err := c.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	// Two runs, so one of them can be deleted WHOLE: Purge takes a run only
	// when every character of it is already deleted, and a stretch deleted
	// inside one run leaves that run partly alive.
	if _, err := doc.Insert(0, "keepthat"); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Insert(4, "GONE"); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Delete(4, 8); err != nil {
		t.Fatal(err)
	}
	m, err := c.Map("meta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("live", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("dead", []byte("no")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete("dead"); err != nil {
		t.Fatal(err)
	}

	before, text := c.Digest(), doc.String()

	if n := doc.Purge(); n == 0 {
		t.Fatal("Purge discarded nothing, so this does not exercise it")
	}
	if n := m.Collect(m.Version(), MaxClock); n == 0 {
		t.Fatal("Collect discarded nothing, so this does not exercise it")
	}
	if got := doc.String(); got != text {
		t.Fatalf("discarding changed the visible text: %q, want %q", got, text)
	}
	if after := c.Digest(); after != before {
		t.Errorf("the digest moved across a purge and a collect:\n before %v\n after  %v", before, after)
	}
}

// TestADigestTellsKindsAndFieldsApart covers the two ways a digest can agree
// where it should not: two different kinds of part holding the same bytes, and
// two adjacent values that would run together without their lengths.
func TestADigestTellsKindsAndFieldsApart(t *testing.T) {
	t.Run("kinds", func(t *testing.T) {
		list, err := NewComposite(1).List("x")
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewComposite(1).Map("x")
		if err != nil {
			t.Fatal(err)
		}
		if list.Digest() == m.Digest() {
			t.Error("an empty list and an empty map share a digest")
		}
		doc, err := NewComposite(1).Text("x")
		if err != nil {
			t.Fatal(err)
		}
		if doc.Digest() == list.Digest() {
			t.Error("an empty text and an empty list share a digest")
		}
	})

	t.Run("fields", func(t *testing.T) {
		// "ab" then "" against "a" then "b": the same bytes in the same order,
		// told apart only by the lengths written before them.
		one, err := NewComposite(1).Map("x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := one.Set("k", []byte("ab")); err != nil {
			t.Fatal(err)
		}
		two, err := NewComposite(1).Map("x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := two.Set("ka", []byte("b")); err != nil {
			t.Fatal(err)
		}
		if one.Digest() == two.Digest() {
			t.Error(`{"k":"ab"} and {"ka":"b"} share a digest: the fields are not framed`)
		}
	})

	t.Run("a digest reads as hex", func(t *testing.T) {
		doc, err := NewComposite(1).Text("x")
		if err != nil {
			t.Fatal(err)
		}
		if got := doc.Digest().String(); len(got) != 64 {
			t.Errorf("Digest.String() = %q, want 64 hex characters", got)
		}
	})
}

// versionsMatch is the fixture's own comparison, so that the failure it
// reproduces is stated rather than assumed.
func versionsMatch(a, b CompositeVersion) bool {
	if len(a) != len(b) {
		return false
	}
	for part, vv := range a {
		other, ok := b[part]
		if !ok || !vv.Equal(other) {
			return false
		}
	}
	return true
}

// TestATombstoneIsNotVisibleToADigest completes the pattern the other two kinds
// already hold: what a reader cannot see does not reach the digest.
//
// A list never discards anything -- it has no Purge and no Collect -- so its
// tombstones stay for ever, and a replica that has deleted an element must still
// agree with one that holds the same visible sequence. Otherwise two replicas
// that genuinely converged would report a disagreement, which is worse than the
// failure this is for: an alarm nobody can act on gets switched off.
func TestATombstoneIsNotVisibleToADigest(t *testing.T) {
	withTombstone, err := NewComposite(1).List("items")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withTombstone.Insert(0, []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if _, err := withTombstone.Insert(1, []byte("gone")); err != nil {
		t.Fatal(err)
	}
	if _, err := withTombstone.Delete(1, 1); err != nil {
		t.Fatal(err)
	}

	clean, err := NewComposite(1).List("items")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clean.Insert(0, []byte("keep")); err != nil {
		t.Fatal(err)
	}

	if withTombstone.Len() != clean.Len() {
		t.Fatalf("the fixture does not compare like with like: %d present against %d",
			withTombstone.Len(), clean.Len())
	}
	if got, want := withTombstone.Digest(), clean.Digest(); got != want {
		t.Errorf("a deleted element reached the digest:\n with tombstone %v\n without       %v", got, want)
	}
}
