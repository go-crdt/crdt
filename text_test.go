package crdt

import (
	"errors"
	"strings"
	"testing"
)

// insert applies a local insertion and fails the test if it is rejected.
func insert(t *testing.T, d *Doc, pos int, text string) []Op {
	t.Helper()
	ops, err := d.Insert(pos, text)
	if err != nil {
		t.Fatalf("Insert(%d, %q): %v", pos, text, err)
	}
	return ops
}

// remove applies a local deletion and fails the test if it is rejected.
func remove(t *testing.T, d *Doc, pos, length int) []Op {
	t.Helper()
	ops, err := d.Delete(pos, length)
	if err != nil {
		t.Fatalf("Delete(%d, %d): %v", pos, length, err)
	}
	return ops
}

// apply delivers operations and fails the test if any is rejected.
func apply(t *testing.T, d *Doc, ops []Op) {
	t.Helper()
	if err := d.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestNewIsEmpty(t *testing.T) {
	d := New(7)
	if got := d.Site(); got != 7 {
		t.Errorf("Site() = %d, want 7", got)
	}
	if got := d.String(); got != "" {
		t.Errorf("String() = %q, want empty", got)
	}
	if got := d.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if got := d.Tombstones(); got != 0 {
		t.Errorf("Tombstones() = %d, want 0", got)
	}
	if got := d.Pending(); got != 0 {
		t.Errorf("Pending() = %d, want 0", got)
	}
	if got := d.Version(); !got.Equal(nil) {
		t.Errorf("Version() = %v, want empty", got)
	}
}

func TestLocalEdits(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "world")
	insert(t, d, 0, "hello ")
	insert(t, d, d.Len(), "!")
	if got, want := d.String(), "hello world!"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := d.Len(), 12; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}

	remove(t, d, 5, 6) // " world"
	if got, want := d.String(), "hello!"; got != want {
		t.Fatalf("after delete String() = %q, want %q", got, want)
	}
	if got, want := d.Tombstones(), 6; got != want {
		t.Errorf("Tombstones() = %d, want %d", got, want)
	}
	if got, want := d.Len(), 6; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

