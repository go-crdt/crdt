// Copyright (c) the go-crdt authors.
// SPDX-License-Identifier: BSD-3-Clause

package crdt

import (
	"fmt"
	"strings"
	"testing"
)

// What a snapshot costs as the duplicate-deletion table grows, with the document
// held still.
//
// The document is deliberately small. Undo is what makes this table large:
// re-deleting a character records another losing operation, so the table grows
// while the document does not. A benchmark that grew both would attribute the
// document's cost to the table.
func BenchmarkSnapshotDuplicateTable(b *testing.B) {
	const chars = 100
	for _, dups := range []int{0, 1000, 8000, 34000} {
		b.Run(fmt.Sprintf("chars=%d/dups=%d", chars, dups), func(b *testing.B) {
			d := docWithTableOf(b, chars, dups)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = d.Snapshot()
			}
		})
	}
}

// docWithTableOf is a document of chars characters carrying dups duplicate
// deletions.
//
// They go in through recordDuplicate, which is the only way a document ever
// fills this table. Filling the map directly leaves the written order empty, and
// then Snapshot writes no duplicates at all and looks four thousand times
// faster -- which is what the first draft of this benchmark measured.
func docWithTableOf(tb testing.TB, chars, dups int) *Doc {
	tb.Helper()
	d := New(1)
	if _, err := d.Insert(0, strings.Repeat("x", chars)); err != nil {
		tb.Fatal(err)
	}
	for i := range dups {
		// Spread across sites, and not in site order, so the sort has the work
		// concurrent deletions from many replicas would give it.
		delID := ID{Site: SiteID(2 + (i*7)%16), Seq: uint64(i/16 + 1)}
		d.recordDuplicate(delID, ID{Site: 1, Seq: uint64(i%chars + 1)})
	}
	if len(d.dupDeletes) != dups {
		tb.Fatalf("wanted %d entries, got %d", dups, len(d.dupDeletes))
	}
	return d
}

// What recording a duplicate deletion costs, which is the half this arrangement
// makes heavier: the map write gained a lookup and an append.
//
// Measured against the snapshot it pays for. A document that snapshots once per
// thousand duplicate deletions would still come out ahead; one that never
// snapshots pays the append for nothing, which is what this number is for.
func BenchmarkRecordDuplicate(b *testing.B) {
	d := New(1)
	if _, err := d.Insert(0, "x"); err != nil {
		b.Fatal(err)
	}
	target := ID{Site: 1, Seq: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		d.recordDuplicate(ID{Site: SiteID(2 + (i*7)%16), Seq: uint64(i/16 + 1)}, target)
	}
}
