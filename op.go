package crdt

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// OpKind distinguishes the operations a text CRDT carries.
type OpKind uint8

const (
	// OpInsert adds one character after an existing one.
	OpInsert OpKind = 1
	// OpDelete tombstones an existing character.
	OpDelete OpKind = 2
	// OpSuperseded stands in for operations the sending replica no longer holds.
	// It names no character and does nothing: it exists so that a peer catching
	// up can account for the sequence numbers and move on, exactly as
	// [MapSuperseded] does for a map.
	//
	// It covers a run of consecutive sequence numbers rather than one, ending at
	// its own ID.Seq and reaching back Span of them.
	//
	// It may only stand in for operations nothing else names. A deletion is
	// named by nothing, so a losing one — the case this exists for, where two
	// replicas deleted the same character and only one of them is the
	// character's recorded deletion — can go. An insertion is named by whatever
	// was inserted after it, so it cannot: a peer sent a run over an insertion
	// would park everything that followed, waiting for an origin it will never
	// be given.
	//
	// Nothing here produces one yet. This release understands one, so that a
	// later release may send one to a peer that has been upgraded in between —
	// the two ends of a session are not deployed at the same moment, and a kind
	// a peer does not know is a kind it refuses. See go-crdt/crdt#80.
	OpSuperseded OpKind = 3
)

// String renders the kind for diagnostics.
func (k OpKind) String() string {
	switch k {
	case OpInsert:
		return "insert"
	case OpDelete:
		return "delete"
	case OpSuperseded:
		return "superseded"
	default:
		return "invalid(" + strconv.FormatUint(uint64(k), 10) + ")"
	}
}

// An Op is one indivisible change to a document. It is the only thing replicas
// exchange, it is self-describing, and applying it is idempotent, so a
// transport may duplicate or reorder operations freely.
//
// Which fields carry meaning depends on Kind: Origin and Char belong to
// OpInsert, Target to OpDelete. The unused fields must be zero, and are checked,
// so a garbled operation is rejected rather than silently reinterpreted.
type Op struct {
	// Kind selects insertion or deletion.
	Kind OpKind
	// ID names this operation. Its Seq is the issuing site's own counter and
	// increases by exactly one per operation.
	ID ID
	// Clock is the Lamport timestamp that orders this operation against
	// concurrent ones. See the package documentation.
	Clock uint64
	// Origin is the character the new one is inserted after; the zero ID means
	// the start of the document. OpInsert only.
	Origin ID
	// Char is the inserted character. OpInsert only.
	Char rune
	// Target is the character to tombstone. OpDelete only.
	Target ID
	// Span is how many consecutive sequence numbers this operation accounts for,
	// ending at ID.Seq. OpSuperseded only, where it is at least one and never
	// reaches below sequence number one.
	Span uint64
}

// first names the earliest sequence number this operation accounts for, which is
// its own for everything but a superseded run.
func (o Op) first() uint64 {
	if o.Kind == OpSuperseded {
		return o.ID.Seq - o.Span + 1
	}
	return o.ID.Seq
}

// ErrInvalidOp reports an operation that cannot be applied because it is not
// well formed — an unknown kind, a missing identity, an unusable character, or a
// field set that does not belong to its kind.
var ErrInvalidOp = errors.New("crdt: invalid operation")

// ErrMalformed reports bytes that are not a valid encoding.
var ErrMalformed = errors.New("crdt: malformed encoding")

// ErrUnknownFormat reports a snapshot this build cannot read because it does not
// know the format version, rather than because the bytes are damaged.
//
// The two are worth telling apart by the person holding them. A snapshot travels
// -- in go-crdt/collab a joining client loads one the server sends it -- so the
// ordinary way to meet this is a peer running a newer build, and the answer is to
// upgrade rather than to go looking for corruption. Reported as a malformed
// encoding, which is what this used to be, it reads as damaged data and sends
// somebody after the wrong thing.
//
// The magic matched, so these bytes are a snapshot of the right kind. Only the
// version is one this build has no reader for -- either later than any it knows,
// or a number reserved for work that has not landed.
// It wraps [ErrMalformed], because these bytes are malformed as far as this build
// is concerned and a caller that only asks that question must go on getting the
// same answer. What is new is being able to ask the narrower one.
var ErrUnknownFormat = fmt.Errorf("%w: format version this build does not know", ErrMalformed)

