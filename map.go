package crdt

import (
	"encoding/binary"
	"sort"
	"strconv"
	"unicode/utf8"
)

// A spreadsheet is a map of cells, not a sequence of them: a cell is written
// and cleared, never woven in between its neighbours. Last-writer-wins is the
// whole of the merge rule, which makes the interesting part of a replicated map
// not the merge but what a replica is allowed to forget once a write has lost.

// MapOpKind distinguishes the operations a replicated map exchanges.
type MapOpKind uint8

const (
	// MapSet writes a value at a key.
	MapSet MapOpKind = 1
	// MapDelete removes a key, and leaves its clock behind. See [Map].
	MapDelete MapOpKind = 2
	// MapSuperseded stands in for operations whose values the sending replica no
	// longer holds, because later writes to the same keys replaced them. It names
	// no key and carries no value: it exists so that a peer catching up can
	// account for the sequence numbers and move on. Only [Map.OpsSince] produces
	// one.
	//
	// It covers a run of consecutive sequence numbers rather than one, because
	// the numbers a replica has forgotten are exactly the gaps between the ones
	// it still holds: a key written a million times leaves one record and one
	// run, not a million operations to send.
	MapSuperseded MapOpKind = 3
)

// String renders the kind for diagnostics.
func (k MapOpKind) String() string {
	switch k {
	case MapSet:
		return "set"
	case MapDelete:
		return "delete"
	case MapSuperseded:
		return "superseded"
	default:
		return "invalid(" + strconv.FormatUint(uint64(k), 10) + ")"
	}
}

// A MapOp is one indivisible change to a [Map]. It is the only thing replicas
// exchange, it is self-describing, and applying it is idempotent, so a transport
// may duplicate or reorder operations freely.
//
// Which fields carry meaning depends on Kind: Key belongs to MapSet and
// MapDelete, Value to MapSet alone, Span to MapSuperseded alone. The unused
// fields must be zero, and are checked, so a garbled operation is rejected
// rather than silently reinterpreted.
type MapOp struct {
	// Kind selects writing, removal, or values the sender has forgotten.
	Kind MapOpKind
	// ID names this operation. Its Seq is the issuing site's own counter and
	// increases by exactly one per operation. For MapSuperseded it is the last
	// of the sequence numbers the operation accounts for.
	ID ID
	// Clock is the Lamport timestamp that orders this operation against
	// concurrent writes to the same key. See the package documentation.
	Clock uint64
	// Key is the key written or removed. MapSet and MapDelete only.
	Key string
	// Value is the bytes written. MapSet only; it may be empty.
	Value []byte
	// Span is how many consecutive sequence numbers this operation accounts for,
	// ending at ID.Seq. MapSuperseded only, where it is at least one.
	Span uint64
}

// first returns the lowest sequence number the operation accounts for. Only a
// superseded run ever accounts for more than its own.
func (o MapOp) first() uint64 {
	if o.Kind == MapSuperseded {
		return o.ID.Seq - o.Span + 1
	}
	return o.ID.Seq
}

// validate reports why an operation cannot be applied, or nil.
//
// A key must be valid UTF-8 because a key crosses into JavaScript, where a
// string is UTF-16 and bytes that are not text cannot survive the trip — the
// same reason [Doc] refuses invalid UTF-8 rather than substituting for it. A
// value has no such constraint: it is opaque, and nothing here reads it.
func (o MapOp) validate() error {
	if !wellFormedStamp(o.ID, o.Clock) {
		return ErrInvalidOp
	}
	switch o.Kind {
	case MapSet:
		if !utf8.ValidString(o.Key) || o.Span != 0 {
			return ErrInvalidOp
		}
	case MapDelete:
		if !utf8.ValidString(o.Key) || len(o.Value) != 0 || o.Span != 0 {
			return ErrInvalidOp
		}
	case MapSuperseded:
		// A run of none says nothing, and one reaching below the first sequence
		// number a site ever issues names operations that cannot exist.
		if o.Key != "" || len(o.Value) != 0 || o.Span == 0 || o.Span > o.ID.Seq {
			return ErrInvalidOp
		}
	default:
		return ErrInvalidOp
	}
	return nil
}

