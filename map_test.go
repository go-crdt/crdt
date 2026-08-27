package crdt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strconv"
	"testing"
	"unicode/utf8"
)

// A replicated map converges for a simpler reason than the text does — a maximum
// per key, taken in whatever order — so the tests here spend their effort on the
// two places that are not simple: what a replica keeps once a write has lost,
// and what it will accept from a peer.

func setKey(t *testing.T, m *Map, key string, value string) MapOp {
	t.Helper()
	op, err := m.Set(key, []byte(value))
	if err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
	return op
}

func deleteKey(t *testing.T, m *Map, key string) MapOp {
	t.Helper()
	op, err := m.Delete(key)
	if err != nil {
		t.Fatalf("Delete(%q): %v", key, err)
	}
	return op
}

func applyMap(t *testing.T, m *Map, ops ...MapOp) {
	t.Helper()
	if err := m.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// sameMapOp compares two operations. A MapOp is not comparable — it carries a
// value — so the round trips and the fuzzers ask here rather than each spelling
// the six fields out.
func sameMapOp(a, b MapOp) bool {
	return a.Kind == b.Kind && a.ID == b.ID && a.Clock == b.Clock &&
		a.Key == b.Key && a.Span == b.Span && bytes.Equal(a.Value, b.Value)
}

// value reports what Get returns, as a string, and fails if the key is absent.
func value(t *testing.T, m *Map, key string) string {
	t.Helper()
	got, ok := m.Get(key)
	if !ok {
		t.Fatalf("Get(%q): not present", key)
	}
	return string(got)
}

func TestMapSetGetDelete(t *testing.T) {
	m := NewMap(1)
	if m.Len() != 0 || len(m.Keys()) != 0 {
		t.Fatalf("a new map holds %d keys", m.Len())
	}
	if _, ok := m.Get("absent"); ok {
		t.Fatal("Get reported a key that was never written")
	}

	setKey(t, m, "b", "two")
	setKey(t, m, "a", "one")
	setKey(t, m, "c", "three")
	if got := value(t, m, "a"); got != "one" {
		t.Fatalf("Get(a) = %q, want %q", got, "one")
	}
	if got, want := m.Keys(), []string{"a", "b", "c"}; !equalStrings(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if m.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", m.Len())
	}

	// Overwriting a key replaces the value without changing how many there are.
	setKey(t, m, "a", "uno")
	if got := value(t, m, "a"); got != "uno" || m.Len() != 3 {
		t.Fatalf("after overwriting: Get(a) = %q, Len() = %d", got, m.Len())
	}

	deleteKey(t, m, "b")
	if _, ok := m.Get("b"); ok {
		t.Fatal("Get returned a deleted key")
	}
	if m.Tombstones() != 1 {
		t.Fatalf("Tombstones() = %d, want 1: the key has to keep its clock", m.Tombstones())
	}
	if got, want := m.Keys(), []string{"a", "c"}; !equalStrings(got, want) {
		t.Fatalf("after deleting: Keys() = %v, want %v", got, want)
	}
	if m.Len() != 2 {
		t.Fatalf("after deleting: Len() = %d, want 2", m.Len())
	}
	// Deleting again is a fresh operation and still leaves the key absent.
	deleteKey(t, m, "b")
	if _, ok := m.Get("b"); ok || m.Len() != 2 {
		t.Fatalf("deleting twice left Len() = %d", m.Len())
	}
	// Writing to a deleted key brings it back, because the new write is later.
	setKey(t, m, "b", "back")
	if got := value(t, m, "b"); got != "back" || m.Len() != 3 {
		t.Fatalf("rewriting a deleted key gave %q, Len() = %d", got, m.Len())
	}
}

// TestMapDeleteUnknownKey pins the choice made in [Map.Delete]: a deletion is an
// operation whatever this replica happens to hold, so that a write still in
// flight loses to it here exactly as it does everywhere else.
func TestMapDeleteUnknownKey(t *testing.T) {
	a, b, c := NewMap(1), NewMap(2), NewMap(3)
	set := setKey(t, b, "k", "written")
	// Only a hears what c writes, so a's clock runs ahead of b's while a still
	// knows nothing at all about the key it is about to delete.
	lift := []MapOp{setKey(t, c, "c1", "x"), setKey(t, c, "c2", "x")}
	applyMap(t, a, lift...)
	del := deleteKey(t, a, "k") // a has never heard of "k"

	applyMap(t, a, set)
	applyMap(t, b, append(lift, del)...)
	if _, ok := a.Get("k"); ok {
		t.Fatal("a write concurrent with a deletion of an unknown key survived")
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("replicas disagree after a deletion of a key one of them lacked")
	}
	if del.Clock <= set.Clock {
		t.Fatalf("the deletion's clock is %d, the write's %d: the fixture proves nothing",
			del.Clock, set.Clock)
	}
}

// TestMapValuesAreCopied is the aliasing property: nothing a caller holds is
// ever a slice the map is reading, in either direction.
func TestMapValuesAreCopied(t *testing.T) {
	m := NewMap(1)
	source := []byte("original")
	op, err := m.Set("k", source)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'X'
	if got := value(t, m, "k"); got != "original" {
		t.Fatalf("writing to the caller's slice changed the map: %q", got)
	}
	op.Value[1] = 'X'
	if got := value(t, m, "k"); got != "original" {
		t.Fatalf("writing to the returned operation changed the map: %q", got)
	}
	got, _ := m.Get("k")
	got[2] = 'X'
	if again := value(t, m, "k"); again != "original" {
		t.Fatalf("writing to what Get returned changed the map: %q", again)
	}

	// An operation from a peer must not keep the batch it was decoded from alive
	// or writable either.
	sender, peer := NewMap(2), NewMap(3)
	encoded, err := AppendMapOps(nil, []MapOp{setKey(t, sender, "k", "original")})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMapOps(encoded)
	if err != nil {
		t.Fatal(err)
	}
	applyMap(t, peer, parsed...)
	for i := range encoded {
		encoded[i] = 0
	}
	if got := value(t, peer, "k"); got != "original" {
		t.Fatalf("a decoded operation aliased its buffer: %q", got)
	}
}

// TestMapEmptyAndNilValue pins that the two are one value, on the way in, on the
// way out, and on the wire.
func TestMapEmptyAndNilValue(t *testing.T) {
	m := NewMap(1)
	if _, err := m.Set("nil", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("empty", []byte{}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nil", "empty"} {
		got, ok := m.Get(key)
		if !ok || got != nil {
			t.Fatalf("Get(%q) = %v, %v; want nil, true", key, got, ok)
		}
	}
	if m.Len() != 2 {
		t.Fatalf("Len() = %d, want 2: an empty value is still a value", m.Len())
	}
	loaded, err := LoadMap(2, m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Snapshot(), m.Snapshot()) {
		t.Fatal("an empty value did not survive a snapshot round trip")
	}
}

func TestMapRejectsInvalidKey(t *testing.T) {
	m := NewMap(1)
	if _, err := m.Set("\xff", nil); err != ErrInvalidText {
		t.Fatalf("Set with an invalid key: %v, want %v", err, ErrInvalidText)
	}
	if _, err := m.Delete("\xff"); err != ErrInvalidText {
		t.Fatalf("Delete with an invalid key: %v, want %v", err, ErrInvalidText)
	}
	if m.Version().Get(1) != 0 {
		t.Fatal("a refused write still consumed a sequence number")
	}
}

// TestMapClockOutranksSite is what the Lamport clock decides here: whoever wrote
// later wins, rather than whoever holds the lower site number.
func TestMapClockOutranksSite(t *testing.T) {
	low, high := NewMap(1), NewMap(9)
	first := setKey(t, low, "k", "first")
	applyMap(t, high, first)
	second := setKey(t, high, "k", "second") // later, but from the higher site

	applyMap(t, low, second)
	if got := value(t, low, "k"); got != "second" {
		t.Fatalf("Get(k) = %q, want %q: the site number outranked the clock", got, "second")
	}
	if second.Clock <= first.Clock {
		t.Fatalf("clocks %d and %d: the fixture is not what it claims", first.Clock, second.Clock)
	}
}

// TestMapConcurrentWritesBreakTie covers the other half: two writes with nothing
// between them carry the same clock, and the site decides — the same way on both
// replicas, whichever arrived first.
func TestMapConcurrentWritesBreakTie(t *testing.T) {
	a, b := NewMap(1), NewMap(2)
	fromA := setKey(t, a, "k", "from a")
	fromB := setKey(t, b, "k", "from b")
	if fromA.Clock != fromB.Clock {
		t.Fatalf("clocks %d and %d: the writes are not concurrent", fromA.Clock, fromB.Clock)
	}
	applyMap(t, a, fromB)
	applyMap(t, b, fromA)
	if got := value(t, a, "k"); got != "from b" {
		t.Fatalf("Get(k) = %q, want %q: the higher site must win a tie", got, "from b")
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("replicas resolved a tie differently")
	}
}

// TestMapDeletedKeyKeepsItsClock is the resurrection test, and the one mistake
// this design exists to avoid. A key that has been deleted must stay deleted
// when an older write for it turns up afterwards — on both replicas, and in
// either delivery order.
func TestMapDeletedKeyKeepsItsClock(t *testing.T) {
	build := func(t *testing.T) (older MapOp, del MapOp) {
		t.Helper()
		writer, deleter := NewMap(1), NewMap(2)
		older = setKey(t, writer, "k", "resurrected")
		// The deleter has seen an unrelated write, so its clock is ahead.
		applyMap(t, deleter, setKey(t, writer, "other", "x"))
		applyMap(t, deleter, older)
		del = deleteKey(t, deleter, "k")
		if del.Clock <= older.Clock {
			t.Fatalf("deletion clock %d, write clock %d: not the case under test",
				del.Clock, older.Clock)
		}
		return older, del
	}

	older, del := build(t)
	for _, order := range [][]MapOp{{older, del}, {del, older}} {
		m := NewMap(3)
		// The deletion names the key first in one order and second in the other;
		// the write it beat has to be applied either way for its site's sequence
		// to stay contiguous.
		applyMap(t, m, order...)
		if _, ok := m.Get("k"); ok {
			t.Fatalf("delivery order %v resurrected the key", kinds(order))
		}
		if m.Pending() != 0 {
			t.Fatalf("delivery order %v left %d operations parked", kinds(order), m.Pending())
		}
	}

	// And the same thing across two replicas, each hearing one order.
	early, late := NewMap(3), NewMap(4)
	applyMap(t, early, older, del)
	applyMap(t, late, del, older)
	if _, ok := early.Get("k"); ok {
		t.Fatal("the replica that heard the write first resurrected the key")
	}
	if !bytes.Equal(early.Snapshot(), late.Snapshot()) {
		t.Fatal("the two delivery orders produced different state")
	}
}

func kinds(ops []MapOp) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.Kind.String()
	}
	return out
}

