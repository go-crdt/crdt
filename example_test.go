package crdt_test

import (
	"fmt"

	"github.com/go-crdt/crdt"
	"github.com/go-crdt/crdt/awareness"
)

// Two people edit the same document at the same time, neither having seen the
// other's change. Once the two operations have crossed, both replicas hold the
// same text — with no server deciding anything.
func Example() {
	ada, grace := crdt.New(1), crdt.New(2)

	opening, err := ada.Insert(0, "the quick fox")
	if err != nil {
		panic(err)
	}
	if err := grace.Apply(opening...); err != nil {
		panic(err)
	}

	// Both edit at once, neither seeing the other.
	fromAda, err := ada.Insert(10, "brown ")
	if err != nil {
		panic(err)
	}
	fromGrace, err := grace.Insert(13, " jumps")
	if err != nil {
		panic(err)
	}

	if err := ada.Apply(fromGrace...); err != nil {
		panic(err)
	}
	if err := grace.Apply(fromAda...); err != nil {
		panic(err)
	}

	fmt.Println(ada)
	fmt.Println(grace)
	// Output:
	// the quick brown fox jumps
	// the quick brown fox jumps
}

// A replica joining an existing session is given a snapshot rather than the
// whole history, and can take part immediately.
func ExampleLoad() {
	server := crdt.New(1)
	if _, err := server.Insert(0, "shared draft"); err != nil {
		panic(err)
	}

	client, err := crdt.Load(2, server.Snapshot())
	if err != nil {
		panic(err)
	}
	edit, err := client.Insert(client.Len(), " — revised")
	if err != nil {
		panic(err)
	}
	if err := server.Apply(edit...); err != nil {
		panic(err)
	}

	fmt.Println(server)
	// Output: shared draft — revised
}

// A replica that has been away asks for what it missed by handing over its
// version vector; it is sent those operations and nothing else.
func ExampleDoc_OpsSince() {
	online, offline := crdt.New(1), crdt.New(2)
	start, err := online.Insert(0, "notes")
	if err != nil {
		panic(err)
	}
	if err := offline.Apply(start...); err != nil {
		panic(err)
	}

	// The offline replica misses everything that follows.
	if _, err := online.Insert(5, ": chapter one"); err != nil {
		panic(err)
	}
	if _, err := online.Delete(0, 1); err != nil {
		panic(err)
	}

	missed := online.OpsSince(offline.Version())
	fmt.Println(len(missed), "operations missed")
	if err := offline.Apply(missed...); err != nil {
		panic(err)
	}
	fmt.Println(offline)
	// Output:
	// 14 operations missed
	// otes: chapter one
}

// Site identities have to be distinct and cannot be drawn at random under
// js/wasm, so they are derived from something the caller already holds.
func ExampleDeriveSiteID() {
	first := crdt.DeriveSiteID([]byte("session-8f2c"))
	second := crdt.DeriveSiteID([]byte("session-8f2c"))
	fmt.Println(first == second, first == crdt.DeriveSiteID([]byte("session-91ab")))
	// Output: true false
}

// A browser's cursor offset counts UTF-16 code units, and an emoji is two of
// them. Handing that offset to Insert would put the text one place to the left
// of where the user asked for it, without an error and without a trace.
func ExampleDoc_InsertUTF16() {
	doc := crdt.New(1)
	if _, err := doc.Insert(0, "ship \U0001F680 it"); err != nil {
		panic(err)
	}

	// Nine characters, ten code units: the rocket is one and two.
	fmt.Println(doc.Len(), doc.LenUTF16())

	// The editor reports its caret just after the rocket, which is code unit
	// seven and character six.
	if _, err := doc.InsertUTF16(7, " now"); err != nil {
		panic(err)
	}
	fmt.Println(doc)

	// The offset between the rocket's two units names no position at all.
	_, err := doc.InsertUTF16(6, "x")
	fmt.Println(err)

	// Output:
	// 9 10
	// ship 🚀 now it
	// crdt: UTF-16 offset splits a surrogate pair
}

// Two replicas write to the same cell at the same time, and one of them then
// deletes it. The later write wins wherever the operations arrive, and the
// deleted cell keeps the clock that beat them: an older write turning up
// afterwards cannot bring it back.
func ExampleMap() {
	ada, grace := crdt.NewMap(1), crdt.NewMap(2)

	fromAda, err := ada.Set("B7", []byte("41"))
	if err != nil {
		panic(err)
	}
	fromGrace, err := grace.Set("B7", []byte("42"))
	if err != nil {
		panic(err)
	}
	if err := ada.Apply(fromGrace); err != nil {
		panic(err)
	}
	if err := grace.Apply(fromAda); err != nil {
		panic(err)
	}

	value, _ := ada.Get("B7")
	fmt.Printf("%s %s\n", value, ada.Keys())

	// Grace clears the cell, having seen both writes.
	cleared, err := grace.Delete("B7")
	if err != nil {
		panic(err)
	}
	if err := ada.Apply(cleared); err != nil {
		panic(err)
	}
	_, present := ada.Get("B7")
	fmt.Println(present, ada.Len())

	// Output:
	// 42 [B7]
	// false 0
}

// Awareness offsets are rune positions, because both peers have to agree what
// an offset means and an update has nowhere to say. A peer whose editor counts
// UTF-16 converts at its own edge — where it has to clamp in any case, since a
// cursor may describe a document longer than the one it now has.
func ExampleDoc_UTF16Offset() {
	doc := crdt.New(1)
	if _, err := doc.Insert(0, "\U0001F600 hello"); err != nil {
		panic(err)
	}

	peers := awareness.New()
	peers.Apply(awareness.Update{Site: 2, Clock: 1, Cursor: awareness.Cursor{Anchor: 2, Head: 99}})

	for _, peer := range peers.Peers() {
		head := min(max(peer.Cursor.Head, 0), doc.Len())
		at, err := doc.UTF16Offset(head)
		if err != nil {
			panic(err)
		}
		fmt.Println("caret at code unit", at)
	}
	// Output: caret at code unit 8
}