// appendTo encodes the operation onto dst: a kind byte, then unsigned varints,
// then the length-prefixed key and value the kind calls for.
func (o MapOp) appendTo(dst []byte) []byte {
	dst = append(dst, byte(o.Kind))
	dst = binary.AppendUvarint(dst, uint64(o.ID.Site))
	dst = binary.AppendUvarint(dst, o.ID.Seq)
	dst = binary.AppendUvarint(dst, o.Clock)
	if o.Kind == MapSuperseded {
		return binary.AppendUvarint(dst, o.Span)
	}
	dst = appendKey(dst, o.Key)
	if o.Kind == MapSet {
		dst = appendSized(dst, o.Value)
	}
	return dst
}

// MarshalBinary encodes the operation. It reports ErrInvalidOp rather than
// producing bytes that would be rejected on arrival.
func (o MapOp) MarshalBinary() ([]byte, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	return o.appendTo(nil), nil
}

// UnmarshalBinary decodes an operation written by MarshalBinary. Trailing bytes
// are an error: an operation is decoded from exactly its own encoding.
func (o *MapOp) UnmarshalBinary(data []byte) error {
	op, rest, err := decodeMapOp(data)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return ErrMalformed
	}
	*o = op
	return nil
}

// decodeMapOp reads one operation and returns the bytes after it.
func decodeMapOp(data []byte) (MapOp, []byte, error) {
	if len(data) == 0 {
		return MapOp{}, nil, ErrMalformed
	}
	op := MapOp{Kind: MapOpKind(data[0])}
	if op.Kind != MapSet && op.Kind != MapDelete && op.Kind != MapSuperseded {
		return MapOp{}, nil, ErrInvalidOp
	}
	r := &reader{buf: data[1:]}
	id, ok1 := r.id()
	clock, ok2 := r.uvarint()
	if !ok1 || !ok2 {
		return MapOp{}, nil, ErrMalformed
	}
	op.ID, op.Clock = id, clock
	if op.Kind == MapSuperseded {
		span, ok := r.uvarint()
		if !ok {
			return MapOp{}, nil, ErrMalformed
		}
		op.Span = span
	} else {
		key, ok := r.sized()
		if !ok {
			return MapOp{}, nil, ErrMalformed
		}
		op.Key = string(key)
	}
	if op.Kind == MapSet {
		value, ok := r.sized()
		if !ok {
			return MapOp{}, nil, ErrMalformed
		}
		// The decoded value must not alias the buffer it came from: that buffer
		// is the caller's, and a transport commonly reuses it.
		op.Value = cloneBytes(value)
	}
	if err := op.validate(); err != nil {
		return MapOp{}, nil, err
	}
	return op, r.buf, nil
}

// AppendMapOps encodes a batch of operations onto dst — the form a transport
// sends. The batch is length-prefixed, so ParseMapOps can reject a truncated
// message instead of silently returning the operations that happened to survive.
func AppendMapOps(dst []byte, ops []MapOp) ([]byte, error) {
	for _, op := range ops {
		if err := op.validate(); err != nil {
			return nil, err
		}
	}
	dst = binary.AppendUvarint(dst, uint64(len(ops)))
	for _, op := range ops {
		dst = op.appendTo(dst)
	}
	return dst, nil
}

// ParseMapOps decodes a batch written by AppendMapOps.
func ParseMapOps(data []byte) ([]MapOp, error) {
	count, used := uvarint(data)
	if used <= 0 {
		return nil, ErrMalformed
	}
	rest := data[used:]
	// An operation is at least four bytes, so a count larger than the remaining
	// bytes allow is a corrupt header — refuse it before allocating for it.
	if count > uint64(len(rest)) {
		return nil, ErrMalformed
	}
	ops := make([]MapOp, 0, count)
	for range count {
		op, tail, err := decodeMapOp(rest)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
		rest = tail
	}
	if len(rest) != 0 {
		return nil, ErrMalformed
	}
	return ops, nil
}

