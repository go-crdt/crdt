package structured

import (
	"reflect"
	"testing"
)

func TestCellRoundTrip(t *testing.T) {
	cases := []Cell{
		{},
		Literal(""),
		Literal("hello, 世界"),
		Formula("=A1+A2"),
		Formula("=SUM(x)",
			CellRef{Row: RowID{Site: 1, Seq: 2}, Col: ColID{Site: 3, Seq: 4}},
			CellRef{Row: RowID{Site: 5, Seq: 6}, Col: ColID{Site: 7, Seq: 8}},
		),
	}
	for _, c := range cases {
		got, ok := decodeCell(c.encode())
		if !ok {
			t.Fatalf("decodeCell(encode(%+v)) refused its own bytes", c)
		}
		// A zero Cell encodes as a literal, so normalise the want the same way the
		// encoder sees it.
		want := c
		if want.Kind == 0 {
			want.Kind = CellLiteral
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip: got %+v, want %+v", got, want)
		}
	}
}

func TestDecodeCellRejectsMalformed(t *testing.T) {
	good := Formula("=A1", CellRef{Row: RowID{Site: 1, Seq: 1}, Col: ColID{Site: 1, Seq: 1}}).encode()
	cases := map[string][]byte{
		"empty":             {},
		"unknown kind":      {9, 0},
		"text runs past":    {byte(CellLiteral), 5, 'a'},
		"trailing byte":     append(Literal("a").encode(), 0xFF),
		"ref count too big": {byte(CellFormula), 0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F},
		"truncated ref":     good[:len(good)-1],
	}
	for name, b := range cases {
		if c, ok := decodeCell(b); ok {
			t.Fatalf("%s: decodeCell(%x) = %+v, true; want refused", name, b, c)
		}
	}
}

func TestLiteralAndFormulaConstructors(t *testing.T) {
	if l := Literal("x"); l.Kind != CellLiteral || l.Text != "x" || l.Refs != nil {
		t.Fatalf("Literal built %+v", l)
	}
	ref := CellRef{Row: RowID{Site: 1, Seq: 1}, Col: ColID{Site: 2, Seq: 2}}
	if frm := Formula("=x", ref); frm.Kind != CellFormula || frm.Text != "=x" || len(frm.Refs) != 1 {
		t.Fatalf("Formula built %+v", frm)
	}
}

func FuzzDecodeCell(f *testing.F) {
	f.Add(Literal("hi").encode())
	f.Add(Formula("=A1", CellRef{Row: RowID{Site: 1, Seq: 1}, Col: ColID{Site: 1, Seq: 1}}).encode())
	f.Fuzz(func(t *testing.T, data []byte) {
		c, ok := decodeCell(data)
		if !ok {
			return
		}
		// What decodes must re-encode to bytes that decode to the same cell: the
		// wire format is canonical for what it accepts.
		if !reflect.DeepEqual(mustDecode(t, c.encode()), c) {
			t.Fatalf("decodeCell(%x) = %+v did not round-trip", data, c)
		}
	})
}

func mustDecode(t *testing.T, b []byte) Cell {
	t.Helper()
	c, ok := decodeCell(b)
	if !ok {
		t.Fatalf("decodeCell(%x) refused re-encoded bytes", b)
	}
	return c
}
