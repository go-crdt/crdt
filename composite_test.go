package crdt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// A Composite adds no merge rule, so what these tests are for is the three
// things it does add: an identity for a part, a canonical shape for the whole,
// and a decoder that has to refuse everything a replica could not have written.
// The convergence properties are still asserted end to end, because a container
// can break convergence without touching a merge rule — by writing its parts in
// an order that depends on a Go map, or by letting a part exist on one replica
// and not on another.

func textPart(t *testing.T, c *Composite, name string) *Doc {
	t.Helper()
	d, err := c.Text(name)
	if err != nil {
		t.Fatalf("Text(%q): %v", name, err)
	}
	return d
}

func listPart(t *testing.T, c *Composite, name string) *List {
	t.Helper()
	l, err := c.List(name)
	if err != nil {
		t.Fatalf("List(%q): %v", name, err)
	}
	return l
}

func mapPart(t *testing.T, c *Composite, name string) *Map {
	t.Helper()
	m, err := c.Map(name)
	if err != nil {
		t.Fatalf("Map(%q): %v", name, err)
	}
	return m
}

func assertParts(t *testing.T, c *Composite, want ...Part) {
	t.Helper()
	got := c.Parts()
	if len(got) != len(want) {
		t.Fatalf("Parts() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Parts() = %v, want %v", got, want)
		}
	}
}

// applyTo hands one part's operations to a replica, which is the shape every
// caller uses: a part names what the operations are addressed to, and nothing
// but the caller knows the answer.
func applyText(t *testing.T, c *Composite, name string, ops []Op) {
	t.Helper()
	if err := c.Apply(PartOps{Part: Part{Kind: PartText, Name: name}, Text: ops}); err != nil {
		t.Fatalf("Apply text %q: %v", name, err)
	}
}

func applyList(t *testing.T, c *Composite, name string, ops []ListOp) {
	t.Helper()
	if err := c.Apply(PartOps{Part: Part{Kind: PartList, Name: name}, List: ops}); err != nil {
		t.Fatalf("Apply list %q: %v", name, err)
	}
}

func applyMapPart(t *testing.T, c *Composite, name string, ops ...MapOp) {
	t.Helper()
	if err := c.Apply(PartOps{Part: Part{Kind: PartMap, Name: name}, Map: ops}); err != nil {
		t.Fatalf("Apply map %q: %v", name, err)
	}
}

