package crdt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// A list is the document's algorithm over values instead of characters, so it is
// held to the document's standard: convergence demonstrated against randomised
// delivery and against every permutation of small histories, replicas compared
// on their encoded state rather than merely on what they show.

func values(t *testing.T, l *List) []string {
	t.Helper()
	out := make([]string, 0, l.Len())
	for _, v := range l.Values() {
		out = append(out, string(v))
	}
	return out
}

func assertList(t *testing.T, l *List, want ...string) {
	t.Helper()
	got := values(t, l)
	if len(got) != len(want) {
		t.Fatalf("list holds %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("list holds %q, want %q", got, want)
		}
	}
}

// put inserts values and fails the test if the list refuses them.
func put(t *testing.T, l *List, pos int, vals ...string) []ListOp {
	t.Helper()
	raw := make([][]byte, len(vals))
	for i, v := range vals {
		raw[i] = []byte(v)
	}
	ops, err := l.Insert(pos, raw...)
	if err != nil {
		t.Fatalf("Insert(%d, %q): %v", pos, vals, err)
	}
	return ops
}

func drop(t *testing.T, l *List, pos, count int) []ListOp {
	t.Helper()
	ops, err := l.Delete(pos, count)
	if err != nil {
		t.Fatalf("Delete(%d, %d): %v", pos, count, err)
	}
	return ops
}

