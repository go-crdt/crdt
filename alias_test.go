package crdt

import (
	"bytes"
	"testing"
)

// An operation handed to Apply belongs to whoever handed it over. It may have
// been decoded into a buffer a transport reuses for the next message, or built
// and passed straight in. Whatever the replica keeps out of it, it must keep a
// copy of — otherwise the value changes under the structure with no operation
// having been applied, and the structure then disagrees with every replica that
// received the same operation. Nothing about that disagreement is repairable:
// no later operation says what the value should have been.
func TestApplyDoesNotAliasTheCallersValue(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		l, remote := NewList(1), NewList(2)
		ops, err := remote.Insert(0, []byte("hello"), []byte("world"))
		if err != nil {
			t.Fatal(err)
		}
		// Stand in for a transport that decodes into one buffer and reuses it.
		buffers := make([][]byte, len(ops))
		for i := range ops {
			buffers[i] = append([]byte(nil), ops[i].Value...)
			ops[i].Value = buffers[i]
		}
		if err := l.Apply(ops...); err != nil {
			t.Fatal(err)
		}
		for _, b := range buffers {
			copy(b, "XXXXX")
		}
		for i, want := range []string{"hello", "world"} {
			got, err := l.Get(i)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(want)) {
				t.Errorf("value %d is %q, want %q", i, got, want)
			}
		}
	})

	t.Run("map", func(t *testing.T) {
		m, remote := NewMap(1), NewMap(2)
		op, err := remote.Set("k", []byte("hello"))
		if err != nil {
			t.Fatal(err)
		}
		buf := append([]byte(nil), op.Value...)
		op.Value = buf
		if err := m.Apply(op); err != nil {
			t.Fatal(err)
		}
		copy(buf, "XXXXX")
		if got, ok := m.Get("k"); !ok || !bytes.Equal(got, []byte("hello")) {
			t.Errorf("value is %q, want %q", got, "hello")
		}
	})
}

// The same holds for what a replica hands back. An operation returned by an edit
// is broadcast, so a caller may hold it for a while; the value inside it must
// not be the one the structure is using.
func TestAnIssuedOperationDoesNotAliasWhatTheReplicaKeeps(t *testing.T) {
	l := NewList(1)
	ops, err := l.Insert(0, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	copy(ops[0].Value, "XXXXX")
	got, err := l.Get(0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("the list holds %q, want %q", got, "hello")
	}

	m := NewMap(1)
	op, err := m.Set("k", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	copy(op.Value, "XXXXX")
	if v, ok := m.Get("k"); !ok || !bytes.Equal(v, []byte("hello")) {
		t.Errorf("the map holds %q, want %q", v, "hello")
	}
}

// And what a loader keeps must not be a window on the snapshot it was given,
// which the caller may reuse or write to the moment the call returns.
func TestLoadingDoesNotAliasTheSnapshot(t *testing.T) {
	l := NewList(1)
	if _, err := l.Insert(0, []byte("hello"), []byte("world")); err != nil {
		t.Fatal(err)
	}
	snap := l.Snapshot()
	back, err := LoadList(2, snap)
	if err != nil {
		t.Fatal(err)
	}
	for i := range snap {
		snap[i] = 0xff
	}
	for i, want := range []string{"hello", "world"} {
		got, err := back.Get(i)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte(want)) {
			t.Errorf("value %d is %q, want %q", i, got, want)
		}
	}
}
