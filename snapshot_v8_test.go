package crdt

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"testing"
)

// A run of the same value is what version 8 was written for, so a document that
// produces one has to round trip — and has to be the shorter bytes, or the
// encoding is doing nothing.
func TestSnapshotWritesARunOfOneValueOnce(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "aaaaaaaa")

	snap := d.Snapshot()
	loaded, err := Load(2, snap)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.String(), "aaaaaaaa"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if string(loaded.Snapshot()) != string(snap) {
		t.Fatal("re-encoding did not reproduce the snapshot")
	}

	// Eight characters, one distinct value: a count and a value, not eight
	// copies. Two bytes of groups plus the encoding byte.
	_, cols, _ := splitColumns(t, snap)
	if got, want := len(cols[6]), 3; got != want {
		t.Fatalf("the text column is %d bytes, want %d: %v", got, want, cols[6])
	}
}

// The same document held in one run and in several must still write the same
// bytes, because that is what lets a snapshot stand in for a comparison of two
// replicas. Groups do not change that: the values a column holds are the same
// either way, so the grouping of them is too.
func TestGroupsDoNotDependOnHowTheDocumentIsHeld(t *testing.T) {
	whole := New(1)
	insert(t, whole, 0, "aaaabbbbaaaa")

	piecemeal := New(1)
	for i, ch := range "aaaabbbbaaaa" {
		insert(t, piecemeal, i, string(ch))
	}
	if string(whole.Snapshot()) != string(piecemeal.Snapshot()) {
		t.Fatal("the same text typed two ways wrote two snapshots")
	}
}

// withColumn replaces one column of a hand-built run with groups of its own.
func withColumn(t *testing.T, b runBuilder, at int, groups []byte) []byte {
	t.Helper()
	head, cols, tail := splitColumns(t, b.build())
	cols[at] = append([]byte{columnGroups}, groups...)
	return joinColumns(head, cols, tail)
}

// The column of text, at index six, is where every case below is made: it is
// the only column of the fixture that holds more than one value, so it is the
// only one in which a grouping can be wrong in more than one way.
const textColumn = 6

// A column has exactly one grouping, and every other one is refused. Without
// that, a peer could send a snapshot that decodes to a document already held
// and yet does not match its bytes — which is the thing this format promises
// cannot happen, and which the padded-varint refusal already protects against
// one level down.
func TestLoadRejectsANonCanonicalGrouping(t *testing.T) {
	// Eight of the same character with the third deleted: nine operations, so
	// there is room for a wrong grouping to hold more values than the right one
	// without being refused for that instead.
	fixture := func() runBuilder {
		return runBuilder{
			sites: [][2]uint64{{1, 9}},
			runs: []encodedRun{{
				site: 1, seq: 1, clock: 1, text: []rune("aaaaaaaa"),
				dels: [][4]uint64{{2, 1, 1, 9}},
			}},
		}
	}
	// The canonical grouping of eight of one value: one run of eight.
	if _, err := Load(2, fixture().build()); err != nil {
		t.Fatalf("the fixture does not load: %v", err)
	}
	if got := groupColumn([]uint64{'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a'}); len(got) != 2 {
		t.Fatalf("the fixture's text column is %d bytes, want 2", len(got))
	}

	uv := binary.AppendUvarint
	run := func(n int, v uint64) []byte {
		return uv(uv(nil, zigzag(int64(n))), v)
	}
	lit := func(vs ...uint64) []byte {
		out := uv(nil, zigzag(-int64(len(vs))))
		for _, v := range vs {
			out = uv(out, v)
		}
		return out
	}
	// Every case holds the eight values the run asks for, so that what refuses
	// it is the grouping and not the count: a column holding fewer values than
	// its run claims is refused before a group is looked at, and a case that
	// went that way would pass while testing nothing.
	for _, c := range []struct {
		name   string
		groups []byte
	}{
		{"a run shorter than the threshold", append(run(2, 'a'),
			lit('b', 'c', 'd', 'e', 'f', 'g')...)},
		{"a group of no values at all", append(uv(nil, zigzag(0)),
			lit('a', 'b', 'c', 'd', 'e', 'f', 'g', 'h')...)},
		{"a literal stretch holding a run", lit('a', 'a', 'a', 'b', 'c', 'd', 'e', 'f')},
		{"two literal stretches in a row", append(lit('a', 'b', 'c', 'd'),
			lit('e', 'f', 'g', 'h')...)},
		{"a run and a literal stretch on the same value", append(run(4, 'a'),
			lit('a', 'b', 'c', 'd')...)},
		{"two runs on the same value", append(run(4, 'a'), run(4, 'a')...)},
		{"a literal stretch that stops mid-value", uv(nil, zigzag(-8))},
		{"a group header with nothing after it", uv(nil, zigzag(8))},
		{"a group header that stops half way", []byte{0x80}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(2, withColumn(t, fixture(), textColumn, c.groups)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load() = %v, want ErrMalformed", err)
			}
		})
	}
}