func send(t *testing.T, l *List, ops []ListOp) {
	t.Helper()
	if err := l.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestListEdits(t *testing.T) {
	l := NewList(1)
	if l.Len() != 0 || len(l.Values()) != 0 {
		t.Fatalf("a new list holds %d values", l.Len())
	}
	if got := l.Site(); got != 1 {
		t.Errorf("Site() = %d, want 1", got)
	}

	put(t, l, 0, "second", "third")
	put(t, l, 0, "first")
	put(t, l, l.Len(), "fourth")
	assertList(t, l, "first", "second", "third", "fourth")

	got, err := l.Get(2)
	if err != nil || string(got) != "third" {
		t.Fatalf("Get(2) = %q, %v; want \"third\"", got, err)
	}
	// What the caller is handed is a copy.
	got[0] = 'X'
	if again, _ := l.Get(2); string(again) != "third" {
		t.Fatalf("Get returned the list's own bytes: %q", again)
	}

	drop(t, l, 1, 2)
	assertList(t, l, "first", "fourth")
	if got, want := l.Tombstones(), 2; got != want {
		t.Errorf("Tombstones() = %d, want %d", got, want)
	}
	if got := l.Pending(); got != 0 {
		t.Errorf("Pending() = %d, want 0", got)
	}
}

// What the caller passes in is copied too, or a later mutation would rewrite
// history.
func TestListCopiesWhatItIsGiven(t *testing.T) {
	l := NewList(1)
	value := []byte("mine")
	if _, err := l.Insert(0, value); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	assertList(t, l, "mine")

	peer := NewList(2)
	send(t, peer, l.OpsSince(nil))
	assertList(t, peer, "mine")
}

func TestListEditErrors(t *testing.T) {
	l := NewList(1)
	put(t, l, 0, "a", "b")
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"insert before the start", func() error { _, err := l.Insert(-1, []byte("x")); return err }, ErrOutOfRange},
		{"insert past the end", func() error { _, err := l.Insert(3, []byte("x")); return err }, ErrOutOfRange},
		{"insert nothing at all", func() error { _, err := l.Insert(0, []byte{}); return err }, ErrEmptyValue},
		{"delete before the start", func() error { _, err := l.Delete(-1, 1); return err }, ErrOutOfRange},
		{"delete a negative count", func() error { _, err := l.Delete(0, -1); return err }, ErrOutOfRange},
		{"delete past the end", func() error { _, err := l.Delete(1, 2); return err }, ErrOutOfRange},
		{"get past the end", func() error { _, err := l.Get(2); return err }, ErrOutOfRange},
		{"get before the start", func() error { _, err := l.Get(-1); return err }, ErrOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	assertList(t, l, "a", "b")

	// Edits that do nothing produce nothing, and consume no sequence number.
	if ops, err := l.Insert(0); err != nil || ops != nil {
		t.Fatalf("Insert of no values = %v, %v", ops, err)
	}
	if ops, err := l.Delete(0, 0); err != nil || ops != nil {
		t.Fatalf("Delete of nothing = %v, %v", ops, err)
	}
	if got, want := l.Version().Get(1), uint64(2); got != want {
		t.Fatalf("Version() = %d, want %d", got, want)
	}
}

func TestListRejectsMalformedOperations(t *testing.T) {
	valid := ID{Site: 1, Seq: 1}
	tests := []struct {
		name string
		op   ListOp
	}{
		{"unknown kind", ListOp{Kind: 7, ID: valid, Clock: 1, Value: []byte("x")}},
		{"root identity", ListOp{Kind: OpInsert, Clock: 1, Value: []byte("x")}},
		{"clock below sequence", ListOp{Kind: OpInsert, ID: ID{Seq: 5}, Clock: 4, Value: []byte("x")}},
		{"insertion with a target", ListOp{Kind: OpInsert, ID: valid, Clock: 1, Value: []byte("x"), Target: valid}},
		{"insertion of nothing", ListOp{Kind: OpInsert, ID: valid, Clock: 1}},
		{"insertion after a malformed origin", ListOp{Kind: OpInsert, ID: valid, Clock: 1, Value: []byte("x"), Origin: ID{Site: 2}}},
		{"removal of nothing", ListOp{Kind: OpDelete, ID: valid, Clock: 1}},
		{"removal with an origin", ListOp{Kind: OpDelete, ID: valid, Clock: 1, Target: valid, Origin: valid}},
		{"removal carrying a value", ListOp{Kind: OpDelete, ID: valid, Clock: 1, Target: valid, Value: []byte("x")}},
		{"removal of a malformed target", ListOp{Kind: OpDelete, ID: valid, Clock: 1, Target: ID{Site: 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewList(9)
			if err := l.Apply(tt.op); !errors.Is(err, ErrInvalidOp) {
				t.Fatalf("Apply = %v, want ErrInvalidOp", err)
			}
			if l.Len() != 0 {
				t.Fatal("a rejected operation changed the list")
			}
		})
	}
}

// Delivery in reverse is the worst case for the waiting buffer: nothing is
// applicable until the very first operation arrives last.
func TestListApplyOutOfOrder(t *testing.T) {
	a, b := NewList(1), NewList(2)
	ops := put(t, a, 0, "one", "two", "three")
	ops = append(ops, drop(t, a, 1, 1)...)

	reversed := make([]ListOp, len(ops))
	for i, op := range ops {
		reversed[len(ops)-1-i] = op
	}
	for _, op := range reversed[:len(reversed)-1] {
		send(t, b, []ListOp{op})
		if b.Pending() == 0 {
			t.Fatalf("operation %v was applied although its dependencies are missing", op.ID)
		}
	}
	send(t, b, reversed[len(reversed)-1:])
	if got := b.Pending(); got != 0 {
		t.Fatalf("Pending() = %d, want 0", got)
	}
	assertList(t, b, "one", "three")
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("the replicas agree on the values but not on the state")
	}

	// Applying it all again changes nothing.
	send(t, b, ops)
	assertList(t, b, "one", "three")
	if got, want := b.Tombstones(), 1; got != want {
		t.Fatalf("Tombstones() = %d, want %d: a replayed removal counted twice", got, want)
	}
}

// Two replicas removing one value at the same time must agree on the list, on
// the state, and both removals must still be replayable to a third.
func TestListConcurrentRemovalOfOneValue(t *testing.T) {
	a, b := NewList(1), NewList(2)
	seed := put(t, a, 0, "keep", "go", "keep too")
	send(t, b, seed)

	fromA := drop(t, a, 1, 1)
	fromB := drop(t, b, 1, 1)
	send(t, a, fromB)
	send(t, b, fromA)

	assertList(t, a, "keep", "keep too")
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("the replicas agree on the values but not on the state")
	}

	c := NewList(3)
	send(t, c, a.OpsSince(nil))
	assertList(t, c, "keep", "keep too")
	if !c.Version().Equal(a.Version()) {
		t.Fatalf("the third replica's version is %v, want %v: a removal was not replayed",
			c.Version(), a.Version())
	}
}

