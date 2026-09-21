// Copyright (c) the go-crdt authors.
// SPDX-License-Identifier: BSD-3-Clause

package crdt

import (
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"testing"
)

// A header saying how many records follow is the cheapest lie in the format: a
// few bytes that decide how much memory is reserved before anything is read.
//
// Four of them compared the count against the number of bytes left while their
// own comments said an operation is at least four or six bytes. That allowed one
// record per byte, and a record is 80 to 96 bytes.

// The guard divides, and is not fooled by a product that wraps.
func TestImpossibleCountDividesRatherThanMultiplies(t *testing.T) {
	for _, c := range []struct {
		what           string
		count          uint64
		left, each     int
		wantImpossible bool
	}{
		{"nothing claimed, nothing left", 0, 0, 4, false},
		{"one record, four bytes for it", 1, 4, 4, false},
		{"one record, three bytes for it", 1, 3, 4, true},
		{"as many as fit", 25, 100, 4, false},
		{"one more than fits", 26, 100, 4, true},
		// The shape the old comparison allowed: one record per byte, when a
		// record needs four.
		{"one per byte, when four are needed", 100, 100, 4, true},
		// And the shape a multiplying guard would let through. count*each is
		// uint64 arithmetic: 1<<62 times 4 is 2^64, which is zero, so
		// "count*each > left" would be false and the claim would pass.
		{"a count whose product wraps to zero", 1 << 62, 100, 4, true},
		{"the largest count there is", math.MaxUint64, 1 << 20, 6, true},
	} {
		t.Run(c.what, func(t *testing.T) {
			if got := impossibleCount(c.count, c.left, c.each); got != c.wantImpossible {
				t.Fatalf("impossibleCount(%d, %d, %d) = %v, want %v", c.count, c.left, c.each, got, c.wantImpossible)
			}
			// And the multiplying form really would be fooled, so the case above
			// is about this guard rather than about arithmetic in general.
			if c.count == 1<<62 {
				if product := c.count * uint64(c.each); product > uint64(c.left) {
					t.Fatalf("1<<62 * 4 came to %d, so this case no longer demonstrates the wrap", product)
				}
			}
		})
	}
}

// Every counted header refuses one record per byte, and refuses it before
// reserving anything.
//
// Measured before the guard divided: 1 MiB in, 96 MiB reserved, a ratio of 96 to
// 1 at every input size from a kilobyte to a megabyte. The allocation is what
// this asserts, not merely the error: the error was already returned -- by
// decodeListOp, on the filler, AFTER the reservation.
func TestACountedHeaderRefusesOneRecordPerByteBeforeReserving(t *testing.T) {
	const n = 1 << 20
	data := binary.AppendUvarint(nil, uint64(n))
	data = append(data, make([]byte, n)...)

	for _, c := range []struct {
		name  string
		parse func([]byte) error
	}{
		{"ParseOps", func(b []byte) error { _, err := ParseOps(b); return err }},
		{"ParseListOps", func(b []byte) error { _, err := ParseListOps(b); return err }},
		{"ParseMapOps", func(b []byte) error { _, err := ParseMapOps(b); return err }},
		{"ParsePartOps", func(b []byte) error { _, err := ParsePartOps(b); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			err := c.parse(data)
			runtime.ReadMemStats(&after)

			if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrInvalidPart) {
				t.Fatalf("a header claiming %d records in %d bytes gave %v", n, len(data), err)
			}
			// A generous ceiling, well under one record per byte and well over
			// the handful of bytes a refusal costs: what it catches is a
			// reservation proportional to the claim.
			const ceiling = 1 << 16
			if spent := after.TotalAlloc - before.TotalAlloc; spent > ceiling {
				t.Fatalf("refusing the claim cost %d bytes, want under %d: it reserved for the claim before refusing it", spent, ceiling)
			}
		})
	}
}
