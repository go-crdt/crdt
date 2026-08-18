package crdt

import (
	"fmt"
	"runtime"
	"strconv"
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

// The same keystroke addressed in UTF-16 code units, on a document that holds
// no supplementary character. That is the common case — ASCII, Latin, Greek,
// BMP CJK — and the conversion has nothing to do in it, so the difference
// between this and BenchmarkInsertAtEnd is what a browser client pays for
// addressing the document the way its editor does.
func BenchmarkInsertAtEndUTF16(b *testing.B) {
	d, _ := filled(b, benchSize)
	b.ResetTimer()
	for range b.N {
		if _, err := d.InsertUTF16(d.LenUTF16(), "a"); err != nil {
			b.Fatal(err)
		}
	}
}

// And on a document that does hold them, where the offset can no longer be
// taken at face value and every call descends the index. This is the cost of
// one emoji, and it is charged per keystroke for as long as the emoji is there.
func BenchmarkInsertAtEndUTF16Supplementary(b *testing.B) {
	d, _ := filled(b, benchSize)
	if _, err := d.Insert(0, "\U0001F600"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := d.InsertUTF16(d.LenUTF16(), "a"); err != nil {
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

// A cursor that jumps — a second cursor, a replace-all, a patch dropped into
// the middle of a document, a peer's operation arriving between two keystrokes —
// asks for a position the mark cannot help with. The document here is built one
// character at a time at positions that jump, so it holds as many runs as
// characters and every walk it forces is the length of the document.
func BenchmarkScatteredInsert(b *testing.B) {
	d := New(1)
	for i := range benchSize {
		if _, err := d.Insert((i*7919)%(i+1), "x"); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := range b.N {
		if _, err := d.Insert((i*7919)%d.Len(), "y"); err != nil {
			b.Fatal(err)
		}
	}
}

// A peer whose operations all name one origin. Integration walks forward from
// the origin over every character that sorts after the new one, so operations
// arranged to sort in front of everything already there make that walk the
// length of the document, every time. Nothing stops a peer sending this, and a
// server integrates what its peers send.
func BenchmarkSameOriginFlood(b *testing.B) {
	const n = 5_000
	ops := make([]Op, n)
	for i := range ops {
		ops[i] = Op{
			Kind:  OpInsert,
			ID:    ID{Site: SiteID(i + 2), Seq: 1},
			Clock: uint64(n - i),
			Char:  'x',
		}
	}
	b.ResetTimer()
	for range b.N {
		if err := New(1).Apply(ops...); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/n, "ns/op-applied")
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

// What collecting changes costs. Apply does not pay it; a caller keeping a view
// in step does, once per operation, for a walk up the index.
func BenchmarkApplyRemoteWithChanges(b *testing.B) {
	source, _ := filled(b, benchSize)
	ops := source.OpsSince(nil)
	b.ResetTimer()
	for range b.N {
		if _, err := New(2).ApplyChanges(ops...); err != nil {
			b.Fatal(err)
		}
	}
}

// A composite is not measured by how much it holds but by how many parts it is
// divided into. The consumer this type was written for gives every comment its
// own map part — so that a "resolved" flag can flip with one write rather than a
// delete and a reinsert — which means hundreds of parts holding eight keys each
// beside one text part holding a paper. Snapshot, Load, Version and OpsSince
// all have to be cheap in that number, not merely correct.
const (
	benchTextPart  = 2000
	benchListParts = 3
	benchListLen   = 100
	benchMapParts  = 300
	benchMapKeys   = 8
)

// The site identities are derived rather than counted, because that is what a
// caller does and because it is what the encoding costs turn on: a [DeriveSiteID]
// hash uses the whole uint64 range and takes ten bytes as a varint, where a
// SiteID of 2 takes one. Measuring with 1, 2, 3 would report a version a
// fraction of its real size.
var benchSites = []SiteID{
	DeriveSiteID([]byte("ada@example.org")),
	DeriveSiteID([]byte("grace@example.org")),
	DeriveSiteID([]byte("alan@example.org")),
}

// loomShaped builds that document, edited by three replicas so that every part's
// version vector names three sites — which is what the version costs on the
// wire, and the thing the encoding shares a site table for.
func loomShaped(b *testing.B) *Composite {
	b.Helper()
	c := NewComposite(benchSites[0])
	d, err := c.Text("file:src/main.tex")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := d.Insert(0, strings.Repeat("the quick brown fox ", benchTextPart/20)); err != nil {
		b.Fatal(err)
	}
	for i := range benchListParts {
		l, err := c.List(fmt.Sprintf("comments:src/file%d.tex", i))
		if err != nil {
			b.Fatal(err)
		}
		for j := range benchListLen {
			if _, err := l.Insert(j, fmt.Appendf(nil, "value %d", j)); err != nil {
				b.Fatal(err)
			}
		}
	}
	for i := range benchMapParts {
		m, err := c.Map(fmt.Sprintf("comment:%08x-4d21-11f0-9cd2-0242ac120002", i))
		if err != nil {
			b.Fatal(err)
		}
		for j := range benchMapKeys {
			if _, err := m.Set(fmt.Sprintf("field%d", j), fmt.Appendf(nil, "value %d", j)); err != nil {
				b.Fatal(err)
			}
		}
	}
	// Two more replicas touch every part, so no part's version is a single site.
	for _, site := range benchSites[1:] {
		peer := NewComposite(site)
		if err := peer.Apply(c.OpsSince(nil)...); err != nil {
			b.Fatal(err)
		}
		var back []PartOps
		for _, p := range peer.Parts() {
			switch p.Kind {
			case PartText:
				d, err := peer.Text(p.Name)
				if err != nil {
					b.Fatal(err)
				}
				ops, err := d.Insert(0, "x")
				if err != nil {
					b.Fatal(err)
				}
				back = append(back, PartOps{Part: p, Text: ops})
			case PartList:
				l, err := peer.List(p.Name)
				if err != nil {
					b.Fatal(err)
				}
				ops, err := l.Insert(0, []byte("x"))
				if err != nil {
					b.Fatal(err)
				}
				back = append(back, PartOps{Part: p, List: ops})
			default:
				m, err := peer.Map(p.Name)
				if err != nil {
					b.Fatal(err)
				}
				op, err := m.Set("touched", []byte("x"))
				if err != nil {
					b.Fatal(err)
				}
				back = append(back, PartOps{Part: p, Map: []MapOp{op}})
			}
		}
		if err := c.Apply(back...); err != nil {
			b.Fatal(err)
		}
	}
	return c
}

func BenchmarkCompositeSnapshot(b *testing.B) {
	c := loomShaped(b)
	b.ResetTimer()
	for range b.N {
		if len(c.Snapshot()) == 0 {
			b.Fatal("empty")
		}
	}
	// Reported after the loop: ResetTimer discards user metrics.
	b.StopTimer()
	b.ReportMetric(float64(len(c.Snapshot())), "snapshot-bytes")
}

func BenchmarkLoadComposite(b *testing.B) {
	snapshot := loomShaped(b).Snapshot()
	b.ResetTimer()
	for range b.N {
		if _, err := LoadComposite(9, snapshot); err != nil {
			b.Fatal(err)
		}
	}
}

// Catching up a replica that holds nothing: the whole history, every part.
func BenchmarkCompositeOpsSinceNothing(b *testing.B) {
	c := loomShaped(b)
	b.ResetTimer()
	for range b.N {
		if len(c.OpsSince(nil)) == 0 {
			b.Fatal("nothing to send")
		}
	}
}

// And the case a live session is almost always in: a peer that is up to date on
// every part but one. It must cost a comparison per part rather than a walk of
// each part's history, which is the difference between this and the benchmark
// above.
func BenchmarkCompositeOpsSinceOnePart(b *testing.B) {
	c := loomShaped(b)
	peer := NewComposite(99)
	if err := peer.Apply(c.OpsSince(nil)...); err != nil {
		b.Fatal(err)
	}
	held := peer.Version()
	m, err := c.Map("comment:00000000-4d21-11f0-9cd2-0242ac120002")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := m.Set("resolved", []byte("true")); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if len(c.OpsSince(held)) != 1 {
			b.Fatal("want the one part that moved")
		}
	}
}

// The version travels on every join, so its size is what a client pays to say
// where it is.
func BenchmarkCompositeVersionMarshal(b *testing.B) {
	v := loomShaped(b).Version()
	encoded, err := v.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := v.MarshalBinary(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(len(encoded)), "version-bytes")
}

// The whole history on the wire: what a joining client is sent, and the shape
// the gRPC service carries. The reported bytes are the message, so that what a
// join costs is a measurement rather than an estimate — and they are reported
// per part kind as well, because the text part and the three hundred map parts
// pay for entirely different things.
func BenchmarkAppendPartOps(b *testing.B) {
	c := loomShaped(b)
	batches := c.OpsSince(nil)
	message, err := AppendPartOps(nil, batches)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := AppendPartOps(message[:0], batches); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(len(message)), "message-bytes")
	byKind := map[PartKind]int{}
	for _, batch := range batches {
		one, err := AppendPartOps(nil, []PartOps{batch})
		if err != nil {
			b.Fatal(err)
		}
		byKind[batch.Part.Kind] += len(one) - 1 // less the count in front
	}
	b.ReportMetric(float64(byKind[PartText]), "text-bytes")
	b.ReportMetric(float64(byKind[PartList]), "list-bytes")
	b.ReportMetric(float64(byKind[PartMap]), "map-bytes")
}

func BenchmarkParsePartOps(b *testing.B) {
	message, err := AppendPartOps(nil, loomShaped(b).OpsSince(nil))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := ParsePartOps(message); err != nil {
			b.Fatal(err)
		}
	}
}

// Applying a history back to front, which is the shape that parks everything:
// nothing is applicable until the last operation arrives, so every one before
// it waits. Doc has such a benchmark against a real editing trace;
// these are the same question asked of the other two types, which park the
// same way and had nothing measuring them under it.
func BenchmarkApplyListReversed(b *testing.B) {
	src := NewList(1)
	var ops []ListOp
	for i := range benchSize {
		got, err := src.Insert(i, []byte{byte(i)})
		if err != nil {
			b.Fatal(err)
		}
		ops = append(ops, got...)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		l := NewList(2)
		b.StartTimer()
		for i := len(ops) - 1; i >= 0; i-- {
			if err := l.Apply(ops[i]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkApplyMapReversed(b *testing.B) {
	src := NewMap(1)
	var ops []MapOp
	for i := range benchSize {
		got, err := src.Set(strconv.Itoa(i), []byte{byte(i)})
		if err != nil {
			b.Fatal(err)
		}
		ops = append(ops, got)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		m := NewMap(2)
		b.StartTimer()
		for i := len(ops) - 1; i >= 0; i-- {
			if err := m.Apply(ops[i]); err != nil {
				b.Fatal(err)
			}
		}
	}
}