// Concurrent insertions at one position are ordered by Lamport clock, highest
// first, so both replicas show the same thing whoever delivered first.
func TestListConcurrentInsertAtOnePosition(t *testing.T) {
	a, b := NewList(1), NewList(2)
	seed := put(t, a, 0, "start", "end")
	send(t, b, seed)

	fromA := put(t, a, 1, "A")
	fromB := put(t, b, 1, "B")
	send(t, a, fromB)
	send(t, b, fromA)

	if got, want := values(t, a), values(t, b); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("diverged: %q against %q", got, want)
	}
	assertList(t, a, "start", "B", "A", "end")
}

func TestListAnchors(t *testing.T) {
	l := NewList(1)
	put(t, l, 0, "one", "two", "three")

	anchor, err := l.Anchor(1)
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	put(t, l, 0, "zero")
	pos, ok := l.Position(anchor)
	if !ok || pos != 2 {
		t.Fatalf("Position after inserting before = %d, %v; want 2, true", pos, ok)
	}
	if !l.Visible(anchor) {
		t.Fatal("the anchored value is reported as gone")
	}

	drop(t, l, 2, 1)
	if l.Visible(anchor) {
		t.Fatal("the anchored value is reported as present after removal")
	}
	if pos, _ := l.Position(anchor); pos != 2 {
		t.Fatalf("Position of the removed value = %d, want 2 — where the list closed up", pos)
	}

	end, err := l.Anchor(l.Len())
	if err != nil || !end.IsRoot() {
		t.Fatalf("Anchor at the end = %v, %v; want the zero ID", end, err)
	}
	if pos, ok := l.Position(end); !ok || pos != l.Len() {
		t.Fatalf("Position of the end = %d, %v", pos, ok)
	}
	if !l.Visible(end) {
		t.Fatal("the end of the list is reported as gone")
	}
	if _, err := l.Anchor(-1); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Anchor(-1) = %v, want ErrOutOfRange", err)
	}
	if _, ok := l.Position(ID{Site: 9, Seq: 9}); ok {
		t.Error("an unknown anchor was resolved")
	}
	if l.Visible(ID{Site: 9, Seq: 9}) {
		t.Error("an unknown anchor is reported as visible")
	}
}

// listSimulation is the same unreliable network the document's tests use.
type listSimulation struct {
	t     *testing.T
	rng   *rand.Rand
	lists []*List
	inbox [][]ListOp
}

func newListSimulation(t *testing.T, seed uint64, replicas int) *listSimulation {
	t.Helper()
	s := &listSimulation{
		t:     t,
		rng:   rand.New(rand.NewPCG(seed, 0x115)),
		lists: make([]*List, replicas),
		inbox: make([][]ListOp, replicas),
	}
	for i := range s.lists {
		s.lists[i] = NewList(SiteID(i + 1))
	}
	return s
}

func (s *listSimulation) edit(i int) {
	s.t.Helper()
	l := s.lists[i]
	var ops []ListOp
	var err error
	if l.Len() > 0 && s.rng.IntN(3) == 0 {
		pos := s.rng.IntN(l.Len())
		ops, err = l.Delete(pos, 1+s.rng.IntN(l.Len()-pos))
	} else {
		n := 1 + s.rng.IntN(3)
		vals := make([][]byte, n)
		for k := range vals {
			vals[k] = fmt.Appendf(nil, "%d-%d", i, s.rng.IntN(1000))
		}
		ops, err = l.Insert(s.rng.IntN(l.Len()+1), vals...)
	}
	if err != nil {
		s.t.Fatalf("replica %d: %v", i, err)
	}
	for k := range s.lists {
		if k != i {
			s.inbox[k] = append(s.inbox[k], ops...)
		}
	}
}

