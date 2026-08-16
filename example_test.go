package crdt_test

import (
	"fmt"

	"github.com/go-crdt/crdt"
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
