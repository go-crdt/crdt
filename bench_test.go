package crdt

import (
	"runtime"
	"strings"
	"testing"
)

// The benchmarks exist to make the next design decision an argument about
// numbers rather than about taste. Version 0.1 stores one item per character in
// a linked list; whether that has to become run-length blocks, and when, is
// answered by BenchmarkInsertAtEnd and BenchmarkMemoryPerCharacter.

const benchSize = 10_000

// filled returns a document of n characters, and the operations that built it.
func filled(b *testing.B, n int) (*Doc, []Op) {
	b.Helper()
	d := New(1)
	ops, err := d.Insert(0, strings.Repeat("x", n))
	if err != nil {
		b.Fatal(err)
	}
	return d, ops
}

// Typing at the end of a document is the common case, and the one that costs
// most: finding the position means walking the list.
func BenchmarkInsertAtEnd(b *testing.B) {
	d, _ := filled(b, benchSize)
	b.ResetTimer()
	for range b.N {
		if _, err := d.Insert(d.Len(), "a"); err != nil {
			b.Fatal(err)
		}
	}
}

// Typing at the start finds its position immediately, so the difference between
// this and BenchmarkInsertAtEnd is the walk alone.
func BenchmarkInsertAtStart(b *testing.B) {
	d, _ := filled(b, benchSize)
	b.ResetTimer()
	for range b.N {
		if _, err := d.Insert(0, "a"); err != nil {
			b.Fatal(err)
		}
	}
}

// Applying a peer's operation needs no walk: the origin is found by identity.
func BenchmarkApplyRemote(b *testing.B) {
	source, _ := filled(b, benchSize)
	ops := source.OpsSince(nil)
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		d := New(2)
		b.StartTimer()
		if err := d.Apply(ops...); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkString(b *testing.B) {
	d, _ := filled(b, benchSize)
	b.ResetTimer()
	for range b.N {
		_ = d.String()
	}
}

func BenchmarkSnapshot(b *testing.B) {
	d, _ := filled(b, benchSize)
	b.ResetTimer()
	b.ReportMetric(float64(len(d.Snapshot()))/benchSize, "bytes/char")
	for range b.N {
		_ = d.Snapshot()
	}
}

func BenchmarkLoad(b *testing.B) {
	d, _ := filled(b, benchSize)
	snapshot := d.Snapshot()
	b.ResetTimer()
	for range b.N {
		if _, err := Load(2, snapshot); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryPerCharacter reports what a character costs in memory, which is
// the number that decides whether run-length blocks are worth their complexity.
func BenchmarkMemoryPerCharacter(b *testing.B) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, _ := filled(b, benchSize)
	runtime.GC()
	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/benchSize, "bytes/char")
	runtime.KeepAlive(d)
	for range b.N {
	}
}
