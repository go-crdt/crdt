package structured

import (
	"encoding/binary"

	"github.com/go-crdt/crdt"
)

// CellKind distinguishes a cell holding a plain value from one holding a formula.
type CellKind uint8

const (
	// CellLiteral is a cell whose content is its own value.
	CellLiteral CellKind = 1
	// CellFormula is a cell whose content is an expression over other cells. Its
	// source text is replicated; its computed value is not — that is derived
	// locally, so it is never a thing two replicas can disagree about.
	CellFormula CellKind = 2
)

// A CellRef names a cell a formula depends on, by the stable identities of its
// row and column rather than by a position. A concurrent insertion or deletion
// of some other row renumbers positions but touches no identity, so a reference
// stored this way still names the same cell afterwards — which is what keeps
// =SUM(...) pointing where its author meant when the sheet changes shape beneath
// it.
type CellRef struct {
	Row RowID
	Col ColID
}

// A Cell is the raw content of a spreadsheet cell: a literal value, or a
// formula's source together with the cells it references. It is what a [Sheet]
// stores and merges; a formula engine and a view are built above it and are no
// concern of this layer.
//
// The zero Cell is a literal with no text, which is how an unset cell reads.
type Cell struct {
	// Kind selects a literal or a formula.
	Kind CellKind
	// Text is the literal's value or the formula's source, as the caller renders
	// it. It is opaque here.
	Text string
	// Refs are the cells a formula depends on, by stable identity. It is empty for
	// a literal, and may be empty for a formula that references nothing.
	Refs []CellRef
}

// Literal is the cell holding value, the common case, without the caller naming
// a kind.
func Literal(value string) Cell { return Cell{Kind: CellLiteral, Text: value} }

// Formula is the cell holding source, referencing refs by stable identity.
func Formula(source string, refs ...CellRef) Cell {
	return Cell{Kind: CellFormula, Text: source, Refs: refs}
}

// encode renders a cell to the opaque bytes a [crdt.Map] stores. The layout is
// fixed — a kind byte, the length-prefixed text, and for a formula a count and
// then four varints per reference — so two replicas holding the same cell
// produce the same bytes, which is what lets a sheet's snapshot double as a
// convergence check.
func (c Cell) encode() []byte {
	// The zero Cell is a literal with no text; it encodes as one so that an unset
	// cell and an explicitly-empty literal are the same bytes.
	kind := c.Kind
	if kind == 0 {
		kind = CellLiteral
	}
	out := []byte{byte(kind)}
	out = binary.AppendUvarint(out, uint64(len(c.Text)))
	out = append(out, c.Text...)
	if kind != CellFormula {
		return out
	}
	out = binary.AppendUvarint(out, uint64(len(c.Refs)))
	for _, ref := range c.Refs {
		out = binary.AppendUvarint(out, uint64(crdt.ID(ref.Row).Site))
		out = binary.AppendUvarint(out, crdt.ID(ref.Row).Seq)
		out = binary.AppendUvarint(out, uint64(crdt.ID(ref.Col).Site))
		out = binary.AppendUvarint(out, crdt.ID(ref.Col).Seq)
	}
	return out
}

// decodeCell reverses [Cell.encode], reporting failure for bytes it did not
// produce. A cell's bytes are the value of a map operation, so they arrive from
// a peer and are a trust boundary: a kind that is neither literal nor formula, a
// length reaching past the end, or any trailing byte is refused rather than
// half-read.
func decodeCell(data []byte) (Cell, bool) {
	rd := cellReader{buf: data}
	kind, ok := rd.byteValue()
	if !ok || (CellKind(kind) != CellLiteral && CellKind(kind) != CellFormula) {
		return Cell{}, false
	}
	text, ok := rd.sized()
	if !ok {
		return Cell{}, false
	}
	c := Cell{Kind: CellKind(kind), Text: text}
	if c.Kind == CellFormula {
		n, ok := rd.uvarint()
		// Each reference is four varints of at least one byte each.
		if !ok || n > uint64(len(rd.buf)) {
			return Cell{}, false
		}
		// A formula with no references keeps a nil slice, so it re-encodes to the
		// bytes it came from rather than to an equal-but-distinct value.
		if n > 0 {
			c.Refs = make([]CellRef, 0, n)
		}
		for range n {
			rs, ok1 := rd.uvarint()
			rq, ok2 := rd.uvarint()
			cs, ok3 := rd.uvarint()
			cq, ok4 := rd.uvarint()
			if !ok1 || !ok2 || !ok3 || !ok4 {
				return Cell{}, false
			}
			c.Refs = append(c.Refs, CellRef{
				Row: RowID{Site: crdt.SiteID(rs), Seq: rq},
				Col: ColID{Site: crdt.SiteID(cs), Seq: cq},
			})
		}
	}
	if len(rd.buf) != 0 {
		return Cell{}, false
	}
	return c, true
}

// cellReader reads the primitives a cell is built from, refusing to run past the
// buffer.
type cellReader struct{ buf []byte }

func (r *cellReader) byteValue() (byte, bool) {
	if len(r.buf) == 0 {
		return 0, false
	}
	b := r.buf[0]
	r.buf = r.buf[1:]
	return b, true
}

func (r *cellReader) uvarint() (uint64, bool) {
	v, used := binary.Uvarint(r.buf)
	if used <= 0 {
		return 0, false
	}
	r.buf = r.buf[used:]
	return v, true
}

func (r *cellReader) sized() (string, bool) {
	n, ok := r.uvarint()
	if !ok || n > uint64(len(r.buf)) {
		return "", false
	}
	s := string(r.buf[:n])
	r.buf = r.buf[n:]
	return s, true
}
