package crdt

import (
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"unicode/utf8"
)

// What an editor holds is not one replicated structure. It is the text, the
// comments on it, the record of who changed what, the messages beside it and
// the cells of a sheet — a [Doc], three [List]s and a [Map]. Kept apart they are
// five snapshots persisted at five moments, five things to authorize, and no
// instant at which the set of them is consistent; a session restored from them
// can hold a comment on a sentence the text it was saved beside does not have.
//
// A [Composite] is those same structures under one name: one snapshot, one
// version, one thing to authorize. It adds no merge rule of its own — every part
// converges exactly as it does standing alone — so what is written below is
// almost entirely about identity, about what a part costs when there are
// hundreds of them, and about what a decoder must refuse.

// PartKind names which of the three replicated structures a part is.
type PartKind uint8

const (
	// PartText is a [Doc], a replicated sequence of characters.
	PartText PartKind = 1
	// PartList is a [List], a replicated sequence of values.
	PartList PartKind = 2
	// PartMap is a [Map], a replicated key-value map.
	PartMap PartKind = 3
)

// String renders the kind for diagnostics.
func (k PartKind) String() string {
	switch k {
	case PartText:
		return "text"
	case PartList:
		return "list"
	case PartMap:
		return "map"
	default:
		return "invalid(" + strconv.FormatUint(uint64(k), 10) + ")"
	}
}

// A Part names one structure inside a [Composite].
//
// The kind is part of the name, not a property of it. Two replicas that have
// never spoken may each reach for "notes", one as a list and one as a map, and
// there is no operation to exchange that would tell them so — a part exists
// because operations for it exist, and those operations were addressed to
// different things. Identifying a part by name alone would make that pair a
// conflict needing a tie-break, and whichever way the tie-break fell one replica
// would find its writes gone. Identifying it by both makes it two parts, which
// is a convergent answer needing no arbitration and no rule anybody has to know.
//
// A name is arbitrary UTF-8 and is expected to carry structure —
// "file:src/main.tex", "comment:9f3c…", "chat". It must be valid UTF-8 because a
// name crosses into JavaScript, where a string is UTF-16 and bytes that are not
// text cannot survive the trip; it is the same rule [Map] holds its keys to, for
// the same reason. It must not be empty, which is a decision rather than an
// oversight: the name is the only thing telling one part from another, and "" is
// what a caller passes when the name it meant to compute never got computed. Two
// unrelated bugs would then share one part, silently, and nothing downstream
// could notice.
type Part struct {
	Kind PartKind
	Name string
}

// ErrInvalidPart reports a part that cannot name anything: an unknown kind, a
// name that is not valid UTF-8, or no name at all.
var ErrInvalidPart = errors.New("crdt: invalid part")

// valid reports whether a part could name a structure some replica actually
// holds.
func (p Part) valid() bool {
	switch p.Kind {
	case PartText, PartList, PartMap:
		return validPartName(p.Name)
	default:
		return false
	}
}

func validPartName(name string) bool { return name != "" && utf8.ValidString(name) }

// partLess orders parts by kind and then by name, which is the order [Parts],
// [Composite.Snapshot] and [CompositeVersion.MarshalBinary] all use.
//
// Ordering by kind first keeps the parts of one kind in a single stretch, so a
// caller that wants only the maps — which is what a consumer holding one map per
// comment wants — finds them adjacent rather than scattered among the lists.
// Sorting by name first would read more naturally and buys nothing.
func partLess(a, b Part) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Name < b.Name
}