func (s *listSimulation) deliver(i int) {
	s.t.Helper()
	queued := s.inbox[i]
	if len(queued) == 0 {
		return
	}
	n := 1 + s.rng.IntN(len(queued))
	batch := append([]ListOp{}, queued[:n]...)
	s.inbox[i] = queued[n:]
	s.rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
	if s.rng.IntN(4) == 0 {
		batch = append(batch, batch[s.rng.IntN(len(batch))])
	}
	if err := s.lists[i].Apply(batch...); err != nil {
		s.t.Fatalf("replica %d: Apply: %v", i, err)
	}
}

// TestListConvergence is the acceptance gate: replicas editing at once while the
// network delivers late, out of order and twice over.
func TestListConvergence(t *testing.T) {
	for seed := range uint64(200) {
		s := newListSimulation(t, seed, 2+int(seed%3))
		for range 10 {
			for i := range s.lists {
				for range 1 + s.rng.IntN(2) {
					s.edit(i)
				}
				if s.rng.IntN(2) == 0 {
					s.deliver(i)
				}
			}
		}
		for i := range s.lists {
			for len(s.inbox[i]) > 0 {
				s.deliver(i)
			}
		}
		want := s.lists[0].Snapshot()
		for i, l := range s.lists {
			if l.Pending() != 0 {
				t.Fatalf("seed %d: replica %d still holds %d operations", seed, i, l.Pending())
			}
			if !bytes.Equal(l.Snapshot(), want) {
				t.Fatalf("seed %d: replica %d holds %q, replica 0 holds %q",
					seed, i, values(t, l), values(t, s.lists[0]))
			}
		}

		// The history replays into a fresh replica, and a snapshot round-trips.
		fresh := NewList(99)
		send(t, fresh, s.lists[0].OpsSince(nil))
		if !bytes.Equal(fresh.Snapshot(), want) {
			t.Fatalf("seed %d: replaying the history did not reproduce the state", seed)
		}
		loaded, err := LoadList(98, want)
		if err != nil {
			t.Fatalf("seed %d: LoadList: %v", seed, err)
		}
		if !bytes.Equal(loaded.Snapshot(), want) {
			t.Fatalf("seed %d: a snapshot did not reload to itself", seed)
		}
	}
}

// Randomised delivery samples the orderings; this covers them. Every permutation
// of a small concurrent history must produce byte-identical state.
func TestListEveryOrderingConverges(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 8))
	for trial := range 30 {
		s := newListSimulation(t, uint64(trial), 3)
		var all []ListOp
		for phase := range 2 {
			batches := make([][]ListOp, len(s.lists))
			for i, l := range s.lists {
				var ops []ListOp
				var err error
				if phase == 1 && l.Len() > 0 && rng.IntN(3) == 0 {
					ops, err = l.Delete(rng.IntN(l.Len()), 1)
				} else {
					ops, err = l.Insert(rng.IntN(l.Len()+1), fmt.Appendf(nil, "v%d%d", trial, i))
				}
				if err != nil {
					t.Fatalf("trial %d: %v", trial, err)
				}
				batches[i] = ops
				all = append(all, ops...)
			}
			for i, l := range s.lists {
				for k, ops := range batches {
					if i != k {
						send(t, l, ops)
					}
				}
			}
		}
		if len(all) != 6 {
			t.Fatalf("trial %d: history has %d operations, want 6", trial, len(all))
		}

		var want []byte
		permuteList(all, func(p []ListOp) {
			l := NewList(99)
			for _, op := range p {
				if err := l.Apply(op); err != nil {
					t.Fatalf("trial %d: Apply: %v", trial, err)
				}
			}
			if l.Pending() != 0 {
				t.Fatalf("trial %d: %d operations never became applicable", trial, l.Pending())
			}
			got := l.Snapshot()
			if want == nil {
				want = got
				return
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("trial %d: one delivery order produced different state", trial)
			}
		})
	}
}

