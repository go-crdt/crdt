package crdt

import (
	"encoding/binary"
	"errors"
	"runtime"
	"testing"
)

// A decoder reads bytes nobody here wrote, so every count it reads is a claim
// rather than a fact. Two different things go wrong with a claim, they fail in
// different ways, and only one of them is visible in the answer -- which is why
// this is two tests rather than one.
//
// Truncation is the visible kind. A count that outruns the input leaves a reader
// helper returning (nil, false), and a caller that does not look at the second
// value indexes the nil and panics.
//
// A bound is the invisible kind. Deleting "n > uint64(len(r.buf))" changes NO
// answer this package ever gives: the loop underneath still runs out of bytes,
// still returns ErrMalformed. What it changes is that a handful of bytes claiming
// sixteen million entries is believed long enough to size an allocation from it.
// Nothing asserted about the RESULT can see that difference, which is exactly how
// a mutation sweep could delete these three guards on 2026-10-08 and leave every
// test in this package green. The assertion has to be about the allocation.

// Every proper prefix of an encoded version is refused rather than read past.
//
// The whole encoding decodes first, as the positive control: a test that fed the
// decoder something it would reject anyway would pass with the guards gone.
func TestEveryTruncationOfAnEncodedVersionIsRefused(t *testing.T) {
	_, version := compositeCorpus(t)

	var whole CompositeVersion
	if err := whole.UnmarshalBinary(version); err != nil {
		t.Fatalf("the control does not decode: %v", err)
	}
	if len(whole) == 0 {
		t.Fatal("the control decoded to an empty version, so no prefix reaches a part")
	}

	for i := range version {
		var got CompositeVersion
		if err := got.UnmarshalBinary(version[:i]); !errors.Is(err, ErrMalformed) {
			t.Errorf("the first %d bytes of %d gave %v, want ErrMalformed", i, len(version), err)
		}
	}
}

// A few bytes cannot make the decoder allocate, however large a count they name.
//
// The three inputs are each the shortest that reaches one of the three counts in
// [CompositeVersion.UnmarshalBinary]: what comes before a count is valid, the
// count is enormous, and nothing follows it. Each is refused either way -- the
// assertion is the memory the refusal costs.
func TestASmallVersionCannotAskTheDecoderForALargeAllocation(t *testing.T) {
	// Big enough that believing it is unmistakable (16Mi sites is 128MiB of
	// SiteID), small enough that a run with the guard gone fails rather than
	// taking the machine down with it.
	const claimed = 1 << 24
	const ceiling = 1 << 20

	sites := binary.AppendUvarint(nil, claimed)

	parts := binary.AppendUvarint(nil, 1) // one site, and here it is
	parts = binary.AppendUvarint(parts, 7)
	parts = binary.AppendUvarint(parts, claimed)

	entries := binary.AppendUvarint(nil, 1)
	entries = binary.AppendUvarint(entries, 7)
	entries = binary.AppendUvarint(entries, 1) // one part, and here it is
	entries = append(entries, byte(PartText))
	entries = binary.AppendUvarint(entries, 1)
	entries = append(entries, 'a')
	entries = binary.AppendUvarint(entries, claimed)

	for _, c := range []struct {
		count string
		in    []byte
	}{
		{"sites", sites},
		{"parts", parts},
		{"entries", entries},
	} {
		t.Run(c.count, func(t *testing.T) {
			if len(c.in) > 32 {
				t.Fatalf("the input is %d bytes, which is no longer the point", len(c.in))
			}

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			var v CompositeVersion
			err := v.UnmarshalBinary(c.in)
			runtime.ReadMemStats(&after)

			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("%d bytes claiming %d %s gave %v, want ErrMalformed", len(c.in), claimed, c.count, err)
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew > ceiling {
				t.Errorf("%d bytes claiming %d %s cost %d bytes of allocation, want under %d",
					len(c.in), claimed, c.count, grew, ceiling)
			}
		})
	}
}
