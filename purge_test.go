package crdt

import "testing"

// revisedText writes a document the way one is written, so that whole runs die.
func revisedText(t *testing.T, edits int) *Doc {
	t.Helper()
	const line = "a sentence somebody wrote, and then thought about again. "
	doc := New(1)
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

// Purging discards characters nobody can read and changes nothing anybody can.
func TestPurgeKeepsWhatTheDocumentSays(t *testing.T) {
	doc := revisedText(t, 600)
	want, tombs := doc.String(), doc.Tombstones()
	n := doc.Purge()
	if n == 0 {
		t.Fatal("nothing was purged from a document two thirds of which is deleted")
	}
	if got := doc.String(); got != want {
		t.Fatalf("purging changed the text: %d characters became %d", len([]rune(want)), len([]rune(got)))
	}
	if doc.Len() != len([]rune(want)) {
		t.Fatalf("length %d, want %d", doc.Len(), len([]rune(want)))
	}
	if doc.Tombstones() != tombs {
		t.Fatalf("%d tombstones after purging, want %d — the identities have to stay",
			doc.Tombstones(), tombs)
	}
	t.Logf("purged %d characters of %d", n, tombs)

	// And it still takes edits at both ends and in the middle.
	for _, at := range []int{0, doc.Len(), doc.Len() / 2} {
		if _, err := doc.Insert(at, "|"); err != nil {
			t.Fatalf("editing at %d after purging: %v", at, err)
		}
	}
	// Purging twice takes nothing the second time.
	if again := doc.Purge(); again != 0 {
		t.Fatalf("purging again discarded %d characters", again)
	}
}

// A purged document has to survive being written down and read back, with the
// characters still missing and everything else where it was.
func TestPurgedDocumentReloads(t *testing.T) {
	doc := revisedText(t, 300)
	before := len(doc.Snapshot())
	n := doc.Purge()
	if n == 0 {
		t.Fatal("nothing was purged")
	}
	after := len(doc.Snapshot())
	if after >= before {
		t.Fatalf("the snapshot did not shrink: %d bytes became %d", before, after)
	}
	t.Logf("purged %d characters; %d bytes became %d (%.2fx)", n, before, after, float64(before)/float64(after))

	back, err := Load(2, doc.Snapshot())
	if err != nil {
		t.Fatalf("a purged document did not reload: %v", err)
	}
	if back.String() != doc.String() {
		t.Fatal("the reloaded document says something else")
	}
	if back.Tombstones() != doc.Tombstones() {
		t.Fatalf("the reloaded document holds %d tombstones, want %d",
			back.Tombstones(), doc.Tombstones())
	}
	// It re-encodes to the same bytes: purging is part of what a snapshot says.
	if string(back.Snapshot()) != string(doc.Snapshot()) {
		t.Fatal("re-encoding a purged document did not reproduce it")
	}
	// And it still takes work from a peer that never purged.
	peer, err := Load(3, doc.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	ops, err := peer.Insert(peer.Len(), " and more")
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Apply(ops...); err != nil {
		t.Fatalf("a purged document refused a peer's work: %v", err)
	}
	if back.Pending() != 0 {
		t.Fatalf("%d operations were stranded", back.Pending())
	}
	if back.String() != peer.String() {
		t.Fatal("the two disagree after an ordinary edit")
	}
}