func TestPartKindString(t *testing.T) {
	for kind, want := range map[PartKind]string{
		PartText: "text",
		PartList: "list",
		PartMap:  "map",
		0:        "invalid(0)",
		9:        "invalid(9)",
	} {
		if got := kind.String(); got != want {
			t.Fatalf("PartKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestCompositeSite(t *testing.T) {
	c := NewComposite(7)
	if got := c.Site(); got != 7 {
		t.Fatalf("Site() = %d, want 7", got)
	}
	// The parts inherit it: a part is edited as the replica that holds it.
	if got := textPart(t, c, "file").Site(); got != 7 {
		t.Fatalf("the text part issues as site %d, want 7", got)
	}
	if got := listPart(t, c, "comments").Site(); got != 7 {
		t.Fatalf("the list part issues as site %d, want 7", got)
	}
}

func TestCompositePartsAreCreatedByUse(t *testing.T) {
	c := NewComposite(1)
	d := textPart(t, c, "file:main.tex")
	if again := textPart(t, c, "file:main.tex"); again != d {
		t.Fatal("asking twice for one part returned two parts")
	}
	// Reaching for a part is not an operation, so until something is written the
	// replica is in the state it started in — the same state as one that has
	// never heard the name.
	listPart(t, c, "chat")
	mapPart(t, c, "cells")
	assertParts(t, c)
	if got := len(c.Version()); got != 0 {
		t.Fatalf("Version() names %d parts, want none", got)
	}
	if !bytes.Equal(c.Snapshot(), NewComposite(2).Snapshot()) {
		t.Fatal("a part reached for and left empty is not free")
	}

	if _, err := d.Insert(0, "x"); err != nil {
		t.Fatal(err)
	}
	assertParts(t, c, Part{Kind: PartText, Name: "file:main.tex"})
}

// A part left holding only tombstones has still had operations, and stays.
func TestCompositeEmptiedPartStays(t *testing.T) {
	c := NewComposite(1)
	l := listPart(t, c, "chat")
	if _, err := l.Insert(0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	assertParts(t, c, Part{Kind: PartList, Name: "chat"})
}

func TestCompositeRejectsUnusableNames(t *testing.T) {
	c := NewComposite(1)
	for _, name := range []string{"", "\xff\xfe", "ok\xffnot"} {
		if _, err := c.Text(name); !errors.Is(err, ErrInvalidPart) {
			t.Fatalf("Text(%q) = %v, want ErrInvalidPart", name, err)
		}
		if _, err := c.List(name); !errors.Is(err, ErrInvalidPart) {
			t.Fatalf("List(%q) = %v, want ErrInvalidPart", name, err)
		}
		if _, err := c.Map(name); !errors.Is(err, ErrInvalidPart) {
			t.Fatalf("Map(%q) = %v, want ErrInvalidPart", name, err)
		}
	}
	// Refusing must not have created anything.
	if got := len(c.texts) + len(c.lists) + len(c.maps); got != 0 {
		t.Fatalf("a refused name left %d parts behind", got)
	}
	// A name that is valid UTF-8 is accepted whatever it looks like: the names
	// this is for carry paths, colons and identifiers.
	for _, name := range []string{"file:src/main.tex", "comment:9f3c-4d21", "chat", "é世🌍", " "} {
		if _, err := c.Text(name); err != nil {
			t.Fatalf("Text(%q) = %v", name, err)
		}
	}
}

// Two replicas that concurrently reach for one name, one as a list and one as a
// map, hold two parts rather than a conflict. Nothing is exchanged and nothing
// arbitrates.
func TestCompositeOneNameTwoKinds(t *testing.T) {
	ada, grace := NewComposite(1), NewComposite(2)
	fromAda, err := listPart(t, ada, "notes").Insert(0, []byte("a list"))
	if err != nil {
		t.Fatal(err)
	}
	fromGrace, err := mapPart(t, grace, "notes").Set("k", []byte("a map"))
	if err != nil {
		t.Fatal(err)
	}
	applyMapPart(t, ada, "notes", fromGrace)
	applyList(t, grace, "notes", fromAda)

	want := []Part{{Kind: PartList, Name: "notes"}, {Kind: PartMap, Name: "notes"}}
	assertParts(t, ada, want...)
	assertParts(t, grace, want...)
	if !bytes.Equal(ada.Snapshot(), grace.Snapshot()) {
		t.Fatal("two replicas that took one name for two kinds did not converge")
	}
}

func TestCompositePartsAreOrdered(t *testing.T) {
	c := NewComposite(1)
	// Written in an order that is neither the answer nor the reverse of it, so a
	// Parts() that returned insertion order would be caught.
	if _, err := mapPart(t, c, "b").Set("k", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := textPart(t, c, "z").Insert(0, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := listPart(t, c, "a").Insert(0, []byte("v")); err != nil {
		t.Fatal(err)
	}
	if _, err := mapPart(t, c, "a").Set("k", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := textPart(t, c, "a").Insert(0, "x"); err != nil {
		t.Fatal(err)
	}
	assertParts(t, c,
		Part{Kind: PartText, Name: "a"},
		Part{Kind: PartText, Name: "z"},
		Part{Kind: PartList, Name: "a"},
		Part{Kind: PartMap, Name: "a"},
		Part{Kind: PartMap, Name: "b"},
	)
	// Go randomises map iteration, so an order taken from one would differ
	// between calls on a document this size.
	for range 20 {
		assertParts(t, c,
			Part{Kind: PartText, Name: "a"},
			Part{Kind: PartText, Name: "z"},
			Part{Kind: PartList, Name: "a"},
			Part{Kind: PartMap, Name: "a"},
			Part{Kind: PartMap, Name: "b"},
		)
	}
}

func TestPartLess(t *testing.T) {
	text := Part{Kind: PartText, Name: "b"}
	list := Part{Kind: PartList, Name: "a"}
	if !partLess(text, list) {
		t.Fatal("kind orders before name")
	}
	if partLess(list, text) {
		t.Fatal("the order is not antisymmetric")
	}
	if !partLess(Part{Kind: PartMap, Name: "a"}, Part{Kind: PartMap, Name: "b"}) {
		t.Fatal("names of one kind order by name")
	}
	if partLess(text, text) {
		t.Fatal("a part orders before itself")
	}
}

// A composite of every kind, edited enough that each part holds a tombstone as
// well as live content, which is the state a snapshot has most to say about.
func filledComposite(t *testing.T, site SiteID) *Composite {
	t.Helper()
	c := NewComposite(site)
	d := textPart(t, c, "file:main.tex")
	if _, err := d.Insert(0, "the quick 世 fox"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Delete(3, 6); err != nil {
		t.Fatal(err)
	}
	l := listPart(t, c, "comments:main.tex")
	if _, err := l.Insert(0, []byte("first"), []byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	m := mapPart(t, c, "cells")
	if _, err := m.Set("B7", []byte("42")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("B8", []byte("43")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete("B8"); err != nil {
		t.Fatal(err)
	}
	if _, err := mapPart(t, c, "comment:9f3c").Set("resolved", []byte("false")); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCompositeSnapshotRoundTrip(t *testing.T) {
	c := filledComposite(t, 1)
	loaded, err := LoadComposite(2, c.Snapshot())
	if err != nil {
		t.Fatalf("LoadComposite: %v", err)
	}
	if !bytes.Equal(loaded.Snapshot(), c.Snapshot()) {
		t.Fatal("reloading a snapshot did not reproduce it")
	}
	if got := loaded.Site(); got != 2 {
		t.Fatalf("the loaded document edits as site %d, want 2", got)
	}
	if got, want := textPart(t, loaded, "file:main.tex").String(), "the 世 fox"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if got := listPart(t, loaded, "comments:main.tex").Values(); len(got) != 1 || string(got[0]) != "second" {
		t.Fatalf("list = %q", got)
	}
	m := mapPart(t, loaded, "cells")
	if got, held := m.Get("B7"); !held || string(got) != "42" {
		t.Fatalf("cells[B7] = %q, %v", got, held)
	}
	if _, held := m.Get("B8"); held {
		t.Fatal("a deleted cell came back")
	}
	if got := m.Tombstones(); got != 1 {
		t.Fatalf("the map kept %d tombstones, want 1", got)
	}
	// And the whole history is still there to serve a peer that has been away.
	fresh := NewComposite(3)
	if err := fresh.Apply(loaded.OpsSince(nil)...); err != nil {
		t.Fatalf("replaying a loaded document's history: %v", err)
	}
	if !bytes.Equal(fresh.Snapshot(), c.Snapshot()) {
		t.Fatal("a document loaded from a snapshot cannot serve a peer the original could")
	}
}

func TestCompositeSnapshotOmitsEmptyParts(t *testing.T) {
	// One replica reaches for three parts and writes to one; another hears only
	// the operation. They must agree byte for byte, or a replica's own habits
	// would be part of its state.
	ada := NewComposite(1)
	textPart(t, ada, "file:untouched")
	mapPart(t, ada, "cells")
	ops, err := listPart(t, ada, "chat").Insert(0, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	grace := NewComposite(2)
	applyList(t, grace, "chat", ops)
	if !bytes.Equal(ada.Snapshot(), grace.Snapshot()) {
		t.Fatal("a part reached for and left empty changed the snapshot")
	}
	if !ada.Version().Equal(grace.Version()) {
		t.Fatal("a part reached for and left empty changed the version")
	}
	if got := len(ada.OpsSince(grace.Version())); got != 0 {
		t.Fatalf("an empty part offered %d batches to a peer that holds everything", got)
	}
}

func TestLoadCompositeDoesNotAliasTheSnapshot(t *testing.T) {
	// The three loaders each get a window on the caller's buffer, and a caller
	// commonly reuses one. Three chances to keep a reference rather than a copy.
	c := filledComposite(t, 1)
	snapshot := c.Snapshot()
	loaded, err := LoadComposite(2, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), loaded.Snapshot()...)
	for i := range snapshot {
		snapshot[i] = 0xAA
	}
	if !bytes.Equal(loaded.Snapshot(), want) {
		t.Fatal("a loaded document held a view of the caller's bytes")
	}
}

// TestCompositeSnapshotIsCanonical is the property the whole test suite leans
// on: replicas are compared by encoded state, not by what they show.
func TestCompositeSnapshotIsCanonical(t *testing.T) {
	ada, grace := NewComposite(1), NewComposite(2)
	textOps, err := textPart(t, ada, "file").Insert(0, "hello")
	if err != nil {
		t.Fatal(err)
	}
	mapOp, err := mapPart(t, ada, "cells").Set("A1", []byte("1"))
	if err != nil {
		t.Fatal(err)
	}
	listOps, err := listPart(t, grace, "chat").Insert(0, []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	// Delivered in opposite orders, and to replicas that created their parts in
	// opposite orders.
	applyList(t, ada, "chat", listOps)
	applyMapPart(t, grace, "cells", mapOp)
	applyText(t, grace, "file", textOps)
	if !bytes.Equal(ada.Snapshot(), grace.Snapshot()) {
		t.Fatalf("delivery order reached the encoding:\n%x\n%x", ada.Snapshot(), grace.Snapshot())
	}
}

func TestCompositePending(t *testing.T) {
	c := NewComposite(1)
	source := NewComposite(2)
	text, err := textPart(t, source, "file").Insert(0, "abc")
	if err != nil {
		t.Fatal(err)
	}
	list, err := listPart(t, source, "chat").Insert(0, []byte("one"), []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := mapPart(t, source, "cells").Set("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mapPart(t, source, "cells").Set("b", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = first
	// Hand each part everything but its first operation: nothing can be applied
	// and nothing may be dropped.
	applyText(t, c, "file", text[1:])
	applyList(t, c, "chat", list[1:])
	applyMapPart(t, c, "cells", second)
	if got, want := c.Pending(), 4; got != want {
		t.Fatalf("Pending() = %d, want %d", got, want)
	}
	assertParts(t, c)
}

// The version

func TestCompositeVersionIsACopy(t *testing.T) {
	// The trap the list loader had, moved up a level: a version handed out has to
	// stop describing this replica the moment it leaves, or a server would find
	// the peer's position advancing every time it applied something.
	c := filledComposite(t, 1)
	before := c.Version()
	promised := before.Clone()
	if _, err := textPart(t, c, "file:main.tex").Insert(0, "more"); err != nil {
		t.Fatal(err)
	}
	if _, err := mapPart(t, c, "cells").Set("C1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := listPart(t, c, "later").Insert(0, []byte("v")); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(promised) {
		t.Fatal("editing the document changed a version already handed out")
	}
	if c.Version().Equal(before) {
		t.Fatal("the version did not move at all")
	}
	// And the clone is independent in the other direction too.
	promised[Part{Kind: PartText, Name: "file:main.tex"}][1] = 99
	if before.Equal(promised) {
		t.Fatal("Clone shared a vector")
	}
}

func TestCompositeVersionEqual(t *testing.T) {
	c := filledComposite(t, 1)
	v := c.Version()
	if !v.Equal(v.Clone()) {
		t.Fatal("a version does not equal its own clone")
	}
	if v.Equal(nil) || CompositeVersion(nil).Equal(v) {
		t.Fatal("a populated version equals nothing")
	}
	if !CompositeVersion(nil).Equal(CompositeVersion{}) {
		t.Fatal("nil and empty are different versions")
	}
	// A part promising nothing is a part not mentioned, in both directions.
	empty := CompositeVersion{Part{Kind: PartMap, Name: "x"}: VersionVector{}}
	if !empty.Equal(nil) || !CompositeVersion(nil).Equal(empty) {
		t.Fatal("a part promising nothing is not the same as an absent one")
	}
	other := v.Clone()
	other[Part{Kind: PartList, Name: "chat"}] = VersionVector{1: 1}
	if v.Equal(other) || other.Equal(v) {
		t.Fatal("an extra part did not show up as a difference")
	}
}

func TestCompositeVersionRoundTrip(t *testing.T) {
	c := filledComposite(t, 1)
	// A second site, so the shared site table has something to share.
	peer := NewComposite(0x9E3779B97F4A7C15)
	if err := peer.Apply(c.OpsSince(nil)...); err != nil {
		t.Fatal(err)
	}
	if _, err := mapPart(t, peer, "cells").Set("Z9", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(peer.OpsSince(c.Version())...); err != nil {
		t.Fatal(err)
	}

	v := c.Version()
	encoded, err := v.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var again CompositeVersion
	if err := again.UnmarshalBinary(encoded); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if !again.Equal(v) {
		t.Fatalf("round trip gave %v, want %v", again, v)
	}
	// The encoding is canonical, so equal versions encode identically and a
	// caller may compare the bytes.
	twice, err := again.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(twice, encoded) {
		t.Fatal("re-encoding a decoded version is not a fixed point")
	}
	// The site that costs ten bytes as a varint appears once, not once per part.
	if got := bytes.Count(encoded, binary.AppendUvarint(nil, 0x9E3779B97F4A7C15)); got != 1 {
		t.Fatalf("the site identity appears %d times in the encoding, want 1", got)
	}
}

func TestCompositeVersionMarshalRefusesWhatWouldBeRejected(t *testing.T) {
	for name, v := range map[string]CompositeVersion{
		"an unnamed part":   {{Kind: PartMap}: VersionVector{1: 1}},
		"a part of no kind": {{Name: "x"}: VersionVector{1: 1}},
		"a name that is not text": {
			{Kind: PartMap, Name: "\xff"}: VersionVector{1: 1},
		},
	} {
		if _, err := v.MarshalBinary(); !errors.Is(err, ErrInvalidPart) {
			t.Fatalf("%s: MarshalBinary = %v, want ErrInvalidPart", name, err)
		}
	}
	beyond := CompositeVersion{{Kind: PartMap, Name: "x"}: VersionVector{1: MaxClock + 1}}
	if _, err := beyond.MarshalBinary(); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("MarshalBinary of a sequence above the ceiling = %v, want ErrInvalidOp", err)
	}
}

func TestCompositeVersionSkipsWhatPromisesNothing(t *testing.T) {
	// A vector recording a site at zero promises nothing, and a part promising
	// nothing is not a part: neither may reach the encoding, or two versions that
	// are equal would not encode alike.
	v := CompositeVersion{
		{Kind: PartMap, Name: "held"}:  VersionVector{4: 2},
		{Kind: PartMap, Name: "empty"}: VersionVector{7: 0},
		{Kind: PartList, Name: "none"}: VersionVector{},
	}
	encoded, err := v.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	want, err := CompositeVersion{{Kind: PartMap, Name: "held"}: VersionVector{4: 2}}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoding kept a part that promises nothing:\n%x\n%x", encoded, want)
	}
	if got := v.sites(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("sites() = %v, want [4]", got)
	}
	// Two parts naming the same site name it once in the table.
	shared := CompositeVersion{
		{Kind: PartMap, Name: "a"}:  VersionVector{4: 2, 9: 1},
		{Kind: PartList, Name: "b"}: VersionVector{4: 5},
	}
	if got := shared.sites(); len(got) != 2 || got[0] != 4 || got[1] != 9 {
		t.Fatalf("sites() = %v, want [4 9]", got)
	}
}

// encodeCompositeVersion assembles version bytes from parts: a uint64 is a
// varint, a string is length-prefixed, a byte slice is raw. It builds the
// malformed cases exactly rather than by corrupting a good encoding and hoping
// the corruption lands where it is needed.
func encodeCompositeVersion(parts ...any) []byte {
	var out []byte
	for _, part := range parts {
		switch v := part.(type) {
		case uint64:
			out = binary.AppendUvarint(out, v)
		case string:
			out = appendKey(out, v)
		case []byte:
			out = append(out, v...)
		default:
			panic(fmt.Sprintf("unsupported part %T", part))
		}
	}
	return out
}

func TestCompositeVersionRejects(t *testing.T) {
	// One site, then whatever the case supplies.
	header := func(rest ...any) []byte {
		return encodeCompositeVersion(append([]any{uint64(1), uint64(5)}, rest...)...)
	}
	// One site, one map part called "a", then its entries.
	part := func(rest ...any) []byte {
		return header(append([]any{uint64(1), []byte{byte(PartMap)}, "a"}, rest...)...)
	}
	for name, data := range map[string][]byte{
		"empty":                    {},
		"a site count and no site": encodeCompositeVersion(uint64(1)),
		// A varint whose continuation bit promises a byte that is not there. The
		// count of sites is within the bytes remaining, so only the varint itself
		// can refuse this.
		"a truncated site": append(encodeCompositeVersion(uint64(2), uint64(5)), 0x80),
		// Two parts, and the first consumes every byte there was room for.
		"a part count the parts do not fill": encodeCompositeVersion(uint64(1), uint64(5),
			uint64(2), []byte{byte(PartMap)}, "a", uint64(1), uint64(0), uint64(1)),
		"more sites than bytes":     encodeCompositeVersion(uint64(9), uint64(1)),
		"sites out of order":        encodeCompositeVersion(uint64(2), uint64(5), uint64(4), uint64(0)),
		"the same site twice":       encodeCompositeVersion(uint64(2), uint64(5), uint64(5), uint64(0)),
		"no part count":             encodeCompositeVersion(uint64(1), uint64(5)),
		"more parts than bytes":     header(uint64(9)),
		"a part with no kind":       header(uint64(1)),
		"a part of no kind":         header(uint64(1), []byte{0}, "a", uint64(1), uint64(0), uint64(1)),
		"a part of an unknown kind": header(uint64(1), []byte{9}, "a", uint64(1), uint64(0), uint64(1)),
		"a part with no name":       header(uint64(1), []byte{byte(PartMap)}),
		"a part named nothing":      header(uint64(1), []byte{byte(PartMap)}, "", uint64(1), uint64(0), uint64(1)),
		"a name that is not text":   header(uint64(1), []byte{byte(PartMap)}, []byte{1, 0xff}, uint64(1), uint64(0), uint64(1)),
		"parts out of order": header(uint64(2),
			[]byte{byte(PartMap)}, "b", uint64(1), uint64(0), uint64(1),
			[]byte{byte(PartMap)}, "a", uint64(1), uint64(0), uint64(1)),
		"the same part twice": header(uint64(2),
			[]byte{byte(PartMap)}, "a", uint64(1), uint64(0), uint64(1),
			[]byte{byte(PartMap)}, "a", uint64(1), uint64(0), uint64(2)),
		"a part with no entry count":     part(),
		"a part promising nothing":       part(uint64(0)),
		"more entries than bytes":        part(uint64(9)),
		"an entry with no sequence":      part(uint64(1), uint64(0)),
		"a site index past the table":    part(uint64(1), uint64(1), uint64(1)),
		"a sequence number of zero":      part(uint64(1), uint64(0), uint64(0)),
		"a sequence above the ceiling":   part(uint64(1), uint64(0), uint64(MaxClock+1)),
		"a site named twice in one part": encodeCompositeVersion(uint64(2), uint64(5), uint64(6), uint64(1), []byte{byte(PartMap)}, "a", uint64(2), uint64(0), uint64(1), uint64(0), uint64(2)),
		"entries out of order":           encodeCompositeVersion(uint64(2), uint64(5), uint64(6), uint64(1), []byte{byte(PartMap)}, "a", uint64(2), uint64(1), uint64(1), uint64(0), uint64(2)),
		"a site no part names":           encodeCompositeVersion(uint64(2), uint64(5), uint64(6), uint64(1), []byte{byte(PartMap)}, "a", uint64(1), uint64(0), uint64(1)),
		"trailing bytes":                 append(part(uint64(1), uint64(0), uint64(1)), 0),
	} {
		var v CompositeVersion
		if err := v.UnmarshalBinary(data); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: UnmarshalBinary = %v, want ErrMalformed", name, err)
		}
	}

	// The smallest thing that must be accepted, so the table above is refusing
	// what it claims to refuse and not merely everything.
	var v CompositeVersion
	if err := v.UnmarshalBinary(part(uint64(1), uint64(0), uint64(3))); err != nil {
		t.Fatalf("a well-formed version was refused: %v", err)
	}
	want := CompositeVersion{{Kind: PartMap, Name: "a"}: VersionVector{5: 3}}
	if !v.Equal(want) {
		t.Fatalf("UnmarshalBinary gave %v, want %v", v, want)
	}
	// And the version of a document with no parts at all, which is two zeroes.
	var none CompositeVersion
	if err := none.UnmarshalBinary(encodeCompositeVersion(uint64(0), uint64(0))); err != nil {
		t.Fatalf("the empty version was refused: %v", err)
	}
	if !none.Equal(nil) {
		t.Fatalf("the empty version decoded to %v", none)
	}
}

// The fuzzer found this one, and it is the boundary of what "canonical" means
// here. binary.Uvarint accepts an overlong varint, so a peer may write a zero in
// two bytes and this decoder cannot tell. Every decoder in this package reads
// its varints through the same reader and has the same hole, and nothing else in
// the encoding is ambiguous — so what is guaranteed is that what this package
// encodes reloads to itself byte for byte, and that what it accepts is
// normalised into that form.
func TestCompositeVersionNormalisesARedundantVarint(t *testing.T) {
	// No sites, no parts: the empty version, with the part count written long.
	redundant := []byte{0x00, 0x80, 0x00}
	var v CompositeVersion
	if err := v.UnmarshalBinary(redundant); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if !v.Equal(nil) {
		t.Fatalf("decoded to %v, want the empty version", v)
	}
	encoded, err := v.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encoded, redundant) {
		t.Fatal("the control failed: the two encodings are the same bytes")
	}
	if want := []byte{0x00, 0x00}; !bytes.Equal(encoded, want) {
		t.Fatalf("re-encoded to %x, want %x", encoded, want)
	}
	// And the normalised form is a fixed point, which is the property that holds.
	var again CompositeVersion
	if err := again.UnmarshalBinary(encoded); err != nil {
		t.Fatal(err)
	}
	twice, err := again.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(twice, encoded) {
		t.Fatal("re-encoding what MarshalBinary produced is not a fixed point")
	}
}

// Operations

func TestCompositeApplyRejectsWholeCall(t *testing.T) {
	source := NewComposite(1)
	good, err := listPart(t, source, "chat").Insert(0, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	for name, batches := range map[string][]PartOps{
		"an unnamed part": {
			{Part: Part{Kind: PartList, Name: "chat"}, List: good},
			{Part: Part{Kind: PartMap}, Map: []MapOp{{Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 1}}},
		},
		"a part of an unknown kind": {
			{Part: Part{Kind: PartList, Name: "chat"}, List: good},
			{Part: Part{Kind: 9, Name: "x"}},
		},
		"a name that is not text": {
			{Part: Part{Kind: PartList, Name: "chat"}, List: good},
			{Part: Part{Kind: PartList, Name: "\xff"}},
		},
	} {
		c := NewComposite(2)
		if err := c.Apply(batches...); !errors.Is(err, ErrInvalidPart) {
			t.Fatalf("%s: Apply = %v, want ErrInvalidPart", name, err)
		}
		// The good batch came first, and must not have been applied: a call that
		// fails leaves nothing behind, parts included.
		assertParts(t, c)
		if got := len(c.lists); got != 0 {
			t.Fatalf("%s: a failed call created %d parts", name, got)
		}
	}
}

func TestPartOpsRejectsFieldsThatDoNotBelong(t *testing.T) {
	text := []Op{{Kind: OpInsert, ID: ID{Site: 1, Seq: 1}, Clock: 1, Char: 'a'}}
	list := []ListOp{{Kind: OpInsert, ID: ID{Site: 1, Seq: 1}, Clock: 1, Value: []byte("v")}}
	maps := []MapOp{{Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 1, Key: "k"}}
	for name, b := range map[string]PartOps{
		"a list in a text batch": {Part: Part{Kind: PartText, Name: "x"}, List: list},
		"a map in a text batch":  {Part: Part{Kind: PartText, Name: "x"}, Map: maps},
		"text in a list batch":   {Part: Part{Kind: PartList, Name: "x"}, Text: text},
		"a map in a list batch":  {Part: Part{Kind: PartList, Name: "x"}, Map: maps},
		"text in a map batch":    {Part: Part{Kind: PartMap, Name: "x"}, Text: text},
		"a list in a map batch":  {Part: Part{Kind: PartMap, Name: "x"}, List: list},
	} {
		if err := NewComposite(1).Apply(b); !errors.Is(err, ErrInvalidOp) {
			t.Fatalf("%s: Apply = %v, want ErrInvalidOp", name, err)
		}
	}
	// A malformed operation of the right kind is refused by the part's own
	// validator, which is the one that has always decided this.
	for name, b := range map[string]PartOps{
		"text": {Part: Part{Kind: PartText, Name: "x"}, Text: []Op{{Kind: OpInsert}}},
		"list": {Part: Part{Kind: PartList, Name: "x"}, List: []ListOp{{Kind: OpInsert}}},
		"map":  {Part: Part{Kind: PartMap, Name: "x"}, Map: []MapOp{{Kind: MapSet}}},
	} {
		if err := NewComposite(1).Apply(b); !errors.Is(err, ErrInvalidOp) {
			t.Fatalf("a malformed %s operation: Apply = %v, want ErrInvalidOp", name, err)
		}
	}
	// An empty batch is valid and does nothing; OpsSince never sends one, but a
	// caller filtering its own may well.
	c := NewComposite(1)
	if err := c.Apply(PartOps{Part: Part{Kind: PartMap, Name: "x"}}); err != nil {
		t.Fatalf("an empty batch: Apply = %v", err)
	}
	assertParts(t, c)
}

func TestCompositeOpsSinceSkipsWhatThePeerHolds(t *testing.T) {
	ada := filledComposite(t, 1)
	grace := NewComposite(2)
	if err := grace.Apply(ada.OpsSince(nil)...); err != nil {
		t.Fatal(err)
	}
	if got := ada.OpsSince(grace.Version()); got != nil {
		t.Fatalf("a peer holding everything was offered %v", got)
	}

	// One part moves. The peer must be sent that part and nothing else, without
	// the others' histories being walked to discover they have nothing to say.
	held := grace.Version()
	if _, err := mapPart(t, ada, "cells").Set("C3", []byte("x")); err != nil {
		t.Fatal(err)
	}
	batches := ada.OpsSince(held)
	if len(batches) != 1 || batches[0].Part != (Part{Kind: PartMap, Name: "cells"}) {
		t.Fatalf("OpsSince offered %d batches, want the one part that moved", len(batches))
	}
	if len(batches[0].Map) != 1 {
		t.Fatalf("OpsSince offered %d operations, want the one that is missing", len(batches[0].Map))
	}
	for _, b := range ada.OpsSince(nil) {
		if len(b.Text)+len(b.List)+len(b.Map) == 0 {
			t.Fatalf("OpsSince offered an empty batch for %v", b.Part)
		}
	}
	// A peer that is ahead on a part is not sent it either.
	ahead := ada.Version()
	ahead[Part{Kind: PartText, Name: "file:main.tex"}][99] = 3
	for _, b := range ada.OpsSince(ahead) {
		if b.Part.Kind == PartText {
			t.Fatal("a peer ahead of this replica was sent the part anyway")
		}
	}
}

func TestCompositeCatchUpOnEachKind(t *testing.T) {
	// Every kind has to be reachable through OpsSince, from nothing and from a
	// position part of the way along.
	ada := filledComposite(t, 1)
	grace := NewComposite(2)
	if err := grace.Apply(ada.OpsSince(nil)...); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ada.Snapshot(), grace.Snapshot()) {
		t.Fatal("catching up from nothing did not reproduce the document")
	}
	half := grace.Version()
	if _, err := textPart(t, ada, "file:main.tex").Insert(0, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := listPart(t, ada, "comments:main.tex").Insert(0, []byte("third")); err != nil {
		t.Fatal(err)
	}
	if _, err := mapPart(t, ada, "cells").Set("D4", []byte("y")); err != nil {
		t.Fatal(err)
	}
	batches := ada.OpsSince(half)
	if len(batches) != 3 {
		t.Fatalf("OpsSince offered %d batches, want three", len(batches))
	}
	if err := grace.Apply(batches...); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ada.Snapshot(), grace.Snapshot()) {
		t.Fatal("catching up part of the way did not reproduce the document")
	}
}

// Convergence

// compositeSimulation is a set of replicas plus an unreliable network, over a
// document of several parts of every kind. A container can break convergence
// without touching a merge rule, so the property is asserted on the whole rather
// than part by part.
type compositeSimulation struct {
	t     *testing.T
	rng   *rand.Rand
	docs  []*Composite
	inbox [][]PartOps
	parts []Part
}

func newCompositeSimulation(t *testing.T, seed uint64, replicas int) *compositeSimulation {
	t.Helper()
	s := &compositeSimulation{
		t:     t,
		rng:   rand.New(rand.NewPCG(seed, 0x5eed)),
		docs:  make([]*Composite, replicas),
		inbox: make([][]PartOps, replicas),
		parts: []Part{
			{Kind: PartText, Name: "file:main.tex"},
			{Kind: PartText, Name: "file:intro.tex"},
			{Kind: PartList, Name: "comments:main.tex"},
			{Kind: PartList, Name: "chat"},
			{Kind: PartMap, Name: "cells"},
			// The same name as a list, which must stay a different part.
			{Kind: PartMap, Name: "chat"},
		},
	}
	for i := range s.docs {
		s.docs[i] = NewComposite(SiteID(i + 1))
	}
	return s
}

// edit performs one random local change to one random part and queues the batch
// for everyone else.
func (s *compositeSimulation) edit(i int) {
	s.t.Helper()
	c := s.docs[i]
	p := s.parts[s.rng.IntN(len(s.parts))]
	batch := PartOps{Part: p}
	var err error
	switch p.Kind {
	case PartText:
		d := textPart(s.t, c, p.Name)
		if d.Len() > 0 && s.rng.IntN(4) == 0 {
			pos := s.rng.IntN(d.Len())
			batch.Text, err = d.Delete(pos, 1+s.rng.IntN(d.Len()-pos))
		} else {
			text := make([]rune, 1+s.rng.IntN(3))
			for j := range text {
				text[j] = alphabet[s.rng.IntN(len(alphabet))]
			}
			batch.Text, err = d.Insert(s.rng.IntN(d.Len()+1), string(text))
		}
	case PartList:
		l := listPart(s.t, c, p.Name)
		if l.Len() > 0 && s.rng.IntN(4) == 0 {
			batch.List, err = l.Delete(s.rng.IntN(l.Len()), 1)
		} else {
			batch.List, err = l.Insert(s.rng.IntN(l.Len()+1), []byte{byte('a' + s.rng.IntN(4))})
		}
	default:
		m := mapPart(s.t, c, p.Name)
		key := string(rune('A' + s.rng.IntN(4)))
		var op MapOp
		if s.rng.IntN(4) == 0 {
			op, err = m.Delete(key)
		} else {
			op, err = m.Set(key, []byte{byte('0' + s.rng.IntN(4))})
		}
		batch.Map = []MapOp{op}
	}
	if err != nil {
		s.t.Fatalf("replica %d: %v", i, err)
	}
	for j := range s.docs {
		if j != i {
			s.inbox[j] = append(s.inbox[j], batch)
		}
	}
}

// deliver hands a replica a random slice of its inbox, shuffled, occasionally
// duplicating a batch. Anything not delivered stays queued.
func (s *compositeSimulation) deliver(i int) {
	s.t.Helper()
	queued := s.inbox[i]
	if len(queued) == 0 {
		return
	}
	n := 1 + s.rng.IntN(len(queued))
	batch := append([]PartOps{}, queued[:n]...)
	s.inbox[i] = queued[n:]
	s.rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
	if s.rng.IntN(4) == 0 {
		batch = append(batch, batch[s.rng.IntN(len(batch))])
	}
	if err := s.docs[i].Apply(batch...); err != nil {
		s.t.Fatalf("replica %d: Apply: %v", i, err)
	}
}

func (s *compositeSimulation) settle() {
	s.t.Helper()
	for i := range s.docs {
		for len(s.inbox[i]) > 0 {
			s.deliver(i)
		}
	}
}

func (s *compositeSimulation) assertConverged(seed uint64) {
	s.t.Helper()
	want := string(s.docs[0].Snapshot())
	for i, c := range s.docs {
		if got := string(c.Snapshot()); got != want {
			s.t.Fatalf("seed %d: replica %d holds a different state from replica 0", seed, i)
		}
		if got := c.Pending(); got != 0 {
			s.t.Fatalf("seed %d: replica %d still holds %d undeliverable operations", seed, i, got)
		}
		if !c.Version().Equal(s.docs[0].Version()) {
			s.t.Fatalf("seed %d: replica %d version = %v, replica 0 version = %v",
				seed, i, c.Version(), s.docs[0].Version())
		}
	}
}

func TestCompositeConvergence(t *testing.T) {
	for seed := range uint64(300) {
		s := newCompositeSimulation(t, seed, 2+int(seed%3))
		for range 14 {
			for i := range s.docs {
				s.edit(i)
				if s.rng.IntN(2) == 0 {
					s.deliver(i)
				}
			}
		}
		s.settle()
		s.assertConverged(seed)
	}
}

// A replica that joins from a snapshot has to be indistinguishable from one that
// heard the whole history, including in what it can serve to the next joiner.
func TestCompositeConvergenceAcrossSnapshot(t *testing.T) {
	for seed := range uint64(100) {
		s := newCompositeSimulation(t, seed, 3)
		for range 8 {
			for i := range s.docs {
				s.edit(i)
				s.deliver(i)
			}
		}
		s.settle()
		s.assertConverged(seed)

		joined, err := LoadComposite(99, s.docs[0].Snapshot())
		if err != nil {
			t.Fatalf("seed %d: LoadComposite: %v", seed, err)
		}
		if !bytes.Equal(joined.Snapshot(), s.docs[0].Snapshot()) {
			t.Fatalf("seed %d: a joining replica holds a different state", seed)
		}
		relayed := NewComposite(98)
		if err := relayed.Apply(joined.OpsSince(nil)...); err != nil {
			t.Fatalf("seed %d: replaying from a joined replica: %v", seed, err)
		}
		if !bytes.Equal(relayed.Snapshot(), s.docs[0].Snapshot()) {
			t.Fatalf("seed %d: a joining replica cannot serve the next one", seed)
		}
	}
}

// compositeHistory builds a small concurrent history over two parts of different
// kinds and returns every batch in it, one operation each. Replicas edit blind,
// exchange everything, then edit blind again, which is the shape that makes an
// ordering bug show up.
func compositeHistory(t *testing.T, rng *rand.Rand, sites int) []PartOps {
	t.Helper()
	docs := make([]*Composite, sites)
	for i := range docs {
		docs[i] = NewComposite(SiteID(i + 1))
	}
	parts := []Part{{Kind: PartText, Name: "file"}, {Kind: PartMap, Name: "cells"}}
	var all []PartOps
	for range 2 {
		phase := make([][]PartOps, sites)
		for i, c := range docs {
			p := parts[rng.IntN(len(parts))]
			batch := PartOps{Part: p}
			if p.Kind == PartText {
				d := textPart(t, c, p.Name)
				ops, err := d.Insert(rng.IntN(d.Len()+1), string(rune('a'+rng.IntN(3))))
				if err != nil {
					t.Fatal(err)
				}
				batch.Text = ops
			} else {
				op, err := mapPart(t, c, p.Name).Set(string(rune('A'+rng.IntN(2))), []byte{byte('0' + rng.IntN(3))})
				if err != nil {
					t.Fatal(err)
				}
				batch.Map = []MapOp{op}
			}
			phase[i] = append(phase[i], batch)
			all = append(all, batch)
		}
		for i, c := range docs {
			for j, batches := range phase {
				if i != j {
					if err := c.Apply(batches...); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	return all
}

// TestCompositeEveryOrderingConverges is the exhaustive form of the property.
// Randomised delivery samples the space of orderings; this covers it, for
// histories the size of the counterexamples in the literature.
func TestCompositeEveryOrderingConverges(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 8))
	for trial := range 40 {
		batches := compositeHistory(t, rng, 3)
		if len(batches) != 6 {
			t.Fatalf("trial %d: history has %d batches, want 6", trial, len(batches))
		}
		want := ""
		permute(batches, func(p []PartOps) {
			c := NewComposite(99)
			for _, b := range p {
				if err := c.Apply(b); err != nil {
					t.Fatalf("trial %d: Apply: %v", trial, err)
				}
			}
			if c.Pending() != 0 {
				t.Fatalf("trial %d: %d operations never became applicable", trial, c.Pending())
			}
			got := string(c.Snapshot())
			if want == "" {
				want = got
				return
			}
			if got != want {
				var order []Part
				for _, b := range p {
					order = append(order, b.Part)
				}
				t.Fatalf("trial %d: delivery order %v produced different state", trial, order)
			}
		})
	}
}

// The snapshot decoder

// encodeCompositeSnapshot assembles snapshot bytes from parts, the same way
// encodeMapSnapshot does for a map.
func encodeCompositeSnapshot(parts ...any) []byte {
	out := append([]byte{}, compositeMagic[:]...)
	out = append(out, compositeSnapshotVersion)
	for _, part := range parts {
		switch v := part.(type) {
		case uint64:
			out = binary.AppendUvarint(out, v)
		case string:
			out = appendKey(out, v)
		case []byte:
			out = append(out, v...)
		default:
			panic(fmt.Sprintf("unsupported part %T", part))
		}
	}
	return out
}

// sized wraps a part's own snapshot the way the composite encoding carries it.
func sizedPayload(payload []byte) []byte { return appendSized(nil, payload) }

func TestLoadCompositeRejectsRubbish(t *testing.T) {
	c := filledComposite(t, 1)
	good := c.Snapshot()
	if _, err := LoadComposite(2, good); err != nil {
		t.Fatalf("LoadComposite of a good snapshot: %v", err)
	}
	// Each of the four snapshot formats in this package refuses the other three.
	for name, foreign := range map[string][]byte{
		"a text snapshot": func() []byte {
			d := New(1)
			if _, err := d.Insert(0, "text"); err != nil {
				t.Fatal(err)
			}
			return d.Snapshot()
		}(),
		"a list snapshot": func() []byte {
			l := NewList(1)
			if _, err := l.Insert(0, []byte("v")); err != nil {
				t.Fatal(err)
			}
			return l.Snapshot()
		}(),
		"a map snapshot": func() []byte {
			m := NewMap(1)
			if _, err := m.Set("k", nil); err != nil {
				t.Fatal(err)
			}
			return m.Snapshot()
		}(),
	} {
		if _, err := LoadComposite(2, foreign); !errors.Is(err, ErrMalformed) {
			t.Fatalf("LoadComposite of %s = %v, want ErrMalformed", name, err)
		}
	}
	// And the composite's own bytes are refused by the other three.
	if _, err := Load(2, good); !errors.Is(err, ErrMalformed) {
		t.Fatal("Load accepted a composite snapshot")
	}
	if _, err := LoadList(2, good); !errors.Is(err, ErrMalformed) {
		t.Fatal("LoadList accepted a composite snapshot")
	}
	if _, err := LoadMap(2, good); !errors.Is(err, ErrMalformed) {
		t.Fatal("LoadMap accepted a composite snapshot")
	}

	for n := range len(good) {
		if _, err := LoadComposite(2, good[:n]); err == nil {
			t.Fatalf("LoadComposite(%d of %d bytes) succeeded, want an error", n, len(good))
		}
	}
	if _, err := LoadComposite(2, append(append([]byte{}, good...), 0)); !errors.Is(err, ErrMalformed) {
		t.Fatal("LoadComposite accepted trailing bytes")
	}
	future := append([]byte{}, good...)
	future[len(compositeMagic)] = compositeSnapshotVersion + 1
	if _, err := LoadComposite(2, future); !errors.Is(err, ErrMalformed) {
		t.Fatal("LoadComposite accepted a future format version")
	}
}

func TestLoadCompositeRejectsMalformedSnapshots(t *testing.T) {
	textPayload := func() []byte {
		d := New(1)
		if _, err := d.Insert(0, "hi"); err != nil {
			t.Fatal(err)
		}
		return d.Snapshot()
	}()
	listPayload := func() []byte {
		l := NewList(1)
		if _, err := l.Insert(0, []byte("v")); err != nil {
			t.Fatal(err)
		}
		return l.Snapshot()
	}()
	mapPayload := func() []byte {
		m := NewMap(1)
		if _, err := m.Set("k", nil); err != nil {
			t.Fatal(err)
		}
		return m.Snapshot()
	}()

	for name, data := range map[string][]byte{
		"empty":                 {},
		"short magic":           []byte("crdt"),
		"foreign magic":         []byte("xxxxx\x01"),
		"no version":            []byte("crdtc"),
		"future version":        []byte("crdtc\x02"),
		"no part count":         encodeCompositeSnapshot(),
		"more parts than bytes": encodeCompositeSnapshot(uint64(9)),
		"a part with no kind":   encodeCompositeSnapshot(uint64(1)),
		"a part of no kind": encodeCompositeSnapshot(uint64(1),
			[]byte{0}, "a", sizedPayload(mapPayload)),
		"a part of an unknown kind": encodeCompositeSnapshot(uint64(1),
			[]byte{9}, "a", sizedPayload(mapPayload)),
		"a part with no name": encodeCompositeSnapshot(uint64(1), []byte{byte(PartMap)}),
		"a part named nothing": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, "", sizedPayload(mapPayload)),
		"a name that is not text": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, []byte{1, 0xff}, sizedPayload(mapPayload)),
		"a truncated name": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, []byte{9, 'a'}),
		"parts out of order": encodeCompositeSnapshot(uint64(2),
			[]byte{byte(PartMap)}, "b", sizedPayload(mapPayload),
			[]byte{byte(PartMap)}, "a", sizedPayload(mapPayload)),
		"kinds out of order": encodeCompositeSnapshot(uint64(2),
			[]byte{byte(PartMap)}, "a", sizedPayload(mapPayload),
			[]byte{byte(PartList)}, "a", sizedPayload(listPayload)),
		"the same part twice": encodeCompositeSnapshot(uint64(2),
			[]byte{byte(PartMap)}, "a", sizedPayload(mapPayload),
			[]byte{byte(PartMap)}, "a", sizedPayload(mapPayload)),
		"a part with no payload": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, "a"),
		"a payload longer than the snapshot": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, "a", []byte{99}, mapPayload),
		"a text part that is not a text snapshot": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartText)}, "a", sizedPayload(mapPayload)),
		"a list part that is not a list snapshot": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartList)}, "a", sizedPayload(textPayload)),
		"a map part that is not a map snapshot": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, "a", sizedPayload(listPayload)),
		"a text part holding nothing": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartText)}, "a", sizedPayload(New(1).Snapshot())),
		"a list part holding nothing": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartList)}, "a", sizedPayload(NewList(1).Snapshot())),
		"a map part holding nothing": encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, "a", sizedPayload(NewMap(1).Snapshot())),
		"trailing bytes": append(encodeCompositeSnapshot(uint64(1),
			[]byte{byte(PartMap)}, "a", sizedPayload(mapPayload)), 0),
	} {
		if got, err := LoadComposite(1, data); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: LoadComposite = %v, %v; want ErrMalformed", name, got, err)
		}
	}

	// The smallest things that must be accepted, so the table above is refusing
	// what it claims to refuse and not merely everything: a document of no parts,
	// and one of a single part of each kind.
	none, err := LoadComposite(1, encodeCompositeSnapshot(uint64(0)))
	if err != nil {
		t.Fatalf("a document of no parts was refused: %v", err)
	}
	assertParts(t, none)
	if !bytes.Equal(none.Snapshot(), encodeCompositeSnapshot(uint64(0))) {
		t.Fatal("the empty document does not re-encode to itself")
	}
	one := encodeCompositeSnapshot(uint64(3),
		[]byte{byte(PartText)}, "a", sizedPayload(textPayload),
		[]byte{byte(PartList)}, "a", sizedPayload(listPayload),
		[]byte{byte(PartMap)}, "a", sizedPayload(mapPayload))
	loaded, err := LoadComposite(1, one)
	if err != nil {
		t.Fatalf("a well-formed snapshot was refused: %v", err)
	}
	assertParts(t, loaded,
		Part{Kind: PartText, Name: "a"},
		Part{Kind: PartList, Name: "a"},
		Part{Kind: PartMap, Name: "a"})
	if !bytes.Equal(loaded.Snapshot(), one) {
		t.Fatal("a hand-built snapshot did not re-encode to itself")
	}
}