// A column may not claim more values than the version vector has operations to
// put in it, and the version vector may not promise more than one site could
// ever issue. Together they are the only bound left on how much work reading a
// snapshot is: every version before 8 spent a byte per value, so a snapshot's
// own length said how much document could be inside it, and a run-length column
// severs that — two bytes now stand for a thousand million values.
//
// Both cases below name a run of 2^50 characters in a column that says it holds
// them. Neither refusal shows up as a different document, so what each of these
// is really asserting is that Load returns rather than sets about reading a
// thousand million million characters: take the ceiling away and the test does
// not fail, it hangs.
func TestLoadRefusesAColumnLargerThanTheDocumentPromised(t *testing.T) {
	huge := func(sites [][2]uint64) []byte {
		b := runBuilder{
			sites: sites,
			runs: []encodedRun{{
				site: 1, seq: 1, clock: 1, text: []rune("abcd"),
				length: 1 << 50,
				dels:   [][4]uint64{{2, 1, 1, 5}},
			}},
		}
		groups := binary.AppendUvarint(nil, zigzag(1<<50))
		return withColumn(t, b, textColumn, binary.AppendUvarint(groups, 'a'))
	}
	for _, c := range []struct {
		name  string
		sites [][2]uint64
	}{
		{"a column holding more values than the document has operations",
			[][2]uint64{{1, 5}}},
		{"a version vector promising more operations than a replica could issue",
			[][2]uint64{{1, MaxClock}, {2, MaxClock}, {3, MaxClock}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(4, huge(c.sites)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load() = %v, want ErrMalformed", err)
			}
		})
	}
	// One site at the ceiling is legal, and refused further down for not
	// accounting for the operations it promises rather than for its size.
	b := wellFormedRun()
	b.sites = [][2]uint64{{1, MaxClock}}
	if _, err := Load(3, b.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Load() = %v, want ErrMalformed", err)
	}
}

