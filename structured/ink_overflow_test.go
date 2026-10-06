package structured

import (
	"encoding/binary"
	"testing"
)

// binary.Uvarint returns a NEGATIVE count for an encoding that overflows
// sixty-four bits, and the line after one of these is value[used:]. A negative
// index panics rather than erring, and a peer's ink operation carries the
// bytes: that would be a remote panic and not a refused message.
//
// decodePoint refuses it, and nothing tested that. Measured with the first
// check deleted, twelve bytes of continuation bits give
//
//	runtime error: slice bounds out of range [-11:]
//
// The whole suite stayed green without it, which is how this was found.
func TestDecodePointRefusesAnOverflowingVarintRatherThanPanicking(t *testing.T) {
	var overflow []byte
	for range 11 {
		overflow = append(overflow, 0x80) // continuation bit, no value
	}
	overflow = append(overflow, 0x02)
	if _, n := binary.Uvarint(overflow); n >= 0 {
		t.Fatalf("binary.Uvarint reports %d for these bytes, so they do not overflow "+
			"and this test is about nothing", n)
	}

	for _, tt := range []struct {
		name  string
		value []byte
	}{
		{"an overflowing site", overflow},
		{"an overflowing sequence", append(binary.AppendUvarint(nil, 7), overflow...)},
		{"nothing at all", nil},
		{"a truncated varint", []byte{0x80}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decodePoint panicked on bytes a peer can send: %v", r)
				}
			}()
			if _, _, ok := decodePoint(tt.value); ok {
				t.Error("decodePoint accepted them")
			}
		})
	}

	// The control: a point this package wrote is still read back, so the
	// refusals above are about these bytes and not about every input.
	id := StrokeID{Site: 3, Seq: 9}
	want := Point{X: 1.5, Y: -2.25, Pressure: 0.5}
	got, p, ok := decodePoint(encodePoint(id, want))
	if !ok || got != id || p != want {
		t.Fatalf("a point this package encoded read back as %v %v ok=%v", got, p, ok)
	}
}
