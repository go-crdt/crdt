package crdt

import (
	"errors"
	"testing"
)

// A batch cut short in its part name is malformed, not addressed to an invalid
// part.
//
// decodePartOps reads the part's name before it can say which part the batch is
// for. A mutation sweep deleted the refusal that checks the read succeeded on
// 2026-10-08 and the suite stayed green, because the answer is still an error:
// the name comes back nil, which is the empty string, which validPartName
// refuses -- so the batch is reported as naming an invalid part.
//
// That is a different thing to tell the sender. ErrInvalidPart means "this
// names a structure no replica could hold", which is the sender's batch being
// wrong; ErrMalformed means the bytes did not survive the trip. The first says
// do not send this again, the second says send it again. Here the truth is the
// second, and the bytes are the only evidence either way.
func TestABatchCutShortInItsPartNameIsMalformed(t *testing.T) {
	c := NewComposite(1)
	text := textPart(t, c, "body")
	ops, err := text.Insert(0, "hi")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := AppendPartOps(nil, []PartOps{{Part: Part{Kind: PartText, Name: "body"}, Text: ops}})
	if err != nil {
		t.Fatal(err)
	}

	// The control, first: these bytes are a batch, so what follows is about the
	// truncation and not about the decoder refusing everything.
	if _, err := ParsePartOps(whole); err != nil {
		t.Fatalf("the control does not parse: %v", err)
	}

	// Every prefix that cannot hold the whole name. The name is four bytes
	// preceded by its length, so cutting anywhere in the first several bytes
	// stops inside the header rather than after it.
	for i := range len(whole) {
		_, err := ParsePartOps(whole[:i])
		if errors.Is(err, ErrInvalidPart) {
			t.Fatalf("the first %d bytes of %d were refused as an invalid part; "+
				"they are a truncated one, and the sender is told not to send it again", i, len(whole))
		}
		if err == nil {
			break // a prefix that is itself a whole batch: nothing to assert
		}
	}
}

// Four more refusals in composite.go survived the same sweep and are not pinned
// here, each because the answer is already made one layer down. The sweep's own
// report warns about exactly this shape, and these were read rather than
// assumed:
//
//	composite.go:1097 and :1101, the kind and the name inside the snapshot
//	loader's part loop. Each is unreachable while the other stands: with the
//	kind's refusal gone the name is read from the same exhausted buffer and
//	refuses first, and with the name's gone the name is nil, which is the empty
//	string, which no part may be named. The same pair as :483 and :487 in the
//	version decoder, for the same reason.
//
//	composite.go:1117, the part's payload. Deleting it passes nil to
//	adoptPart, which hands it to Load, LoadList or LoadMap -- and each of those
//	reads a magic number out of an empty buffer and returns ErrMalformed.
//
//	composite.go:930, mergeKeys' "a is empty" path. Deleting it merges against
//	an empty list, which is the same list by the longer route; the only
//	difference is that the result no longer shares its backing array with b,
//	which is the safer of the two.

// And three in list.go, named for the same reason:
//
//	list.go:829, the site count. Deleting it leaves nSites at zero, the loop
//	is skipped, and the reads after it fail on the same exhausted buffer.
//
//	list.go:861, the element count's bound. This one was checked on the
//	allocation channel as well as the suite's, because a bound of exactly this
//	shape in CompositeVersion.UnmarshalBinary turned four bytes into 134MB
//	once its guard was gone. Not here: nothing is sized from this count, the
//	loop under it calls adopt, and adopt fails on the first turn. A 14-byte
//	snapshot claiming 16777216 elements costs 608 bytes of allocation with the
//	bound and 608 without it.
//
//	list.go:664, duplicatesInOrder's "nothing has arrived since" path -- cost,
//	reaching the same value by the longer route.
//
// All of these were run under `mutate -expect-pass`, which succeeds only when
// the suite still passes. That is one channel and it is not the whole answer,
// which is why the bound above was measured on a second one.
