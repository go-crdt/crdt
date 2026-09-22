// Copyright (c) the go-crdt authors.
// SPDX-License-Identifier: BSD-3-Clause

package structured

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"

	"github.com/go-crdt/crdt"
)

// A formula's reference count is a number a peer chose, and it used to decide how
// many 32-byte CellRefs to reserve before one was read. The comment beside it
// already said each reference is four varints of at least one byte each; the
// comparison did not divide by four.
//
// multi.go's own comment vouched for this one -- "the rule ParsePartOps states and
// decodeCell keeps" -- which is what makes a rule restated in prose at each header
// worth naming once instead.
func TestAReferenceCountLargerThanTheBytesAllowIsRefusedBeforeReserving(t *testing.T) {
	for _, n := range []int{1 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("bytes=%d", n), func(t *testing.T) {
			data := formulaHeader(uint64(n))
			data = append(data, make([]byte, n)...)

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			_, ok := decodeCell(data)
			runtime.ReadMemStats(&after)

			if ok {
				t.Fatalf("a claim of %d references in %d bytes was accepted", n, n)
			}
			// Asserted only where the signal exceeds the noise: see the sibling
			// in awareness. Runtime noise between two ReadMemStats calls runs to
			// thousands of bytes on the emulated lanes, which is more than a
			// kibibyte of input, so the small case asserts only the refusal. At a
			// mebibyte the defect reserved 32 MB and a ceiling of one times the
			// input leaves four orders of magnitude.
			if n < 1<<20 {
				return
			}
			if spent := after.TotalAlloc - before.TotalAlloc; spent > uint64(len(data)) {
				t.Fatalf("refusing a claim of %d references cost %d bytes for %d bytes of input: it reserved for the claim before refusing it", n, spent, len(data))
			}
		})
	}
}

// And an honest formula still decodes, including the smallest reference there is.
//
// The smallest is what pins the divisor: four varints of zero are four bytes, so
// one reference in four bytes sits exactly on a limit of one per four and any
// tighter limit refuses it. Without that case a test passes with the bound at a
// fifth or a sixth, because a reference with real identities is longer.
func TestHonestFormulasStillDecode(t *testing.T) {
	t.Run("the smallest reference there is", func(t *testing.T) {
		data := formulaHeader(1)
		data = append(data, 0, 0, 0, 0) // row site, row seq, col site, col seq
		c, ok := decodeCell(data)
		if !ok {
			t.Fatalf("one reference in %d bytes was refused", len(data))
		}
		if len(c.Refs) != 1 {
			t.Fatalf("it decoded %d references", len(c.Refs))
		}
	})

	for _, refs := range []int{1, 2, 64, 1000} {
		t.Run(fmt.Sprintf("refs=%d", refs), func(t *testing.T) {
			list := make([]CellRef, 0, refs)
			for i := range refs {
				list = append(list, CellRef{
					Row: RowID{Site: crdt.SiteID(1 + i%8), Seq: uint64(i + 1)},
					Col: ColID{Site: crdt.SiteID(1 + i%4), Seq: uint64(i + 1)},
				})
			}
			sent := Formula("=SUM(A1:Z9)", list...)
			raw := sent.encode()
			got, ok := decodeCell(raw)
			if !ok {
				t.Fatalf("%d honest references in %d bytes were refused", refs, len(raw))
			}
			if len(got.Refs) != refs {
				t.Fatalf("%d references came back as %d", refs, len(got.Refs))
			}
		})
	}
}

// A count the bound allows, whose reference bytes do not hold what they promised.
//
// This is the case the tighter bound took away from the other tests: a claim of
// one reference per byte used to get past the count check and fail here, so this
// path was covered by inputs that are now refused earlier. The exact coverage gate
// named both statements the moment that happened, which is what it is for -- and
// the path is still perfectly reachable, so it is reached deliberately rather than
// left to a fuzz corpus.
func TestAReferenceWithinTheBoundWhoseBytesRunOutIsRefused(t *testing.T) {
	for _, c := range []struct {
		what  string
		bytes []byte
	}{
		// Four continuation bytes and no terminator: the first uvarint of the
		// first reference cannot be read at all.
		{"a truncated varint", []byte{0x80, 0x80, 0x80, 0x80}},
		// Three readable varints and a fourth that is only a continuation byte.
		{"three of the four fields", []byte{0x01, 0x01, 0x01, 0x80}},
	} {
		t.Run(c.what, func(t *testing.T) {
			data := append(formulaHeader(1), c.bytes...)
			if _, ok := decodeCell(data); ok {
				t.Fatalf("%s was accepted", c.what)
			}
		})
	}
}

// formulaHeader is a formula cell with empty text, up to its reference count.
func formulaHeader(refs uint64) []byte {
	out := []byte{byte(CellFormula)}
	out = binary.AppendUvarint(out, 0) // no text
	return binary.AppendUvarint(out, refs)
}