// A Map is a replicated key-value map: any number of replicas may write to it at
// once, offline, in any delivery order, and every replica ends up holding the
// same keys and the same values. Values are opaque bytes — the caller encodes
// whatever it likes — and they are copied in and out, so no slice is ever shared
// between a caller and the map.
//
// Writes to one key are ordered by the same (clock, site) total order the text
// uses: a Lamport clock raised past everything the replica has seen, ties broken
// by site. The highest write wins, and it wins everywhere whatever order the
// writes arrived in, because a maximum does not depend on the order it is taken
// in. Writes to different keys never interact at all.
//
// # A deleted key keeps its clock
//
// A deletion leaves a record behind rather than dropping the key. Dropping it
// would leave nothing for an older write arriving later to lose against, so that
// write would take effect — resurrecting the key on the replica that heard it
// late and not on the one that heard it early, permanently. Keeping the clock is
// also what makes a delete and a concurrent set to the same key resolve
// identically everywhere. [Map.Len] and [Map.Keys] do not count a deleted key;
// [Map.Snapshot] writes it.
//
// # What a replica may forget, and what it may not
//
// A map keeps one record per key, so the value a write put there is gone the
// moment a later write replaces it. The operation is not gone. Sequence numbers
// are contiguous per site — that is what lets a [VersionVector] describe a
// replica exactly — and [Map.Apply] never skips one: an operation that arrives
// before its predecessor waits for it rather than being dropped. A peer catching
// up therefore has to be told that the number was used, and [Map.OpsSince] tells
// it with a [MapSuperseded] operation, which names no key, carries no value, and
// covers a whole run of numbers at once. Catching up a peer from nothing
// therefore costs one operation per key it does not hold plus one per stretch it
// does not need, never one per write ever made.
//
// That is sound only because the operation which superseded it travels in the
// same batch: it is either the record now held for that key, which OpsSince
// sends whenever the peer lacks it, or something the peer already has. A caller
// that filters what OpsSince returns breaks the map.
//
// A Map is not safe for concurrent use. The zero Map is unusable — construct one
// with [NewMap] or [LoadMap].
type Map struct {
	site  SiteID
	clock uint64
	vv    VersionVector

	// records holds one entry per key ever written, tombstones included.
	records map[string]mapRecord

	// pending holds operations that arrived before the operation the same site
	// issued before them, filed under the one they are waiting for.
	pending map[ID][]MapOp
	parked  int

	// parkSlab is where a parked operation's one-element slice comes from; see
	// Doc.parkSlab, which explains why one allocation per parked operation was
	// worth removing and why widening the map's value instead was not.
	parkSlab []MapOp

	// live counts the records that are not tombstones, so Len answers without
	// walking them.
	live int

	// touched, when not nil, gathers the keys whose value or presence a batch
	// changed. It is set only for the duration of ApplyChanges, so Apply pays
	// nothing for it.
	touched map[string]struct{}

	// collectedBelow is the highest clock a tombstone was dropped under by
	// [Map.Collect], and zero for a map nobody has collected. It is what
	// recognises a write that would have lost to a tombstone that is gone; see
	// [Map.Collect].
	collectedBelow uint64

	// lastClock is the clock of the last operation integrated from each site.
	// A site issues in increasing clock order and a transport delivers a site's
	// operations in order, so the next operation to arrive from that site has a
	// clock above this one — which is the only thing a replica can say about
	// what has not arrived yet. See [Map.LastClocks], which is what a caller
	// computes a collection floor from.
	lastClock map[SiteID]uint64
}

// A mapRecord is what a replica keeps for one key: the write that is winning,
// and the identity that decides what may beat it.
type mapRecord struct {
	id    ID
	clock uint64
	value []byte
	dead  bool
}

// op rebuilds the operation the record was written by, which is what a peer
// missing it has to be sent.
func (r mapRecord) op(key string) MapOp {
	if r.dead {
		return MapOp{Kind: MapDelete, ID: r.id, Clock: r.clock, Key: key}
	}
	return MapOp{Kind: MapSet, ID: r.id, Clock: r.clock, Key: key, Value: cloneBytes(r.value)}
}

// NewMap returns an empty map that issues operations as site. Every replica
// writing to a map concurrently must pass a distinct site; see [SiteID].
func NewMap(site SiteID) *Map {
	return &Map{site: site, vv: VersionVector{}, records: map[string]mapRecord{}}
}

// mint allocates this site's next operation identity and Lamport timestamp. Seq
// follows the site's own version vector entry, so the site's sequence never has
// a gap; the clock jumps past everything the replica has seen.
func (m *Map) mint() (ID, uint64) {
	m.clock++
	return ID{Site: m.site, Seq: m.vv[m.site] + 1}, m.clock
}

// Site returns the replica this map writes as. [Doc] and [List] answer the same
// question the same way.
func (m *Map) Site() SiteID { return m.site }

