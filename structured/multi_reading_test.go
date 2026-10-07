package structured

import (
	"encoding/binary"
	"testing"
)

// "Only the canonical spelling is a key this type writes", and the same goes
// for a value: a cleared reading is a version vector followed by one marker
// byte and nothing else. Trailing bytes are refused rather than ignored.
//
// Ignoring them would give the same reading two spellings, and replicas here
// are compared on their ENCODED state and not merely on what they display --
// so a byte nobody reads is still a difference between two documents that
// agree. It is the rule cell.go states as "any trailing byte is refused rather
// than half-read".
//
// Nothing tested it: deleting the length check leaves the whole suite green.
func TestAClearedReadingWithTrailingBytesIsRefused(t *testing.T) {
	canonical := append(binary.AppendUvarint(nil, 0), 0) // no versions, then "cleared"

	// The control: the canonical spelling is still read, so what follows is
	// about the trailing bytes and not about an encoding that was never valid.
	if _, _, cleared, ok := decodeReading(canonical); !ok || !cleared {
		t.Fatalf("the canonical cleared reading was refused: cleared=%v ok=%v", cleared, ok)
	}

	for _, tt := range []struct {
		name  string
		extra []byte
	}{
		{"one trailing byte", []byte{0xFF}},
		{"one trailing zero", []byte{0x00}},
		{"ten trailing bytes", make([]byte, 10)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, ok := decodeReading(append(append([]byte(nil), canonical...), tt.extra...)); ok {
				t.Error("accepted: the same reading now has more than one spelling, and " +
					"two replicas holding different spellings compare unequal on state")
			}
		})
	}
}
