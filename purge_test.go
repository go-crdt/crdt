package crdt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

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

// A purged run whose characters are not all deleted is refused rather than
// repaired: a character with nothing in it would otherwise be visible, and
// nothing sound could have written one.
//
// This is the reader's own rejection, and until the fixture builder could set
// the flag nothing could reach it — every other test in this file round-trips a
// document that [Doc.Purge] produced, which is well formed by construction.
func TestLoadRefusesAPurgedRunThatIsStillPartlyAlive(t *testing.T) {
	// The control comes from the only thing that writes these for real, so that
	// the rejection below cannot be passing merely because the flag was set.
	doc := revisedText(t, 40)
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}
	if _, err := Load(2, doc.Snapshot()); err != nil {
		t.Fatalf("a document Purge wrote did not load: %v", err)
	}

	// Four characters, one deletion covering one of them, and the run claiming
	// its characters were discarded. The other three have nothing behind them.
	partly := wellFormedRun()
	partly.purgedBelow = 4 // the floor a purge of this run would have left
	partly.runs[0].purged = true
	partly.runs[0].text = nil // a purged run costs nothing in the text column
	partly.runs[0].length = 4
	if _, err := Load(2, partly.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a partly deleted purged run loaded with %v, want ErrMalformed", err)
	}
}

// A document that reloads has to remember what it gave up.
//
// Without this the floor comes back as zero, and readable would answer that
// every version is still serveable — telling a caller it can serve a peer whose
// history it has thrown away. Not reachable while nothing calls readable, and
// exactly the defect a map's collection floor had until it was written down.
func TestThePurgeFloorSurvivesASnapshot(t *testing.T) {
	doc := revisedText(t, 40)
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}
	before := doc.PurgedBelow()
	if before == 0 {
		t.Fatal("a document that purged reports a floor of zero")
	}

	back, err := Load(2, doc.Snapshot())
	if err != nil {
		t.Fatalf("a purged document did not reload: %v", err)
	}
	if got := back.PurgedBelow(); got != before {
		t.Fatalf("PurgedBelow() = %d after a round trip, want %d", got, before)
	}

	// And a document that never purged still says so, rather than inheriting a
	// floor from the field being written at all.
	fresh := revisedText(t, 4)
	plain, err := Load(2, fresh.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.PurgedBelow(); got != 0 {
		t.Fatalf("a document that never purged reloaded with a floor of %d", got)
	}
}

// A floor above the clock ceiling names a clock no operation could carry.
func TestLoadRefusesAPurgeFloorAboveTheCeiling(t *testing.T) {
	tooHigh := wellFormedRun()
	tooHigh.purgedBelow = MaxClock + 1
	if _, err := Load(2, tooHigh.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a floor above the ceiling loaded with %v, want ErrMalformed", err)
	}
}

// What CanServe promises, pinned directly. Its caller is a server, and this
// stands in for one until there is one; TestAServerAsksBeforeItServes below is
// the same thing written as the loop a server actually runs.
func TestCanServeAnswersForAVersionBehindThePurge(t *testing.T) {
	// A document that has purged nothing can serve anybody, including a version
	// that has seen nothing at all.
	fresh := revisedText(t, 4)
	if err := fresh.CanServe(VersionVector{}); err != nil {
		t.Fatalf("a document that purged nothing refused the empty version: %v", err)
	}

	doc := revisedText(t, 40)
	// The version as it stood when everything was still there. Taken before the
	// purge, so it is exactly a peer that stopped listening early.
	early := doc.Version().Clone()
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}

	// That peer saw the insertions and, for at least one purged character, not
	// the deletion that removed it -- so it cannot be served.
	behind := VersionVector{}
	for site, seq := range early {
		behind[site] = seq / 2
	}
	if err := doc.CanServe(behind); !errors.Is(err, ErrPurged) {
		t.Fatalf("a version from before the deletions = %v, want ErrPurged", err)
	}

	// And the one that has seen nothing at all, which is the case the weaker
	// condition let through: it was never shown a purged character, so nothing
	// purged was visible in it, and everything purged is what it is owed.
	if err := doc.CanServe(VersionVector{}); !errors.Is(err, ErrPurged) {
		t.Fatalf("the empty version = %v, want ErrPurged", err)
	}

	// And the version the document itself is at has every operation the purge
	// took, so there is nothing to be owed.
	if err := doc.CanServe(doc.Version()); err != nil {
		t.Fatalf("a document refused its own version: %v", err)
	}
}

