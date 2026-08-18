package structured

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/go-crdt/crdt"
)

func applyDocument(t *testing.T, d *Document, batches ...crdt.PartOps) {
	t.Helper()
	if err := d.Apply(batches...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// exhaust drives one family's map part to the Lamport ceiling from a peer, so the
// next local write to that family has no clock left to mint.
func exhaust(t *testing.T, d *Document, fam Family) {
	t.Helper()
	op := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: "seed"}
	applyDocument(t, d, partOf(fam, op))
}

func TestFamilyValid(t *testing.T) {
	for _, f := range families {
		if !f.valid() {
			t.Fatalf("family %q reported invalid", f)
		}
	}
	if len(families) != 5 {
		t.Fatalf("families has %d entries, want the 5 of the model", len(families))
	}
	if Family("nope").valid() {
		t.Fatal("an invented family reported valid")
	}
}

func TestDocumentAddHasIDsRemove(t *testing.T) {
	d := NewDocument(1)
	if d.Site() != 1 {
		t.Fatalf("Site = %d", d.Site())
	}
	// An entity created with no fields is still present.
	if _, err := d.Add(Nodes, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Add(Nodes, "b"); err != nil {
		t.Fatal(err)
	}
	if !d.Has(Nodes, "a") || !d.Has(Nodes, "b") {
		t.Fatal("a freshly added entity is not present")
	}
	if d.Has(Nodes, "missing") {
		t.Fatal("an entity never added is present")
	}
	if got, want := d.IDs(Nodes), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
	// Families do not leak into one another: a node is not a zone.
	if d.Has(Zones, "a") {
		t.Fatal("an id added to Nodes shows in Zones")
	}
	if got := d.IDs(Zones); len(got) != 0 {
		t.Fatalf("IDs(Zones) = %v, want none", got)
	}
	// Remove clears it; a second Remove is refused.
	if _, err := d.Remove(Nodes, "a"); err != nil {
		t.Fatal(err)
	}
	if d.Has(Nodes, "a") {
		t.Fatal("Has after Remove = true")
	}
	if got, want := d.IDs(Nodes), []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs after Remove = %v, want %v", got, want)
	}
	if _, err := d.Remove(Nodes, "a"); !errors.Is(err, ErrUnknownEntity) {
		t.Fatalf("Remove(already removed) = %v, want ErrUnknownEntity", err)
	}
}

func TestDocumentAddIsIdempotentAcrossReplicas(t *testing.T) {
	// The point of a caller-chosen id: two replicas that each create "shape-7"
	// converge on one entity, not two.
	a, b := NewDocument(1), NewDocument(2)
	fromA, err := a.Add(Zones, "shape-7")
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.Add(Zones, "shape-7")
	if err != nil {
		t.Fatal(err)
	}
	applyDocument(t, a, fromB)
	applyDocument(t, b, fromA)
	for _, d := range []*Document{a, b} {
		if got, want := d.IDs(Zones), []string{"shape-7"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("IDs = %v, want a single converged entity %v", got, want)
		}
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("two replicas creating the same id diverged")
	}
}

func TestDocumentFields(t *testing.T) {
	d := NewDocument(1)
	if _, err := d.Set(TextBoxes, "t1", "text", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Set(TextBoxes, "t1", "size", EncodeInt(14)); err != nil {
		t.Fatal(err)
	}
	// Set creates the entity without a prior Add.
	if !d.Has(TextBoxes, "t1") {
		t.Fatal("Set did not bring the entity into existence")
	}
	if v, ok := d.Field(TextBoxes, "t1", "text"); !ok || string(v) != "hello" {
		t.Fatalf("Field text = %q,%v", v, ok)
	}
	if v, ok := d.Field(TextBoxes, "t1", "size"); !ok {
		t.Fatalf("Field size ok=%v", ok)
	} else if n, ok := DecodeInt(v); !ok || n != 14 {
		t.Fatalf("DecodeInt(size) = %d,%v", n, ok)
	}
	// An unset field reads absent.
	if _, ok := d.Field(TextBoxes, "t1", "colour"); ok {
		t.Fatal("an unset field reads present")
	}
	// Fields lists exactly the caller's names, ascending, presence marker hidden.
	if got, want := d.Fields(TextBoxes, "t1"), []string{"size", "text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields = %v, want %v", got, want)
	}
	// After an Add, the presence marker still does not show among the fields.
	if _, err := d.Add(TextBoxes, "t1"); err != nil {
		t.Fatal(err)
	}
	if got, want := d.Fields(TextBoxes, "t1"), []string{"size", "text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields after Add = %v, want %v (presence hidden)", got, want)
	}
	// DeleteField removes one and leaves the rest.
	if _, err := d.DeleteField(TextBoxes, "t1", "size"); err != nil {
		t.Fatal(err)
	}
	if got, want := d.Fields(TextBoxes, "t1"), []string{"text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields after DeleteField = %v, want %v", got, want)
	}
	// Deleting an absent field is a no-op, not an error.
	if _, err := d.DeleteField(TextBoxes, "t1", "gone"); err != nil {
		t.Fatalf("DeleteField(absent) = %v", err)
	}
}

