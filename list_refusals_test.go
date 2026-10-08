package crdt

import (
	"errors"
	"testing"
)

// Two refusals in list.go survived a mutation sweep on 2026-10-08 -- deleted,
// the whole suite stayed green. Neither is a bug; both are decisions nothing
// was holding, and they are held here for opposite reasons.

// An operation naming a kind this build does not know is invalid, not malformed,
// whatever follows the kind byte.
//
// Deleting the refusal does not make the operation acceptable: the unknown kind
// falls through to the branch that reads a target, and op.validate() refuses it
// at the end. The answer only changes when the bytes run out first, and then it
// changes from ErrInvalidOp to ErrMalformed -- measured on 0, 1 and 4 bytes
// after the kind byte, all three ErrInvalidOp as written and all three
// ErrMalformed with the refusal gone.
//
// That difference is the whole point of having two errors. ErrInvalidOp says a
// peer sent an operation this build does not know, which is a dialect; the kind
// byte is the one field that can say so, and it says so before anything after it
// is read. ErrMalformed says the bytes are damaged, which is a transport. collab
// maps them to different gRPC codes, so a peer told the wrong one is told to do
// the wrong thing about it.
func TestAnUnknownListOpKindIsInvalidRatherThanMalformed(t *testing.T) {
	// A real operation, encoded, with the batch's count varint taken off: this
	// is one operation's bytes, exactly as decodeListOp expects them.
	src := NewList(1)
	ops, err := src.Insert(0, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := AppendListOps(nil, ops)
	if err != nil {
		t.Fatal(err)
	}
	one := batch[1:]

	// The control, and it has to come first: if these bytes did not decode, a
	// decoder that refused everything would pass the assertions below.
	if _, _, err := decodeListOp(one); err != nil {
		t.Fatalf("the control does not decode: %v", err)
	}

	// The same bytes, with only the kind byte changed to one this build does
	// not know. Everything after it is well formed, so nothing else can be the
	// reason for the refusal.
	unknown := append([]byte(nil), one...)
	unknown[0] = 9
	if _, _, err := decodeListOp(unknown); !errors.Is(err, ErrInvalidOp) {
		t.Errorf("a kind this build does not know, followed by a well-formed operation, gave %v, want ErrInvalidOp", err)
	}

	// And with the bytes cut short, which is where deleting the refusal changes
	// the answer: ErrMalformed, as though the transport were at fault.
	for _, after := range []int{0, 1, 4} {
		data := append([]byte{9}, one[1:1+after]...)
		if _, _, err := decodeListOp(data); !errors.Is(err, ErrInvalidOp) {
			t.Errorf("a kind this build does not know, with %d bytes after it, gave %v, want ErrInvalidOp", after, err)
		}
	}
}

// Apply does not copy the version vector, because it does not need to.
//
// applyWith takes that copy to answer "did anything change", which is what
// ApplyChanges returns and Apply discards -- so the refusal to take it on the
// Apply path is a cost decision, and deleting it changes no answer anywhere.
// Nothing asserted about a RESULT can see it. This measures the work instead:
// zero allocations as written, three with the refusal gone.
//
// The vector has fifty sites for a reason. At one site the same measurement
// reads zero either way: make(VersionVector, 1) is small enough that the
// compiler keeps the clone on the stack, and a test written that way could not
// have failed.
func TestApplyDoesNotCopyTheVersionVector(t *testing.T) {
	var ops []ListOp
	for s := 1; s <= 50; s++ {
		src := NewList(SiteID(s))
		o, err := src.Insert(0, []byte("v"))
		if err != nil {
			t.Fatal(err)
		}
		ops = append(ops, o...)
	}
	dst := NewList(999)
	if err := dst.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	if len(dst.vv) < 50 {
		t.Fatalf("the vector holds %d sites, too few for a clone of it to leave the stack", len(dst.vv))
	}

	// A duplicate: admit returns at the first check, so everything measured
	// here is what applyWith itself spends.
	dup := ops[len(ops)-1]
	if n := testing.AllocsPerRun(200, func() { _ = dst.Apply(dup) }); n != 0 {
		t.Errorf("Apply allocated %v times per call, want 0 -- the vector is being copied for an answer Apply throws away", n)
	}
}