// CanServe asks about the insertion as well as the deletion, and the insertion
// half is the one a document written by a single site can say nothing about.
//
// On such a document the two are inseparable: a site deletes only what it has
// already inserted, so the deletion always carries the higher sequence number
// and a version that includes it includes the insertion too. Every case above
// is written that way, and with them alone the insertion half of the condition
// can be deleted outright and nothing fails -- which was true of this package
// until this test.
//
// It takes two sites to separate them, and the peer it describes is real rather
// than contrived: a deletion whose insertion has not arrived is parked rather
// than refused, so a replica genuinely reaches a version holding one and not the
// other.
func TestCanServeAsksAboutTheInsertionAsWellAsTheDeletion(t *testing.T) {
	writer := New(1)
	if _, err := writer.Insert(0, "abcd"); err != nil {
		t.Fatal(err)
	}
	deleter := New(2)
	if err := deleter.Apply(writer.OpsSince(nil)...); err != nil {
		t.Fatal(err)
	}
	if _, err := deleter.Delete(0, 4); err != nil {
		t.Fatal(err)
	}
	if err := writer.Apply(deleter.OpsSince(writer.Version())...); err != nil {
		t.Fatal(err)
	}
	if writer.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}

	// The peer that has parked the deletions: it holds everything site 2 issued
	// and nothing of site 1's. What the purge took is exactly what it is owed.
	parked := VersionVector{2: writer.Version()[2]}
	if err := writer.CanServe(parked); !errors.Is(err, ErrPurged) {
		t.Fatalf("a peer holding the deletions and none of the insertions = %v, want ErrPurged", err)
	}
	// And its mirror, so that the case above cannot be passing on the other
	// half of the condition.
	unaware := VersionVector{1: writer.Version()[1]}
	if err := writer.CanServe(unaware); !errors.Is(err, ErrPurged) {
		t.Fatalf("a peer holding the insertions and none of the deletions = %v, want ErrPurged", err)
	}
	// And a peer holding both is served, so neither refusal above is the
	// document refusing everybody.
	if err := writer.CanServe(writer.Version()); err != nil {
		t.Fatalf("a peer holding both halves = %v, want nil", err)
	}
}

// The consumer, written as a server writes it: ask, and send a snapshot to a
// peer the answer refuses.
//
// It measures the failure first, because a guard is only worth what it prevents:
// a fresh peer sent what OpsSince returns across a purge parks every operation
// of it and holds none of the text.
func TestAServerAsksBeforeItServes(t *testing.T) {
	doc := revisedText(t, 40)
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}

	// Serving without asking, which is what there was no way to avoid.
	naive := New(9)
	owed := doc.OpsSince(naive.Version())
	if err := naive.Apply(owed...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if naive.Pending() != len(owed) || naive.String() != "" {
		t.Fatalf("serving a fresh peer across a purge left %d of %d operations parked and %q of text; "+
			"if this no longer fails, the refusal below is guarding nothing",
			naive.Pending(), len(owed), naive.String())
	}
	t.Logf("without asking: %d operations sent, %d parked", len(owed), naive.Pending())

	// What a server does instead. The peer is caught up by whichever arm the
	// answer chooses, and the two arms have to agree about the document.
	serve := func(t *testing.T, peer *Doc) *Doc {
		t.Helper()
		if err := doc.CanServe(peer.Version()); errors.Is(err, ErrPurged) {
			caught, err := Load(peer.Site(), doc.Snapshot())
			if err != nil {
				t.Fatalf("loading the snapshot a refused peer is sent: %v", err)
			}
			return caught
		} else if err != nil {
			t.Fatalf("CanServe: %v", err)
		}
		if err := peer.Apply(doc.OpsSince(peer.Version())...); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		return peer
	}

	// A peer that has seen nothing takes the snapshot arm.
	caught := serve(t, New(9))
	if caught.String() != doc.String() || caught.Pending() != 0 {
		t.Fatalf("the peer reads %q with %d parked, want %q with none",
			caught.String(), caught.Pending(), doc.String())
	}

	// And once it holds everything the purge took, the operations arm serves it:
	// an edit made after the purge reaches it as operations rather than as a
	// second snapshot.
	if _, err := doc.Insert(0, "more"); err != nil {
		t.Fatal(err)
	}
	if err := doc.CanServe(caught.Version()); err != nil {
		t.Fatalf("a peer that holds everything purged was refused: %v", err)
	}
	again := serve(t, caught)
	if again != caught {
		t.Fatal("a peer that could be served was sent a snapshot")
	}
	if again.String() != doc.String() || again.Pending() != 0 {
		t.Fatalf("after the edit the peer reads %q with %d parked, want %q with none",
			again.String(), again.Pending(), doc.String())
	}
}