// unknownFormat reports [ErrUnknownFormat] and says which version was found and
// which is the highest this build reads, because the two numbers are what the
// person holding the bytes has to compare and neither is in the snapshot they
// can see.
//
// Loro says the same thing the same way -- its IncompatibleFutureEncodingError
// carries the version -- and its message is worth quoting for the doctrine
// rather than the wording: "Loro's encoding is backward compatible but not
// forward compatible. Please upgrade the version of Loro to support this version
// of the exported data."
func unknownFormat(found, highest byte) error {
	return fmt.Errorf("%w: found version %d, this build reads up to %d",
		ErrUnknownFormat, found, highest)
}

// ErrExhausted reports a replica that can issue no further operations because
// its Lamport clock has reached [MaxClock]. Reaching it honestly is not
// something a running program does; see [MaxClock].
var ErrExhausted = errors.New("crdt: the site has no clock left")

// MaxClock is the highest Lamport timestamp an operation may carry, and so also
// the highest sequence number, since a clock is never below the sequence number
// beside it.
//
// A clock counts operations: to reach this one, a session would have to issue
// four quintillion of them, one per nanosecond for a century and a half. The
// ceiling exists for what arrives from elsewhere, not for what is issued here. A
// replica raises its clock past every clock it is told about, so without a
// ceiling one operation from one peer — a corrupted varint is enough, no
// malice required — leaves the receiver's clock at the top of the range, and its
// next edit wraps to zero. That edit is then an operation every replica rejects
// as invalid, its own author included, and one that loses every tie it takes
// part in: the peer is silently and permanently unable to write. Refusing the
// clock on arrival is what keeps that from being reachable at all.
const MaxClock = 1 << 62

// wellFormedStamp reports whether an identity and a Lamport timestamp could
// belong together on an operation some replica actually issued. Every kind of
// operation here is stamped the same way and is held to this.
//
// A clock below the operation's own sequence number is impossible for an honest
// replica: a site's clock advances at least once per operation it issues, so
// after n operations its clock is at least n.
func wellFormedStamp(id ID, clock uint64) bool {
	return !id.IsRoot() && clock >= id.Seq && clock <= MaxClock
}

// room reports whether this site can still issue n operations without its clock
// passing [MaxClock]. It is asked once for a whole edit rather than once per
// operation, so that an edit either happens or does not.
//
// The subtraction is the way round that cannot overflow.
func room(clock uint64, n int) error {
	if uint64(n) > MaxClock-clock {
		return ErrExhausted
	}
	return nil
}

// validate reports why an operation cannot be applied, or nil.
func (o Op) validate() error {
	if !wellFormedStamp(o.ID, o.Clock) {
		return ErrInvalidOp
	}
	if !o.Origin.wellFormed() || !o.Target.wellFormed() {
		return ErrInvalidOp
	}
	switch o.Kind {
	case OpInsert:
		if !o.Target.IsRoot() {
			return ErrInvalidOp
		}
		if o.Char < 0 || o.Char > utf8.MaxRune || (o.Char >= 0xD800 && o.Char <= 0xDFFF) {
			return ErrInvalidOp
		}
	case OpDelete:
		if o.Target.IsRoot() || !o.Origin.IsRoot() || o.Char != 0 || o.Span != 0 {
			return ErrInvalidOp
		}
	case OpSuperseded:
		// It names nothing and carries nothing, and its clock is its own
		// sequence number: there is no Lamport time to report for operations
		// that are not here, and one encoding of the same meaning is better
		// than a free field somebody could vary.
		if !o.Origin.IsRoot() || !o.Target.IsRoot() || o.Char != 0 {
			return ErrInvalidOp
		}
		if o.Clock != o.ID.Seq || o.Span == 0 || o.Span > o.ID.Seq {
			return ErrInvalidOp
		}
	default:
		return ErrInvalidOp
	}
	return nil
}

