package crdt

import (
	"encoding/binary"
	"errors"
	"testing"
)

func validInsert() Op {
	return Op{Kind: OpInsert, ID: ID{Site: 3, Seq: 2}, Clock: 9, Origin: ID{Site: 1, Seq: 1}, Char: '🌍'}
}

func validDelete() Op {
	return Op{Kind: OpDelete, ID: ID{Site: 3, Seq: 4}, Clock: 11, Target: ID{Site: 1, Seq: 1}}
}

func TestOpKindString(t *testing.T) {
	tests := []struct {
		kind OpKind
		want string
	}{
		{OpInsert, "insert"},
		{OpDelete, "delete"},
		{0, "invalid(0)"},
		{200, "invalid(200)"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("OpKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestOpRoundTrip(t *testing.T) {
	for _, want := range []Op{
		validInsert(),
		validDelete(),
		{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Char: 0},        // site zero, root origin, NUL
		{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Char: 0x10FFFF}, // the highest valid rune
	} {
		data, err := want.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary(%+v): %v", want, err)
		}
		var got Op
		if err := got.UnmarshalBinary(data); err != nil {
			t.Fatalf("UnmarshalBinary(%+v): %v", want, err)
		}
		if got != want {
			t.Errorf("round trip gave %+v, want %+v", got, want)
		}
	}
}

func TestOpValidation(t *testing.T) {
	tests := []struct {
		name string
		op   Op
	}{
		{"unknown kind", Op{Kind: 7, ID: ID{Seq: 1}, Clock: 1}},
		{"root identity", Op{Kind: OpInsert, Clock: 1}},
		{"clock below sequence", Op{Kind: OpInsert, ID: ID{Seq: 5}, Clock: 4}},
		{"insert with target", Op{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Target: ID{Seq: 1}}},
		// A site paired with a sequence number of zero is neither the root nor a
		// real operation, and must not be mistaken for either.
		{"insert with an ill-formed origin", Op{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Origin: ID{Site: 2}}},
		{"delete with an ill-formed target", Op{Kind: OpDelete, ID: ID{Seq: 1}, Clock: 1, Target: ID{Site: 2}}},
		{"insert of negative rune", Op{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Char: -1}},
		{"insert above max rune", Op{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Char: 0x110000}},
		{"insert of surrogate", Op{Kind: OpInsert, ID: ID{Seq: 1}, Clock: 1, Char: 0xD800}},
		{"delete without target", Op{Kind: OpDelete, ID: ID{Seq: 1}, Clock: 1}},
		{"delete with origin", Op{Kind: OpDelete, ID: ID{Seq: 1}, Clock: 1, Target: ID{Seq: 1}, Origin: ID{Seq: 2}}},
		{"delete with character", Op{Kind: OpDelete, ID: ID{Seq: 1}, Clock: 1, Target: ID{Seq: 1}, Char: 'x'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.op.validate(); !errors.Is(err, ErrInvalidOp) {
				t.Errorf("validate() = %v, want ErrInvalidOp", err)
			}
			if _, err := tt.op.MarshalBinary(); !errors.Is(err, ErrInvalidOp) {
				t.Errorf("MarshalBinary() error = %v, want ErrInvalidOp", err)
			}
			if _, err := AppendOps(nil, []Op{tt.op}); !errors.Is(err, ErrInvalidOp) {
				t.Errorf("AppendOps() error = %v, want ErrInvalidOp", err)
			}
			d := New(1)
			if err := d.Apply(tt.op); !errors.Is(err, ErrInvalidOp) {
				t.Errorf("Apply() = %v, want ErrInvalidOp", err)
			}
		})
	}
}

// Every proper prefix of an encoding is truncated, and truncation must be
// reported rather than guessed at.
func TestOpDecodeTruncated(t *testing.T) {
	for _, op := range []Op{validInsert(), validDelete()} {
		data, err := op.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		for n := range len(data) {
			var got Op
			if err := got.UnmarshalBinary(data[:n]); !errors.Is(err, ErrMalformed) {
				t.Errorf("UnmarshalBinary(%d of %d bytes) = %v, want ErrMalformed", n, len(data), err)
			}
		}
	}
}

func TestOpDecodeRejectsGarbage(t *testing.T) {
	valid, err := validInsert().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"unknown kind", []byte{0x7f, 1, 1, 1, 1, 1, 1}, ErrInvalidOp},
		{"trailing bytes", append(append([]byte{}, valid...), 0), ErrMalformed},
		{
			"character above max rune",
			func() []byte {
				b := []byte{byte(OpInsert)}
				for _, v := range []uint64{3, 2, 9, 1, 1} {
					b = binary.AppendUvarint(b, v)
				}
				return binary.AppendUvarint(b, 0x200000)
			}(),
			ErrMalformed,
		},
		{
			"surrogate character",
			func() []byte {
				b := []byte{byte(OpInsert)}
				for _, v := range []uint64{3, 2, 9, 1, 1} {
					b = binary.AppendUvarint(b, v)
				}
				return binary.AppendUvarint(b, 0xD800)
			}(),
			ErrInvalidOp,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Op
			if err := got.UnmarshalBinary(tt.data); !errors.Is(err, tt.want) {
				t.Errorf("UnmarshalBinary() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestOpBatchRoundTrip(t *testing.T) {
	d := New(1)
	ops := insert(t, d, 0, "batched")
	ops = append(ops, remove(t, d, 0, 2)...)

	data, err := AppendOps([]byte("prefix"), ops)
	if err != nil {
		t.Fatalf("AppendOps: %v", err)
	}
	if got := string(data[:6]); got != "prefix" {
		t.Fatalf("AppendOps overwrote the destination: %q", got)
	}
	got, err := ParseOps(data[6:])
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if len(got) != len(ops) {
		t.Fatalf("ParseOps returned %d operations, want %d", len(got), len(ops))
	}
	for i := range got {
		if got[i] != ops[i] {
			t.Errorf("operation %d = %+v, want %+v", i, got[i], ops[i])
		}
	}

	empty, err := AppendOps(nil, nil)
	if err != nil {
		t.Fatalf("AppendOps(nil): %v", err)
	}
	if parsed, err := ParseOps(empty); err != nil || len(parsed) != 0 {
		t.Fatalf("ParseOps of an empty batch = %v, %v; want no operations", parsed, err)
	}
}

func TestParseOpsRejectsGarbage(t *testing.T) {
	valid, err := AppendOps(nil, []Op{validInsert(), validDelete()})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"count without operations", []byte{0x05}},
		{"count larger than the message", append([]byte{0x40}, valid[1:]...)},
		{"trailing bytes", append(append([]byte{}, valid...), 0, 0, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseOps(tt.data); !errors.Is(err, ErrMalformed) {
				t.Errorf("ParseOps() = %v, want ErrMalformed", err)
			}
		})
	}
	for n := range len(valid) {
		if _, err := ParseOps(valid[:n]); err == nil {
			t.Errorf("ParseOps(%d of %d bytes) succeeded, want an error", n, len(valid))
		}
	}
}