// permuteList calls fn with every ordering of ops (Heap's algorithm).
func permuteList(ops []ListOp, fn func([]ListOp)) {
	work := append([]ListOp{}, ops...)
	counters := make([]int, len(work))
	fn(work)
	for i := 0; i < len(work); {
		if counters[i] >= i {
			counters[i] = 0
			i++
			continue
		}
		j := 0
		if i%2 == 1 {
			j = counters[i]
		}
		work[i], work[j] = work[j], work[i]
		fn(work)
		counters[i]++
		i = 0
	}
}

func TestLoadListRejectsRubbish(t *testing.T) {
	l := NewList(1)
	put(t, l, 0, "one", "two")
	drop(t, l, 0, 1)
	good := l.Snapshot()

	if _, err := LoadList(2, good); err != nil {
		t.Fatalf("LoadList of a good snapshot: %v", err)
	}
	// A document snapshot is not a list snapshot, and is refused rather than
	// misread.
	doc := New(1)
	if _, err := doc.Insert(0, "text"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadList(2, doc.Snapshot()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("LoadList of a document snapshot = %v, want ErrMalformed", err)
	}
	for n := range len(good) {
		if _, err := LoadList(2, good[:n]); err == nil {
			t.Fatalf("LoadList(%d of %d bytes) succeeded, want an error", n, len(good))
		}
	}
	if _, err := LoadList(2, append(append([]byte{}, good...), 0)); !errors.Is(err, ErrMalformed) {
		t.Fatal("LoadList accepted trailing bytes")
	}
	bad := append([]byte{}, good...)
	bad[4] = listVersion + 1
	if _, err := LoadList(2, bad); !errors.Is(err, ErrMalformed) {
		t.Fatal("LoadList accepted a future format version")
	}
}

// listBuilder assembles list snapshots field by field, so every rejection in the
// decoder can be provoked directly rather than by corrupting good bytes and
// hoping the corruption lands where it is needed.
type listBuilder struct {
	version  byte
	sites    [][2]uint64
	elements []encodedElement
	count    int // overrides the encoded number of elements
	dups     [][4]uint64
	dupCount int
	tail     []byte
}

type encodedElement struct {
	site, seq, clock, originSite, originSeq, delSite, delSeq uint64
	value                                                    string
	size                                                     uint64 // overrides the encoded length
}

func (b listBuilder) build() []byte {
	out := append([]byte{}, "crdl"...)
	version := b.version
	if version == 0 {
		version = listVersion
	}
	out = append(out, version)
	out = binary.AppendUvarint(out, uint64(len(b.sites)))
	for _, s := range b.sites {
		out = binary.AppendUvarint(out, s[0])
		out = binary.AppendUvarint(out, s[1])
	}
	n := b.count
	if n == 0 {
		n = len(b.elements)
	}
	out = binary.AppendUvarint(out, uint64(n))
	for _, e := range b.elements {
		for _, v := range []uint64{e.site, e.seq, e.clock, e.originSite, e.originSeq, e.delSite, e.delSeq} {
			out = binary.AppendUvarint(out, v)
		}
		size := e.size
		if size == 0 {
			size = uint64(len(e.value))
		}
		out = binary.AppendUvarint(out, size)
		out = append(out, e.value...)
	}
	dn := b.dupCount
	if dn == 0 {
		dn = len(b.dups)
	}
	out = binary.AppendUvarint(out, uint64(dn))
	for _, d := range b.dups {
		for _, v := range d {
			out = binary.AppendUvarint(out, v)
		}
	}
	return append(out, b.tail...)
}

// wellFormedList is two values from site 1, the second removed, which is the
// shape every rejection below varies from.
func wellFormedList() listBuilder {
	return listBuilder{
		sites: [][2]uint64{{1, 3}},
		elements: []encodedElement{
			{site: 1, seq: 1, clock: 1, value: "one"},
			{site: 1, seq: 2, clock: 2, originSite: 1, originSeq: 1, value: "two", delSite: 1, delSeq: 3},
		},
	}
}

func TestLoadListAcceptsAHandBuiltSnapshot(t *testing.T) {
	l, err := LoadList(2, wellFormedList().build())
	if err != nil {
		t.Fatalf("LoadList: %v", err)
	}
	assertList(t, l, "one")
	if got, want := l.Tombstones(), 1; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}
	if !bytes.Equal(l.Snapshot(), wellFormedList().build()) {
		t.Fatal("re-encoding a hand-built snapshot did not reproduce it")
	}
}