// purgedRun is a hand-built snapshot of one run of four characters, all deleted
// and then purged. It is byte-for-byte what Doc.Purge writes for that document,
// which is how it was arrived at rather than by reasoning about the format.
//
// Two things are easy to get wrong and were: a deletion's span counts
// consecutive deletion *operations*, so the version vector has to promise
// seq+span-1 rather than seq; and a purged run writes no text at all, its length
// living in the lengths column with the version vector as its only bound.
func purgedRun() runBuilder {
	b := wellFormedRun()
	b.sites = [][2]uint64{{1, 8}} // four characters and four deletions
	b.purgedBelow = 4
	b.runs[0].purged = true
	b.runs[0].text = nil
	b.runs[0].length = 4
	b.runs[0].dels = [][4]uint64{{0, 4, 1, 5}} // gap 0, four of them, from 5@1
	return b
}

// A hand-built purged run loads, and says what the document it came from says.
func TestLoadAcceptsAHandBuiltPurgedRun(t *testing.T) {
	d, err := Load(2, purgedRun().build())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := d.String(); got != "" {
		t.Fatalf("String() = %q, want the empty string", got)
	}
	if got, want := d.Tombstones(), 4; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}
	if got, want := d.PurgedBelow(), uint64(4); got != want {
		t.Fatalf("PurgedBelow() = %d, want %d", got, want)
	}
	// It is the same document Purge writes, which is the point of the fixture.
	real := New(1)
	if _, err := real.Insert(0, "abcd"); err != nil {
		t.Fatal(err)
	}
	if _, err := real.Delete(0, 4); err != nil {
		t.Fatal(err)
	}
	real.Purge()
	if !bytes.Equal(purgedRun().build(), real.Snapshot()) {
		t.Fatal("the fixture is not what Purge writes")
	}
}

// A purged run may not reach past what its site has issued: with no text to
// bound it, the version vector is the only thing that does.
//
// The first two cases below would be refused with or without that bound, by the
// ledger a few lines later -- a run reaching past its site names operations the
// vector never promised, and a snapshot that does not account for exactly the
// operations it claims is refused whole. What the bound changes is *when*, and
// the third case is the one that shows it: a purged run costs nothing in the
// text column, so a crafted length is not held by the bytes the way every other
// run's is, and reading one means writing out that many characters before
// anything is checked. Take the bound away and that case does not fail, it
// hangs. This was measured by taking it away: the case ran past a twenty-second
// timeout, and the two above still passed.
func TestLoadRefusesAPurgedRunPastItsVersion(t *testing.T) {
	past := purgedRun()
	past.runs[0].length = 9 // the vector promises eight
	if _, err := Load(2, past.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run past its version loaded with %v, want ErrMalformed", err)
	}

	// And one whose site has issued nothing at all has no room for any length.
	none := purgedRun()
	none.sites = [][2]uint64{{2, 8}}
	if _, err := Load(2, none.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run from a site with nothing issued loaded with %v, want ErrMalformed", err)
	}

	// A length no machine could hold, from a version vector small enough that
	// nothing else in the snapshot bounds it.
	huge := purgedRun()
	huge.runs[0].length = 1 << 50
	if _, err := Load(2, huge.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run of 2^50 characters loaded with %v, want ErrMalformed", err)
	}
}