// A foreign key a peer injects into a family's map — one no caller field or
// presence marker would produce — is not mistaken for a field.
func TestDocumentFieldsIgnoresForeignKey(t *testing.T) {
	d := NewDocument(1)
	if _, err := d.Set(Layers, "base", "name", []byte("Base")); err != nil {
		t.Fatal(err)
	}
	// "raw" is a well-formed record field but carries no user prefix.
	foreign := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: 1, Key: fieldKey("base", "raw"), Value: []byte("x")}
	applyDocument(t, d, partOf(Layers, foreign))
	if got, want := d.Fields(Layers, "base"), []string{"name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields = %v, want %v (foreign key hidden)", got, want)
	}
}

func TestDocumentConcurrentDifferentFieldsBothSurvive(t *testing.T) {
	a, b := NewDocument(1), NewDocument(2)
	// Both know the entity exists first.
	seed, err := a.Add(Nodes, "n")
	if err != nil {
		t.Fatal(err)
	}
	applyDocument(t, b, seed)
	moveX, err := a.Set(Nodes, "n", "x", EncodeInt(3))
	if err != nil {
		t.Fatal(err)
	}
	colour, err := b.Set(Nodes, "n", "color", []byte("#f00"))
	if err != nil {
		t.Fatal(err)
	}
	applyDocument(t, a, colour)
	applyDocument(t, b, moveX)
	for _, d := range []*Document{a, b} {
		x, ok1 := d.Field(Nodes, "n", "x")
		col, ok2 := d.Field(Nodes, "n", "color")
		if !ok1 || !ok2 {
			t.Fatalf("a concurrent two-field edit lost one: x=%v color=%v", ok1, ok2)
		}
		if n, ok := DecodeInt(x); !ok || n != 3 {
			t.Fatalf("x = %d,%v", n, ok)
		}
		if string(col) != "#f00" {
			t.Fatalf("color = %q", col)
		}
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("documents diverged")
	}
}

func TestDocumentUnknownFamily(t *testing.T) {
	d := NewDocument(1)
	bad := Family("bogus")
	if _, err := d.Add(bad, "x"); !errors.Is(err, ErrUnknownFamily) {
		t.Fatalf("Add(bad) = %v, want ErrUnknownFamily", err)
	}
	if _, err := d.Set(bad, "x", "f", nil); !errors.Is(err, ErrUnknownFamily) {
		t.Fatalf("Set(bad) = %v, want ErrUnknownFamily", err)
	}
	if _, err := d.DeleteField(bad, "x", "f"); !errors.Is(err, ErrUnknownFamily) {
		t.Fatalf("DeleteField(bad) = %v, want ErrUnknownFamily", err)
	}
	if _, err := d.Remove(bad, "x"); !errors.Is(err, ErrUnknownFamily) {
		t.Fatalf("Remove(bad) = %v, want ErrUnknownFamily", err)
	}
	// The read side answers "nothing" rather than erroring.
	if d.Has(bad, "x") {
		t.Fatal("Has(bad) = true")
	}
	if got := d.IDs(bad); got != nil {
		t.Fatalf("IDs(bad) = %v, want nil", got)
	}
	if _, ok := d.Field(bad, "x", "f"); ok {
		t.Fatal("Field(bad) ok = true")
	}
	if got := d.Fields(bad, "x"); got != nil {
		t.Fatalf("Fields(bad) = %v, want nil", got)
	}
}