// The clock ceiling is held by the parts, because a composite has no clock: it
// is the parts that mint operations. A crafted part carries it into this loader
// and has to be refused there.
func TestLoadCompositeHoldsTheClockCeiling(t *testing.T) {
	// A map snapshot whose vector promises a sequence number past the ceiling.
	beyond := append([]byte{}, mapMagic[:]...)
	beyond = append(beyond, mapVersion)
	beyond = binary.AppendUvarint(beyond, 1)
	beyond = binary.AppendUvarint(beyond, 1)
	beyond = binary.AppendUvarint(beyond, MaxClock+1)
	beyond = binary.AppendUvarint(beyond, 0)
	if _, err := LoadMap(1, beyond); !errors.Is(err, ErrMalformed) {
		t.Fatalf("the control failed: LoadMap = %v, want ErrMalformed", err)
	}
	wrapped := encodeCompositeSnapshot(uint64(1), []byte{byte(PartMap)}, "cells", sizedPayload(beyond))
	if _, err := LoadComposite(1, wrapped); !errors.Is(err, ErrMalformed) {
		t.Fatalf("LoadComposite = %v, want ErrMalformed", err)
	}
}

// A part written by an older build still opens, and is written back in the
// current form. That is the one input this decoder accepts and does not return
// unchanged, and it is why the fuzzer asserts the fixed point on what it
// produced rather than on what it was given.
func TestLoadCompositeNormalisesAnOlderPart(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "ab"); err != nil {
		t.Fatal(err)
	}
	current := d.Snapshot()
	// Version 1 wrote one record per character; the loader still reads it.
	old := append([]byte{}, snapshotMagic[:]...)
	old = append(old, snapshotVersionV1)
	old = binary.AppendUvarint(old, 1)
	old = binary.AppendUvarint(old, 1)
	old = binary.AppendUvarint(old, 2)
	old = binary.AppendUvarint(old, 2)
	for i, ch := range []rune("ab") {
		originSite := uint64(0)
		if i > 0 {
			originSite = 1
		}
		old = binary.AppendUvarint(old, 1)           // site
		old = binary.AppendUvarint(old, uint64(i+1)) // seq
		old = binary.AppendUvarint(old, uint64(i+1)) // clock
		old = binary.AppendUvarint(old, originSite)  // origin site
		old = binary.AppendUvarint(old, uint64(i))   // origin seq
		old = binary.AppendUvarint(old, uint64(ch))  // character
		old = binary.AppendUvarint(old, 0)           // deletion site
		old = binary.AppendUvarint(old, 0)           // deletion seq
	}
	old = binary.AppendUvarint(old, 0)
	if bytes.Equal(old, current) {
		t.Fatal("the control failed: the two encodings are the same bytes")
	}

	wrapped := encodeCompositeSnapshot(uint64(1), []byte{byte(PartText)}, "file", sizedPayload(old))
	loaded, err := LoadComposite(2, wrapped)
	if err != nil {
		t.Fatalf("LoadComposite of a part in the older form: %v", err)
	}
	if got := textPart(t, loaded, "file").String(); got != "ab" {
		t.Fatalf("text = %q, want %q", got, "ab")
	}
	want := encodeCompositeSnapshot(uint64(1), []byte{byte(PartText)}, "file", sizedPayload(current))
	if !bytes.Equal(loaded.Snapshot(), want) {
		t.Fatal("an older part was not written back in the current form")
	}
	// And what it wrote back is a fixed point, which is the property that holds
	// for everything this package produces.
	again, err := LoadComposite(2, loaded.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Snapshot(), loaded.Snapshot()) {
		t.Fatal("re-encoding what the loader produced is not a fixed point")
	}
}

