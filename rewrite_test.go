package crdt

import (
	"errors"
	"fmt"
	"testing"
)

// revised builds a text that was written and partly unwritten, which is the only
// shape where a rewrite has anything to win.
func revised(t *testing.T, site SiteID, edits int) *Doc {
	t.Helper()
	const line = "a sentence somebody wrote, and then thought about again. "
	doc := New(site)
	for n := 0; n < edits; {
		if _, err := doc.Insert(doc.Len(), line); err != nil {
			t.Fatal(err)
		}
		n++
		if n%3 == 0 && doc.Len() >= len(line) {
			if _, err := doc.Delete(0, len(line)); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	return doc
}

// A rewrite has to keep every character, or it is not a rewrite.
func TestRewrittenKeepsTheContent(t *testing.T) {
	doc := revised(t, 1, 600)
	fresh, err := doc.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fresh.String(), doc.String(); got != want {
		t.Fatalf("the rewrite changed the text: %d characters became %d", len(want), len(got))
	}
	if got, want := fresh.Len(), doc.Len(); got != want {
		t.Fatalf("length %d, want %d", got, want)
	}
	if fresh.Site() != 2 {
		t.Fatalf("the rewrite kept site %d, so its characters could collide with the original's", fresh.Site())
	}
}

// And it has to be smaller, in proportion to what was deleted.
func TestRewrittenIsSmallerByWhatWasDeleted(t *testing.T) {
	doc := revised(t, 1, 3000)
	fresh, err := doc.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	before, after := len(doc.Snapshot()), len(fresh.Snapshot())
	if after >= before {
		t.Fatalf("the rewrite saved nothing: %d bytes became %d", before, after)
	}
	t.Logf("%d bytes of history became %d bytes of text (%.1fx)",
		before, after, float64(before)/float64(after))

	// A document nothing was removed from has nothing to win, and must not lose.
	untouched := New(1)
	for i := 0; i < 500; i++ {
		if _, err := untouched.Insert(untouched.Len(), "some text that stays. "); err != nil {
			t.Fatal(err)
		}
	}
	plain, err := untouched.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	if plain.String() != untouched.String() {
		t.Fatal("rewriting a document with no deletions changed its text")
	}
}

// The price, demonstrated: a replica still holding the old identities cannot
// merge into the rewrite. Its operations are not rejected and they do not
// corrupt anything — they anchor to characters the rewrite never had, so they
// park, and stay parked. This is why a rewrite replaces the old replica rather
// than joining it.
func TestRewrittenCannotBeMergedWithTheOldReplica(t *testing.T) {
	doc := revised(t, 1, 300)
	fresh, err := doc.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	text := fresh.String()

	// Someone still on the old replica keeps typing.
	kept, err := doc.Insert(doc.Len(), "written after the rewrite. ")
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Apply(kept...); err != nil {
		t.Fatalf("the rewrite rejected the old replica's work outright: %v", err)
	}
	if fresh.Pending() == 0 {
		t.Fatal("the old replica's operations were absorbed; the rewrite is supposed to be unable to place them")
	}
	if got := fresh.String(); got != text {
		t.Fatalf("parked operations changed the text anyway:\n got %q\nwant %q", got, text)
	}
	t.Logf("%d operations from the old replica parked, and the text is untouched", fresh.Pending())
}

// The other two primitives, on the same terms.
func TestRewrittenList(t *testing.T) {
	list := NewList(1)
	for i := 0; i < 400; i++ {
		if _, err := list.Insert(list.Len(), []byte(fmt.Sprintf("value %d", i))); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 && list.Len() > 0 {
			if _, err := list.Delete(0, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	fresh, err := list.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	want, got := list.Values(), fresh.Values()
	if len(got) != len(want) {
		t.Fatalf("the rewrite holds %d values, want %d", len(got), len(want))
	}
	for i := range want {
		if string(got[i]) != string(want[i]) {
			t.Fatalf("value %d is %q, want %q", i, got[i], want[i])
		}
	}
	if before, after := len(list.Snapshot()), len(fresh.Snapshot()); after >= before {
		t.Fatalf("the rewrite saved nothing: %d bytes became %d", before, after)
	}
}

func TestRewrittenMap(t *testing.T) {
	m := NewMap(1)
	for i := 0; i < 200; i++ {
		if _, err := m.Set(fmt.Sprintf("key-%d", i%50), []byte(fmt.Sprintf("value %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := m.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(fresh.Keys()), len(m.Keys()); got != want {
		t.Fatalf("the rewrite holds %d keys, want %d", got, want)
	}
	for _, key := range m.Keys() {
		want, _ := m.Get(key)
		got, ok := fresh.Get(key)
		if !ok {
			t.Fatalf("the rewrite lost key %q", key)
		}
		if string(got) != string(want) {
			t.Fatalf("key %q is %q, want %q", key, got, want)
		}
	}
}

// An empty document rewrites to an empty document, not to an error.
func TestRewrittenEmpty(t *testing.T) {
	doc := New(1)
	if _, err := doc.Insert(0, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Delete(0, 4); err != nil {
		t.Fatal(err)
	}
	fresh, err := doc.Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.String() != "" {
		t.Fatalf("an emptied document rewrote to %q", fresh.String())
	}
	list, err := NewList(1).Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	if list.Len() != 0 {
		t.Fatal("an empty list rewrote to something")
	}
	m, err := NewMap(1).Rewritten(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Keys()) != 0 {
		t.Fatal("an empty map rewrote to something")
	}
}

// A rewrite needs sequence numbers to mint the new copy with, and a replica that
// has run out of them has to say so rather than hand back a half-written
// document.
func TestRewrittenReportsAnExhaustedClock(t *testing.T) {
	doc := revised(t, 1, 30)
	full := New(2)
	if err := full.Apply(remoteInsert(3, MaxClock)); err != nil {
		t.Fatalf("Apply(clock=MaxClock) = %v, want it accepted", err)
	}
	if _, err := doc.rewriteInto(full); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Doc rewrite into an exhausted replica = %v, want ErrExhausted", err)
	}

	list := NewList(1)
	if _, err := list.Insert(0, []byte("a value")); err != nil {
		t.Fatal(err)
	}
	fullList := NewList(2)
	if err := fullList.Apply(ListOp{Kind: OpInsert, ID: ID{Site: 3, Seq: 1}, Clock: MaxClock, Value: []byte("x")}); err != nil {
		t.Fatalf("List.Apply(clock=MaxClock) = %v, want it accepted", err)
	}
	if _, err := list.rewriteInto(fullList); !errors.Is(err, ErrExhausted) {
		t.Fatalf("List rewrite into an exhausted replica = %v, want ErrExhausted", err)
	}

	m := NewMap(1)
	if _, err := m.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	fullMap := NewMap(2)
	if err := fullMap.Apply(MapOp{Kind: MapSet, ID: ID{Site: 3, Seq: 1}, Clock: MaxClock, Key: "k"}); err != nil {
		t.Fatalf("Map.Apply(clock=MaxClock) = %v, want it accepted", err)
	}
	if _, err := m.rewriteInto(fullMap); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Map rewrite into an exhausted replica = %v, want ErrExhausted", err)
	}
}