// The purged column holds a flag, and a flag is nought or one. Anything else
// describes a run this encoder could not have written, so it is refused rather
// than read as truthy.
func TestLoadRefusesAPurgedFlagThatIsNeitherTrueNorFalse(t *testing.T) {
	odd := purgedRun()
	odd.runs[0].purgedFlag = 2
	if _, err := Load(2, odd.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged flag of 2 loaded with %v, want ErrMalformed", err)
	}

	// And a run that asks for a flag the column does not hold. This one is
	// reached through the columns rather than through the builder, which is also
	// what makes splitColumns count the thirteenth: a walk that stopped at
	// twelve would leave the purge column in the tail, and this case would be
	// emptying something else.
	head, cols, tail := splitColumns(t, purgedRun().build())
	if len(cols) != 13 {
		t.Fatalf("a version 9 snapshot came apart into %d columns, want 13", len(cols))
	}
	cols[12] = []byte{columnGroups}
	if _, err := Load(2, joinColumns(head, cols, tail)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a run whose purge flag is missing loaded with %v, want ErrMalformed", err)
	}
}

// A purged run in a document whose floor is zero could not have been written:
// Purge sets the floor every time it discards anything. It is refused rather
// than read, and that refusal is what lets the floor alone decide the format
// version -- a document whose floor is zero has nothing version 9 exists to
// say.
func TestLoadRefusesAPurgedRunWithNoFloor(t *testing.T) {
	// The control, so that the rejection below cannot be passing because the
	// fixture was wrong in some other way.
	if _, err := Load(2, purgedRun().build()); err != nil {
		t.Fatalf("the fixture it varies from does not load: %v", err)
	}

	orphan := purgedRun()
	orphan.purgedBelow = 0
	// Without this the builder would write version 8, which has no column to
	// carry the flag, and the run would not be purged at all.
	orphan.asVersion = snapshotVersion
	if _, err := Load(2, orphan.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run with no floor loaded with %v, want ErrMalformed", err)
	}
}

// Version 9 is written by a document that has purged, and by nothing else.
//
// This is the compatibility story, and it is the one this package has learnt
// twice: understand before write. #83 taught a text and a list to understand a
// superseded run in one release so that a later one could send it, and
// go-crdt/collab#98 added a required field to the wire and broke three
// transports that had not been rebuilt. A snapshot version is the same hazard
// with a longer memory, because bytes are stored: a build that writes version 9
// hands them to whatever reads next, which may be an older build or an older
// peer.
//
// So Load understands version 9 from this release, and Snapshot writes it only
// for a document whose owner asked for something version 8 cannot say. Nothing
// stored by a build that merely contains Purge is unreadable by one that does
// not, unless somebody called Purge.
func TestOnlyAPurgedDocumentWritesVersionNine(t *testing.T) {
	doc := revisedText(t, 40)
	before := doc.Snapshot()
	if got := before[len(snapshotMagic)]; got != snapshotVersionV8 {
		t.Fatalf("a document that never purged wrote version %d, want %d", got, snapshotVersionV8)
	}
	if !columnsFit(t, before, 12) || columnsFit(t, before, 13) {
		t.Fatal("a document that never purged did not write twelve columns -- it is either " +
			"paying for a column it has nothing to put in, or short of one")
	}

	runs := len(doc.runs())
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}
	after := doc.Snapshot()
	if got := after[len(snapshotMagic)]; got != snapshotVersion {
		t.Fatalf("a purged document wrote version %d, want %d", got, snapshotVersion)
	}
	if !columnsFit(t, after, 13) || columnsFit(t, after, 12) {
		t.Fatal("a purged document did not write thirteen columns")
	}

	// Both round-trip, which is what says the two versions are one format and
	// not two.
	for _, c := range []struct {
		name string
		data []byte
	}{{"version 8", before}, {"version 9", after}} {
		back, err := Load(2, c.data)
		if err != nil {
			t.Fatalf("%s did not load: %v", c.name, err)
		}
		if !bytes.Equal(back.Snapshot(), c.data) {
			t.Fatalf("%s did not re-encode to itself", c.name)
		}
	}

	// What the bump still saves a document that never purged, which under
	// version 8's groups is now a handful of bytes rather than one per run: a
	// column of nought repeated is a count and a value, whatever the count.
	// The bump is kept for the compatibility above and not for these.
	zeros := make([]uint64, runs)
	col := len(groupColumn(zeros)) + 1
	saved := col + len(binary.AppendUvarint(nil, uint64(col))) + 1
	t.Logf("%d runs; version 8 costs %d bytes, and would have paid %d more in version 9",
		runs, len(before), saved)
	if saved > 16 {
		t.Fatalf("the flag column costs %d bytes on a column of one repeated value, "+
			"which is not what version 8's groups are for", saved)
	}
}

