package crdt

import (
	"encoding/binary"
	"errors"
)

// A [List] replicates a sequence of values the way [Doc] replicates a sequence
// of characters, and by the same algorithm: every element names the element it
// was inserted after, so concurrent insertions land in an order every replica
// agrees on without anyone arbitrating.
//
// It exists because the things built around a document are sequences too — the
// comments on it, the record of who changed what, the messages beside it — and
// they need the same guarantees as the text: edit while disconnected, merge
// afterwards, converge.
//
// The two are separate types rather than one generic one, because their shapes
// differ by orders of magnitude. A document holds hundreds of thousands of
// characters and earns run-length storage and an index over runs; a list holds
// tens or hundreds of values, where a slice is both faster and obviously
// correct. Sharing the code would mean carrying the document's machinery for no
// gain, and a list's element is a byte slice the caller encodes rather than a
// rune this package understands.

// ErrEmptyValue reports an attempt to insert a value of no bytes. A list of
// nothings is almost always a caller encoding badly, and allowing it would make
// "absent" and "empty" the same on the wire.
var ErrEmptyValue = errors.New("crdt: a list value must not be empty")

// A ListOp is one indivisible change to a list. Like [Op] it is self-describing
// and applying it is idempotent, so a transport may duplicate or reorder freely.
type ListOp struct {
	// Kind selects insertion or deletion.
	Kind OpKind
	// ID names this operation, its Seq counting the issuing site's operations.
	ID ID
	// Clock is the Lamport timestamp ordering this against concurrent
	// operations; see the package documentation on the two counters.
	Clock uint64
	// Origin is the element the new one is inserted after; the zero ID means
	// the start of the list. OpInsert only.
	Origin ID
	// Value is the inserted element. OpInsert only.
	Value []byte
	// Target is the element to remove. OpDelete only.
	Target ID
}

// validate reports why an operation cannot be applied, or nil.
func (o ListOp) validate() error {
	if o.ID.IsRoot() || o.Clock < o.ID.Seq ||
		!o.Origin.wellFormed() || !o.Target.wellFormed() {
		return ErrInvalidOp
	}
	switch o.Kind {
	case OpInsert:
		if !o.Target.IsRoot() || len(o.Value) == 0 {
			return ErrInvalidOp
		}
	case OpDelete:
		if o.Target.IsRoot() || !o.Origin.IsRoot() || o.Value != nil {
			return ErrInvalidOp
		}
	default:
		return ErrInvalidOp
	}
	return nil
}

// An element is one value of the sequence, present or removed. Elements are
// never dropped: a concurrent insertion may still name a removed element as its
// origin.
type element struct {
	id     ID
	clock  uint64
	origin ID
	value  []byte
	delID  ID // zero while the element is present
}

func (e *element) present() bool { return e.delID.IsRoot() }

// A List is one replica of a sequence of values. It is not safe for concurrent
// use; serialize access from the outside.
//
// The zero List is unusable — construct one with [NewList] or [LoadList].
type List struct {
	site  SiteID
	clock uint64
	vv    VersionVector

	// elements holds the sequence in order, tombstones included. A slice rather
	// than a linked list: a list is small enough that moving pointers costs less
	// than chasing them, and it makes position lookup immediate.
	elements []*element
	byID     map[ID]int // element identity to its place in elements

	pending map[ID][]ListOp
	parked  int

	dupDeletes map[ID]ID
	present    int
}

// NewList returns an empty list that issues operations as site. Every replica
// editing a list concurrently must pass a distinct site; see [SiteID].
func NewList(site SiteID) *List {
	return &List{site: site, vv: VersionVector{}, byID: map[ID]int{}}
}

// Site returns the replica identity this list issues operations as.
func (l *List) Site() SiteID { return l.site }

// Len returns the number of values present.
func (l *List) Len() int { return l.present }

// Tombstones returns the number of removed values still held. They cannot be
// dropped: a concurrent insertion may still name one as its origin.
func (l *List) Tombstones() int { return len(l.elements) - l.present }

