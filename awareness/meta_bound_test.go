// Copyright (c) the go-crdt authors.
// SPDX-License-Identifier: BSD-3-Clause

package awareness

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"
)

// An update's meta count is a number a peer chose, and it used to decide how
// large a map to reserve before a single entry was read.
//
// Presence is worse than a document for this, because it is FANNED OUT: the
// server decodes an update and broadcasts the same bytes to every other
// participant, each of whom decodes it too. Measured before the bound, at every
// size from a kibibyte to a mebibyte, 80 bytes allocated per byte received --
// so a mebibyte of presence cost the server 84 MB and every participant another
// 84 MB.
func TestAMetaCountLargerThanTheBytesAllowIsRefusedBeforeReserving(t *testing.T) {
	for _, n := range []int{1 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes=%d", n), func(t *testing.T) {
			data := presenceHeader(uint64(n))
			data = append(data, make([]byte, n)...)

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			var u Update
			err := u.UnmarshalBinary(data)
			runtime.ReadMemStats(&after)

			if err == nil {
				t.Fatalf("a claim of %d entries in %d bytes was accepted", n, n)
			}
			// The ceiling is the INPUT, not a fixed number of bytes.
			//
			// What is being asserted is that refusing does not cost something
			// proportional to the claim: it was 80 bytes per byte received, so a
			// ceiling of one leaves eighty times' margin. A fixed ceiling does
			// not travel -- 4 KiB passed here and failed on riscv64 under qemu at
			// 5 256 bytes, which is runtime noise between two ReadMemStats calls
			// and not a reservation.
			if spent := after.TotalAlloc - before.TotalAlloc; spent > uint64(len(data)) {
				t.Fatalf("refusing a claim of %d entries cost %d bytes for %d bytes of input: it reserved for the claim before refusing it", n, spent, len(data))
			}
		})
	}
}

// And honest presence still decodes, including an update with many entries.
//
// This is the half that matters for a bound: one set too tight refuses what
// people actually send. A meta entry of one-byte key and one-byte value is four
// bytes encoded, so a real update sits well inside a limit of one entry per two
// bytes -- but that has to be asserted rather than reasoned about, because the
// entry's minimum and the bound's divisor are two numbers somebody has to keep
// in step.
func TestHonestPresenceWithManyEntriesStillDecodes(t *testing.T) {
	// The SMALLEST entry there is, which is what pins the divisor. An empty key
	// and an empty value are two lengths of zero: two bytes, count of one, so a
	// bound of one entry per two bytes accepts it exactly and any tighter one
	// refuses it. Without this case the test passed with the bound at a quarter,
	// a fifth and a sixth, because keys of two characters leave six bytes an
	// entry to hide in.
	t.Run("the smallest entry there is", func(t *testing.T) {
		sent := Update{Site: 7, Clock: 3, Meta: map[string]string{"": ""}}
		raw, err := sent.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var got Update
		if err := got.UnmarshalBinary(raw); err != nil {
			t.Fatalf("the smallest possible entry, in %d bytes, was refused: %v", len(raw), err)
		}
		if v, held := got.Meta[""]; !held || v != "" {
			t.Fatalf("it came back as %q, held=%v", v, held)
		}
	})

	for _, entries := range []int{1, 2, 64, 1000} {
		t.Run(fmt.Sprintf("entries=%d", entries), func(t *testing.T) {
			meta := make(map[string]string, entries)
			for i := range entries {
				meta[fmt.Sprintf("k%d", i)] = fmt.Sprintf("v%d", i)
			}
			sent := Update{Site: 7, Clock: 3, Cursor: Cursor{Anchor: 1, Head: 2}, Meta: meta}
			raw, err := sent.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			var got Update
			if err := got.UnmarshalBinary(raw); err != nil {
				t.Fatalf("%d honest entries in %d bytes were refused: %v", entries, len(raw), err)
			}
			if len(got.Meta) != entries {
				t.Fatalf("%d entries came back as %d", entries, len(got.Meta))
			}
			for k, v := range meta {
				if got.Meta[k] != v {
					t.Fatalf("%q came back as %q, sent %q", k, got.Meta[k], v)
				}
			}
		})
	}
}

// presenceHeader is everything an update carries before its meta entries, with
// the count it claims.
func presenceHeader(count uint64) []byte {
	var out []byte
	out = binary.AppendUvarint(out, 7) // site
	out = binary.AppendUvarint(out, 1) // clock
	out = binary.AppendUvarint(out, 0) // not gone
	out = binary.AppendVarint(out, 0)  // anchor
	out = binary.AppendVarint(out, 0)  // head
	return binary.AppendUvarint(out, count)
}