func TestMapOpKindString(t *testing.T) {
	for _, tc := range []struct {
		kind MapOpKind
		want string
	}{
		{MapSet, "set"},
		{MapDelete, "delete"},
		{MapSuperseded, "superseded"},
		{MapOpKind(7), "invalid(7)"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Fatalf("MapOpKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestMapApplyIsIdempotent(t *testing.T) {
	source := NewMap(1)
	ops := []MapOp{
		setKey(t, source, "a", "one"),
		setKey(t, source, "b", "two"),
		deleteKey(t, source, "a"),
		setKey(t, source, "b", "three"),
	}
	m := NewMap(2)
	applyMap(t, m, ops...)
	before := m.Snapshot()
	applyMap(t, m, ops...)
	applyMap(t, m, ops[2], ops[0], ops[3])
	if !bytes.Equal(m.Snapshot(), before) {
		t.Fatal("replaying an applied batch changed the map")
	}
	if !bytes.Equal(before, source.Snapshot()) {
		t.Fatal("the replica did not reach the state it was replaying")
	}
}

// TestMapBuffersOutOfOrderDelivery is the readiness rule: an operation that
// arrives before the one its site issued first waits rather than being dropped,
// because the sequence number it would skip is a key nobody would ever hear
// about again.
func TestMapBuffersOutOfOrderDelivery(t *testing.T) {
	source := NewMap(1)
	first := setKey(t, source, "first", "1")
	second := setKey(t, source, "second", "2")
	third := setKey(t, source, "third", "3")

	m := NewMap(2)
	applyMap(t, m, third, second)
	if m.Pending() != 2 {
		t.Fatalf("Pending() = %d, want 2", m.Pending())
	}
	if m.Len() != 0 {
		t.Fatal("an operation was applied before its predecessor")
	}
	// A duplicate of something already parked must not be applied twice when the
	// chain unblocks.
	applyMap(t, m, third, first)
	if m.Pending() != 0 {
		t.Fatalf("Pending() = %d after the chain unblocked, want 0", m.Pending())
	}
	if !bytes.Equal(m.Snapshot(), source.Snapshot()) {
		t.Fatal("buffered delivery did not reproduce the source state")
	}
}

func TestMapVersion(t *testing.T) {
	m := NewMap(1)
	setKey(t, m, "a", "1")
	setKey(t, m, "b", "2")
	vv := m.Version()
	if vv.Get(1) != 2 {
		t.Fatalf("Version() = %v, want site 1 at 2", vv)
	}
	// The vector is a copy: writing to it cannot move the map.
	vv[1] = 99
	if m.Version().Get(1) != 2 {
		t.Fatal("Version returned the map's own vector")
	}
}

func TestMapSnapshotRoundTrip(t *testing.T) {
	m := NewMap(1)
	setKey(t, m, "kept", "value")
	setKey(t, m, "removed", "value")
	setKey(t, m, "日本", "unicode key")
	deleteKey(t, m, "removed")
	setKey(t, m, "empty", "")

	loaded, err := LoadMap(2, m.Snapshot())
	if err != nil {
		t.Fatalf("LoadMap: %v", err)
	}
	if !bytes.Equal(loaded.Snapshot(), m.Snapshot()) {
		t.Fatal("a loaded map does not re-encode to the snapshot it came from")
	}
	if loaded.Len() != m.Len() || !equalStrings(loaded.Keys(), m.Keys()) {
		t.Fatalf("Keys() = %v, want %v", loaded.Keys(), m.Keys())
	}
	if got := value(t, loaded, "日本"); got != "unicode key" {
		t.Fatalf("Get after loading = %q", got)
	}
	if !loaded.Version().Equal(m.Version()) {
		t.Fatalf("Version() = %v, want %v", loaded.Version(), m.Version())
	}
	// The deleted key kept its clock, so an older write still loses on the
	// replica that only ever saw the snapshot.
	older := MapOp{Kind: MapSet, ID: ID{Site: 8, Seq: 1}, Clock: 1, Key: "removed", Value: []byte("back")}
	applyMap(t, loaded, older)
	if _, ok := loaded.Get("removed"); ok {
		t.Fatal("a snapshot lost the tombstone's clock: an older write resurrected the key")
	}
}

// TestMapLoadedClockOutranksTheSnapshot checks the clock floor directly: a write
// made by a replica that joined from a snapshot must beat everything the
// snapshot recorded, or it would lose to writes it was made after.
func TestMapLoadedClockOutranksTheSnapshot(t *testing.T) {
	source := NewMap(1)
	var last MapOp
	for i := range 5 {
		last = setKey(t, source, "k"+strconv.Itoa(i), "v")
	}
	loaded, err := LoadMap(2, source.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	next := setKey(t, loaded, "k0", "written by the joiner")
	if next.Clock <= last.Clock {
		t.Fatalf("the joiner wrote at clock %d, the snapshot recorded %d", next.Clock, last.Clock)
	}
	applyMap(t, source, next)
	if got := value(t, source, "k0"); got != "written by the joiner" {
		t.Fatalf("Get(k0) = %q: the joiner's write lost", got)
	}
}

// TestMapLoadedClockClearsTheVector covers a snapshot whose version vector
// promises more than its records account for. That is what a replica holds when
// everything it received had already been superseded: nothing names those
// operations, and the clock still has to clear their sequence numbers, or the
// next write this replica makes carries a clock below its own sequence number
// and its own validator refuses it.
func TestMapLoadedClockClearsTheVector(t *testing.T) {
	snapshot := encodeMapSnapshot(uint64(1), uint64(1), uint64(3), uint64(0))
	m, err := LoadMap(1, snapshot) // as the very site the vector names
	if err != nil {
		t.Fatalf("LoadMap: %v", err)
	}
	op := setKey(t, m, "k", "v")
	if op.ID.Seq != 4 {
		t.Fatalf("the first write after loading is %v, want sequence number 4", op.ID)
	}
	if err := op.validate(); err != nil {
		t.Fatalf("the first write after loading is invalid: %v (clock %d)", err, op.Clock)
	}
}

// TestMapSnapshotIsCanonical is the property the convergence tests rest on: the
// bytes depend on the operations applied, not on the order they arrived in or on
// which replica is asking.
func TestMapSnapshotIsCanonical(t *testing.T) {
	a, b := NewMap(1), NewMap(2)
	fromA := []MapOp{setKey(t, a, "x", "1"), setKey(t, a, "y", "2"), deleteKey(t, a, "x")}
	fromB := []MapOp{setKey(t, b, "y", "3"), setKey(t, b, "z", "4")}
	applyMap(t, a, fromB...)
	applyMap(t, b, fromA...)
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("two replicas holding the same operations encoded differently")
	}
	// Nothing in the snapshot depends on which site loads it.
	loaded, err := LoadMap(7, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Snapshot(), a.Snapshot()) {
		t.Fatal("the loading site leaked into the snapshot")
	}
}

// TestMapOpsSinceReplays covers the whole catch-up path, including the part that
// only exists because a map forgets: an operation whose value has been
// overwritten comes back as MapSuperseded, and the fresh replica must still end
// up byte-identical.
func TestMapOpsSinceReplays(t *testing.T) {
	source := NewMap(1)
	peer := NewMap(2)
	setKey(t, source, "a", "one")
	setKey(t, source, "a", "two")   // supersedes the first
	setKey(t, source, "b", "three") // and gets superseded by the deletion below
	deleteKey(t, source, "b")
	applyMap(t, source, setKey(t, peer, "c", "from the peer"))

	all := source.OpsSince(nil)
	if len(all) != int(source.Version().Get(1))+1 {
		t.Fatalf("OpsSince(nil) returned %d operations for a history of %d",
			len(all), source.Version().Get(1)+1)
	}
	superseded := 0
	for _, op := range all {
		if op.Kind == MapSuperseded {
			superseded++
		}
	}
	if superseded != 2 {
		t.Fatalf("%d superseded operations, want 2: the fixture must exercise them", superseded)
	}

	fresh := NewMap(3)
	applyMap(t, fresh, all...)
	if !bytes.Equal(fresh.Snapshot(), source.Snapshot()) {
		t.Fatal("replaying OpsSince(nil) did not reproduce the source")
	}
	if fresh.Pending() != 0 {
		t.Fatalf("%d operations never became applicable", fresh.Pending())
	}

	// And incrementally, which is what a replica returning from offline asks for.
	partial := NewMap(4)
	applyMap(t, partial, all[:2]...)
	applyMap(t, partial, source.OpsSince(partial.Version())...)
	if !bytes.Equal(partial.Snapshot(), source.Snapshot()) {
		t.Fatal("an incremental catch-up did not reproduce the source")
	}
	// A peer that is not behind is sent nothing, including one claiming to be
	// further ahead than this replica could know.
	if ops := source.OpsSince(source.Version()); len(ops) != 0 {
		t.Fatalf("OpsSince returned %d operations to a peer that is up to date", len(ops))
	}
	ahead := source.Version()
	for site := range ahead {
		ahead[site] = ^uint64(0)
	}
	if ops := source.OpsSince(ahead); len(ops) != 0 {
		t.Fatalf("OpsSince returned %d operations to a peer claiming everything", len(ops))
	}
}

// TestMapSupersededCarriesNothing pins what a superseded operation is allowed to
// say, since it is the one operation a replica invents rather than replays.
func TestMapSupersededCarriesNothing(t *testing.T) {
	m := NewMap(1)
	setKey(t, m, "k", "first")
	setKey(t, m, "k", "second")
	ops := m.OpsSince(nil)
	if len(ops) != 2 || ops[0].Kind != MapSuperseded {
		t.Fatalf("OpsSince(nil) = %v", ops)
	}
	if ops[0].Key != "" || ops[0].Value != nil || ops[0].Span != 1 {
		t.Fatalf("a superseded operation carried %q = %q over %d numbers",
			ops[0].Key, ops[0].Value, ops[0].Span)
	}
	if ops[0].Clock != ops[0].ID.Seq {
		t.Fatalf("clock %d, sequence number %d: a superseded operation's clock is its "+
			"sequence number, the only lower bound left", ops[0].Clock, ops[0].ID.Seq)
	}
	// Applied on its own it advances the version vector and touches nothing else,
	// which is the whole of what it is for.
	fresh := NewMap(2)
	applyMap(t, fresh, ops[0])
	if fresh.Len() != 0 || fresh.Version().Get(1) != 1 {
		t.Fatalf("applying it gave %d keys and version %v", fresh.Len(), fresh.Version())
	}
}

// TestMapSupersededRunsCollapse is why a superseded operation covers a run of
// sequence numbers rather than one: a cell rewritten all afternoon leaves one
// record, and catching a peer up has to cost what the state costs rather than
// what the history did.
func TestMapSupersededRunsCollapse(t *testing.T) {
	source := NewMap(1)
	for i := range 50 {
		setKey(t, source, "cell", strconv.Itoa(i))
	}
	setKey(t, source, "other", "x")

	ops := source.OpsSince(nil)
	if len(ops) != 3 {
		t.Fatalf("OpsSince(nil) returned %d operations for 51 writes, want 3", len(ops))
	}
	if ops[0].Kind != MapSuperseded || ops[0].ID.Seq != 49 || ops[0].Span != 49 {
		t.Fatalf("the run is %+v, want the 49 numbers ending at 49", ops[0])
	}
	fresh := NewMap(2)
	applyMap(t, fresh, ops...)
	if !bytes.Equal(fresh.Snapshot(), source.Snapshot()) || fresh.Pending() != 0 {
		t.Fatal("a history sent as runs did not reproduce the map it came from")
	}
}

// TestMapRunWakesWhatItSkips covers what a run does to operations already
// waiting: it accounts for numbers they were waiting on, and it may account for
// far more numbers than there are operations parked, so neither the run nor the
// queue is left to decide on its own how much work releasing them costs.
func TestMapRunWakesWhatItSkips(t *testing.T) {
	source := NewMap(1)
	var issued []MapOp
	for i := range 5 {
		issued = append(issued, setKey(t, source, "cell", strconv.Itoa(i)))
	}
	issued = append(issued, setKey(t, source, "other", "x"))
	sent := source.OpsSince(nil) // one run over 1 to 4, then the two records

	// One operation parked on a number inside the run — the real operation, from
	// a peer that has not superseded it — and a run covering more numbers than
	// there are operations waiting.
	m := NewMap(2)
	applyMap(t, m, issued[3])
	if m.Pending() != 1 {
		t.Fatalf("Pending() = %d, want 1", m.Pending())
	}
	applyMap(t, m, sent[0])
	if m.Pending() != 0 {
		t.Fatalf("a run left %d operations waiting on numbers it accounted for", m.Pending())
	}
	applyMap(t, m, sent[1:]...)
	if !bytes.Equal(m.Snapshot(), source.Snapshot()) {
		t.Fatal("the map did not reach the state the run described")
	}

	// And with more parked than the run covers, which is the other walk.
	other := NewMap(3)
	applyMap(t, other, issued[5], issued[4])
	if other.Pending() != 2 {
		t.Fatalf("Pending() = %d, want 2", other.Pending())
	}
	applyMap(t, other, sent[0], sent[1])
	if other.Pending() != 0 {
		t.Fatalf("%d operations never became applicable", other.Pending())
	}
	if !bytes.Equal(other.Snapshot(), source.Snapshot()) {
		t.Fatal("waking through the numbers reached a different state")
	}
}

// TestMapVastHistoryCostsWhatItDescribes is a regression the fuzzer found: a
// snapshot may promise a history far longer than the records describing it — no
// replica sends one, but a decoder cannot tell — and OpsSince must answer with
// the runs that stand for it rather than trying to name every number in it.
func TestMapVastHistoryCostsWhatItDescribes(t *testing.T) {
	const promised = 1 << 40
	m, err := LoadMap(2, encodeMapSnapshot(uint64(1), uint64(1), uint64(promised), uint64(0)))
	if err != nil {
		t.Fatalf("LoadMap: %v", err)
	}
	ops := m.OpsSince(nil)
	if len(ops) != 1 || ops[0].Span != promised {
		t.Fatalf("OpsSince(nil) returned %d operations, want one run of %d", len(ops), uint64(promised))
	}
	fresh := NewMap(3)
	applyMap(t, fresh, ops...)
	if !bytes.Equal(fresh.Snapshot(), m.Snapshot()) {
		t.Fatal("a history that stands for nothing did not replay to the same state")
	}
}

// equalStrings compares two key lists.
func equalStrings(a, b []string) bool {
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

// A mapSimulation is a set of replicas plus an unreliable network: operations
// sit in per-replica inboxes and are delivered late, out of order, and more than
// once. It mirrors the text document's simulation, because the property under
// test is the same one.
type mapSimulation struct {
	t     *testing.T
	rng   *rand.Rand
	maps  []*Map
	inbox [][]MapOp
	n     int
}

// mapKeys is deliberately short: replicas have to collide on the same key often
// for last-writer-wins to be exercised at all.
var mapKeys = []string{"a", "b", "c", "d", "é", "🔑"}

func newMapSimulation(t *testing.T, seed uint64, replicas int) *mapSimulation {
	t.Helper()
	s := &mapSimulation{
		t:     t,
		rng:   rand.New(rand.NewPCG(seed, 0x5eed)),
		maps:  make([]*Map, replicas),
		inbox: make([][]MapOp, replicas),
	}
	for i := range s.maps {
		s.maps[i] = NewMap(SiteID(i + 1))
	}
	return s
}

func (s *mapSimulation) write(i int) {
	s.t.Helper()
	m := s.maps[i]
	key := mapKeys[s.rng.IntN(len(mapKeys))]
	var op MapOp
	var err error
	if s.rng.IntN(4) == 0 {
		op, err = m.Delete(key)
	} else {
		s.n++
		op, err = m.Set(key, fmt.Appendf(nil, "site %d write %d", i, s.n))
	}
	if err != nil {
		s.t.Fatalf("replica %d: %v", i, err)
	}
	for j := range s.maps {
		if j != i {
			s.inbox[j] = append(s.inbox[j], op)
		}
	}
}

// deliver hands a replica a random slice of its inbox, shuffled, occasionally
// duplicating an operation. Anything not delivered stays queued.
func (s *mapSimulation) deliver(i int) {
	s.t.Helper()
	queued := s.inbox[i]
	if len(queued) == 0 {
		return
	}
	n := 1 + s.rng.IntN(len(queued))
	batch := append([]MapOp{}, queued[:n]...)
	s.inbox[i] = queued[n:]
	s.rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
	if s.rng.IntN(4) == 0 {
		batch = append(batch, batch[s.rng.IntN(len(batch))])
	}
	if err := s.maps[i].Apply(batch...); err != nil {
		s.t.Fatalf("replica %d: Apply: %v", i, err)
	}
}

func (s *mapSimulation) settle() {
	s.t.Helper()
	for i := range s.maps {
		for len(s.inbox[i]) > 0 {
			s.deliver(i)
		}
	}
}

// assertConverged is the property under test: identical keys, identical values,
// identical bytes, nothing left waiting — and a replica built from what each one
// would send a newcomer is identical too.
func (s *mapSimulation) assertConverged(seed uint64) {
	s.t.Helper()
	want := s.maps[0]
	wantSnapshot := want.Snapshot()
	for i, m := range s.maps {
		if !equalStrings(m.Keys(), want.Keys()) {
			s.t.Fatalf("seed %d: replica %d holds %v, replica 0 holds %v", seed, i, m.Keys(), want.Keys())
		}
		for _, key := range m.Keys() {
			got, _ := m.Get(key)
			expected, _ := want.Get(key)
			if !bytes.Equal(got, expected) {
				s.t.Fatalf("seed %d: replica %d has %q = %q, replica 0 has %q",
					seed, i, key, got, expected)
			}
		}
		if !bytes.Equal(m.Snapshot(), wantSnapshot) {
			s.t.Fatalf("seed %d: replica %d agrees on the values but not on the state", seed, i)
		}
		if m.Pending() != 0 {
			s.t.Fatalf("seed %d: replica %d still holds %d undeliverable operations", seed, i, m.Pending())
		}
		if !m.Version().Equal(want.Version()) {
			s.t.Fatalf("seed %d: replica %d version = %v, replica 0 version = %v",
				seed, i, m.Version(), want.Version())
		}
		fresh := NewMap(SiteID(100 + i))
		if err := fresh.Apply(m.OpsSince(nil)...); err != nil {
			s.t.Fatalf("seed %d: replaying replica %d's history: %v", seed, i, err)
		}
		if !bytes.Equal(fresh.Snapshot(), wantSnapshot) || fresh.Pending() != 0 {
			s.t.Fatalf("seed %d: replica %d's history does not replay to its own state", seed, i)
		}
	}
}

// TestMapConvergence runs many randomised sessions: replicas write and delete
// concurrently while the network delivers late, out of order, and with
// duplicates. Once delivery catches up, every replica must hold the same map.
func TestMapConvergence(t *testing.T) {
	for seed := range uint64(300) {
		s := newMapSimulation(t, seed, 2+int(seed%3))
		for range 12 {
			for i := range s.maps {
				for range 1 + s.rng.IntN(3) {
					s.write(i)
				}
				if s.rng.IntN(2) == 0 {
					s.deliver(i)
				}
			}
		}
		s.settle()
		s.assertConverged(seed)
	}
}

// TestMapConvergenceAcrossSnapshot checks that a replica which joins late by
// loading a snapshot is indistinguishable from one that replayed the history: it
// must keep converging with the others as writing continues.
func TestMapConvergenceAcrossSnapshot(t *testing.T) {
	for seed := range uint64(100) {
		s := newMapSimulation(t, seed, 3)
		for range 6 {
			for i := range s.maps {
				s.write(i)
			}
		}
		s.settle()
		s.assertConverged(seed)

		joined, err := LoadMap(SiteID(len(s.maps)+1), s.maps[0].Snapshot())
		if err != nil {
			t.Fatalf("seed %d: LoadMap: %v", seed, err)
		}
		s.maps = append(s.maps, joined)
		s.inbox = append(s.inbox, nil)

		for range 6 {
			for i := range s.maps {
				s.write(i)
				if s.rng.IntN(2) == 0 {
					s.deliver(i)
				}
			}
		}
		s.settle()
		s.assertConverged(seed)
	}
}

// mapHistory builds a small concurrent history and returns every operation in
// it. Replicas write blind, then exchange everything, then write blind again, so
// the operations form concurrent branches on top of a shared prefix.
func mapHistory(t *testing.T, rng *rand.Rand, sites, writesPerPhase int) []MapOp {
	t.Helper()
	maps := make([]*Map, sites)
	for i := range maps {
		maps[i] = NewMap(SiteID(i + 1))
	}
	var all []MapOp
	for phase := range 2 {
		phaseOps := make([][]MapOp, sites)
		for i, m := range maps {
			for range writesPerPhase {
				key := mapKeys[rng.IntN(2)] // two keys, so every write collides
				var op MapOp
				var err error
				if rng.IntN(3) == 0 {
					op, err = m.Delete(key)
				} else {
					op, err = m.Set(key, fmt.Appendf(nil, "%d/%d", i, phase))
				}
				if err != nil {
					t.Fatalf("phase %d, site %d: %v", phase, i, err)
				}
				phaseOps[i] = append(phaseOps[i], op)
				all = append(all, op)
			}
		}
		for i, m := range maps {
			for j, ops := range phaseOps {
				if i != j {
					applyMap(t, m, ops...)
				}
			}
		}
	}
	return all
}

// TestMapEveryOrderingConverges is the exhaustive form of the convergence
// property: for each small history, applying its operations in every possible
// order must produce byte-identical state. Randomised delivery samples the space
// of orderings; this covers it.
func TestMapEveryOrderingConverges(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 8))
	for trial := range 40 {
		ops := mapHistory(t, rng, 3, 1) // three sites, two phases: six operations
		if len(ops) != 6 {
			t.Fatalf("trial %d: history has %d operations, want 6", trial, len(ops))
		}
		var want []byte
		permute(ops, func(p []MapOp) {
			m := NewMap(99)
			for _, op := range p {
				if err := m.Apply(op); err != nil {
					t.Fatalf("trial %d: Apply: %v", trial, err)
				}
			}
			if m.Pending() != 0 {
				t.Fatalf("trial %d: %d operations never became applicable", trial, m.Pending())
			}
			got := m.Snapshot()
			if want == nil {
				want = got
				return
			}
			if !bytes.Equal(got, want) {
				order := make([]ID, len(p))
				for i, op := range p {
					order[i] = op.ID
				}
				t.Fatalf("trial %d: delivery order %v produced different state; keys %v",
					trial, order, m.Keys())
			}
		})
	}
}

// TestMapOpRoundTrip covers the wire format for each kind, including the empty
// key and the empty value, which are legal and must survive.
func TestMapOpRoundTrip(t *testing.T) {
	ops := []MapOp{
		{Kind: MapSet, ID: ID{Site: 3, Seq: 2}, Clock: 9, Key: "k", Value: []byte("v")},
		{Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "", Value: nil},
		{Kind: MapDelete, ID: ID{Site: 2, Seq: 4}, Clock: 12, Key: "日本"},
		{Kind: MapSuperseded, ID: ID{Site: 5, Seq: 7}, Clock: 7, Span: 1},
		{Kind: MapSuperseded, ID: ID{Site: 5, Seq: 9}, Clock: 9, Span: 9},
	}
	for _, op := range ops {
		encoded, err := op.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary(%+v): %v", op, err)
		}
		var got MapOp
		if err := got.UnmarshalBinary(encoded); err != nil {
			t.Fatalf("UnmarshalBinary(%+v): %v", op, err)
		}
		if got.Kind != op.Kind || got.ID != op.ID || got.Clock != op.Clock ||
			got.Key != op.Key || !bytes.Equal(got.Value, op.Value) || got.Span != op.Span {
			t.Fatalf("round trip gave %+v, want %+v", got, op)
		}
		if err := got.UnmarshalBinary(append(encoded, 0)); err != ErrMalformed {
			t.Fatalf("trailing bytes: %v, want %v", err, ErrMalformed)
		}
	}

	batch, err := AppendMapOps([]byte{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMapOps(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(ops) {
		t.Fatalf("ParseMapOps returned %d operations, want %d", len(parsed), len(ops))
	}
	if _, err := ParseMapOps(append(batch, 0)); err != ErrMalformed {
		t.Fatalf("trailing bytes after a batch: %v, want %v", err, ErrMalformed)
	}
}

func TestMapOpRejectsInvalid(t *testing.T) {
	for name, op := range map[string]MapOp{
		"root identity": {Kind: MapSet, ID: ID{}, Clock: 1},
		"clock below its own sequence number": {
			Kind: MapSet, ID: ID{Site: 1, Seq: 5}, Clock: 4,
		},
		"unknown kind":         {Kind: MapOpKind(9), ID: ID{Site: 1, Seq: 1}, Clock: 1},
		"invalid key":          {Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "\xff"},
		"invalid key, deleted": {Kind: MapDelete, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "\xff"},
		"value on a deletion": {
			Kind: MapDelete, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "k", Value: []byte("v"),
		},
		"key on a superseded run": {
			Kind: MapSuperseded, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "k", Span: 1,
		},
		"value on a superseded run": {
			Kind: MapSuperseded, ID: ID{Site: 1, Seq: 1}, Clock: 1, Value: []byte("v"), Span: 1,
		},
		"a run of nothing": {Kind: MapSuperseded, ID: ID{Site: 1, Seq: 1}, Clock: 1},
		"a run reaching below the first sequence number": {
			Kind: MapSuperseded, ID: ID{Site: 1, Seq: 2}, Clock: 2, Span: 3,
		},
		"a span on a write": {
			Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "k", Span: 1,
		},
		"a span on a deletion": {
			Kind: MapDelete, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "k", Span: 1,
		},
	} {
		if _, err := op.MarshalBinary(); err != ErrInvalidOp {
			t.Fatalf("%s: MarshalBinary = %v, want %v", name, err, ErrInvalidOp)
		}
		if _, err := AppendMapOps(nil, []MapOp{op}); err != ErrInvalidOp {
			t.Fatalf("%s: AppendMapOps = %v, want %v", name, err, ErrInvalidOp)
		}
		m := NewMap(1)
		if err := m.Apply(op); err != ErrInvalidOp {
			t.Fatalf("%s: Apply = %v, want %v", name, err, ErrInvalidOp)
		}
		if m.Len() != 0 {
			t.Fatalf("%s: a rejected batch changed the map", name)
		}
	}
}

// TestMapApplyRejectsWholeBatch pins that a bad operation anywhere in a batch
// leaves the map untouched, so a caller can retry without wondering how far it
// got.
func TestMapApplyRejectsWholeBatch(t *testing.T) {
	source := NewMap(1)
	good := setKey(t, source, "k", "v")
	m := NewMap(2)
	if err := m.Apply(good, MapOp{}); err != ErrInvalidOp {
		t.Fatalf("Apply = %v, want %v", err, ErrInvalidOp)
	}
	if m.Len() != 0 {
		t.Fatal("the valid operation in a rejected batch was applied")
	}
}

func TestMapOpDecoderRejects(t *testing.T) {
	valid, err := MapOp{Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "k", Value: []byte("v")}.
		MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"empty":                {},
		"unknown kind":         {9, 1, 1, 1},
		"truncated identity":   {byte(MapSet)},
		"truncated clock":      {byte(MapSet), 1, 1},
		"truncated key":        {byte(MapSet), 1, 1, 1},
		"key longer than left": {byte(MapSet), 1, 1, 1, 9},
		"truncated value":      {byte(MapSet), 1, 1, 1, 1, 'k'},
		"root identity":        {byte(MapSet), 0, 0, 1, 0, 0},
		"invalid key":          {byte(MapSet), 1, 1, 1, 1, 0xff, 0},
		"truncated":            valid[:len(valid)-1],
	} {
		var op MapOp
		if err := op.UnmarshalBinary(data); err == nil {
			t.Fatalf("%s: decoded to %+v", name, op)
		}
		if _, err := ParseMapOps(append([]byte{1}, data...)); err == nil {
			t.Fatalf("%s: ParseMapOps accepted it", name)
		}
	}
	for name, data := range map[string][]byte{
		"no count":               {},
		"count beyond the batch": {9},
	} {
		if _, err := ParseMapOps(data); err != ErrMalformed {
			t.Fatalf("%s: %v, want %v", name, err, ErrMalformed)
		}
	}
	// A superseded operation reads a span where the others read a key, and stops
	// there, so bytes after it are trailing rather than a key.
	if _, err := ParseMapOps([]byte{1, byte(MapSuperseded), 1, 1, 1}); err != ErrMalformed {
		t.Fatal("a superseded operation without a span was accepted")
	}
	if _, err := ParseMapOps([]byte{1, byte(MapSuperseded), 1, 1, 1, 1, 0}); err != ErrMalformed {
		t.Fatal("trailing bytes after a superseded operation were accepted")
	}
}

// encodeMapSnapshot assembles snapshot bytes from parts: a uint64 is a varint, a
// string is length-prefixed, a byte slice is raw. It builds the malformed cases
// below exactly rather than by mutating a valid snapshot and hoping.
func encodeMapSnapshot(parts ...any) []byte {
	out := append([]byte{}, mapMagic[:]...)
	out = append(out, mapVersion)
	// Version 2: the clock tombstones were collected under. Nothing built here
	// has ever been collected, so it is zero — and a case that wants to say
	// otherwise passes it as the first part.
	out = binary.AppendUvarint(out, 0)
	for _, part := range parts {
		switch v := part.(type) {
		case uint64:
			out = binary.AppendUvarint(out, v)
		case string:
			out = appendKey(out, v)
		case []byte:
			out = append(out, v...)
		default:
			panic(fmt.Sprintf("unsupported part %T", part))
		}
	}
	return out
}

func TestMapLoadRejects(t *testing.T) {
	// One site at sequence 3, then whatever the case under test supplies.
	header := func(records ...any) []byte {
		return encodeMapSnapshot(append([]any{uint64(1), uint64(1), uint64(3)}, records...)...)
	}
	for name, data := range map[string][]byte{
		"empty":                                 {},
		"short magic":                           []byte("crd"),
		"foreign magic":                         []byte("xxxxx\x01"),
		"a text snapshot":                       New(1).Snapshot(),
		"no version":                            []byte("crdtm"),
		"future version":                        []byte("crdtm\x02"),
		"truncated vector":                      encodeMapSnapshot(uint64(2), uint64(1)),
		"vector site without a sequence number": encodeMapSnapshot(uint64(1), uint64(1)),
		"sequence number of zero":               encodeMapSnapshot(uint64(1), uint64(1), uint64(0)),
		"site listed twice":                     encodeMapSnapshot(uint64(2), uint64(1), uint64(3), uint64(1), uint64(4)),
		"no key count":                          encodeMapSnapshot(uint64(0)),
		"key count beyond the snapshot":         header(uint64(9)),
		"truncated key":                         header(uint64(1), uint64(4), []byte("ab")),
		"invalid key":                           header(uint64(1), []byte{1, 0xff}, uint64(1), uint64(1), uint64(1), []byte{1}, uint64(0)),
		"keys out of order": header(uint64(2),
			"b", uint64(1), uint64(1), uint64(1), []byte{0},
			"a", uint64(1), uint64(2), uint64(2), []byte{0}),
		"key given twice": header(uint64(2),
			"a", uint64(1), uint64(1), uint64(1), []byte{0},
			"a", uint64(1), uint64(2), uint64(2), []byte{0}),
		"truncated identity":              header(uint64(1), "a", uint64(1)),
		"truncated clock":                 header(uint64(1), "a", uint64(1), uint64(1)),
		"no presence byte":                header(uint64(1), "a", uint64(1), uint64(1), uint64(1)),
		"unknown presence":                header(uint64(1), "a", uint64(1), uint64(1), uint64(1), []byte{2}),
		"root identity":                   header(uint64(1), "a", uint64(0), uint64(0), uint64(1), []byte{0}),
		"unpromised site":                 header(uint64(1), "a", uint64(7), uint64(1), uint64(1), []byte{0}),
		"sequence beyond the vector":      header(uint64(1), "a", uint64(1), uint64(4), uint64(4), []byte{0}),
		"clock below the sequence number": header(uint64(1), "a", uint64(1), uint64(3), uint64(2), []byte{0}),
		"one operation, two records": header(uint64(2),
			"a", uint64(1), uint64(1), uint64(1), []byte{0},
			"b", uint64(1), uint64(1), uint64(1), []byte{0}),
		"truncated value": header(uint64(1), "a", uint64(1), uint64(1), uint64(1), []byte{1}, uint64(4), []byte("ab")),
		"trailing bytes":  append(header(uint64(0)), 0),
	} {
		if m, err := LoadMap(1, data); err != ErrMalformed {
			t.Fatalf("%s: LoadMap = %v, %v; want %v", name, m, err, ErrMalformed)
		}
	}

	// The smallest thing that must be accepted, so the table above is refusing
	// what it claims to refuse and not merely everything.
	m, err := LoadMap(1, header(uint64(1), "a", uint64(1), uint64(3), uint64(3), []byte{1}, uint64(1), []byte("v")))
	if err != nil {
		t.Fatalf("a well-formed snapshot was refused: %v", err)
	}
	if got := value(t, m, "a"); got != "v" {
		t.Fatalf("Get(a) = %q", got)
	}
}

// TestMapLoadKeepsTheVectorsPromise records what this decoder can check and what
// it cannot. A map forgets the operations it has superseded, so — unlike the text
// document's Load — it cannot insist that every operation the vector promises is
// accounted for. What it can insist on is that no record claims an operation the
// vector does not promise, and that no operation is claimed twice; the rest of
// the sequence is the superseded set, and [Map.OpsSince] derives it from exactly
// this difference.
func TestMapLoadKeepsTheVectorsPromise(t *testing.T) {
	source := NewMap(1)
	setKey(t, source, "k", "first")
	setKey(t, source, "k", "second")
	loaded, err := LoadMap(2, source.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	ops := loaded.OpsSince(nil)
	if len(ops) != 2 || ops[0].Kind != MapSuperseded {
		t.Fatalf("a loaded map offers %v, not the history it stands for", ops)
	}
	fresh := NewMap(3)
	applyMap(t, fresh, ops...)
	if !bytes.Equal(fresh.Snapshot(), source.Snapshot()) {
		t.Fatal("a map loaded from a snapshot cannot serve a peer the original could")
	}
}

func TestMapSizedReader(t *testing.T) {
	r := &reader{buf: []byte{2, 'h', 'i'}}
	got, ok := r.sized()
	if !ok || string(got) != "hi" || len(r.buf) != 0 {
		t.Fatalf("sized() = %q, %v, leaving %d bytes", got, ok, len(r.buf))
	}
	for name, buf := range map[string][]byte{
		"no length":                {},
		"length beyond the buffer": {9, 'h'},
	} {
		r := &reader{buf: buf}
		if got, ok := r.sized(); ok {
			t.Fatalf("%s: sized() = %q, true", name, got)
		}
	}
}

func TestCloneBytes(t *testing.T) {
	if got := cloneBytes(nil); got != nil {
		t.Fatalf("cloneBytes(nil) = %v", got)
	}
	if got := cloneBytes([]byte{}); got != nil {
		t.Fatalf("cloneBytes(empty) = %v, want nil: the two are one value", got)
	}
	source := []byte("value")
	got := cloneBytes(source)
	source[0] = 'X'
	if string(got) != "value" {
		t.Fatalf("cloneBytes returned an alias: %q", got)
	}
}

// mapCorpus returns encoded operations and a snapshot from a small real session,
// to seed the fuzzers with input shaped like the real thing.
func mapCorpus(t *testing.T) (ops []byte, snapshot []byte) {
	t.Helper()
	a, b := NewMap(1), NewMap(2)
	written := []MapOp{setKey(t, a, "k", "value"), setKey(t, a, "gone", "value")}
	applyMap(t, b, written...)
	written = append(written, deleteKey(t, b, "gone"), setKey(t, a, "k", "again"))
	applyMap(t, a, written[2])
	encoded, err := AppendMapOps(nil, a.OpsSince(nil))
	if err != nil {
		t.Fatal(err)
	}
	return encoded, a.Snapshot()
}

func FuzzParseMapOps(f *testing.F) {
	ops, _ := mapCorpus(&testing.T{})
	f.Add(ops)
	f.Add([]byte{})
	f.Add([]byte{1})

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseMapOps(data)
		if err != nil {
			return
		}
		// Anything that decodes must re-encode to something that decodes the same
		// way; otherwise two replicas could disagree about what they received.
		encoded, err := AppendMapOps(nil, parsed)
		if err != nil {
			t.Fatalf("re-encoding accepted operations failed: %v", err)
		}
		again, err := ParseMapOps(encoded)
		if err != nil {
			t.Fatalf("re-encoded operations no longer parse: %v", err)
		}
		if len(again) != len(parsed) {
			t.Fatalf("round trip changed the batch size: %d, want %d", len(again), len(parsed))
		}
		for i := range again {
			if !sameMapOp(again[i], parsed[i]) {
				t.Fatalf("round trip changed operation %d: %+v, want %+v", i, again[i], parsed[i])
			}
		}
		// Whatever it accepts, the map must stay coherent: applying the same batch
		// twice must change nothing, and nothing may panic.
		m := NewMap(99)
		if err := m.Apply(parsed...); err != nil {
			t.Fatalf("applying accepted operations was rejected: %v", err)
		}
		if m.Len() != len(m.Keys()) {
			t.Fatalf("Len() = %d, but Keys() returned %d", m.Len(), len(m.Keys()))
		}
		before := m.Snapshot()
		if err := m.Apply(parsed...); err != nil {
			t.Fatalf("replaying an accepted batch was rejected: %v", err)
		}
		if !bytes.Equal(m.Snapshot(), before) {
			t.Fatal("replaying an accepted batch changed the map")
		}
	})
}

func FuzzLoadMap(f *testing.F) {
	_, snapshot := mapCorpus(&testing.T{})
	f.Add(snapshot)
	f.Add([]byte("crdtm\x01"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := LoadMap(1, data)
		if err != nil {
			return
		}
		if m.Len() != len(m.Keys()) {
			t.Fatalf("Len() = %d, but Keys() returned %d", m.Len(), len(m.Keys()))
		}
		// Re-encoding a loaded map must be a fixed point: the snapshot format has
		// one canonical form, whatever an accepted input looked like.
		encoded := m.Snapshot()
		again, err := LoadMap(1, encoded)
		if err != nil {
			t.Fatalf("a map could not reload its own snapshot: %v", err)
		}
		if !bytes.Equal(again.Snapshot(), encoded) {
			t.Fatal("re-encoding a loaded snapshot is not a fixed point")
		}
		// And the history has to survive the round trip too: what a loaded map
		// would send a newcomer must rebuild the same map.
		replayed := NewMap(2)
		if err := replayed.Apply(m.OpsSince(nil)...); err != nil {
			t.Fatalf("replaying a loaded map's history was rejected: %v", err)
		}
		if replayed.Pending() != 0 {
			t.Fatalf("%d operations never became applicable", replayed.Pending())
		}
		// The state, not the bytes: a replica that replays another's history
		// does not inherit its collection. What the sending map collected
		// under is in its snapshot and in no operation — nothing on the wire
		// says "and I have forgotten some tombstones" — so a newcomer rebuilds
		// the same keys and values while remembering no clock of its own.
		if replayed.CollectedBelow() != 0 {
			t.Fatalf("a newcomer inherited a collection clock of %d", replayed.CollectedBelow())
		}
		if m.CollectedBelow() == 0 {
			if !bytes.Equal(replayed.Snapshot(), encoded) {
				t.Fatal("replaying a loaded map's history did not reproduce it")
			}
		} else if !sameContents(replayed, m) {
			t.Fatal("replaying a collected map's history did not reproduce what it holds")
		}
		// A key must not be able to smuggle in a byte sequence a browser peer
		// could not hold as a string.
		for _, key := range m.sortedKeys() {
			if !utf8.ValidString(key) {
				t.Fatalf("a key that is not valid UTF-8 was accepted: %q", key)
			}
		}
	})
}

// Site and Stamp are what something built on a map needs to order two writes
// the same way the map did — see structured.Tree, which decides which of two
// concurrent moves happened later.
func TestMapSiteAndStamp(t *testing.T) {
	m := NewMap(7)
	if m.Site() != 7 {
		t.Fatalf("the map writes as site %d, want 7", m.Site())
	}

	if _, _, ok := m.Stamp("absent"); ok {
		t.Fatal("a key that was never written has a stamp")
	}

	op, err := m.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	clock, site, ok := m.Stamp("k")
	if !ok {
		t.Fatal("a key that was just written has no stamp")
	}
	if clock != op.Clock || site != op.ID.Site {
		t.Fatalf("the stamp is (%d, %d), want the write's (%d, %d)",
			clock, site, op.Clock, op.ID.Site)
	}

	// A peer's write that wins takes the stamp with it.
	high := MapOp{Kind: MapSet, ID: ID{Site: 9, Seq: 1}, Clock: op.Clock + 1, Key: "k", Value: []byte("w")}
	if err := m.Apply(high); err != nil {
		t.Fatal(err)
	}
	if clock, site, _ := m.Stamp("k"); clock != high.Clock || site != 9 {
		t.Fatalf("after a peer's write the stamp is (%d, %d), want (%d, 9)", clock, site, high.Clock)
	}

	// A deleted key has no stamp, even though the map keeps its clock.
	if _, err := m.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := m.Stamp("k"); ok {
		t.Fatal("a deleted key still reports a stamp")
	}
}

// sameContents reports whether two maps hold the same keys with the same
// values, which is what survives a replay when one of them has collected.
func sameContents(a, b *Map) bool {
	ka, kb := a.Keys(), b.Keys()
	if len(ka) != len(kb) {
		return false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
		va, _ := a.Get(ka[i])
		vb, _ := b.Get(kb[i])
		if !bytes.Equal(va, vb) {
			return false
		}
	}
	return true
}