// Pending returns the number of operations waiting for the operations they
// depend on.
func (l *List) Pending() int { return l.parked }

// Version returns a copy of the version vector describing which operations this
// replica holds.
func (l *List) Version() VersionVector { return l.vv.Clone() }

// Get returns a copy of the value at index pos.
func (l *List) Get(pos int) ([]byte, error) {
	if pos < 0 || pos >= l.present {
		return nil, ErrOutOfRange
	}
	e := l.elements[l.at(pos)]
	return append([]byte(nil), e.value...), nil
}

// Values returns copies of every value present, in order.
func (l *List) Values() [][]byte {
	out := make([][]byte, 0, l.present)
	for _, e := range l.elements {
		if e.present() {
			out = append(out, append([]byte(nil), e.value...))
		}
	}
	return out
}

// at returns the place in elements of the value at index pos, which the caller
// must have checked is in range.
func (l *List) at(pos int) int {
	i, n := 0, 0
	for ; ; i++ {
		if !l.elements[i].present() {
			continue
		}
		if n == pos {
			break
		}
		n++
	}
	return i
}

// mint allocates this site's next operation identity and Lamport timestamp.
func (l *List) mint() (ID, uint64) {
	l.clock++
	return ID{Site: l.site, Seq: l.vv[l.site] + 1}, l.clock
}