// Clock reports the Lamport clock this replica has reached.
//
// Every operation it issues from now on carries a clock above this one, which
// is what makes it the bound on a site that has written nothing yet: a
// participant handed this document writes later than it, whatever it does. A
// caller building a floor for [Map.Collect] needs that bound for the sites
// [Map.LastClocks] cannot speak for.
func (m *Map) Clock() uint64 { return m.clock }

// Stamp returns the (clock, site) the current value of key was written at, and
// whether the key is live. It is the total order the map resolves concurrent
// writes by, made readable so that something built on the map can order two
// writes the same way the map did — see structured.Tree, which has to decide
// which of two concurrent moves happened later.
func (m *Map) Stamp(key string) (clock uint64, site SiteID, ok bool) {
	rec, held := m.records[key]
	if !held || rec.dead {
		return 0, 0, false
	}
	return rec.clock, rec.id.Site, true
}

// Get returns the value stored at key, and whether the key is present. The value
// is a copy: writing to it does not change what the map holds.
//
// A value stored as empty reads back as nil — the two are one value here, and
// the encoding cannot tell them apart either.
func (m *Map) Get(key string) ([]byte, bool) {
	rec, held := m.records[key]
	if !held || rec.dead {
		return nil, false
	}
	return cloneBytes(rec.value), true
}

// Set stores value at key and returns the operation that describes it. The
// operation is already applied here; send it to every peer.
//
// The map copies value, so the caller may reuse the slice, and the returned
// operation carries a copy of its own, so writing to it cannot reach the map.
// A key that is not valid UTF-8 is refused; see [MapOp].
func (m *Map) Set(key string, value []byte) (MapOp, error) {
	if !utf8.ValidString(key) {
		return MapOp{}, ErrInvalidText
	}
	if err := room(m.clock, 1); err != nil {
		return MapOp{}, err
	}
	id, clock := m.mint()
	op := MapOp{Kind: MapSet, ID: id, Clock: clock, Key: key, Value: cloneBytes(value)}
	m.integrate(op)
	return op, nil
}

// Delete removes key and returns the operation that describes it.
//
// It writes a tombstone whether or not this replica holds the key. What a
// deletion means cannot depend on what the deleting replica happens to have
// heard: a peer's write still in flight has to lose to a later deletion on every
// replica, including one that has not yet seen the write it is beating.
func (m *Map) Delete(key string) (MapOp, error) {
	if !utf8.ValidString(key) {
		return MapOp{}, ErrInvalidText
	}
	if err := room(m.clock, 1); err != nil {
		return MapOp{}, err
	}
	id, clock := m.mint()
	op := MapOp{Kind: MapDelete, ID: id, Clock: clock, Key: key}
	m.integrate(op)
	return op, nil
}

