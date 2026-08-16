package crdt

import (
	"encoding/binary"
	"errors"
	"strconv"
	"unicode/utf8"
)

// OpKind distinguishes the two operations a text CRDT needs.
type OpKind uint8

const (
	// OpInsert adds one character after an existing one.
	OpInsert OpKind = 1
	// OpDelete tombstones an existing character.
	OpDelete OpKind = 2
)

// String renders the kind for diagnostics.
func (k OpKind) String() string {
	switch k {
	case OpInsert:
		return "insert"
	case OpDelete:
		return "delete"
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
}

// ErrInvalidOp reports an operation that cannot be applied because it is not
// well formed — an unknown kind, a missing identity, an unusable character, or a
// field set that does not belong to its kind.
var ErrInvalidOp = errors.New("crdt: invalid operation")

// ErrMalformed reports bytes that are not a valid encoding.
var ErrMalformed = errors.New("crdt: malformed encoding")

// validate reports why an operation cannot be applied, or nil.
//
// A Clock below the operation's own Seq is impossible for an honest replica: a
// site's Lamport clock advances at least once per operation it issues, so after
// n operations its clock is at least n.
func (o Op) validate() error {
	if o.ID.IsRoot() || o.Clock < o.ID.Seq {
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
		if o.Target.IsRoot() || !o.Origin.IsRoot() || o.Char != 0 {
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
	if o.Kind == OpInsert {
		dst = binary.AppendUvarint(dst, uint64(o.Origin.Site))
		dst = binary.AppendUvarint(dst, o.Origin.Seq)
		return binary.AppendUvarint(dst, uint64(o.Char))
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
	if op.Kind != OpInsert && op.Kind != OpDelete {
		return Op{}, nil, ErrInvalidOp
	}
	rest := data[1:]
	var fields [5]uint64
	for i := range fields {
		v, used := binary.Uvarint(rest)
		if used <= 0 {
			return Op{}, nil, ErrMalformed
		}
		fields[i] = v
		rest = rest[used:]
	}
	op.ID = ID{Site: SiteID(fields[0]), Seq: fields[1]}
	op.Clock = fields[2]
	if op.Kind == OpInsert {
		op.Origin = ID{Site: SiteID(fields[3]), Seq: fields[4]}
		v, used := binary.Uvarint(rest)
		if used <= 0 || v > utf8.MaxRune {
			return Op{}, nil, ErrMalformed
		}
		op.Char = rune(v)
		rest = rest[used:]
	} else {
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
	count, used := binary.Uvarint(data)
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