// A Composite is a document made of named parts, each a [Doc], a [List] or a
// [Map]. It is one replica of the whole: one snapshot to persist, one version to
// hand a peer, one thing to authorize.
//
// # Parts are created by being used
//
// [Composite.Text], [Composite.List] and [Composite.Map] return the part with
// that name, creating it on first use. There is no operation for creating one
// and none is needed: two replicas that each reach for the same name are already
// holding the same part, because a part is identified by nothing but its name
// and its kind. Nothing has to be exchanged, so nothing can be lost, arrive
// late, or conflict.
//
// The consequence is that a part which exists and holds nothing is
// indistinguishable from one that was never created — and that had better be
// true rather than merely convenient, because it is: a replica that has reached
// for "chat" and typed nothing must produce the same snapshot bytes as one that
// has never heard the word, or two replicas holding exactly the same operations
// would disagree about their state. So an empty part is written to no snapshot,
// carried in no version, and not returned by [Composite.Parts]. It costs its
// creator a map entry and every other replica nothing at all.
//
// # Each part keeps its own counters
//
// A part is an ordinary [Doc], [List] or [Map], with its own site counter,
// Lamport clock and version vector, and it is edited through its own methods.
// The alternative — one counter for the whole composite — was considered and
// rejected twice over. It would make [Doc.Version] describe operations a
// standalone [Doc] knows nothing about, damaging three clean types for a
// container's convenience; and it would not buy cross-part causality anyway,
// since contiguous sequence numbers only order operations issued by the same
// site, so a comment Bob writes on text Ada typed is unprotected either way.
//
// What follows from that is worth stating: operations are addressed to a part by
// the caller, in a [PartOps], and nothing here can check that the address is the
// one the operations came from. Editing the text and sending its operations
// labelled as a list is a caller bug this type cannot catch.
//
// A Composite is not safe for concurrent use. The zero Composite is unusable —
// construct one with [NewComposite] or [LoadComposite].
type Composite struct {
	site SiteID

	// One map per kind rather than one keyed by Part: it is what makes
	// (name, kind) the identity without a single line saying so, and it keeps
	// the stored pointers typed.
	texts map[string]*Doc
	lists map[string]*List
	maps  map[string]*Map
}

// NewComposite returns a document with no parts, whose parts issue operations as
// site. Every replica editing a composite concurrently must pass a distinct
// site; see [SiteID].
func NewComposite(site SiteID) *Composite {
	return &Composite{
		site:  site,
		texts: map[string]*Doc{},
		lists: map[string]*List{},
		maps:  map[string]*Map{},
	}
}

// Site returns the replica identity this document's parts issue operations as.
func (c *Composite) Site() SiteID { return c.site }

// Text returns the text part called name, creating an empty one on first use.
// The name is rejected rather than corrected; see [Part].
func (c *Composite) Text(name string) (*Doc, error) {
	if !validPartName(name) {
		return nil, ErrInvalidPart
	}
	return c.text(name), nil
}

// List returns the list part called name, creating an empty one on first use.
func (c *Composite) List(name string) (*List, error) {
	if !validPartName(name) {
		return nil, ErrInvalidPart
	}
	return c.list(name), nil
}

// Map returns the map part called name, creating an empty one on first use.
func (c *Composite) Map(name string) (*Map, error) {
	if !validPartName(name) {
		return nil, ErrInvalidPart
	}
	return c.mapPart(name), nil
}

// text, list and mapPart are the same lookups without the name check, for the
// paths that have already made it.
func (c *Composite) text(name string) *Doc {
	d, held := c.texts[name]
	if !held {
		d = New(c.site)
		c.texts[name] = d
	}
	return d
}

func (c *Composite) list(name string) *List {
	l, held := c.lists[name]
	if !held {
		l = NewList(c.site)
		c.lists[name] = l
	}
	return l
}

func (c *Composite) mapPart(name string) *Map {
	m, held := c.maps[name]
	if !held {
		m = NewMap(c.site)
		c.maps[name] = m
	}
	return m
}

