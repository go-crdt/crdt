package structured

import (
	"encoding/binary"
	"testing"
	"unicode/utf8"

	"github.com/go-crdt/crdt"
)

func TestEncodeIDRoundTrip(t *testing.T) {
	cases := []crdt.ID{
		{Site: 0, Seq: 0},
		{Site: 1, Seq: 1},
		{Site: 1 << 40, Seq: 1 << 50},
		{Site: 1<<64 - 1, Seq: 1<<64 - 1},
	}
	for _, id := range cases {
		s := encodeID(id)
		if !utf8.ValidString(s) {
			t.Fatalf("encodeID(%v) = %q, not valid UTF-8 — cannot be a map key", id, s)
		}
		got, ok := decodeID(s)
		if !ok || got != id {
			t.Fatalf("decodeID(%q) = %v, %v; want %v, true", s, got, ok, id)
		}
	}
}

func TestDecodeIDRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "1", "1.", ".1", "x.1", "1.y", "1.2.3", "-1.2", "18446744073709551616.1"} {
		if id, ok := decodeID(s); ok {
			t.Fatalf("decodeID(%q) = %v, true; want it refused", s, id)
		}
	}
}

func TestCellKeyRoundTrip(t *testing.T) {
	row := RowID{Site: 7, Seq: 3}
	col := ColID{Site: 9, Seq: 4}
	key := cellKey(row, col)
	if !utf8.ValidString(key) {
		t.Fatalf("cellKey = %q, not valid UTF-8", key)
	}
	gotRow, gotCol, ok := decodeCellKey(key)
	if !ok || gotRow != row || gotCol != col {
		t.Fatalf("decodeCellKey(%q) = %v,%v,%v; want %v,%v,true", key, gotRow, gotCol, ok, row, col)
	}
}

func TestDecodeCellKeyRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "1.2", "1.2:", ":1.2", "x:1.2", "1.2:y", "1.2:3.4:5.6"} {
		if r, c, ok := decodeCellKey(s); ok {
			t.Fatalf("decodeCellKey(%q) = %v,%v,true; want it refused", s, r, c)
		}
	}
}

func TestFieldKeyRoundTrip(t *testing.T) {
	// A record identity that itself contains the separator, and a field name that
	// does too, to prove the length prefix — not a separator search — splits it.
	cases := []struct{ rec, field string }{
		{"1.2", "pos"},
		{"1.2", "a.b.c"},
		{"", "x"},
		{"weird.rec.id", ""},
		{"10.20", "3.field"},
	}
	for _, c := range cases {
		key := fieldKey(c.rec, c.field)
		if !utf8.ValidString(key) {
			t.Fatalf("fieldKey(%q,%q) = %q, not valid UTF-8", c.rec, c.field, key)
		}
		rec, field, ok := splitFieldKey(key)
		if !ok || rec != c.rec || field != c.field {
			t.Fatalf("splitFieldKey(%q) = %q,%q,%v; want %q,%q,true", key, rec, field, ok, c.rec, c.field)
		}
	}
}

func TestSplitFieldKeyRejectsMalformed(t *testing.T) {
	// No separator; empty length; non-numeric length; length past the end.
	for _, s := range []string{"", "pos", ".pos", "x.pos", "-1.pos", "99.ab"} {
		if rec, field, ok := splitFieldKey(s); ok {
			t.Fatalf("splitFieldKey(%q) = %q,%q,true; want it refused", s, rec, field)
		}
	}
}

func TestDecodePos(t *testing.T) {
	for _, c := range []struct{ x, y int32 }{{0, 0}, {5, -7}, {-2147483648, 2147483647}} {
		x, y, ok := decodePos(encodePos(c.x, c.y))
		if !ok || x != c.x || y != c.y {
			t.Fatalf("decodePos(encodePos(%d,%d)) = %d,%d,%v", c.x, c.y, x, y, ok)
		}
	}
}

func TestDecodePosRejectsMalformed(t *testing.T) {
	// Empty; only one varint; trailing byte; a value past int32.
	tooBig := binary.AppendVarint(nil, 1<<40)
	tooBig = binary.AppendVarint(tooBig, 0)
	cases := [][]byte{
		{},
		encodePos(1, 2)[:1],
		append(encodePos(1, 2), 0x01),
		tooBig,
	}
	for i, b := range cases {
		if x, y, ok := decodePos(b); ok {
			t.Fatalf("case %d: decodePos(%x) = %d,%d,true; want refused", i, b, x, y)
		}
	}
}

func FuzzDecodeID(f *testing.F) {
	f.Add("1.2")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		id, ok := decodeID(s)
		if ok && encodeID(id) != s {
			// Not every accepted string re-encodes to itself (leading zeros), but a
			// decoded id must round-trip through encode+decode to the same id.
			if again, ok2 := decodeID(encodeID(id)); !ok2 || again != id {
				t.Fatalf("decodeID(%q)=%v did not round-trip", s, id)
			}
		}
	})
}

func FuzzDecodeCellKey(f *testing.F) {
	f.Add("1.2:3.4")
	f.Fuzz(func(t *testing.T, s string) {
		if row, col, ok := decodeCellKey(s); ok {
			if r2, c2, ok2 := decodeCellKey(cellKey(row, col)); !ok2 || r2 != row || c2 != col {
				t.Fatalf("decodeCellKey(%q) did not round-trip", s)
			}
		}
	})
}

func FuzzSplitFieldKey(f *testing.F) {
	f.Add("3.1.2pos")
	f.Fuzz(func(t *testing.T, s string) {
		if rec, field, ok := splitFieldKey(s); ok {
			if r2, f2, ok2 := splitFieldKey(fieldKey(rec, field)); !ok2 || r2 != rec || f2 != field {
				t.Fatalf("splitFieldKey(%q) did not round-trip", s)
			}
		}
	})
}

func TestIDStrings(t *testing.T) {
	if got := (RowID{Site: 2, Seq: 5}).String(); got != "5@2" {
		t.Fatalf("RowID.String = %q, want 5@2", got)
	}
	if got := (ColID{Site: 3, Seq: 6}).String(); got != "6@3" {
		t.Fatalf("ColID.String = %q", got)
	}
	if got := (NodeID{Site: 4, Seq: 7}).String(); got != "7@4" {
		t.Fatalf("NodeID.String = %q", got)
	}
	if got := (ConnID{}).String(); got != "root" {
		t.Fatalf("ConnID zero String = %q, want root", got)
	}
}