func TestDocumentInvalidID(t *testing.T) {
	d := NewDocument(1)
	for _, id := range []string{"", "\xff\xfe"} {
		if _, err := d.Add(Nodes, id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Add(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := d.Set(Nodes, id, "f", nil); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Set(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := d.DeleteField(Nodes, id, "f"); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("DeleteField(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := d.Remove(Nodes, id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Remove(%q) = %v, want ErrInvalidID", id, err)
		}
	}
	// The read side treats the empty id as simply absent.
	if d.Has(Nodes, "") {
		t.Fatal("Has(empty) = true")
	}
	if _, ok := d.Field(Nodes, "", "f"); ok {
		t.Fatal("Field(empty) ok = true")
	}
	if got := d.Fields(Nodes, ""); got != nil {
		t.Fatalf("Fields(empty) = %v, want nil", got)
	}
}

func TestDocumentEditsRefuseWhenClockExhausted(t *testing.T) {
	// Add.
	d := NewDocument(1)
	exhaust(t, d, Nodes)
	if _, err := d.Add(Nodes, "n"); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("Add = %v, want ErrExhausted", err)
	}
	// Set.
	d = NewDocument(1)
	exhaust(t, d, Zones)
	if _, err := d.Set(Zones, "z", "w", EncodeInt(4)); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("Set = %v, want ErrExhausted", err)
	}
	// DeleteField.
	d = NewDocument(1)
	if _, err := d.Set(Connectors, "c", "style", []byte("solid")); err != nil {
		t.Fatal(err)
	}
	applyDocument(t, d, partOf(Connectors, crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: "seed"}))
	if _, err := d.DeleteField(Connectors, "c", "style"); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("DeleteField = %v, want ErrExhausted", err)
	}
	// Remove: the entity is present, but there is no clock left to tombstone it.
	d = NewDocument(1)
	if _, err := d.Add(Layers, "base"); err != nil {
		t.Fatal(err)
	}
	applyDocument(t, d, partOf(Layers, crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: "seed"}))
	if _, err := d.Remove(Layers, "base"); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("Remove = %v, want ErrExhausted", err)
	}
}

func TestDocumentTransportAndSnapshotRoundTrip(t *testing.T) {
	d := NewDocument(1)
	if _, err := d.Add(Nodes, "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Set(Zones, "z", "label", []byte("Region")); err != nil {
		t.Fatal(err)
	}
	// A late joiner built from OpsSince(nil) reaches the same state.
	joiner := NewDocument(2)
	applyDocument(t, joiner, d.OpsSince(nil)...)
	if joiner.Pending() != 0 {
		t.Fatalf("joiner has %d pending", joiner.Pending())
	}
	if !bytes.Equal(joiner.Snapshot(), d.Snapshot()) {
		t.Fatal("OpsSince(nil) did not reproduce the document")
	}
	if !d.Version().Equal(joiner.Version()) {
		t.Fatal("versions differ after full sync")
	}
	// A snapshot reloads to itself.
	loaded, err := LoadDocument(3, d.Snapshot())
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if !bytes.Equal(loaded.Snapshot(), d.Snapshot()) {
		t.Fatal("a document snapshot did not reload to itself")
	}
	if _, err := LoadDocument(1, []byte("garbage")); err == nil {
		t.Fatal("LoadDocument accepted garbage")
	}
}

func TestEncodeInt(t *testing.T) {
	for _, v := range []int32{0, 1, -1, 42, -42, math.MinInt32, math.MaxInt32} {
		got, ok := DecodeInt(EncodeInt(v))
		if !ok || got != v {
			t.Fatalf("DecodeInt(EncodeInt(%d)) = %d,%v", v, got, ok)
		}
	}
}

func TestDecodeIntRejectsMalformed(t *testing.T) {
	tooBig := binary.AppendVarint(nil, 1<<40)
	cases := [][]byte{
		{},                         // empty
		EncodeInt(1234567)[:1],     // truncated (multi-byte varint cut short)
		append(EncodeInt(5), 0x00), // trailing byte after a complete varint
		tooBig,                     // a value outside int32
	}
	for i, b := range cases {
		if v, ok := DecodeInt(b); ok {
			t.Fatalf("case %d: DecodeInt(%x) = %d,true; want refused", i, b, v)
		}
	}
}

func TestEncodeBool(t *testing.T) {
	for _, v := range []bool{true, false} {
		got, ok := DecodeBool(EncodeBool(v))
		if !ok || got != v {
			t.Fatalf("DecodeBool(EncodeBool(%v)) = %v,%v", v, got, ok)
		}
	}
}

func TestDecodeBoolRejectsMalformed(t *testing.T) {
	for i, b := range [][]byte{{}, {0, 1}, {2}, {0xff}} {
		if v, ok := DecodeBool(b); ok {
			t.Fatalf("case %d: DecodeBool(%x) = %v,true; want refused", i, b, v)
		}
	}
}

func FuzzDecodeInt(f *testing.F) {
	f.Add([]byte{0})
	f.Add(EncodeInt(-123456))
	f.Fuzz(func(t *testing.T, data []byte) {
		if v, ok := DecodeInt(data); ok {
			if v2, ok2 := DecodeInt(EncodeInt(v)); !ok2 || v2 != v {
				t.Fatalf("DecodeInt(%x)=%d did not round-trip", data, v)
			}
		}
	})
}

func FuzzDecodeBool(f *testing.F) {
	f.Add([]byte{1})
	f.Add([]byte{2})
	f.Fuzz(func(t *testing.T, data []byte) {
		if v, ok := DecodeBool(data); ok {
			if v2, ok2 := DecodeBool(EncodeBool(v)); !ok2 || v2 != v {
				t.Fatalf("DecodeBool(%x)=%v did not round-trip", data, v)
			}
		}
	})
}