// Parts returns every part holding at least one operation, ordered by kind and
// then by name. Two replicas that have applied the same operations return the
// same slice, which is what makes anything a caller derives from it — a list of
// files, a count of comments — the same everywhere. Parts a caller has reached
// for and left empty are not among them; see [Composite].
func (c *Composite) Parts() []Part {
	out := make([]Part, 0, len(c.texts)+len(c.lists)+len(c.maps))
	for name, d := range c.texts {
		if d.vv.promises() {
			out = append(out, Part{Kind: PartText, Name: name})
		}
	}
	for name, l := range c.lists {
		if l.vv.promises() {
			out = append(out, Part{Kind: PartList, Name: name})
		}
	}
	for name, m := range c.maps {
		if m.vv.promises() {
			out = append(out, Part{Kind: PartMap, Name: name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return partLess(out[i], out[j]) })
	return out
}

// Pending reports how many operations, across every part, are still waiting for
// operations they depend on.
func (c *Composite) Pending() int {
	n := 0
	for _, d := range c.texts {
		n += d.Pending()
	}
	for _, l := range c.lists {
		n += l.Pending()
	}
	for _, m := range c.maps {
		n += m.Pending()
	}
	return n
}

// A CompositeVersion records what a replica holds, part by part. There is no
// single vector for a composite, because there is no single sequence of
// operations: each part counts its own, so what a peer is missing is a question
// asked once per part.
//
// The nil version is valid and reads as "nothing at all", and a part mapped to a
// vector that promises nothing is the same as a part not mentioned.
type CompositeVersion map[Part]VersionVector

// Version returns what this replica holds, to be handed to a peer that will send
// back what it is missing; see [Composite.OpsSince].
//
// Every vector in it is a copy. Handing out the live ones would put the thing a
// caller measures against under the control of the thing being measured: a
// server that took a client's version, then applied an operation, and then asked
// what the client was missing would find the question had answered itself.
//
// A map has no order, so this is one of the two places that walk the parts
// without asking [Composite.Parts] for them in order — which on a document of
// three hundred parts is most of the cost of the answer.
func (c *Composite) Version() CompositeVersion {
	out := make(CompositeVersion, len(c.texts)+len(c.lists)+len(c.maps))
	for name, d := range c.texts {
		if d.vv.promises() {
			out[Part{Kind: PartText, Name: name}] = d.Version()
		}
	}
	for name, l := range c.lists {
		if l.vv.promises() {
			out[Part{Kind: PartList, Name: name}] = l.Version()
		}
	}
	for name, m := range c.maps {
		if m.vv.promises() {
			out[Part{Kind: PartMap, Name: name}] = m.Version()
		}
	}
	return out
}

// Clone returns an independent copy, vectors included.
func (v CompositeVersion) Clone() CompositeVersion {
	out := make(CompositeVersion, len(v))
	for p, vec := range v {
		out[p] = vec.Clone()
	}
	return out
}

// Equal reports whether v and other describe the same operations. A part
// promising nothing counts as absent, so a nil version equals one whose every
// part is empty.
func (v CompositeVersion) Equal(other CompositeVersion) bool {
	for p, vec := range v {
		if !vec.Equal(other[p]) {
			return false
		}
	}
	for p, vec := range other {
		if !vec.Equal(v[p]) {
			return false
		}
	}
	return true
}

// parts returns the parts that promise at least one operation, in the canonical
// order.
func (v CompositeVersion) parts() []Part {
	out := make([]Part, 0, len(v))
	for p, vec := range v {
		if vec.promises() {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return partLess(out[i], out[j]) })
	return out
}

// sites returns every site any part names, ascending, which is the table the
// encoding shares between parts.
func (v CompositeVersion) sites() []SiteID {
	seen := map[SiteID]struct{}{}
	out := []SiteID{}
	for _, vec := range v {
		for site, seq := range vec {
			if seq == 0 {
				continue
			}
			if _, dup := seen[site]; dup {
				continue
			}
			seen[site] = struct{}{}
			out = append(out, site)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// MarshalBinary encodes the version. A peer sends this on every join, and a
// document with one map part per comment has hundreds of parts and a handful of
// sites, so the sites are written once in a table and each part's entries name
// an index into it.
//
// That is not a micro-economy. A [SiteID] is a whole uint64 — [DeriveSiteID]
// hashes one, so it uses the range — and a uint64 is ten bytes as a varint,
// while an index into a table of three is one. Repeating the identity in every
// part would put nine tenths of the message in the same three numbers written
// three hundred times.
//
// Like [VersionVector.MarshalBinary] it carries no magic: it travels in a field
// whose type is already known.
//
// A part that could not name anything is refused rather than encoded, as is a
// sequence number above [MaxClock], which no replica could have issued.
func (v CompositeVersion) MarshalBinary() ([]byte, error) {
	for p, vec := range v {
		if !p.valid() {
			return nil, ErrInvalidPart
		}
		for _, seq := range vec {
			if seq > MaxClock {
				return nil, ErrInvalidOp
			}
		}
	}
	sites := v.sites()
	index := make(map[SiteID]uint64, len(sites))
	out := binary.AppendUvarint(nil, uint64(len(sites)))
	for i, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		index[site] = uint64(i)
	}

	parts := v.parts()
	out = binary.AppendUvarint(out, uint64(len(parts)))
	for _, p := range parts {
		vec := v[p]
		entries := vec.sites()
		out = append(out, byte(p.Kind))
		out = appendKey(out, p.Name)
		out = binary.AppendUvarint(out, uint64(len(entries)))
		for _, site := range entries {
			out = binary.AppendUvarint(out, index[site])
			out = binary.AppendUvarint(out, vec[site])
		}
	}
	return out, nil
}

// UnmarshalBinary decodes a version written by MarshalBinary.
//
// It arrives from a peer, so it is a trust boundary, and what it is held to is
// that no two *structures* describe one version: the site table ascends and is
// used in full, the parts ascend, each part's entries ascend, and no part is
// written that promises nothing. Each of those would otherwise let a peer state
// the same thing two ways, and one of the two would be a shape nothing here
// produces.
//
// What that does not reach is the varint layer underneath it. [binary.Uvarint]
// accepts an overlong encoding — 0x80 0x00 is a zero written in two bytes — so
// bytes this decoder accepts may still re-encode shorter. Every decoder in this
// package inherits that from the reader they share, and the guarantee is
// therefore the one they all make: what this package encodes reloads to itself byte for
// byte, and what it accepts is normalised. A caller wanting to compare two peers
// by their bytes must compare what MarshalBinary gave back, not what arrived.
//
// A sequence number above [MaxClock] names an operation no replica could have
// issued, and is refused here rather than in the parts, because a version never
// passes through a part's loader; see [MaxClock].
func (v *CompositeVersion) UnmarshalBinary(data []byte) error {
	r := &reader{buf: data}
	nSites, ok := r.uvarint()
	// Each entry costs at least one byte still to be read.
	if !ok || nSites > uint64(len(r.buf)) {
		return ErrMalformed
	}
	sites := make([]SiteID, 0, nSites)
	for i := range nSites {
		site, ok := r.uvarint()
		if !ok {
			return ErrMalformed
		}
		if i > 0 && SiteID(site) <= sites[len(sites)-1] {
			return ErrMalformed
		}
		sites = append(sites, SiteID(site))
	}
	used := make([]bool, len(sites))

	nParts, ok := r.uvarint()
	if !ok || nParts > uint64(len(r.buf)) {
		return ErrMalformed
	}
	out := make(CompositeVersion, nParts)
	var prev Part
	for i := range nParts {
		kind, ok := r.bytes(1)
		if !ok {
			return ErrMalformed
		}
		name, ok := r.sized()
		if !ok {
			return ErrMalformed
		}
		part := Part{Kind: PartKind(kind[0]), Name: string(name)}
		if !part.valid() {
			return ErrMalformed
		}
		if i > 0 && !partLess(prev, part) {
			return ErrMalformed
		}
		prev = part

		nEntries, ok := r.uvarint()
		// A part promising nothing is a part not mentioned, so writing one is an
		// encoding of a version that already has one.
		if !ok || nEntries == 0 || nEntries > uint64(len(r.buf)) {
			return ErrMalformed
		}
		vec := make(VersionVector, nEntries)
		last := uint64(0)
		for j := range nEntries {
			at, ok1 := r.uvarint()
			seq, ok2 := r.uvarint()
			if !ok1 || !ok2 || at >= uint64(len(sites)) || seq == 0 || seq > MaxClock {
				return ErrMalformed
			}
			if j > 0 && at <= last {
				return ErrMalformed
			}
			last = at
			used[at] = true
			vec[sites[at]] = seq
		}
		out[part] = vec
	}
	// A site in the table that no part names would survive decoding and vanish on
	// re-encoding, which is the one way left for two encodings to mean one thing.
	for _, u := range used {
		if !u {
			return ErrMalformed
		}
	}
	if len(r.buf) != 0 {
		return ErrMalformed
	}
	*v = out
	return nil
}

// PartOps is a batch of operations addressed to one part. Operations travel
// grouped rather than one by one because a composite has many parts and few
// operations each: naming the part once per batch keeps a document of three
// hundred comment parts from paying its part names three hundred times, and it
// keeps a batch's operations the one kind its part can hold.
//
// Exactly the field matching the part's kind may be set, and the other two are
// checked to be empty, so a batch built with the wrong field is refused rather
// than silently read as an empty one.
type PartOps struct {
	// Part names what the operations are addressed to.
	Part Part
	// Text carries the operations of a PartText batch.
	Text []Op
	// List carries the operations of a PartList batch.
	List []ListOp
	// Map carries the operations of a PartMap batch.
	Map []MapOp
}

// validate reports why a batch cannot be applied, or nil. An empty batch is
// valid and does nothing; [Composite.OpsSince] never produces one.
func (b PartOps) validate() error {
	if !b.Part.valid() {
		return ErrInvalidPart
	}
	switch b.Part.Kind {
	case PartText:
		if len(b.List) != 0 || len(b.Map) != 0 {
			return ErrInvalidOp
		}
		for _, op := range b.Text {
			if err := op.validate(); err != nil {
				return err
			}
		}
	case PartList:
		if len(b.Text) != 0 || len(b.Map) != 0 {
			return ErrInvalidOp
		}
		for _, op := range b.List {
			if err := op.validate(); err != nil {
				return err
			}
		}
	default:
		if len(b.Text) != 0 || len(b.List) != 0 {
			return ErrInvalidOp
		}
		for _, op := range b.Map {
			if err := op.validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Apply integrates batches of operations from peers, creating any part they name
// and do not find. Duplicates are ignored and an operation arriving before what
// it depends on waits, exactly as it does in the part standing alone.
//
// A malformed batch is rejected and nothing in the call is applied, parts
// included: a call that fails creates no part. That is why every batch is
// checked before any is applied.
func (c *Composite) Apply(batches ...PartOps) error {
	for _, b := range batches {
		if err := b.validate(); err != nil {
			return err
		}
	}
	for _, b := range batches {
		// Each part validates with the same function that has just passed here, and
		// its Apply reports nothing else, so these cannot fail. The errors are
		// dropped rather than turned into branches no test could reach.
		switch b.Part.Kind {
		case PartText:
			_ = c.text(b.Part.Name).Apply(b.Text...)
		case PartList:
			_ = c.list(b.Part.Name).Apply(b.List...)
		default:
			_ = c.mapPart(b.Part.Name).Apply(b.Map...)
		}
	}
	return nil
}

// OpsSince returns the operations this replica holds that v does not, batched by
// part and ready to send to the peer that produced v. Pass a nil version for
// everything.
//
// The version is compared before anything else is done, so a part the peer is up
// to date on costs a walk of its version vector rather than of its history. That
// is what a document of hundreds of parts needs: a peer that has missed one
// comment must not pay for the other two hundred and ninety-nine.
//
// No batch it returns is empty, which is the same statement: a part with nothing
// to send is a part whose version the peer already covers. A part that has been
// reached for and left empty needs no case of its own — the empty vector is
// covered by everything, this one included.
func (c *Composite) OpsSince(v CompositeVersion) []PartOps {
	var out []PartOps
	for name, d := range c.texts {
		p := Part{Kind: PartText, Name: name}
		held := v[p]
		if held.covers(d.vv) {
			continue
		}
		out = append(out, PartOps{Part: p, Text: d.OpsSince(held)})
	}
	for name, l := range c.lists {
		p := Part{Kind: PartList, Name: name}
		held := v[p]
		if held.covers(l.vv) {
			continue
		}
		out = append(out, PartOps{Part: p, List: l.OpsSince(held)})
	}
	for name, m := range c.maps {
		p := Part{Kind: PartMap, Name: name}
		held := v[p]
		if held.covers(m.vv) {
			continue
		}
		out = append(out, PartOps{Part: p, Map: m.OpsSince(held)})
	}
	// Sorted at the end rather than walked in order, because the parts worth
	// sorting are the ones with something to send, which in a live session is one
	// of three hundred. Asking [Composite.Parts] for them in order first would
	// spend more on the sort than on the answer.
	sort.Slice(out, func(i, j int) bool { return partLess(out[i].Part, out[j].Part) })
	return out
}

// compositeMagic prefixes every composite snapshot, followed by a one-byte
// format version. It extends the text document's rather than replacing it, which
// is what makes each of the four decoders here refuse the other three: a text
// snapshot reads "crdt" and then this 'c' where a version byte was meant, and a
// map snapshot's "crdtm" differs in the byte that says which it is.
var compositeMagic = [...]byte{'c', 'r', 'd', 't', 'c'}

const compositeSnapshotVersion = 1

// Snapshot encodes the whole document: every part that holds anything, in the
// canonical order, each as its own snapshot with its name and kind in front. It
// is what a server sends a client joining an existing session, and what it
// persists — one file rather than one per part, saved at one moment rather than
// at five.
//
// A part's bytes are its own snapshot, verbatim, including that snapshot's magic
// and format version. Stripping the six bytes back off was the obvious economy
// and was not taken: it would tie this format to the internal layout of the
// other three, so a part's format could not be revised without revising this
// one, and a document a previous build wrote could not be opened by wrapping its
// bytes. Six bytes per part is a twentieth of what a part's name costs.
//
// The encoding is canonical. Two replicas that have applied the same operations
// produce identical bytes, whatever order those operations arrived in, because
// each part's own encoding is canonical and the order the parts are written in
// is fixed. That is what lets the test suite compare snapshots rather than
// values, which is the stronger claim: two replicas can agree on every value and
// still disagree about which write produced it.
func (c *Composite) Snapshot() []byte {
	parts := c.Parts()
	out := make([]byte, 0, len(compositeMagic)+1+64*len(parts))
	out = append(out, compositeMagic[:]...)
	out = append(out, compositeSnapshotVersion)
	out = binary.AppendUvarint(out, uint64(len(parts)))
	for _, p := range parts {
		out = append(out, byte(p.Kind))
		out = appendKey(out, p.Name)
		out = appendSized(out, c.snapshotOf(p))
	}
	return out
}

// snapshotOf encodes one part. The kind came from [Composite.Parts], so there is
// no fourth case to answer for.
func (c *Composite) snapshotOf(p Part) []byte {
	switch p.Kind {
	case PartText:
		return c.texts[p.Name].Snapshot()
	case PartList:
		return c.lists[p.Name].Snapshot()
	default:
		return c.maps[p.Name].Snapshot()
	}
}

// LoadComposite rebuilds a document from a snapshot, to be edited as site. The
// site need not be one that appears in the snapshot — a client joining brings its
// own.
//
// Each part's bytes are handed to that part's own loader, which is where the
// clock ceiling, the version vector's promise and the rest of what [Load],
// [LoadList] and [LoadMap] refuse are enforced; nothing is re-checked here and
// nothing is waived. What this loader adds is what only it can see: that the
// parts are the ones a replica could have written, in the order it would have
// written them, and that none of them is empty — an empty part is one no snapshot
// carries, so a snapshot carrying one is not a snapshot this package produced,
// and accepting it would mean re-encoding gave back different bytes.
//
// What it deliberately does not insist on is that a part's bytes are themselves
// the current encoding. A text part written by an older build is still read by
// [Load], and is written back in the current form — so a snapshot this package
// accepts is normalised on load, while one it produced reloads to itself byte
// for byte. Refusing the older form would buy an exact fixed point on arbitrary
// input at the price of the migration the older form exists for.
func LoadComposite(site SiteID, snapshot []byte) (*Composite, error) {
	r := &reader{buf: snapshot}
	magic, ok := r.bytes(len(compositeMagic))
	if !ok || string(magic) != string(compositeMagic[:]) {
		return nil, ErrMalformed
	}
	if v, ok := r.bytes(1); !ok || v[0] != compositeSnapshotVersion {
		return nil, ErrMalformed
	}

	c := NewComposite(site)
	nParts, ok := r.uvarint()
	// A part costs at least four bytes still to be read, so a count larger than
	// the remaining bytes allow is a corrupt header.
	if !ok || nParts > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	var prev Part
	for i := range nParts {
		kind, ok := r.bytes(1)
		if !ok {
			return nil, ErrMalformed
		}
		name, ok := r.sized()
		if !ok {
			return nil, ErrMalformed
		}
		part := Part{Kind: PartKind(kind[0]), Name: string(name)}
		if !part.valid() {
			return nil, ErrMalformed
		}
		// Ascending, and strictly: parts out of order are not something a replica
		// wrote, and the same part twice would leave which of the two applies up to
		// decoding order.
		if i > 0 && !partLess(prev, part) {
			return nil, ErrMalformed
		}
		prev = part

		payload, ok := r.sized()
		if !ok {
			return nil, ErrMalformed
		}
		if err := c.adoptPart(part, payload); err != nil {
			return nil, err
		}
	}
	if len(r.buf) != 0 {
		return nil, ErrMalformed
	}
	return c, nil
}

// adoptPart loads one part's bytes with the loader its kind calls for.
//
// The payload is a window on the caller's snapshot, which the caller may reuse
// or write to afterwards, so nothing decoded from it may alias it. Each of the
// three loaders copies what it keeps — and it is three chances to get that
// wrong rather than one, which is why the round trip is asserted against a
// snapshot buffer that is overwritten after loading.
func (c *Composite) adoptPart(p Part, payload []byte) error {
	switch p.Kind {
	case PartText:
		d, err := Load(c.site, payload)
		if err != nil {
			return err
		}
		if !d.vv.promises() {
			return ErrMalformed
		}
		c.texts[p.Name] = d
	case PartList:
		l, err := LoadList(c.site, payload)
		if err != nil {
			return err
		}
		if !l.vv.promises() {
			return ErrMalformed
		}
		c.lists[p.Name] = l
	default:
		m, err := LoadMap(c.site, payload)
		if err != nil {
			return err
		}
		if !m.vv.promises() {
			return ErrMalformed
		}
		c.maps[p.Name] = m
	}
	return nil
}