// Fuzzing

// compositeCorpus returns a snapshot and an encoded version from a small real
// session, to seed the fuzzers with input shaped like the real thing.
func compositeCorpus(t *testing.T) (snapshot, version []byte) {
	t.Helper()
	c := filledComposite(t, 1)
	peer := NewComposite(2)
	if err := peer.Apply(c.OpsSince(nil)...); err != nil {
		t.Fatal(err)
	}
	if _, err := mapPart(t, peer, "cells").Set("Z1", []byte("z")); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(peer.OpsSince(c.Version())...); err != nil {
		t.Fatal(err)
	}
	encoded, err := c.Version().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return c.Snapshot(), encoded
}

// A composite snapshot is what a server sends a joining client and what it
// persists, so its decoder reads bytes nobody here wrote.
func FuzzLoadComposite(f *testing.F) {
	snapshot, _ := compositeCorpus(&testing.T{})
	f.Add(snapshot)
	f.Add([]byte("crdtc\x01\x00"))
	f.Add([]byte("crdtc\x01"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		loaded, err := LoadComposite(1, data)
		if err != nil {
			return
		}
		// Nothing it accepts may hold an empty part: an empty part is one no
		// snapshot carries, so holding one would mean re-encoding lost it.
		for _, p := range loaded.Parts() {
			if len(loaded.snapshotOf(p)) == 0 {
				t.Fatalf("part %v encodes to nothing", p)
			}
		}
		// What it produced must be a fixed point. The input itself need not be:
		// a part in an older form is accepted and normalised.
		encoded := loaded.Snapshot()
		again, err := LoadComposite(1, encoded)
		if err != nil {
			t.Fatalf("a document could not reload its own snapshot: %v", err)
		}
		if !bytes.Equal(again.Snapshot(), encoded) {
			t.Fatal("re-encoding a loaded snapshot is not a fixed point")
		}
		// The version has to survive the wire, and the history has to replay into
		// a fresh replica and reproduce the state.
		v, err := loaded.Version().MarshalBinary()
		if err != nil {
			t.Fatalf("encoding a loaded document's version: %v", err)
		}
		var decoded CompositeVersion
		if err := decoded.UnmarshalBinary(v); err != nil {
			t.Fatalf("a loaded document's version does not decode: %v", err)
		}
		if !decoded.Equal(loaded.Version()) {
			t.Fatal("a version did not survive the wire")
		}
		replayed := NewComposite(2)
		if err := replayed.Apply(loaded.OpsSince(nil)...); err != nil {
			t.Fatalf("replaying a loaded document's history was rejected: %v", err)
		}
		if !bytes.Equal(replayed.Snapshot(), encoded) {
			t.Fatal("replaying the history did not reproduce the state")
		}
	})
}

