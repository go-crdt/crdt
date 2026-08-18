package structured

import (
	"bytes"
	"testing"
)

func TestRegisterSetGetClear(t *testing.T) {
	r := NewRegister(1)
	if v, ok := r.Get(); ok {
		t.Fatalf("a fresh register read %q, want absent", v)
	}
	if _, err := r.Set([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if v, ok := r.Get(); !ok || !bytes.Equal(v, []byte("hello")) {
		t.Fatalf("Get = %q,%v", v, ok)
	}
	if _, err := r.Clear(); err != nil {
		t.Fatal(err)
	}
	if v, ok := r.Get(); ok {
		t.Fatalf("after Clear, Get = %q,%v; want absent", v, ok)
	}
}

// Two replicas write concurrently; the (clock, site) order picks one winner, and
// both replicas agree on it whatever order the writes arrive in.
func TestRegisterConcurrentWriteConverges(t *testing.T) {
	a, b := NewRegister(1), NewRegister(2)
	opA, err := a.Set([]byte("from-a"))
	if err != nil {
		t.Fatal(err)
	}
	opB, err := b.Set([]byte("from-b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(opB); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(opA); err != nil {
		t.Fatal(err)
	}
	va, _ := a.Get()
	vb, _ := b.Get()
	if !bytes.Equal(va, vb) {
		t.Fatalf("diverged: a=%q b=%q", va, vb)
	}
	// Both minted clock 1, so the tie breaks to the higher site: site 2.
	if !bytes.Equal(va, []byte("from-b")) {
		t.Fatalf("winner = %q, want the higher site's write", va)
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatalf("snapshots differ after convergence")
	}
}

func TestRegisterSnapshotRoundTrip(t *testing.T) {
	r := NewRegister(1)
	if _, err := r.Set([]byte("v")); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRegister(2, r.Snapshot())
	if err != nil {
		t.Fatalf("LoadRegister: %v", err)
	}
	if !bytes.Equal(loaded.Snapshot(), r.Snapshot()) {
		t.Fatalf("a register snapshot did not reload to itself")
	}
	// The loaded replica keeps editing and stays in step.
	op, err := loaded.Set([]byte("w"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(op); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.Snapshot(), loaded.Snapshot()) {
		t.Fatalf("registers diverged after a post-load edit")
	}
}

func TestRegisterOpsSinceAndVersion(t *testing.T) {
	r := NewRegister(1)
	if v := r.Version(); v.Get(1) != 0 {
		t.Fatalf("fresh register version = %v", v)
	}
	if _, err := r.Set([]byte("v")); err != nil {
		t.Fatal(err)
	}
	ops := r.OpsSince(nil)
	if len(ops) != 1 {
		t.Fatalf("OpsSince(nil) = %d ops, want 1", len(ops))
	}
	if none := r.OpsSince(r.Version()); len(none) != 0 {
		t.Fatalf("OpsSince(current) = %d ops, want 0", len(none))
	}
}

func TestLoadRegisterRejectsMalformed(t *testing.T) {
	if _, err := LoadRegister(1, []byte("not a snapshot")); err == nil {
		t.Fatal("LoadRegister accepted garbage")
	}
}