// deflated wraps groups the way a peer that had decided to compress a column
// would. Nothing in this package writes one; see [readColumnStream].
func deflated(t *testing.T, groups []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw, err := flate.NewWriter(&out, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(groups); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return append([]byte{columnDeflated}, out.Bytes()...)
}

// A deflated column is understood, and says the same thing as the same column
// written out. This is the "understand before write" half of the compressed
// text chunk: a peer or a later release can send one without this build
// refusing the whole snapshot, and nothing here sends one, because on the trace
// it saves 131 619 bytes uncompressed and costs 25 to a store that compresses
// the columns anyway — and costs the promise that the same state is the same
// bytes, which a compressor cannot keep across versions of itself.
func TestLoadUnderstandsADeflatedColumn(t *testing.T) {
	b := wellFormedRun()
	head, cols, tail := splitColumns(t, b.build())
	cols[textColumn] = deflated(t, cols[textColumn][1:])
	d, err := Load(2, joinColumns(head, cols, tail))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := d.String(), "abd"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// Re-encoding writes the column out, because that is what this build sends.
	fresh := d.Snapshot()
	if string(fresh) != string(b.build()) {
		t.Fatal("re-encoding a deflated column did not write it back plainly")
	}
}

func TestLoadRejectsABadDeflatedColumn(t *testing.T) {
	b := wellFormedRun()
	for _, c := range []struct {
		name    string
		payload []byte
	}{
		{"a stream that is not deflate at all", []byte{columnDeflated, 0xff, 0xff, 0xff}},
		{"a stream cut off", deflated(t, groupColumn([]uint64{'a', 'b', 'c', 'd'}))[:2]},
		// This one is refused by the ceiling on how much a column may
		// decompress to, and would be refused a few lines later by the ceiling
		// on how many values it may hold if that ceiling were lifted. The
		// difference between the two is when the bytes are allocated, which is
		// not something Load's answer can show; see [readColumnStream].
		{"more bytes out than the version vector leaves room for", deflated(t,
			groupColumn(spread(60)))},
	} {
		t.Run(c.name, func(t *testing.T) {
			head, cols, tail := splitColumns(t, b.build())
			cols[textColumn] = c.payload
			if _, err := Load(2, joinColumns(head, cols, tail)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load() = %v, want ErrMalformed", err)
			}
		})
	}
}

// Version 6 is the format before the columns had encodings, and it still loads.
// Without this the reader for it stops being exercised the moment the current
// version moves on, which is how a format that claims to accept an older one
// quietly stops doing so.
func TestLoadStillAcceptsVersionSix(t *testing.T) {
	old := wellFormedRun()
	old.asVersion = snapshotVersionV6
	raw := old.build()
	if raw[4] != snapshotVersionV6 {
		t.Fatalf("the fixture wrote version %d, want %d", raw[4], snapshotVersionV6)
	}
	was, err := Load(2, raw)
	if err != nil {
		t.Fatalf("a version 6 snapshot did not load: %v", err)
	}
	now, err := Load(2, wellFormedRun().build())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if was.String() != now.String() || was.Tombstones() != now.Tombstones() {
		t.Fatalf("version 6 reads %q with %d tombstones, the current version %q with %d",
			was.String(), was.Tombstones(), now.String(), now.Tombstones())
	}
	if fresh := was.Snapshot(); fresh[4] != snapshotVersion {
		t.Fatalf("re-encoding wrote version %d, want %d", fresh[4], snapshotVersion)
	}
}

// Version 7 is not skipped because nothing used it: it is being written on
// another branch, for the collection floor. Reading it here would be reading
// bytes this build does not know the shape of, so it is refused, and this test
// is what stops the number being quietly reused.
func TestLoadRefusesTheReservedVersion(t *testing.T) {
	b := wellFormedRun()
	b.asVersion = 7
	if _, err := Load(2, b.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Load() = %v, want ErrMalformed", err)
	}
}

// A version 6 column whose length runs past the end of the snapshot is refused
// where it is read, not field by field afterwards.
func TestLoadRejectsAVersionSixColumnPastTheEnd(t *testing.T) {
	old := wellFormedRun()
	old.asVersion = snapshotVersionV6
	raw := old.build()
	at := columnsStart(t, raw)
	broken := append([]byte{}, raw[:at]...)
	broken = binary.AppendUvarint(broken, 1<<20)
	broken = append(broken, raw[at+1:]...)
	if _, err := Load(2, broken); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Load() = %v, want ErrMalformed", err)
	}
}

// The fixture the rejection tests vary from has to be the bytes a document that
// was actually typed produces. Reasoning about the format is how a builder ends
// up agreeing with itself and with nothing else; this puts the two side by side.
func TestABuiltRunIsWhatARealDocumentWrites(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abcd")
	remove(t, d, 2, 1)

	if typed, built := d.Snapshot(), wellFormedRun().build(); !bytes.Equal(typed, built) {
		t.Fatalf("the builder and a real document disagree:\n  typed %x\n  built %x", typed, built)
	}

	// And one with repeats in it, so that what is pinned is the grouping and not
	// only the order of the fields: "aabbbc" holds a pair, which stays in the
	// literals, and a triple, which does not.
	e := New(1)
	insert(t, e, 0, "aabbbc")
	remove(t, e, 5, 1)
	repeats := runBuilder{
		sites: [][2]uint64{{1, 7}},
		runs: []encodedRun{{
			site: 1, seq: 1, clock: 1, text: []rune("aabbbc"),
			dels: [][4]uint64{{5, 1, 1, 7}},
		}},
	}
	if typed, built := e.Snapshot(), repeats.build(); !bytes.Equal(typed, built) {
		t.Fatalf("the builder and a real document with repeats disagree:\n  typed %x\n  built %x",
			typed, built)
	}
}

// spread is n values that do not repeat and are two bytes each, so that the
// column they make is longer than a small document's ceiling allows.
func spread(n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(1000 + i)
	}
	return out
}