func TestLoadListRejectsMalformedSnapshots(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*listBuilder)
	}{
		{"a site with no operations", func(b *listBuilder) { b.sites = [][2]uint64{{1, 0}} }},
		{"the same site twice", func(b *listBuilder) { b.sites = [][2]uint64{{1, 3}, {1, 3}} }},
		{"more elements than bytes", func(b *listBuilder) { b.count = 1 << 20 }},
		{"more duplicates than bytes", func(b *listBuilder) { b.dupCount = 1 << 20 }},
		{"a value of no bytes", func(b *listBuilder) { b.elements[0].value = ""; b.elements[0].size = 0 }},
		{"a value longer than the snapshot", func(b *listBuilder) { b.elements[0].size = 1 << 20 }},
		{"a clock below the sequence", func(b *listBuilder) { b.elements[1].clock = 1 }},
		{"an identity of zero", func(b *listBuilder) { b.elements[0].seq = 0 }},
		{"an origin with no sequence", func(b *listBuilder) { b.elements[0].originSite = 1 }},
		{"a removal with no sequence", func(b *listBuilder) { b.elements[1].delSite, b.elements[1].delSeq = 1, 0 }},
		{"an identity the vector does not cover", func(b *listBuilder) { b.elements[1].seq = 9 }},
		{"a repeated identity", func(b *listBuilder) { b.elements[1].seq = b.elements[0].seq }},
		{"an origin that does not exist", func(b *listBuilder) { b.elements[1].originSeq = 7 }},
		{"a removal the vector does not cover", func(b *listBuilder) { b.elements[1].delSeq = 9 }},
		{"a history with a gap", func(b *listBuilder) { b.sites = [][2]uint64{{1, 4}} }},
		{"an order integration could not produce", func(b *listBuilder) {
			// Both hang off the start, so the higher clock has to come first.
			b.sites = [][2]uint64{{1, 2}, {2, 1}}
			b.elements = []encodedElement{
				{site: 2, seq: 1, clock: 1, value: "a"},
				{site: 1, seq: 1, clock: 9, value: "b"},
				{site: 1, seq: 2, clock: 10, originSite: 1, originSeq: 1, value: "c"},
			}
		}},
		{"a duplicate removal of nothing", func(b *listBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.dups = [][4]uint64{{1, 4, 0, 0}}
		}},
		{"a duplicate removal of a value still present", func(b *listBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.dups = [][4]uint64{{1, 4, 1, 1}}
		}},
		{"a duplicate removal of a value that does not exist", func(b *listBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.dups = [][4]uint64{{1, 4, 1, 9}}
		}},
		{"a duplicate removal below the one kept", func(b *listBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.elements[1].delSeq = 4
			b.dups = [][4]uint64{{1, 3, 1, 2}}
		}},
		{"a duplicate removal the vector does not cover", func(b *listBuilder) {
			b.dups = [][4]uint64{{1, 9, 1, 2}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := wellFormedList()
			tt.alter(&b)
			if _, err := LoadList(2, b.build()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("LoadList() = %v, want ErrMalformed", err)
			}
		})
	}
}
