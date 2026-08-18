package structured

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/go-crdt/crdt"
)

func TestRecordMapFields(t *testing.T) {
	r := NewRecordMap(1)
	if _, err := r.SetField("node.1", "colour", []byte("red")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetField("node.1", "label", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetField("node.2", "colour", []byte("blue")); err != nil {
		t.Fatal(err)
	}
	if v, ok := r.GetField("node.1", "colour"); !ok || !bytes.Equal(v, []byte("red")) {
		t.Fatalf("GetField colour = %q,%v", v, ok)
	}
	if got, want := r.Fields("node.1"), []string{"colour", "label"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields = %v, want %v", got, want)
	}
	if got, want := r.Records(), []string{"node.1", "node.2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Records = %v, want %v", got, want)
	}
	if _, err := r.DeleteField("node.1", "label"); err != nil {
		t.Fatal(err)
	}
	if got, want := r.Fields("node.1"), []string{"colour"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after DeleteField, Fields = %v, want %v", got, want)
	}
}

func TestRecordMapDeleteRecord(t *testing.T) {
	r := NewRecordMap(1)
	for _, f := range []string{"a", "b", "c"} {
		if _, err := r.SetField("rec", f, []byte(f)); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := r.DeleteRecord("rec")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("DeleteRecord returned %d ops, want 3", len(ops))
	}
	if got := r.Records(); len(got) != 0 {
		t.Fatalf("after DeleteRecord, Records = %v, want none", got)
	}
	// Deleting an absent record is a no-op, not an error.
	if ops, err := r.DeleteRecord("gone"); err != nil || len(ops) != 0 {
		t.Fatalf("DeleteRecord(absent) = %v,%v", ops, err)
	}
}

// Two replicas change two different fields of the same record at once: because
// each field is its own register, both survive.
func TestRecordMapConcurrentDifferentFieldsBothSurvive(t *testing.T) {
	a, b := NewRecordMap(1), NewRecordMap(2)
	opA, err := a.SetField("node", "pos", []byte("1,2"))
	if err != nil {
		t.Fatal(err)
	}
	opB, err := b.SetField("node", "colour", []byte("green"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Map().Apply(opB); err != nil {
		t.Fatal(err)
	}
	if err := b.Map().Apply(opA); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*RecordMap{a, b} {
		pos, ok1 := r.GetField("node", "pos")
		colour, ok2 := r.GetField("node", "colour")
		if !ok1 || !ok2 || !bytes.Equal(pos, []byte("1,2")) || !bytes.Equal(colour, []byte("green")) {
			t.Fatalf("a concurrent two-field edit lost one: pos=%q,%v colour=%q,%v", pos, ok1, colour, ok2)
		}
	}
	if !bytes.Equal(a.Map().Snapshot(), b.Map().Snapshot()) {
		t.Fatalf("record maps diverged")
	}
}

// A field write concurrent with a record delete is resolved per field by
// (clock, site): the write that happened later, by that order, is what remains.
func TestRecordMapDeleteVersusSet(t *testing.T) {
	a, b := NewRecordMap(1), NewRecordMap(2)
	seed, err := a.SetField("node", "label", []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Map().Apply(seed); err != nil {
		t.Fatal(err)
	}
	// a deletes the record; b, not having seen the delete, rewrites the field.
	del, err := a.DeleteRecord("node")
	if err != nil {
		t.Fatal(err)
	}
	set, err := b.SetField("node", "label", []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Map().Apply(set); err != nil {
		t.Fatal(err)
	}
	if err := b.Map().Apply(del...); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Map().Snapshot(), b.Map().Snapshot()) {
		t.Fatalf("delete-versus-set diverged")
	}
	// b's set has the higher clock (it saw the seed, so its clock advanced past
	// it), so the record survives with the new value on both replicas.
	for _, r := range []*RecordMap{a, b} {
		if v, ok := r.GetField("node", "label"); !ok || !bytes.Equal(v, []byte("new")) {
			t.Fatalf("expected the later write to win: got %q,%v", v, ok)
		}
	}
}

// A key an operation injected that fieldKey could not have produced is not a
// record: Records and Fields skip it rather than mistaking it for one.
func TestRecordMapIgnoresForeignKeys(t *testing.T) {
	r := NewRecordMap(1)
	if _, err := r.SetField("rec", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	// "nope" has no length-prefix separator, so splitFieldKey refuses it.
	foreign := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: 1, Key: "nope", Value: []byte("x")}
	if err := r.Map().Apply(foreign); err != nil {
		t.Fatal(err)
	}
	if got, want := r.Records(), []string{"rec"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Records = %v, want %v (foreign key must be skipped)", got, want)
	}
	if got, want := r.Fields("rec"), []string{"f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields = %v, want %v", got, want)
	}
}

func TestRecordMapDeleteRecordExhausted(t *testing.T) {
	r := NewRecordMap(1)
	if _, err := r.SetField("rec", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	// Raise the map's Lamport clock to the ceiling from a peer, leaving this
	// replica no room to mint the deletion.
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: fieldKey("other", "g"), Value: []byte("x")}
	if err := r.Map().Apply(top); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DeleteRecord("rec"); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("DeleteRecord with no clock left = %v, want ErrExhausted", err)
	}
}

func TestRecordsOfSharesStorage(t *testing.T) {
	m := crdt.NewMap(1)
	r := RecordsOf(m)
	if _, err := r.SetField("rec", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	// The write is visible through the underlying map, because they are one store.
	if v, ok := m.Get(fieldKey("rec", "f")); !ok || !bytes.Equal(v, []byte("v")) {
		t.Fatalf("RecordsOf did not share storage: %q,%v", v, ok)
	}
	if r.Map() != m {
		t.Fatal("Map() returned a different map than the one viewed")
	}
}