// A purged run that is not a whole block once it has landed is refused.
//
// The writer only ever produces maximal runs, so no honest snapshot cuts one in
// two; a crafted one can, and the halves integrate into a single block, because
// that is what consecutive characters from one site do. The flag then names
// characters the block does not end at, and there is nothing to discard that is
// not also somebody else's. Reached by splitting the run rather than by
// reasoning about the check: what makes the block wider than the run is the
// second half naming the first half's last character as its origin.
func TestLoadRefusesAPurgedRunThatIsNotAWholeBlock(t *testing.T) {
	// Four characters from one site, written as two runs, the second purged:
	// four insertions and two deletions, so the vector promises six.
	split := runBuilder{
		sites:       [][2]uint64{{1, 6}},
		purgedBelow: 4,
		runs: []encodedRun{
			{site: 1, seq: 1, clock: 1, text: []rune("ab")},
			{
				site: 1, seq: 3, clock: 3, originSite: 1, originSeq: 2,
				purged: true, length: 2,
				dels: [][4]uint64{{0, 2, 1, 5}}, // both of them, from 5@1
			},
		},
	}
	if _, err := Load(2, split.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run that is half a block loaded with %v, want ErrMalformed", err)
	}

	// The same two runs with the second left alone load, which is what says the
	// refusal above is about the purge and not about the split.
	whole := split
	whole.purgedBelow = 0
	whole.runs = append([]encodedRun{}, split.runs...)
	whole.runs[1].purged = false
	whole.runs[1].length = 0
	whole.runs[1].text = []rune("cd")
	if _, err := Load(2, whole.build()); err != nil {
		t.Fatalf("the same snapshot without the purge did not load: %v", err)
	}
}

// A composite carrying a text that has purged says so.
//
// It is what stops the fuzzer expecting a newcomer to reproduce such a
// document's bytes: what a purge took is in no operation, so a peer replaying
// the history rebuilds the characters and the two snapshots differ there. The
// same shape as Composite.collected, and true for the same reason.
//
// This was covered by a fuzz corpus entry alone until version 7 became version
// 9, at which point the entry stopped decoding and the branch stopped being
// reached while every test still passed. A named case is what says so out loud.
func TestACompositeKnowsOneOfItsTextsHasPurged(t *testing.T) {
	c := NewComposite(1)
	part, err := c.Text("t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Insert(0, "abcd"); err != nil {
		t.Fatal(err)
	}
	if c.purged() {
		t.Fatal("a composite that has purged nothing says it has")
	}
	if _, err := part.Delete(0, 4); err != nil {
		t.Fatal(err)
	}
	if part.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}
	if !c.purged() {
		t.Fatal("a composite whose text has purged says it has not")
	}

	loaded, err := LoadComposite(2, c.Snapshot())
	if err != nil {
		t.Fatalf("LoadComposite: %v", err)
	}
	if !loaded.purged() {
		t.Fatal("the purge did not survive the composite's snapshot")
	}
	replayed := NewComposite(3)
	if err := replayed.Apply(loaded.OpsSince(nil)...); err != nil {
		t.Fatalf("replaying the history was rejected: %v", err)
	}
	if replayed.purged() {
		t.Fatal("a newcomer inherited a purge nothing sent it")
	}
}

