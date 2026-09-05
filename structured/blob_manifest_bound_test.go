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
