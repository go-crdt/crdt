package structured

import (
	"encoding/binary"
	"math/bits"
	"testing"
)

// A manifest's size is a uint64 a peer wrote and is handed back as an int.
// A value past what an int holds is refused: on a 64-bit build it would turn
// negative, and on a 32-bit one it truncates to an unrelated number that can
// even equal the assembled length and pass an unrelated file off as complete.
func TestAManifestSizeIntCannotCountIsRefused(t *testing.T) {
	// One past what int holds on this target: 1<<63 on 64-bit, 1<<31 on 32-bit.
	tooWide := uint64(1) << (bits.UintSize - 1)
	value := binary.AppendUvarint(nil, tooWide)
	value = binary.AppendUvarint(value, 1)
	value = append(value, make([]byte, 32)...) // one (fake) digest
	if total, _, ok := decodeManifest(value); ok {
		t.Errorf("a manifest claiming %d bytes was accepted with total %d on a %d-bit int", tooWide, total, bits.UintSize)
	}
	b := NewBlobs(1)
	if _, err := b.manifest.Set("wide.bin", value); err != nil {
		t.Fatal(err)
	}
	if size, ok := b.Size("wide.bin"); ok {
		t.Errorf("Blobs.Size reports %d for a manifest int cannot count", size)
	}
	if _, ok := b.Get("wide.bin"); ok {
		t.Error("Blobs.Get handed back a file int cannot count")
	}
}

// On a 32-bit target a size of 1<<32+len(chunk) used to truncate to
// len(chunk), so Get handed back the chunk as the whole file while a 64-bit
// replica of the same document said it was incomplete: the two diverge on what
// the document holds. Runs only where int is 32 bits wide; the 386/arm/mips
// lanes are where it proves anything.
func TestAManifestThatWrapsOn32BitIsRefusedThere(t *testing.T) {
	if bits.UintSize != 32 {
		t.Skip("needs a 32-bit int target (GOARCH=386/arm/mips); js/wasm has a 64-bit int")
	}
	chunk := []byte("hello")
	b := NewBlobs(1)
	if _, err := b.Put("real.bin", chunk); err != nil {
		t.Fatal(err)
	}
	_, keys, ok := b.read("real.bin")
	if !ok || len(keys) != 1 {
		t.Fatalf("read: ok=%v keys=%d", ok, len(keys))
	}
	value := binary.AppendUvarint(nil, uint64(1)<<32+uint64(len(chunk)))
	value = binary.AppendUvarint(value, 1)
	value = append(value, keys[0]...)
	if _, err := b.manifest.Set("lying.bin", value); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get("lying.bin"); ok {
		t.Error("a 32-bit replica hands back a complete file a 64-bit replica refuses")
	}
	if size, ok := b.Size("lying.bin"); ok {
		t.Errorf("Size = %d for a manifest int cannot count", size)
	}
}

// A file of no bytes has no chunks, and a file of some bytes has some. A
// manifest saying otherwise cannot be assembled, and decodeManifest refuses it
// -- a line nothing tested, which is how this was found: deleting it left the
// whole structured suite green.
//
// What it protects is not an allocation but an ANSWER. Measured with the check
// removed, a ten-byte manifest saying "one gibibyte, no chunks" decodes to
// total=1073741824 with ok=true, and then: Size reports a gibibyte and says so
// is true, Missing reports nothing missing because there are no keys to miss,
// and Get refuses -- len 0, ok false. So a reader is shown a gibibyte that
// nothing is waiting for and that never arrives, which is worse than an error
// because there is nothing to retry and nothing to report.
//
// The shape has a name. Sassaman, Patterson, Bratus, Locasto and Shubina
// (Security Applications of Formal Language Theory, Dartmouth TR2011-709,
// 2011) call it a parse tree differential: they found X.509 cases where "two
// implementations of the X.509 system behaved differently when given the same
// input", a certificate authority signing what it read one way while the
// browser read the same bytes another. This is that, inside ONE program:
// without the check, Size and Missing read these ten bytes as a file of a
// gibibyte with nothing outstanding, and Get reads the same ten bytes as not a
// file. The recognizer let through an input the rest of the package cannot
// agree on, which is the reason a bound like this belongs in the decoder and
// not in each caller.
func TestAManifestWhoseSizeAndChunkCountDisagreeIsNotAFile(t *testing.T) {
	gibibyteWithNoChunks := binary.AppendUvarint(nil, 1<<30)
	gibibyteWithNoChunks = binary.AppendUvarint(gibibyteWithNoChunks, 0)

	noBytesButAChunk := binary.AppendUvarint(nil, 0)
	noBytesButAChunk = binary.AppendUvarint(noBytesButAChunk, 1)
	noBytesButAChunk = append(noBytesButAChunk, make([]byte, 32)...) // one (fake) digest

	for _, tt := range []struct {
		name  string
		value []byte
	}{
		{"a gibibyte with no chunks", gibibyteWithNoChunks},
		{"no bytes but a chunk", noBytesButAChunk},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if total, keys, ok := decodeManifest(tt.value); ok {
				t.Errorf("decodeManifest accepted it: total=%d keys=%d", total, len(keys))
			}
			b := NewBlobs(1)
			if _, err := b.manifest.Set("claim.bin", tt.value); err != nil {
				t.Fatal(err)
			}
			if size, ok := b.Size("claim.bin"); ok {
				t.Errorf("Blobs.Size reports %d bytes for a manifest that cannot be assembled", size)
			}
			if got, ok := b.Get("claim.bin"); ok {
				t.Errorf("Blobs.Get handed back %d bytes as a complete file", len(got))
			}
			if missing := b.Missing("claim.bin"); missing != 0 {
				t.Logf("Missing reports %d, which is only meaningful for a file this reads at all", missing)
			}
		})
	}

	// The control: a manifest whose two numbers agree is still a file, so the
	// check above refuses a shape rather than everything.
	real := NewBlobs(1)
	if _, err := real.Put("real.bin", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if size, ok := real.Size("real.bin"); !ok || size != 5 {
		t.Fatalf("an ordinary file reads as size=%d ok=%v, so this test refuses everything", size, ok)
	}
}