// The safety belongs on the whole as well as on the part, because
// [Composite.Text] hands out the *Doc: anything holding a composite can purge
// one of its texts, and [Composite.OpsSince] would otherwise serve a peer it
// cannot serve with nothing to ask.
func TestACompositeRefusesAPeerItsTextCannotServe(t *testing.T) {
	c := NewComposite(1)
	text, err := c.Text("t")
	if err != nil {
		t.Fatal(err)
	}
	// A version from before anything happened, which is the peer a purge is
	// likeliest to have outrun.
	empty := CompositeVersion{}
	if err := c.CanServe(empty); err != nil {
		t.Fatalf("a composite that purged nothing refused a peer: %v", err)
	}

	if _, err := text.Insert(0, "a sentence somebody wrote, and then thought about again."); err != nil {
		t.Fatal(err)
	}
	if _, err := text.Delete(0, text.Len()); err != nil {
		t.Fatal(err)
	}
	if n := text.Purge(); n == 0 {
		t.Fatal("nothing was purged through the part a composite hands out")
	}

	if err := c.CanServe(empty); !errors.Is(err, ErrPurged) {
		t.Fatalf("the composite offered to serve a peer its text cannot: %v", err)
	}
	// Its own version it can still answer for.
	if err := c.CanServe(c.Version()); err != nil {
		t.Fatalf("a composite refused its own version: %v", err)
	}
	// And a part the peer names nothing for must not be skipped: an absent
	// vector is the emptiest one, not a reason to stop asking.
	if err := c.CanServe(CompositeVersion{Part{Kind: PartMap, Name: "other"}: VersionVector{}}); !errors.Is(err, ErrPurged) {
		t.Fatal("a version naming another part let the purged text through")
	}
}

// The fuzz corpus still holds a composite that has purged, and it still decodes.
//
// A corpus entry is pinned to a format version by its own bytes, so renumbering
// the format expires it — and silently, because a seed the loader refuses makes
// nothing fail: the fuzz target returns on the error and the suite stays green.
// That is not a hypothetical. testdata/fuzz/FuzzLoadComposite/f3e1eaa8e4e94eda
// was the input the fuzzer found for "a purged history cannot be replayed into
// the same document", kept deliberately so the case would not have to be
// rediscovered; when the purge moved from version 7 to version 9 it began being
// refused at the version byte and reached nothing at all, and nothing said so.
// It is removed in the same change as this, because an input the loader turns
// away at its first byte seeds no fuzzing and misdescribes itself to whoever
// reads the directory next.
//
// So the seed that replaced it is named rather than hashed, because a name is
// what lets a person find it and rewrite it, and this asserts what it is for
// rather than trusting it: a seed that stops being a purged composite fails here
// instead of quietly becoming a few hundred bytes of nothing.
func TestTheFuzzCorpusStillHoldsAPurgedComposite(t *testing.T) {
	const path = "testdata/fuzz/FuzzLoadComposite/purged-composite"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the purged seed is gone: %v", err)
	}
	// A corpus file is a version line and then one Go literal per argument,
	// which for this target is a single []byte.
	//
	// Trimmed, because the line ending is the checkout's and not the file's: a
	// Windows runner takes this file with CRLF, and a carriage return left on
	// the end puts it past the closing parenthesis, where it stops being a Go
	// literal. Which is how this test first failed -- on Windows only, and
	// nowhere it was written.
	literal := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "[]byte("); ok {
			literal = strings.TrimSuffix(after, ")")
		}
	}
	seed, err := strconv.Unquote(literal)
	if err != nil {
		t.Fatalf("%s does not hold a []byte literal: %v", path, err)
	}

	loaded, err := LoadComposite(2, []byte(seed))
	if err != nil {
		t.Fatalf("the purged seed no longer decodes, so it seeds nothing: %v", err)
	}
	if !loaded.purged() {
		t.Fatal("the purged seed decodes but nothing in it has purged")
	}
}
