package crdt_test

import (
	"bytes"
	"encoding/hex"
	"flag"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden-composite.snapshot from this build")

// goldenDocument builds one document from a fixed script. Every identity,
// clock and sequence number it mints is a function of that script alone, so
// two builds of this package produce the same document — which is what makes
// its bytes comparable at all.
//
// It is deliberately a document with all four kinds in it, and with the
// characters that have historically told architectures apart: multi-byte
// UTF-8, a supplementary character (two UTF-16 code units), and the float32
// coordinates of an ink stroke, which are the only fixed-width multibyte
// field in the whole encoding.
func goldenDocument(t *testing.T) *crdt.Composite {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	c := crdt.NewComposite(7)

	body, err := c.Text("body")
	must(err)
	_, err = body.Insert(0, "hello, ")
	must(err)
	_, err = body.Insert(7, "élan 日本語 ")
	must(err)
	_, err = body.Insert(body.Len(), "\U0001F600 end")
	must(err)
	_, err = body.Delete(0, 5) // a tombstoned stretch, so the deletion columns carry something
	must(err)

	list, err := c.List("items")
	must(err)
	// A list value may not be empty, which is a rule of the encoding, not a
	// gap in this fixture.
	for i, v := range []string{"alpha", "beta", "\U0001F600", "oméga"} {
		_, err = list.Insert(i, []byte(v))
		must(err)
	}
	_, err = list.Delete(1, 1)
	must(err)

	m, err := c.Map("cells")
	must(err)
	for _, kv := range [][2]string{{"a", "1"}, {"b", ""}, {"é", "\U0001F600"}, {"z", "last"}} {
		_, err = m.Set(kv[0], []byte(kv[1]))
		must(err)
	}
	_, err = m.Delete("b")
	must(err)
	return c
}

// The encoding is the same bytes on every architecture.
//
// Every length, identity, clock and count is a varint, and the one fixed-width
// field there is (an ink stroke's float32 coordinates) is written little-endian
// explicitly on both sides — so this holds by construction. It had never been
// checked: the architecture lanes each ran the suite against themselves, and
// this repository's CI comment claimed they compared "the snapshot they
// exchange ... byte for byte", which nothing did.
//
// Comparing every lane against ONE file checked in here is that comparison: a
// big-endian s390x replica and a little-endian amd64 one are peers in a
// session only if they agree on these bytes, and now each says so.
//
// Regenerate with:
//
//	go test -run TestTheEncodingIsTheSameBytesEverywhere -update-golden .
func TestTheEncodingIsTheSameBytesEverywhere(t *testing.T) {
	const path = "testdata/golden-composite.snapshot"
	got := goldenDocument(t).Snapshot()

	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d bytes to %s on %s/%s", len(got), path, runtime.GOOS, runtime.GOARCH)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with -update-golden)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the snapshot this build writes is not the one checked in, on %s/%s.\n"+
			"  got  %d bytes: %s\n  want %d bytes: %s\n"+
			"An architecture that disagrees here cannot be a peer of one that does not.",
			runtime.GOOS, runtime.GOARCH, len(got), firstBytes(got), len(want), firstBytes(want))
	}

	// And the bytes are readable by this build, which is the other half of
	// "these two are peers": agreeing on what to write is no use without
	// agreeing on what it says.
	back, err := crdt.LoadComposite(9, want)
	if err != nil {
		t.Fatalf("this build cannot read the bytes it agrees to write: %v", err)
	}
	if again := back.Snapshot(); !bytes.Equal(again, want) {
		t.Fatalf("re-encoding what was read gives %d bytes, not the %d read", len(again), len(want))
	}
	text, err := back.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if want := ", élan 日本語 \U0001F600 end"; text.String() != want {
		t.Fatalf("the golden document reads %q, want %q", text.String(), want)
	}
}

// firstBytes is enough of a snapshot to see where two disagree.
func firstBytes(b []byte) string {
	const n = 48
	if len(b) > n {
		return hex.EncodeToString(b[:n]) + "…"
	}
	return hex.EncodeToString(b)
}

// The golden file is bytes, and a person reading a diff of it should be told
// what it is rather than shown a wall of hex.
func TestTheGoldenFileIsWhatItSaysItIs(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden-composite.snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "crdtc") {
		t.Fatalf("the golden file does not start with a composite's magic: %s", firstBytes(raw))
	}
}