// appendTo encodes the operation onto dst. The layout is a kind byte followed by
// unsigned varints, so a document of ASCII text costs about six bytes per
// character on the wire.
func (o Op) appendTo(dst []byte) []byte {
	dst = append(dst, byte(o.Kind))
	dst = binary.AppendUvarint(dst, uint64(o.ID.Site))
	dst = binary.AppendUvarint(dst, o.ID.Seq)
	dst = binary.AppendUvarint(dst, o.Clock)
	switch o.Kind {
	case OpInsert:
		dst = binary.AppendUvarint(dst, uint64(o.Origin.Site))
		dst = binary.AppendUvarint(dst, o.Origin.Seq)
		return binary.AppendUvarint(dst, uint64(o.Char))
	case OpSuperseded:
		// The two fields a delete spends on its target hold the span and a zero,
		// so every operation is the same shape on the wire whatever it is.
		dst = binary.AppendUvarint(dst, o.Span)
		return binary.AppendUvarint(dst, 0)
	}
	dst = binary.AppendUvarint(dst, uint64(o.Target.Site))
	return binary.AppendUvarint(dst, o.Target.Seq)
}

// MarshalBinary encodes the operation. It reports ErrInvalidOp rather than
// producing bytes that would be rejected on arrival.
func (o Op) MarshalBinary() ([]byte, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	return o.appendTo(nil), nil
}

// UnmarshalBinary decodes an operation written by MarshalBinary. Trailing bytes
// are an error: an operation is decoded from exactly its own encoding.
func (o *Op) UnmarshalBinary(data []byte) error {
	op, rest, err := decodeOp(data)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return ErrMalformed
	}
	*o = op
	return nil
}

// decodeOp reads one operation and returns the bytes after it.
//
// Every operation carries five varints; an insertion carries a sixth for the
// character. The trailing field differs by kind, which is why the character is
// read separately from the fixed five.
func decodeOp(data []byte) (Op, []byte, error) {
	if len(data) == 0 {
		return Op{}, nil, ErrMalformed
	}
	op := Op{Kind: OpKind(data[0])}
	if op.Kind != OpInsert && op.Kind != OpDelete && op.Kind != OpSuperseded {
		return Op{}, nil, ErrInvalidOp
	}
	rest := data[1:]
	var fields [5]uint64
	for i := range fields {
		v, used := uvarint(rest)
		if used <= 0 {
			return Op{}, nil, ErrMalformed
		}
		fields[i] = v
		rest = rest[used:]
	}
	op.ID = ID{Site: SiteID(fields[0]), Seq: fields[1]}
	op.Clock = fields[2]
	switch op.Kind {
	case OpInsert:
		op.Origin = ID{Site: SiteID(fields[3]), Seq: fields[4]}
		v, used := uvarint(rest)
		if used <= 0 || v > utf8.MaxRune {
			return Op{}, nil, ErrMalformed
		}
		op.Char = rune(v)
		rest = rest[used:]
	case OpSuperseded:
		op.Span = fields[3]
		if fields[4] != 0 {
			// The field a delete spends on its target's sequence number, which
			// this kind does not use. Refusing anything else keeps one encoding
			// per operation.
			return Op{}, nil, ErrMalformed
		}
	default:
		op.Target = ID{Site: SiteID(fields[3]), Seq: fields[4]}
	}
	if err := op.validate(); err != nil {
		return Op{}, nil, err
	}
	return op, rest, nil
}

// AppendOps encodes a batch of operations onto dst — the form a transport sends.
// The batch is length-prefixed, so ParseOps can reject a truncated message
// instead of silently returning the operations that happened to survive.
func AppendOps(dst []byte, ops []Op) ([]byte, error) {
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

// ParseOps decodes a batch written by AppendOps.
func ParseOps(data []byte) ([]Op, error) {
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
	ops := make([]Op, 0, count)
	for range count {
		op, tail, err := decodeOp(rest)
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