// Insert adds values at index pos and returns the operations describing it. The
// operations are already applied here; send them to every peer.
//
// pos may equal [List.Len], which appends. Inserting nothing is a no-op.
func (l *List) Insert(pos int, values ...[]byte) ([]ListOp, error) {
	if pos < 0 || pos > l.present {
		return nil, ErrOutOfRange
	}
	for _, v := range values {
		if len(v) == 0 {
			return nil, ErrEmptyValue
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	origin := ID{}
	if pos > 0 {
		origin = l.elements[l.at(pos-1)].id
	}
	ops := make([]ListOp, 0, len(values))
	for _, v := range values {
		id, clock := l.mint()
		op := ListOp{
			Kind:   OpInsert,
			ID:     id,
			Clock:  clock,
			Origin: origin,
			Value:  append([]byte(nil), v...),
		}
		l.integrate(op)
		ops = append(ops, op)
		origin = id
	}
	return ops, nil
}

// Delete removes count values from index pos and returns the operations
// describing it. Removing nothing is a no-op.
func (l *List) Delete(pos, count int) ([]ListOp, error) {
	if pos < 0 || count < 0 || pos+count > l.present {
		return nil, ErrOutOfRange
	}
	if count == 0 {
		return nil, nil
	}
	// Collect the targets first: removing one shifts every later index.
	targets := make([]ID, 0, count)
	for i := l.at(pos); len(targets) < count; i++ {
		if e := l.elements[i]; e.present() {
			targets = append(targets, e.id)
		}
	}
	ops := make([]ListOp, 0, count)
	for _, target := range targets {
		id, clock := l.mint()
		op := ListOp{Kind: OpDelete, ID: id, Clock: clock, Target: target}
		l.integrate(op)
		ops = append(ops, op)
	}
	return ops, nil
}

// Apply integrates operations from peers. Duplicates are ignored, and an
// operation arriving before what it depends on waits until that lands.
//
// A malformed operation is rejected and nothing in the batch is applied.
func (l *List) Apply(ops ...ListOp) error {
	for _, op := range ops {
		if err := op.validate(); err != nil {
			return err
		}
	}
	for _, op := range ops {
		l.admit(op)
	}
	return nil
}

// admit integrates an operation, or files it under whatever it waits for, and
// then does the same for everything that was waiting on what it integrated.
func (l *List) admit(op ListOp) {
	queue := []ListOp{op}
	for len(queue) > 0 {
		next := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if l.vv.Includes(next.ID) {
			continue
		}
		if !l.ready(next) {
			l.park(next)
			continue
		}
		l.integrate(next)
		if woken, ok := l.pending[next.ID]; ok {
			delete(l.pending, next.ID)
			l.parked -= len(woken)
			queue = append(queue, woken...)
		}
	}
}

// ready reports whether an operation can be integrated now.
func (l *List) ready(op ListOp) bool {
	if op.ID.Seq != l.vv[op.ID.Site]+1 {
		return false
	}
	if op.Kind == OpInsert {
		return op.Origin.IsRoot() || l.known(op.Origin)
	}
	return l.known(op.Target)
}

func (l *List) known(id ID) bool {
	_, ok := l.byID[id]
	return ok
}

// park files an operation under the one it is waiting for.
func (l *List) park(op ListOp) {
	if l.pending == nil {
		l.pending = map[ID][]ListOp{}
	}
	waitingFor := l.blockedOn(op)
	l.pending[waitingFor] = append(l.pending[waitingFor], op)
	l.parked++
}

// blockedOn names the operation that has to land before this one can be tried
// again.
func (l *List) blockedOn(op ListOp) ID {
	if op.ID.Seq != l.vv[op.ID.Site]+1 {
		return ID{Site: op.ID.Site, Seq: op.ID.Seq - 1}
	}
	if op.Kind == OpInsert {
		return op.Origin
	}
	return op.Target
}

// integrate applies a ready, validated operation.
func (l *List) integrate(op ListOp) {
	if op.Clock > l.clock {
		l.clock = op.Clock
	}
	l.vv[op.ID.Site] = op.ID.Seq
	if op.Kind == OpDelete {
		l.tombstone(op)
		return
	}

	// Walk past the elements sorting after the new one, exactly as the document
	// does: everything inserted after the origin by a replica that had seen it
	// carries a higher Lamport clock, so the first element sorting before the
	// new one lies outside the region this insertion can land in.
	at := 0
	if !op.Origin.IsRoot() {
		at = l.byID[op.Origin] + 1
	}
	for at < len(l.elements) {
		e := l.elements[at]
		if !before(op.Clock, op.ID.Site, e.clock, e.id.Site) {
			break
		}
		at++
	}

	fresh := &element{id: op.ID, clock: op.Clock, origin: op.Origin, value: op.Value}
	l.elements = append(l.elements, nil)
	copy(l.elements[at+1:], l.elements[at:])
	l.elements[at] = fresh
	for i := at; i < len(l.elements); i++ {
		l.byID[l.elements[i].id] = i
	}
	l.present++
}

// tombstone marks an element removed.
//
// Two replicas may remove the same element at once. The element can record only
// one of those operations, so it keeps the lower ID — the same choice on every
// replica, whatever order they arrive in — and the other is remembered
// separately so it can still be replayed to peers.
func (l *List) tombstone(op ListOp) {
	e := l.elements[l.byID[op.Target]]
	switch {
	case e.present():
		e.delID = op.ID
		l.present--
	case idLess(op.ID, e.delID):
		l.recordListDuplicate(e.delID, op.Target)
		e.delID = op.ID
	default:
		l.recordListDuplicate(op.ID, op.Target)
	}
}

func (l *List) recordListDuplicate(delID, target ID) {
	if l.dupDeletes == nil {
		l.dupDeletes = map[ID]ID{}
	}
	l.dupDeletes[delID] = target
}

// Anchor returns the identity of the value at index pos, which keeps naming that
// value however the list moves around it — what a reference to a comment should
// hold. pos may equal [List.Len], which anchors to the end and returns the zero
// ID.
func (l *List) Anchor(pos int) (ID, error) {
	if pos < 0 || pos > l.present {
		return ID{}, ErrOutOfRange
	}
	if pos == l.present {
		return ID{}, nil
	}
	return l.elements[l.at(pos)].id, nil
}

// Position returns where the value an anchor names sits now, or where it was if
// it has been removed. ok is false for an identity this list has never seen.
func (l *List) Position(anchor ID) (pos int, ok bool) {
	if anchor.IsRoot() {
		return l.present, true
	}
	at, known := l.byID[anchor]
	if !known {
		return 0, false
	}
	n := 0
	for _, e := range l.elements[:at] {
		if e.present() {
			n++
		}
	}
	return n, true
}

// Visible reports whether the value an anchor names is still in the list.
func (l *List) Visible(anchor ID) bool {
	if anchor.IsRoot() {
		return true
	}
	at, known := l.byID[anchor]
	return known && l.elements[at].present()
}

// OpsSince returns the operations this replica holds that vv does not, ready to
// send to the peer that produced vv. Pass a nil vector for the whole history.
//
// The result is in list order, which for insertions is a causal order: an
// element always follows its origin.
func (l *List) OpsSince(vv VersionVector) []ListOp {
	var ops []ListOp
	for _, e := range l.elements {
		if !vv.Includes(e.id) {
			ops = append(ops, ListOp{
				Kind:   OpInsert,
				ID:     e.id,
				Clock:  e.clock,
				Origin: e.origin,
				Value:  append([]byte(nil), e.value...),
			})
		}
		if !e.delID.IsRoot() && !vv.Includes(e.delID) {
			ops = append(ops, ListOp{
				Kind: OpDelete, ID: e.delID, Clock: e.delID.Seq, Target: e.id,
			})
		}
	}
	dups := make([]ID, 0, len(l.dupDeletes))
	for delID := range l.dupDeletes {
		if !vv.Includes(delID) {
			dups = append(dups, delID)
		}
	}
	sortIDs(dups)
	for _, delID := range dups {
		ops = append(ops, ListOp{
			Kind: OpDelete, ID: delID, Clock: delID.Seq, Target: l.dupDeletes[delID],
		})
	}
	return ops
}

// listMagic prefixes a list snapshot, so a document snapshot handed to [LoadList]
// is refused rather than misread.
var listMagic = [...]byte{'c', 'r', 'd', 'l'}

const listVersion = 1

// Snapshot encodes the whole list — every value, present or removed, in order,
// plus the version vector. It is what a server sends a client joining, and what
// it persists.
//
// The encoding is deterministic: two replicas holding the same operations
// produce identical bytes, so a snapshot doubles as a convergence check. The
// full history is recoverable: [List.OpsSince] on a loaded list returns what it
// would have on the original.
func (l *List) Snapshot() []byte {
	out := append([]byte{}, listMagic[:]...)
	out = append(out, listVersion)

	sites := l.vv.sites()
	out = binary.AppendUvarint(out, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, l.vv[site])
	}

	out = binary.AppendUvarint(out, uint64(len(l.elements)))
	for _, e := range l.elements {
		out = binary.AppendUvarint(out, uint64(e.id.Site))
		out = binary.AppendUvarint(out, e.id.Seq)
		out = binary.AppendUvarint(out, e.clock)
		out = binary.AppendUvarint(out, uint64(e.origin.Site))
		out = binary.AppendUvarint(out, e.origin.Seq)
		out = binary.AppendUvarint(out, uint64(e.delID.Site))
		out = binary.AppendUvarint(out, e.delID.Seq)
		out = binary.AppendUvarint(out, uint64(len(e.value)))
		out = append(out, e.value...)
	}

	dups := make([]ID, 0, len(l.dupDeletes))
	for delID := range l.dupDeletes {
		dups = append(dups, delID)
	}
	sortIDs(dups)
	out = binary.AppendUvarint(out, uint64(len(dups)))
	for _, delID := range dups {
		target := l.dupDeletes[delID]
		out = binary.AppendUvarint(out, uint64(delID.Site))
		out = binary.AppendUvarint(out, delID.Seq)
		out = binary.AppendUvarint(out, uint64(target.Site))
		out = binary.AppendUvarint(out, target.Seq)
	}
	return out
}

// LoadList rebuilds a list from a snapshot, to be edited as site.
func LoadList(site SiteID, snapshot []byte) (*List, error) {
	r := &reader{buf: snapshot}
	magic, ok := r.bytes(len(listMagic))
	if !ok || string(magic) != string(listMagic[:]) {
		return nil, ErrMalformed
	}
	if v, ok := r.bytes(1); !ok || v[0] != listVersion {
		return nil, ErrMalformed
	}

	l := NewList(site)
	nSites, ok := r.uvarint()
	if !ok {
		return nil, ErrMalformed
	}
	for range nSites {
		s, ok1 := r.uvarint()
		seq, ok2 := r.uvarint()
		if !ok1 || !ok2 || seq == 0 {
			return nil, ErrMalformed
		}
		if _, dup := l.vv[SiteID(s)]; dup {
			return nil, ErrMalformed
		}
		l.vv[SiteID(s)] = seq
	}

	// A snapshot has to account for every operation its version vector claims,
	// exactly once, or the list could not reproduce its own history.
	//
	// The ledger gets its own copy of the vector, because putting the elements
	// back runs the same integration an operation would and that advances the
	// list's vector as it goes. The snapshot's vector is the authority, and is
	// put back once everything is in.
	promised := l.vv.Clone()
	ledger := &ledger{vv: promised, seen: map[ID]struct{}{}, counts: map[SiteID]uint64{}}

	count, ok := r.uvarint()
	if !ok || count > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	for range count {
		if err := l.adopt(r, ledger); err != nil {
			return nil, err
		}
	}

	nDups, ok := r.uvarint()
	if !ok || nDups > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	for range nDups {
		delID, ok1 := r.id()
		target, ok2 := r.id()
		if !ok1 || !ok2 || !ledger.claim(delID) || target.IsRoot() {
			return nil, ErrMalformed
		}
		at, known := l.byID[target]
		if !known || l.elements[at].present() || !idLess(l.elements[at].delID, delID) {
			return nil, ErrMalformed
		}
		l.recordListDuplicate(delID, target)
	}
	if len(r.buf) != 0 || !ledger.complete() {
		return nil, ErrMalformed
	}
	l.vv = promised

	// The Lamport clock must be at least as high as anything recorded, including
	// the sequence numbers of removals, whose clocks a snapshot does not keep.
	for _, seq := range l.vv {
		if seq > l.clock {
			l.clock = seq
		}
	}
	return l, nil
}

// adopt decodes one element and puts it in, checking that it is consistent with
// what came before.
//
// The order a snapshot states is not a matter of choice: integration determines
// exactly where an element goes. So rather than trusting the stated order, the
// element is integrated as an operation would be and then required to have
// landed at the end. Anywhere else means the snapshot claims an order
// integration could not have produced.
func (l *List) adopt(r *reader, ldg *ledger) error {
	id, ok1 := r.id()
	clock, ok2 := r.uvarint()
	origin, ok3 := r.id()
	delID, ok4 := r.id()
	size, ok5 := r.uvarint()
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || size == 0 || size > uint64(len(r.buf)) {
		return ErrMalformed
	}
	value, ok := r.bytes(int(size))
	if !ok || clock < id.Seq || !origin.wellFormed() || !delID.wellFormed() {
		return ErrMalformed
	}
	if !ldg.claim(id) {
		return ErrMalformed
	}
	if !origin.IsRoot() && !l.known(origin) {
		return ErrMalformed
	}
	if !delID.IsRoot() && !ldg.claim(delID) {
		return ErrMalformed
	}

	l.integrate(ListOp{
		Kind:   OpInsert,
		ID:     id,
		Clock:  clock,
		Origin: origin,
		Value:  append([]byte(nil), value...),
	})
	if l.byID[id] != len(l.elements)-1 {
		return ErrMalformed
	}
	if !delID.IsRoot() {
		l.elements[len(l.elements)-1].delID = delID
		l.present--
	}
	return nil
}