// Keys returns the keys present, in ascending order, so that anything a caller
// derives from them is the same on every replica. Deleted keys are not among
// them.
func (m *Map) Keys() []string {
	out := make([]string, 0, m.live)
	for key, rec := range m.records {
		if !rec.dead {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Len returns how many keys are present, not counting deleted ones.
func (m *Map) Len() int { return m.live }

// Tombstones returns how many keys are held only to keep their clock. They are
// the one thing here that grows without bound, so a caller measuring what a long
// session costs measures this.
func (m *Map) Tombstones() int { return len(m.records) - m.live }

// Pending reports how many received operations are still waiting for the
// operation their own site issued before them.
func (m *Map) Pending() int { return m.parked }

// DropPending forgets the operations this replica is holding back, and returns
// how many there were.
//
// An operation that arrives before the one it depends on is parked, which is
// right: it may become applicable a moment later, and dropping it silently
// would lose an edit. What is not right is that nothing bounds the pile. A peer
// sending operations that can never apply — each waiting on a sequence number
// that site never issues — costs about 140 bytes apiece, forever, for a
// document that stays empty.
//
// This is the lever for that, and it is safe for one reason: a parked operation
// has had no effect on the state, so it is not in the version vector. A peer
// asked what this replica is missing sends it again. Dropping and re-syncing
// therefore loses nothing and diverges from nobody — which is asserted in the
// tests rather than argued here.
//
// It is deliberately the caller's decision. A cap inside this package would
// have to choose what to do when it is reached, and the only answers are to
// drop — which is a policy, not a merge rule — or to refuse an Apply for
// reasons that have nothing to do with what it was handed.
func (m *Map) DropPending() int {
	n := m.parked
	m.pending = nil
	m.parkSlab = nil
	m.parked = 0
	return n
}

// Version returns what this replica holds, to be handed to a peer that will send
// back what it is missing; see [Map.OpsSince].
func (m *Map) Version() VersionVector { return m.vv.Clone() }

// Apply integrates operations from peers. Duplicates are ignored, and an
// operation that arrives before the operation its site issued before it is
// buffered until that one lands, so the caller needs no ordered delivery.
//
// A malformed operation is rejected and nothing in the batch is applied.
func (m *Map) Apply(ops ...MapOp) error {
	_, err := m.applyWith(false, ops, nil)
	return err
}

// ApplyChanges is [Map.Apply], and also reports which keys it changed: those
// whose value or presence is not what it was, in ascending order so that two
// replicas given the same batch report the same thing.
//
// Only what actually happened is reported. An operation already applied, one
// still waiting for the operation its site issued before it, and one that lost
// to a write already held all change nothing and say nothing; when a waiting
// one lands, its key is reported then.
//
// A key is named, not its new value. A caller reads what it wants of the key it
// is told about, which is also what keeps this honest when a batch writes one
// key twice: the key appears once, and reading it gives the winner.
func (m *Map) ApplyChanges(ops ...MapOp) ([]string, error) {
	return m.applyWith(true, ops, nil)
}

func (m *Map) applyWith(watching bool, ops []MapOp, absorbed *[]MapOp) ([]string, error) {
	for _, op := range ops {
		if err := op.validate(); err != nil {
			return nil, err
		}
	}
	if watching {
		m.touched = map[string]struct{}{}
		defer func() { m.touched = nil }()
	}
	for _, op := range ops {
		if err := m.admit(op, absorbed); err != nil {
			return nil, err
		}
	}
	if !watching || len(m.touched) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(m.touched))
	for key := range m.touched {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

// admit integrates an operation and then anything that was waiting for it,
// through an explicit queue rather than by recursion: a history delivered back
// to front unblocks a chain as long as the history itself.
// admit takes an optional place to record what it integrates; see
// [Doc.admit] on why it is a parameter and not a field.
func (m *Map) admit(op MapOp, absorbed *[]MapOp) error {
	queue := []MapOp{op}
	for len(queue) > 0 {
		next := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if m.vv.Includes(next.ID) {
			continue // already applied; applying twice must not change anything
		}
		had := m.vv[next.ID.Site]
		if next.first() > had+1 {
			m.park(next)
			continue
		}
		if m.resurrects(next) {
			return ErrStranded
		}
		m.integrate(next)
		if absorbed != nil {
			*absorbed = append(*absorbed, next)
		}
		queue = m.wake(queue, next.ID.Site, had, next.ID.Seq)
	}
	return nil
}

// park files an operation under the one it is waiting for.
//
// A write depends on no key and on no other site, only on the operation its own
// site issued before the first one it accounts for, so there is only ever one
// thing to wait for. That predecessor exists: an operation reaching back to
// sequence number one is either ready or already applied, so nothing parked here
// waits on the root.
func (m *Map) park(op MapOp) {
	if m.pending == nil {
		m.pending = map[ID][]MapOp{}
	}
	waitingFor := ID{Site: op.ID.Site, Seq: op.first() - 1}
	if held := m.pending[waitingFor]; held != nil {
		m.pending[waitingFor] = append(held, op)
		m.parked++
		return
	}
	if len(m.parkSlab) == cap(m.parkSlab) {
		m.parkSlab = make([]MapOp, 0, parkChunk)
	}
	n := len(m.parkSlab)
	m.parkSlab = append(m.parkSlab, op)
	m.pending[waitingFor] = m.parkSlab[n : n+1 : n+1]
	m.parked++
}

// wake queues whatever was waiting for a sequence number that has just been
// accounted for — every number in (had, last] at this site.
//
// A superseded run can account for a great many at once, since a replica may
// hold a history far longer than the operations that describe it, so the walk is
// over whichever is shorter: the numbers the operation covered, or everything
// parked. Neither is left free to decide the cost on its own.
func (m *Map) wake(queue []MapOp, site SiteID, had, last uint64) []MapOp {
	release := func(id ID) {
		waiting, ok := m.pending[id]
		if !ok {
			return
		}
		delete(m.pending, id)
		m.parked -= len(waiting)
		queue = append(queue, waiting...)
	}
	if last-had > uint64(len(m.pending)) {
		for id := range m.pending {
			if id.Site == site && id.Seq > had && id.Seq <= last {
				release(id)
			}
		}
		return queue
	}
	for seq := had + 1; seq <= last; seq++ {
		release(ID{Site: site, Seq: seq})
	}
	return queue
}

// integrate applies a ready, validated operation.
func (m *Map) integrate(op MapOp) {
	if op.Clock > m.clock {
		m.clock = op.Clock
	}
	m.vv[op.ID.Site] = op.ID.Seq
	if op.Kind == MapSuperseded {
		// A superseded run stands for operations whose clocks are gone with
		// them, so it says nothing about how far that site's clock had reached
		// and is not counted. Leaving it out understates the floor, which is
		// the direction that refuses to collect rather than the one that
		// collects too much.
		return
	}
	if m.lastClock == nil {
		m.lastClock = map[SiteID]uint64{}
	}
	if op.Clock > m.lastClock[op.ID.Site] {
		m.lastClock[op.ID.Site] = op.Clock
	}
	old, held := m.records[op.Key]
	if held && !before(old.clock, old.id, op.Clock, op.ID) {
		// The operation lost. Its value is dropped and the key keeps the write
		// that beat it — which is what stops an older set resurrecting a key
		// somebody has since deleted.
		return
	}
	if held && !old.dead {
		m.live--
	}
	fresh := mapRecord{id: op.ID, clock: op.Clock, dead: op.Kind == MapDelete}
	if !fresh.dead {
		fresh.value = cloneBytes(op.Value)
		m.live++
	}
	m.records[op.Key] = fresh
	if m.touched != nil {
		m.touched[op.Key] = struct{}{}
	}
}

// OpsSince returns the operations this replica holds that vv does not, ready to
// be sent to the peer that produced vv. Pass a nil vector for everything.
//
// The result is ordered by site and then by sequence number, so the peer can
// apply it as it arrives without ever having to buffer. Operations whose values
// have since been overwritten come back as [MapSuperseded] runs, carrying
// nothing but the sequence numbers they stand for; the writes that overwrote
// them are in the same result, which is what makes that safe. See [Map].
func (m *Map) OpsSince(vv VersionVector) []MapOp {
	held := make(map[ID]string, len(m.records))
	ids := make([]ID, 0, len(m.records))
	for key, rec := range m.records {
		held[rec.id] = key
		ids = append(ids, rec.id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })

	var ops []MapOp
	at := 0
	for _, site := range m.vv.sites() {
		// Every record's operation is one the version vector promises, and the
		// identities are sorted by site first, so this site's records are the
		// next stretch of them.
		start := at
		for at < len(ids) && ids[at].Site == site {
			at++
		}
		// Comparing rather than counting up from what the peer claims also keeps
		// a peer claiming the largest sequence number there is from wrapping.
		last := m.vv[site]
		if from := vv.Get(site); from < last {
			ops = m.appendSince(ops, site, from, last, ids[start:at], held)
		}
	}
	return ops
}

// appendSince writes what the peer is missing from one site: each record it does
// not hold, and one superseded run for each stretch between them.
func (m *Map) appendSince(ops []MapOp, site SiteID, from, last uint64, ids []ID, held map[ID]string) []MapOp {
	at := from
	for _, id := range ids {
		if id.Seq <= from {
			continue
		}
		if id.Seq > at+1 {
			ops = append(ops, supersededRun(site, at+1, id.Seq-1))
		}
		key := held[id]
		ops = append(ops, m.records[key].op(key))
		at = id.Seq
	}
	if last > at {
		ops = append(ops, supersededRun(site, at+1, last))
	}
	return ops
}

// supersededRun accounts for the sequence numbers from first to last, whose
// values this replica no longer holds.
//
// Their Lamport timestamps went with their values. A sequence number is a lower
// bound on the timestamp of the operation that carried it, which is all a
// receiver needs of operations that can no longer win anything.
func supersededRun(site SiteID, first, last uint64) MapOp {
	return MapOp{
		Kind:  MapSuperseded,
		ID:    ID{Site: site, Seq: last},
		Clock: last,
		Span:  last - first + 1,
	}
}

// mapMagic prefixes every map snapshot, followed by a one-byte format version.
// It is one byte longer than the text document's, and deliberately not the same:
// each decoder rejects the other's snapshots outright rather than reading a
// length where a version byte was meant.
var mapMagic = [...]byte{'c', 'r', 'd', 't', 'm'}

// mapVersion 2 carries the clock tombstones were collected under. Version 1,
// which did not, was read until #99 and is refused as unknown now: nothing holds
// a version 1 map, and a reader kept for bytes nobody has is a reader nothing
// real exercises.
const mapVersion = 2

// Snapshot encodes the whole map — every key ever written, deleted ones
// included, each with the identity and Lamport timestamp of the write it holds —
// plus the version vector. It is what a server sends a client joining an
// existing session, and what it persists.
//
// Keys are written in ascending order, so two replicas that have applied the
// same operations produce identical bytes whatever order those operations
// arrived in. That makes a snapshot a convergence check: agreeing on the keys
// and values is weaker than agreeing on the state, because two replicas can
// agree on every value while disagreeing about which write produced it, and
// would then resolve the next concurrent write differently.
//
// What a snapshot does not keep is the values of writes that have already lost;
// see [Map] for what [Map.OpsSince] sends in their place.
func (m *Map) Snapshot() []byte {
	sites := m.vv.sites()
	out := make([]byte, 0, len(mapMagic)+1+3*len(sites)+16*len(m.records))
	out = append(out, mapMagic[:]...)
	out = append(out, mapVersion)
	// Version 2: the clock tombstones were collected under, zero for a map
	// nobody has collected, which costs one byte. Without it a reloaded map
	// forgets that a write it does not recognise may be one that should have
	// lost, and lets a collected key come back.
	out = binary.AppendUvarint(out, m.collectedBelow)

	out = binary.AppendUvarint(out, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, m.vv[site])
	}

	keys := m.sortedKeys()
	out = binary.AppendUvarint(out, uint64(len(keys)))
	for _, key := range keys {
		rec := m.records[key]
		out = appendKey(out, key)
		out = binary.AppendUvarint(out, uint64(rec.id.Site))
		out = binary.AppendUvarint(out, rec.id.Seq)
		out = binary.AppendUvarint(out, rec.clock)
		if rec.dead {
			out = append(out, 0)
			continue
		}
		out = append(out, 1)
		out = appendSized(out, rec.value)
	}
	return out
}

// sortedKeys returns every key the map records, tombstones included, in the
// order the snapshot writes them.
func (m *Map) sortedKeys() []string {
	out := make([]string, 0, len(m.records))
	for key := range m.records {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// LoadMap rebuilds a map from a snapshot, to be written as site. The site need
// not be one that appears in the snapshot — a client joining an existing map
// brings its own.
//
// A snapshot arrives over a network, so most of what follows is refusing states
// no replica could have reached. The version vector is what the rest is measured
// against: a record written by an operation the vector does not promise, or by
// an operation another record already claims, describes a map that could not
// reproduce its own history.
func LoadMap(site SiteID, snapshot []byte) (*Map, error) {
	r := &reader{buf: snapshot}
	magic, ok := r.bytes(len(mapMagic))
	if !ok || string(magic) != string(mapMagic[:]) {
		return nil, ErrMalformed
	}
	v, ok := r.bytes(1)
	if !ok {
		return nil, ErrMalformed
	}
	if v[0] != mapVersion {
		return nil, unknownFormat(v[0], mapVersion)
	}

	m := NewMap(site)
	below, ok := r.uvarint()
	// A clock above the ceiling names an operation no replica could have issued,
	// and this one decides which writes are refused: a crafted value would refuse
	// every write there is.
	if !ok || below > MaxClock {
		return nil, ErrMalformed
	}
	m.collectedBelow = below
	nSites, ok := r.uvarint()
	if !ok {
		return nil, ErrMalformed
	}
	for range nSites {
		s, ok1 := r.uvarint()
		seq, ok2 := r.uvarint()
		// A sequence number above the clock ceiling names an operation no replica
		// could have issued. This loader has no ledger to hold the vector to, so it
		// is the only thing standing between a crafted vector and a replica whose
		// own next operation would wrap; see [MaxClock].
		if !ok1 || !ok2 || seq == 0 || seq > MaxClock {
			return nil, ErrMalformed
		}
		// A site listed twice would leave which of the two entries applies up to
		// decoding order.
		if _, dup := m.vv[SiteID(s)]; dup {
			return nil, ErrMalformed
		}
		m.vv[SiteID(s)] = seq
	}

	nKeys, ok := r.uvarint()
	// Every record costs at least five bytes still to be read, so a count larger
	// than the remaining bytes allow is a corrupt header.
	if !ok || nKeys > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	claimed := make(map[ID]struct{}, nKeys)
	prev := ""
	for i := range nKeys {
		key, ok := r.sized()
		if !ok || !utf8.Valid(key) {
			return nil, ErrMalformed
		}
		text := string(key)
		// Ascending, and strictly: keys out of order are not something a replica
		// wrote, and a key given twice would leave which record applies up to
		// decoding order.
		if i > 0 && text <= prev {
			return nil, ErrMalformed
		}
		prev = text

		id, ok1 := r.id()
		clock, ok2 := r.uvarint()
		kind, ok3 := r.bytes(1)
		if !ok1 || !ok2 || !ok3 || kind[0] > 1 {
			return nil, ErrMalformed
		}
		// The root is checked for by hand: Includes admits a sequence number of
		// zero against any site, because zero is what a version vector reads as
		// when it has never heard of that site at all.
		if id.IsRoot() || !m.vv.Includes(id) || clock < id.Seq || clock > MaxClock {
			return nil, ErrMalformed
		}
		if _, dup := claimed[id]; dup {
			return nil, ErrMalformed
		}
		claimed[id] = struct{}{}

		rec := mapRecord{id: id, clock: clock, dead: kind[0] == 0}
		if !rec.dead {
			value, ok := r.sized()
			if !ok {
				return nil, ErrMalformed
			}
			// The snapshot belongs to the caller, who may reuse or write to it.
			rec.value = cloneBytes(value)
			m.live++
		}
		if clock > m.clock {
			m.clock = clock
		}
		m.records[text] = rec
	}
	if len(r.buf) != 0 {
		return nil, ErrMalformed
	}
	// The records that carried the highest clocks are exactly the tombstones
	// Collect drops, so a collected snapshot can understate the clock this
	// replica had reached — and collectedBelow is a clock it had certainly
	// seen. Every write it mints from now on has to be above that floor, or
	// its first Set on a collected key is refused by every peer as a
	// resurrection (ErrStranded) while the writer itself holds it: the one
	// divergence a Lamport clock exists to prevent.
	if m.clock < m.collectedBelow {
		m.clock = m.collectedBelow
	}

	// The Lamport clock must be at least as high as anything the map records,
	// including the sequence numbers of writes whose clocks the snapshot does not
	// keep. Otherwise this replica's next operation could claim a clock below its
	// own sequence number, which its own validator refuses.
	for _, seq := range m.vv {
		if seq > m.clock {
			m.clock = seq
		}
	}

	// And what each site had reached, as far as this snapshot can say. It keeps
	// the winning write per key and nothing about the ones that lost, so the
	// answer is at or below the truth — which is the direction that refuses to
	// collect rather than the one that collects too much. See [Map.LastClocks].
	for _, rec := range m.records {
		if m.lastClock == nil {
			m.lastClock = map[SiteID]uint64{}
		}
		if rec.clock > m.lastClock[rec.id.Site] {
			m.lastClock[rec.id.Site] = rec.clock
		}
	}
	return m, nil
}

// sized reads a length-prefixed byte string — the form a key and a value take in
// both encodings here — reporting failure rather than trusting the length.
func (r *reader) sized() ([]byte, bool) {
	n, ok := r.uvarint()
	if !ok || n > uint64(len(r.buf)) {
		return nil, false
	}
	return r.bytes(int(n))
}

// appendKey writes a length-prefixed key. It takes the string rather than bytes
// to save the copy a conversion would make on every encode.
func appendKey(dst []byte, key string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(key)))
	return append(dst, key...)
}

// appendSized writes a length-prefixed value.
func appendSized(dst, v []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(v)))
	return append(dst, v...)
}

// cloneBytes copies a value, so that no slice is ever held by both a caller and
// the map. An empty value and a nil one are the same value, and both are kept as
// nil so that equal maps hold equal records.
func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return append([]byte(nil), b...)
}