// Inserting between tombstones must count only visible characters, and the new
// text must land where the user sees it, not where the tombstones are.
func TestInsertAroundTombstones(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abcdef")
	remove(t, d, 1, 4) // leaves "af", tombstones between them
	insert(t, d, 1, "X")
	if got, want := d.String(), "aXf"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	insert(t, d, 0, "0")
	insert(t, d, d.Len(), "9")
	if got, want := d.String(), "0aXf9"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestDeleteSpanningTombstones(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abcdef")
	remove(t, d, 2, 1) // "abdef", tombstone for c
	remove(t, d, 1, 3) // must skip the tombstone and remove b, d, e
	if got, want := d.String(), "af"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestMultibyteText(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "héllo — 世界 🌍")
	if got, want := d.Len(), len([]rune("héllo — 世界 🌍")); got != want {
		t.Fatalf("Len() = %d, want %d runes", got, want)
	}
	remove(t, d, 0, 6) // "héllo "
	if got, want := d.String(), "— 世界 🌍"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestEmptyEditsAreNoOps(t *testing.T) {
	d := New(1)
	if ops, err := d.Insert(0, ""); err != nil || ops != nil {
		t.Fatalf("Insert(0, \"\") = %v, %v; want nil, nil", ops, err)
	}
	if ops, err := d.Delete(0, 0); err != nil || ops != nil {
		t.Fatalf("Delete(0, 0) = %v, %v; want nil, nil", ops, err)
	}
	if !d.Version().Equal(nil) {
		t.Error("a no-op edit consumed a sequence number")
	}
}

func TestEditErrors(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abc")
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"insert before start", func() error { _, err := d.Insert(-1, "x"); return err }, ErrOutOfRange},
		{"insert past end", func() error { _, err := d.Insert(4, "x"); return err }, ErrOutOfRange},
		{"insert invalid utf8", func() error { _, err := d.Insert(0, "\xff\xfe"); return err }, ErrInvalidText},
		{"delete before start", func() error { _, err := d.Delete(-1, 1); return err }, ErrOutOfRange},
		{"delete negative length", func() error { _, err := d.Delete(0, -1); return err }, ErrOutOfRange},
		{"delete past end", func() error { _, err := d.Delete(2, 2); return err }, ErrOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if got, want := d.String(), "abc"; got != want {
		t.Errorf("a rejected edit changed the document: %q, want %q", got, want)
	}
}

// A rejected batch must leave the document untouched, including the operations
// that preceded the bad one.
func TestApplyRejectsWholeBatch(t *testing.T) {
	a, b := New(1), New(2)
	ops := insert(t, a, 0, "ok")
	bad := append(append([]Op{}, ops...), Op{Kind: OpInsert, ID: ID{Site: 1, Seq: 9}, Clock: 0})
	if err := b.Apply(bad...); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("Apply = %v, want ErrInvalidOp", err)
	}
	if got := b.String(); got != "" {
		t.Fatalf("String() = %q, want empty: a rejected batch was partly applied", got)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	a, b := New(1), New(2)
	ops := insert(t, a, 0, "hello")
	ops = append(ops, remove(t, a, 0, 1)...)
	apply(t, b, ops)
	apply(t, b, ops)
	apply(t, b, ops)
	if got, want := b.String(), a.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := b.Tombstones(), a.Tombstones(); got != want {
		t.Fatalf("Tombstones() = %d, want %d: a replayed delete was counted twice", got, want)
	}
	if !b.Version().Equal(a.Version()) {
		t.Fatalf("Version() = %v, want %v", b.Version(), a.Version())
	}
}

// Operations delivered in reverse are the worst case for the pending buffer:
// nothing is applicable until the very first operation arrives last.
func TestApplyOutOfOrder(t *testing.T) {
	a, b := New(1), New(2)
	ops := insert(t, a, 0, "abcdef")
	ops = append(ops, remove(t, a, 2, 2)...)
	reversed := make([]Op, len(ops))
	for i, op := range ops {
		reversed[len(ops)-1-i] = op
	}
	for _, op := range reversed[:len(reversed)-1] {
		apply(t, b, []Op{op})
		if b.Pending() == 0 {
			t.Fatalf("operation %v was applied although its dependencies are missing", op.ID)
		}
	}
	apply(t, b, reversed[len(reversed)-1:])
	if got := b.Pending(); got != 0 {
		t.Fatalf("Pending() = %d, want 0 once every dependency arrived", got)
	}
	if got, want := b.String(), a.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// A duplicate sitting in the pending buffer must be discarded, not applied a
// second time, once the real operation lands.
func TestPendingDuplicateIsDropped(t *testing.T) {
	a, b := New(1), New(2)
	ops := insert(t, a, 0, "xy")
	apply(t, b, []Op{ops[1], ops[1], ops[0]})
	if got := b.Pending(); got != 0 {
		t.Fatalf("Pending() = %d, want 0", got)
	}
	if got, want := b.String(), "xy"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// Two replicas deleting the same character concurrently must agree on the
// document and on its encoding, in either delivery order, and the losing
// operation must still be replayable to a third replica.
func TestConcurrentDeleteOfSameCharacter(t *testing.T) {
	a, b := New(1), New(2)
	seed := insert(t, a, 0, "abc")
	apply(t, b, seed)

	delA := remove(t, a, 1, 1)
	delB := remove(t, b, 1, 1)
	apply(t, a, delB)
	apply(t, b, delA)

	if got, want := a.String(), "ac"; got != want {
		t.Fatalf("a.String() = %q, want %q", got, want)
	}
	if a.String() != b.String() {
		t.Fatalf("a = %q, b = %q", a.String(), b.String())
	}
	if string(a.Snapshot()) != string(b.Snapshot()) {
		t.Fatal("replicas agree on the text but not on the encoded state")
	}
	if got, want := a.Tombstones(), 1; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}

	c := New(3)
	apply(t, c, must(a.OpsSince(nil)))
	if got, want := c.String(), a.String(); got != want {
		t.Fatalf("third replica String() = %q, want %q", got, want)
	}
	if !c.Version().Equal(a.Version()) {
		t.Fatalf("third replica Version() = %v, want %v: the losing delete was not replayed",
			c.Version(), a.Version())
	}
}

// RGA orders concurrent insertions at the same position by Lamport timestamp,
// highest first, so the outcome is the same on both replicas and does not depend
// on who happened to deliver first.
func TestConcurrentInsertAtSamePosition(t *testing.T) {
	a, b := New(1), New(2)
	seed := insert(t, a, 0, "()")
	apply(t, b, seed)

	fromA := insert(t, a, 1, "A")
	fromB := insert(t, b, 1, "B")
	apply(t, a, fromB)
	apply(t, b, fromA)

	if a.String() != b.String() {
		t.Fatalf("diverged: a = %q, b = %q", a.String(), b.String())
	}
	// Both insertions carry clock 3; site 2 breaks the tie and sorts later, so
	// it is placed first.
	if got, want := a.String(), "(BA)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// When two concurrent insertions land at the same position, the Lamport clock
// decides, and only when the clocks tie does the site break it. This test is the
// one that separates the two: the replica with the *lower* site identity is made
// to carry the *higher* clock, so an implementation that ranked by site would
// produce the opposite text.
//
// The rule matters to a user, not just to the algorithm: whoever had seen more
// of the document when they typed is placed first, rather than whoever happens
// to hold the smaller identifier.
func TestClockOutranksSite(t *testing.T) {
	a, b := New(1), New(2)
	seed := insert(t, a, 0, "()")
	apply(t, b, seed)

	// a edits elsewhere; b has not seen it yet, so a's clock runs ahead of b's.
	hidden := insert(t, a, 2, "zzz")

	fromA := insert(t, a, 1, "A")
	fromB := insert(t, b, 1, "B")
	if fromA[0].Clock <= fromB[0].Clock || fromA[0].ID.Site >= fromB[0].ID.Site {
		t.Fatalf("fixture is wrong: a must have the higher clock and the lower site; "+
			"a = %+v, b = %+v", fromA[0], fromB[0])
	}
	apply(t, a, fromB)
	// The concurrent insertion reaches b before the edits it was made on top of,
	// so b has to buffer it until they catch up.
	apply(t, b, fromA)
	if b.Pending() != 1 {
		t.Fatalf("Pending() = %d, want 1", b.Pending())
	}
	apply(t, b, hidden)

	if a.String() != b.String() {
		t.Fatalf("diverged: a = %q, b = %q", a.String(), b.String())
	}
	if got, want := a.String(), "(AB)zzz"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestOpsSinceSendsOnlyWhatIsMissing(t *testing.T) {
	a, b := New(1), New(2)
	first := insert(t, a, 0, "abc")
	apply(t, b, first)
	mark := b.Version()

	insert(t, a, 3, "def")
	remove(t, a, 0, 1)

	missing := must(a.OpsSince(mark))
	if got, want := len(missing), 4; got != want {
		t.Fatalf("OpsSince returned %d operations, want %d", got, want)
	}
	apply(t, b, missing)
	if got, want := b.String(), a.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got := must(a.OpsSince(b.Version())); len(got) != 0 {
		t.Fatalf("OpsSince after catching up returned %d operations, want 0", len(got))
	}
}

func TestOpsSinceIsCausallyOrderedForInsertions(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abcdef")
	remove(t, d, 1, 2)
	seen := map[ID]bool{{}: true}
	for _, op := range must(d.OpsSince(nil)) {
		if op.Kind != OpInsert {
			continue
		}
		if !seen[op.Origin] {
			t.Fatalf("operation %v names origin %v, which has not been sent yet", op.ID, op.Origin)
		}
		seen[op.ID] = true
	}
}

// Finding a position resumes from the last local insertion, which is what makes
// typing cheap. The shortcut must be invisible: every edit has to land where it
// would have without it, whether it follows the mark, precedes it, or arrives
// after a peer moved the ground underneath.
func TestPositionMarkDoesNotChangeResults(t *testing.T) {
	marked, plain := New(1), New(2)
	// plain never keeps a usable mark, because every insertion is preceded by a
	// remote operation that clears it.
	scratch := New(3)

	steps := []struct {
		pos  int
		text string
	}{
		{0, "abc"},   // fresh document
		{3, "def"},   // straight after the mark: the case the mark is for
		{6, "gh"},    // still after it
		{0, "ZZ"},    // before the mark: the shortcut must be skipped
		{4, "-"},     // between the mark and the start
		{11, "end"},  // after it again
		{2, "mid"},   // before it again
		{0, "héllo"}, // multibyte, at the very start
	}
	for i, step := range steps {
		insert(t, marked, step.pos, step.text)

		noise := insert(t, scratch, 0, "")
		if len(noise) != 0 {
			t.Fatalf("step %d: the empty insertion produced operations", i)
		}
		plain.mark = nil // as if a peer's operation had just been integrated
		insert(t, plain, step.pos, step.text)

		if got, want := marked.String(), plain.String(); got != want {
			t.Fatalf("step %d: with the mark %q, without it %q", i, got, want)
		}
	}

	// Deleting has to invalidate the mark too: it shifts every later index.
	remove(t, marked, 1, 4)
	remove(t, plain, 1, 4)
	insert(t, marked, 3, "X")
	plain.mark = nil
	insert(t, plain, 3, "X")
	if got, want := marked.String(), plain.String(); got != want {
		t.Fatalf("after deleting, with the mark %q, without it %q", got, want)
	}
}

// A peer's operation can move the character the mark points at, so integrating
// one must drop the mark rather than leave a stale index behind.
func TestRemoteOperationClearsTheMark(t *testing.T) {
	a, b := New(1), New(2)
	seed := insert(t, a, 0, "hello")
	apply(t, b, seed)
	insert(t, b, 5, "!") // b now holds a mark at index 5

	apply(t, b, insert(t, a, 0, "say "))
	if b.mark != nil {
		t.Fatal("integrating a peer's operation left the mark in place")
	}
	insert(t, b, 4, "X")
	apply(t, a, must(b.OpsSince(a.Version())))
	if got, want := b.String(), a.String(); got != want {
		t.Fatalf("diverged: b = %q, a = %q", got, want)
	}
	if got, want := b.String(), "say Xhello!"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// Characters are indexed by the sequence number of the operation that made
// them, and a deletion makes none — it leaves a hole. An operation naming that
// hole refers to a character that does not exist and must never be integrated,
// however plausible its identity looks.
func TestADeletionsIdentityNamesNoCharacter(t *testing.T) {
	source := New(1)
	insert(t, source, 0, "a")
	deletion := remove(t, source, 0, 1)
	if got, want := deletion[0].ID, (ID{Site: 1, Seq: 2}); got != want {
		t.Fatalf("fixture is wrong: the deletion is %v, want %v", got, want)
	}

	for _, tt := range []struct {
		name string
		op   Op
	}{
		{"an insertion after it", Op{
			Kind: OpInsert, ID: ID{Site: 2, Seq: 1}, Clock: 9,
			Origin: deletion[0].ID, Char: 'x',
		}},
		{"a deletion of it", Op{
			Kind: OpDelete, ID: ID{Site: 2, Seq: 1}, Clock: 9,
			Target: deletion[0].ID,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := New(3)
			apply(t, d, must(source.OpsSince(nil)))
			apply(t, d, []Op{tt.op})
			if got := d.Pending(); got != 1 {
				t.Fatalf("Pending() = %d, want 1: the operation was integrated", got)
			}
			if got, want := d.String(), source.String(); got != want {
				t.Fatalf("String() = %q, want %q", got, want)
			}
		})
	}
}

// records counts the deletion records the document holds, which is what decides
// whether deleting is cheap.
func records(d *Doc) int {
	n := 0
	for b := d.head.next; b != nil; b = b.next {
		n += len(b.dels)
	}
	return n
}

// A stretch deleted in one go is one record, however long it is; corrections
// made in separate places are one each. This is the whole point of storing
// deletions as ranges, so it is asserted rather than left to a memory figure.
func TestDeletionsAreStoredAsStretches(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "the quick brown fox")

	remove(t, d, 4, 6) // "quick "
	if got := records(d); got != 1 {
		t.Fatalf("deleting one stretch left %d records, want 1", got)
	}
	remove(t, d, 0, 4) // "the ", a separate place
	if got := records(d); got != 2 {
		t.Fatalf("deleting a second stretch left %d records, want 2", got)
	}
	if got, want := d.String(), "brown fox"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := d.Tombstones(), 10; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}

	// Every deletion still has to be repeatable to a peer, one operation per
	// character, whatever the records look like here.
	peer := New(2)
	apply(t, peer, must(d.OpsSince(nil)))
	if got, want := peer.String(), d.String(); got != want {
		t.Fatalf("the peer holds %q, want %q", got, want)
	}
	if string(peer.Snapshot()) != string(d.Snapshot()) {
		t.Fatal("the peer agrees on the text but not on the state")
	}
}

// An insertion landing inside a deleted stretch divides the record, and each
// half has to keep the right identity for its characters — otherwise the
// deletions replay onto the wrong characters, or not at all.
func TestAnInsertionInsideADeletedStretchDividesTheRecord(t *testing.T) {
	a, b := New(1), New(2)
	seed := insert(t, a, 0, "abcdef")
	apply(t, b, seed)

	// b writes between c and d; a meanwhile deletes both, in one stretch.
	fromB := insert(t, b, 3, "X")
	fromA := remove(t, a, 2, 2)
	if got := records(a); got != 1 {
		t.Fatalf("the deletion left %d records, want 1", got)
	}

	apply(t, a, fromB)
	apply(t, b, fromA)
	if got := records(a); got != 2 {
		t.Fatalf("an insertion inside the stretch left %d records, want it divided into 2", got)
	}

	if a.String() != b.String() {
		t.Fatalf("diverged: a = %q, b = %q", a.String(), b.String())
	}
	if got, want := a.String(), "abXef"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if string(a.Snapshot()) != string(b.Snapshot()) {
		t.Fatal("the replicas agree on the text but not on the state")
	}

	// The divided records must still name the right operations.
	third := New(3)
	apply(t, third, must(a.OpsSince(nil)))
	if got, want := third.String(), "abXef"; got != want {
		t.Fatalf("replaying gave %q, want %q", got, want)
	}
	if string(third.Snapshot()) != string(a.Snapshot()) {
		t.Fatal("replaying the history did not reproduce the state")
	}
	loaded, err := Load(4, a.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(loaded.Snapshot()) != string(a.Snapshot()) {
		t.Fatal("a snapshot of the divided records did not reload to the same state")
	}
}

func TestStringBuildsFromVisibleCharactersOnly(t *testing.T) {
	d := New(1)
	insert(t, d, 0, strings.Repeat("x", 10))
	remove(t, d, 0, 10)
	if got := d.String(); got != "" {
		t.Fatalf("String() = %q, want empty", got)
	}
	if got, want := d.Tombstones(), 10; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}
}

// Several operations waiting on the same one, which is the case the chunk a
// parked operation is cut from has to survive.
//
// Each parked operation gets a one-element slice into a shared chunk, so a
// second operation filed under the same identity appends to a slice whose
// capacity is one — which copies it out rather than writing into the next
// operation's element. If that capacity were wrong, the neighbour would be
// silently replaced and the document would converge on the wrong text, or on
// nothing at all.
//
// Three operations end up under one identity here: site 1's second character
// waits on its own predecessor, and sites 2 and 3 both insert after that same
// first character, which has not arrived. Then it arrives, and all three wake.
func TestParkingSeveralOperationsUnderOneIdentity(t *testing.T) {
	one := New(1)
	first, err := one.Insert(0, "A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := one.Insert(1, "B")
	if err != nil {
		t.Fatal(err)
	}

	// Two replicas that have seen only the first character, each inserting
	// after it, so both of their operations name it as their origin.
	two, three := New(2), New(3)
	apply(t, two, first)
	apply(t, three, first)
	fromTwo, err := two.Insert(1, "X")
	if err != nil {
		t.Fatal(err)
	}
	fromThree, err := three.Insert(1, "Y")
	if err != nil {
		t.Fatal(err)
	}

	// A fifth site, whose *second* operation is delivered without its first, so
	// it parks under an identity of its own rather than joining the group.
	//
	// That is what makes this test load-bearing. An append that overruns its
	// one-element slice writes into the chunk's next slot without the chunk
	// knowing, and the chunk hands that same slot to the next operation filed
	// under a *different* identity — quietly replacing an operation that is
	// still waiting. Only an operation parked under another key afterwards can
	// reveal it, and every operation above shares one key.
	five := New(6)
	apply(t, five, first)
	fifthFirst, err := five.Insert(1, "Z")
	if err != nil {
		t.Fatal(err)
	}
	fifthSecond, err := five.Insert(2, "W")
	if err != nil {
		t.Fatal(err)
	}

	// Everything that depends on the first character, before it.
	late := New(4)
	apply(t, late, second)
	apply(t, late, fromTwo)
	apply(t, late, fromThree)
	apply(t, late, fifthSecond) // parks under its own predecessor, not the group
	if got := late.String(); got != "" {
		t.Fatalf("nothing should have integrated yet, but the document reads %q", got)
	}
	if got, want := late.parked, 4; got != want {
		t.Fatalf("parked = %d, want %d — three under one identity and one under another", got, want)
	}

	apply(t, late, first)
	apply(t, late, fifthFirst)
	if got := late.parked; got != 0 {
		t.Fatalf("Parked() = %d after the identity they waited on arrived, want 0", got)
	}

	// The order the three settle in is the CRDT's business; agreeing on it is
	// the point. A replica given the same operations in a sensible order must
	// read the same thing.
	ordered := New(5)
	apply(t, ordered, first)
	apply(t, ordered, second)
	apply(t, ordered, fromTwo)
	apply(t, ordered, fromThree)
	apply(t, ordered, fifthFirst)
	apply(t, ordered, fifthSecond)
	if late.String() != ordered.String() {
		t.Fatalf("delivery order changed the document: %q against %q", late.String(), ordered.String())
	}
	if len([]rune(late.String())) != 6 {
		t.Fatalf("the document lost a character: %q", late.String())
	}
	t.Logf("four parked under two identities, woken together: %q", late.String())
}