// A version arrives from a peer asking to be caught up, so it too is decoded
// from bytes someone else wrote.
func FuzzCompositeVersion(f *testing.F) {
	_, version := compositeCorpus(&testing.T{})
	f.Add(version)
	f.Add([]byte{0, 0})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		var v CompositeVersion
		if err := v.UnmarshalBinary(data); err != nil {
			return
		}
		encoded, err := v.MarshalBinary()
		if err != nil {
			t.Fatalf("re-encoding an accepted version failed: %v", err)
		}
		var again CompositeVersion
		if err := again.UnmarshalBinary(encoded); err != nil {
			t.Fatalf("a re-encoded version no longer decodes: %v", err)
		}
		if !again.Equal(v) {
			t.Fatalf("round trip gave %v, want %v", again, v)
		}
		// What it encodes is a fixed point. The input itself need not be: this
		// asserted equality with data until the fuzzer produced "\x00\x80\x00",
		// where the second varint is an overlong encoding of zero that
		// binary.Uvarint accepts. Every decoder in this package inherits that from
		// reader.uvarint, so the guarantee is that what this package encodes
		// round-trips byte for byte, not that every accepted input does.
		// TestCompositeVersionNormalisesARedundantVarint pins the case.
		twice, err := again.MarshalBinary()
		if err != nil {
			t.Fatalf("re-encoding a decoded version failed: %v", err)
		}
		if !bytes.Equal(twice, encoded) {
			t.Fatalf("re-encoding a decoded version is not a fixed point:\n%x\n%x", twice, encoded)
		}
		// And whatever it decodes to must be usable as a peer's position.
		c := filledComposite(&testing.T{}, 1)
		_ = c.OpsSince(v)
	})
}
