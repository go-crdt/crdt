package crdt

import (
	"testing"
	"unsafe"
)

// What a block costs, and which of Go's size classes it lands in.
//
// docs/performance.md records three numbers that went the wrong way when the
// index arrived — memory held, InsertAtStart, and applying a whole history —
// and says all three are the same number: the index costs bytes on every block,
// and the block crosses a size-class boundary because of them. This pins that
// arithmetic so it cannot drift unnoticed, and so anyone trying to shrink the
// block can see at a glance whether they have crossed back.
//
// It is not a limit anybody should tighten by shaving a field until it passes.
// The classes are 16 bytes apart up here, so the difference between 152 and 144
// is real and the difference between 152 and 150 is nothing at all.
func TestBlockFitsItsSizeClass(t *testing.T) {
	size := unsafe.Sizeof(block{})

	// Go's size classes in this range.
	classes := []uintptr{112, 128, 144, 160, 176, 192}
	var class uintptr
	for _, c := range classes {
		if size <= c {
			class = c
			break
		}
	}
	if class == 0 {
		t.Fatalf("a block is %d bytes, past every size class this test knows about", size)
	}

	t.Logf("a block is %d bytes and is allocated in the %d-byte class, wasting %d",
		size, class, class-size)

	// The tail is what decides the class. subVis and subSup fill the word after
	// subMin exactly; nsup and height spill past it and cost a whole further
	// word, which is what takes the block from 144 to 152 and so from the
	// 144-byte class to the 160-byte one — sixteen bytes a block, on every
	// block, for five bytes of summary.
	//
	// Neither is free to remove. height is an AVL height, structural rather than
	// a priority that could be derived from the identity; nsup could become a
	// single bit, but that alone leaves the block at 148 and so in the same
	// class. Reaching 144 means capping one of the counts, and a cap that is
	// wrong is silent corruption rather than a slow document.
	//
	// The answer to this is the B-tree with runs in its leaves that
	// docs/performance.md describes: no per-run node at all.
	const tail = unsafe.Sizeof(int32(0))*3 + 1 // subVis, subSup, nsup, height
	if tail <= 8 {
		t.Fatalf("the summary tail is %d bytes and now fits in one word: a block "+
			"should have dropped a size class, and this test should say so", tail)
	}

	if size > class {
		t.Fatalf("a block is %d bytes, past the %d-byte class", size, class)
	}
}
