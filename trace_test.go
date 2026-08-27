package crdt

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"
)

// The sequential editing traces published at github.com/josephg/editing-traces
// are what every serious text CRDT is measured on — Yjs, Automerge and
// diamond-types all report against them. automerge-paper is the canonical one:
// 259 778 single-character edits recorded from someone actually writing a paper,
// ending in 104 852 characters.
//
// It is used here for two things, and the first matters more. Replaying a real
// editing history and requiring the result to equal the recorded final text is a
// correctness check no synthetic test reproduces: a quarter of a million edits
// at positions a real person chose, with the answer known in advance. Only then
// is it worth timing.
//
// Set CRDT_TRACE to the trace file (.json or .json.gz) to run these. CI does
// that; a plain `go test` skips them rather than pretending.

// A tracePatch is one recorded edit: delete del characters at pos, then insert
// text there.
type tracePatch struct {
	pos, del int
	text     string
}

type traceFile struct {
	EndContent string `json:"endContent"`
	Txns       []struct {
		Patches [][]any `json:"patches"`
	} `json:"txns"`
}

// loadTrace reads the trace named by CRDT_TRACE, or skips the test.
func loadTrace(tb testing.TB) ([]tracePatch, string) {
	tb.Helper()
	path := os.Getenv("CRDT_TRACE")
	if path == "" {
		tb.Skip("CRDT_TRACE is not set; skipping the editing-trace replay")
	}
	f, err := os.Open(path)
	if err != nil {
		tb.Fatalf("opening the trace: %v", err)
	}
	defer f.Close()

	var src interface{ Read([]byte) (int, error) } = f
	if len(path) > 3 && path[len(path)-3:] == ".gz" {
		gz, err := gzip.NewReader(f)
		if err != nil {
			tb.Fatalf("decompressing the trace: %v", err)
		}
		defer gz.Close()
		src = gz
	}

	var parsed traceFile
	if err := json.NewDecoder(src).Decode(&parsed); err != nil {
		tb.Fatalf("decoding the trace: %v", err)
	}
	var patches []tracePatch
	for _, txn := range parsed.Txns {
		for _, p := range txn.Patches {
			if len(p) != 3 {
				tb.Fatalf("a patch has %d fields, want 3", len(p))
			}
			pos, ok1 := p[0].(float64)
			del, ok2 := p[1].(float64)
			text, ok3 := p[2].(string)
			if !ok1 || !ok2 || !ok3 {
				tb.Fatalf("unreadable patch %v", p)
			}
			patches = append(patches, tracePatch{pos: int(pos), del: int(del), text: text})
		}
	}
	if len(patches) == 0 {
		tb.Fatal("the trace holds no edits")
	}
	return patches, parsed.EndContent
}

// replay applies a whole trace to one document.
func replay(tb testing.TB, d *Doc, patches []tracePatch) {
	tb.Helper()
	for i, p := range patches {
		if p.del > 0 {
			if _, err := d.Delete(p.pos, p.del); err != nil {
				tb.Fatalf("patch %d, Delete(%d, %d): %v", i, p.pos, p.del, err)
			}
		}
		if p.text != "" {
			if _, err := d.Insert(p.pos, p.text); err != nil {
				tb.Fatalf("patch %d, Insert(%d, %q): %v", i, p.pos, p.text, err)
			}
		}
	}
}

// TestEditingTrace is the correctness half: a real editing history, replayed,
// against the text it is known to produce.
func TestEditingTrace(t *testing.T) {
	patches, want := loadTrace(t)

	// Two collections before the baseline: the first frees the decoder's
	// garbage, the second frees what finalising that produced. Otherwise the
	// document appears to cost less than nothing.
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)

	d := New(1)
	start := time.Now()
	replay(t, d, patches)
	elapsed := time.Since(start)

	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(patches) // or the trace itself would be collected between the two readings
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)

	if got := d.String(); got != want {
		t.Fatalf("the replayed document does not match the recorded text: %d characters against %d",
			len([]rune(got)), len([]rune(want)))
	}
	t.Logf("%d edits in %v (%.0f ns/edit); %d characters + %d tombstones held in %d KiB, %.2f bytes/char",
		len(patches), elapsed.Round(time.Millisecond),
		float64(elapsed.Nanoseconds())/float64(len(patches)),
		d.Len(), d.Tombstones(), held/1024,
		float64(held)/float64(d.Len()+d.Tombstones()))

	// A snapshot of a real document has to round-trip like any other, and the
	// history has to replay into a fresh replica.
	loaded, err := Load(2, d.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.String() != want {
		t.Fatal("a snapshot of the replayed document did not reload to the same text")
	}
	if string(loaded.Snapshot()) != string(d.Snapshot()) {
		t.Fatal("re-encoding the reloaded document did not reproduce the snapshot")
	}
}

// TestEditingTraceConverges takes the same history and delivers it to a second
// replica as operations, in reverse, so nothing is applicable until the very
// last one arrives. A quarter of a million operations through the pending
// buffer, ending in the same text.
func TestEditingTraceConverges(t *testing.T) {
	if testing.Short() {
		t.Skip("the reversed-delivery replay is slow; skipped under -short")
	}
	patches, want := loadTrace(t)

	d := New(1)
	replay(t, d, patches)
	ops := must(d.OpsSince(nil))

	reversed := make([]Op, len(ops))
	for i, op := range ops {
		reversed[len(ops)-1-i] = op
	}
	peer := New(2)
	if err := peer.Apply(reversed...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := peer.Pending(); got != 0 {
		t.Fatalf("Pending() = %d, want 0", got)
	}
	if got := peer.String(); got != want {
		t.Fatalf("the peer holds %d characters, want %d", len([]rune(got)), len([]rune(want)))
	}
	if string(peer.Snapshot()) != string(d.Snapshot()) {
		t.Fatal("the peer agrees on the text but not on the state")
	}
}

// BenchmarkEditingTrace is the timing half, on the same trace the other
// implementations publish against.
func BenchmarkEditingTrace(b *testing.B) {
	patches, _ := loadTrace(b)
	b.ResetTimer()
	for range b.N {
		replay(b, New(1), patches)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(patches)), "ns/edit")
}

// BenchmarkEditingTraceRemote measures the other side of a session: applying a
// real history as it arrives from a peer, rather than making it locally.
func BenchmarkEditingTraceRemote(b *testing.B) {
	patches, _ := loadTrace(b)
	d := New(1)
	replay(b, d, patches)
	ops := must(d.OpsSince(nil))
	b.ResetTimer()
	for range b.N {
		peer := New(2)
		if err := peer.Apply(ops...); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(ops)), "ns/op-applied")
}
