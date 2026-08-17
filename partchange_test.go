package crdt

import (
	"bytes"
	"reflect"
	"slices"
	"testing"
)

// A view of a document is told what changed rather than asked to look. What it
// needs told differs by kind, and these tests are that difference: a text editor
// cannot re-read a document per keystroke and keep a cursor, a view of a map
// reads back the keys it hears about, and a view of a list reads the list.

// sessionOps returns what a peer would send after editing three kinds of part.
func sessionOps(t *testing.T) []PartOps {
	t.Helper()
	peer := NewComposite(1)
	body, err := peer.Text("file:main.tex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := body.Insert(0, "bonjour"); err != nil {
		t.Fatal(err)
	}
	chat, err := peer.List("chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Insert(0, []byte("salut")); err != nil {
		t.Fatal(err)
	}
	cells, err := peer.Map("cells")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"B7", "A1"} {
		if _, err := cells.Set(key, []byte("42")); err != nil {
			t.Fatal(err)
		}
	}
	return peer.OpsSince(nil)
}

func TestApplyChangesReportsWhatEachKindNeeds(t *testing.T) {
	c := NewComposite(2)
	changes, err := c.ApplyChanges(sessionOps(t)...)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("reported %d parts, want 3: %+v", len(changes), changes)
	}
	// Canonical part order: kinds ascend, so text, then list, then map.
	wantParts := []Part{
		{Kind: PartText, Name: "file:main.tex"},
		{Kind: PartList, Name: "chat"},
		{Kind: PartMap, Name: "cells"},
	}
	for i, want := range wantParts {
		if changes[i].Part != want {
			t.Fatalf("change %d is for %v, want %v", i, changes[i].Part, want)
		}
	}

	// The text says what to do, and doing it reproduces the text.
	text := changes[0].Text
	if len(text) == 0 {
		t.Fatal("the text part reported no edits")
	}
	var view string
	for _, ch := range text {
		view = view[:ch.Pos] + ch.Text + view[ch.Pos+ch.Removed:]
	}
	body, err := c.Text("file:main.tex")
	if err != nil {
		t.Fatal(err)
	}
	if view != body.String() {
		t.Fatalf("applying the reported edits gave %q, the document holds %q", view, body.String())
	}

	// The list says only that it moved.
	if changes[1].Text != nil || changes[1].Keys != nil {
		t.Fatalf("the list reported %+v, want the part alone", changes[1])
	}

	// The map names its keys, ascending, each once.
	if got, want := changes[2].Keys, []string{"A1", "B7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the map reported %q, want %q", got, want)
	}
}

// Only what happened is reported, which is what stops a view redrawing on every
// message a transport happens to repeat.
func TestApplyChangesReportsNothingForOperationsThatDoNothing(t *testing.T) {
	ops := sessionOps(t)
	c := NewComposite(2)
	if _, err := c.ApplyChanges(ops...); err != nil {
		t.Fatal(err)
	}
	again, err := c.ApplyChanges(ops...)
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("applying the same operations twice reported %+v", again)
	}
	// A batch for a part that does nothing produces no change for that part
	// either, rather than an empty one.
	empty, err := c.ApplyChanges(PartOps{Part: Part{Kind: PartMap, Name: "cells"}})
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Fatalf("an empty batch reported %+v", empty)
	}
}

// An operation waiting for the one its site issued before it changes nothing and
// says nothing — and says it when it lands, not before.
func TestApplyChangesReportsAParkedOperationWhenItLands(t *testing.T) {
	ops := sessionOps(t)
	var mapBatch PartOps
	for _, b := range ops {
		if b.Part.Kind == PartMap {
			mapBatch = b
		}
	}
	if len(mapBatch.Map) < 2 {
		t.Fatalf("expected at least two map operations, got %d", len(mapBatch.Map))
	}
	first, rest := mapBatch.Map[0], mapBatch.Map[1:]

	c := NewComposite(2)
	held, err := c.ApplyChanges(PartOps{Part: mapBatch.Part, Map: rest})
	if err != nil {
		t.Fatal(err)
	}
	if held != nil {
		t.Fatalf("operations waiting on a predecessor reported %+v", held)
	}
	landed, err := c.ApplyChanges(PartOps{Part: mapBatch.Part, Map: []MapOp{first}})
	if err != nil {
		t.Fatal(err)
	}
	if len(landed) != 1 || len(landed[0].Keys) != 2 {
		t.Fatalf("the predecessor landing reported %+v, want both keys", landed)
	}
}

// Two batches for one part add up to one account of it, with the keys unioned
// and still ascending — a view is told about a key once however it was sent.
func TestApplyChangesFoldsTwoBatchesForOnePart(t *testing.T) {
	peer := NewComposite(1)
	cells, err := peer.Map("cells")
	if err != nil {
		t.Fatal(err)
	}
	var first, second []MapOp
	for _, key := range []string{"C3", "A1"} {
		op, err := cells.Set(key, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, op)
	}
	for _, key := range []string{"B2", "A1"} {
		op, err := cells.Set(key, []byte("y"))
		if err != nil {
			t.Fatal(err)
		}
		second = append(second, op)
	}

	part := Part{Kind: PartMap, Name: "cells"}
	c := NewComposite(2)
	changes, err := c.ApplyChanges(
		PartOps{Part: part, Map: first},
		PartOps{Part: part, Map: second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("two batches for one part reported %d changes", len(changes))
	}
	if got, want := changes[0].Keys, []string{"A1", "B2", "C3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys are %q, want %q", got, want)
	}
	if !slices.IsSorted(changes[0].Keys) {
		t.Error("the keys are not ascending")
	}
}

// Two text batches for one part are reported as one run of edits, in the order a
// view has to make them.
func TestApplyChangesConcatenatesTextBatches(t *testing.T) {
	peer := NewComposite(1)
	body, err := peer.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	firstOps, err := body.Insert(0, "monde")
	if err != nil {
		t.Fatal(err)
	}
	secondOps, err := body.Insert(0, "bonjour ")
	if err != nil {
		t.Fatal(err)
	}

	part := Part{Kind: PartText, Name: "body"}
	c := NewComposite(2)
	changes, err := c.ApplyChanges(
		PartOps{Part: part, Text: firstOps},
		PartOps{Part: part, Text: secondOps},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("two batches for one part reported %d changes", len(changes))
	}
	var view string
	for _, ch := range changes[0].Text {
		view = view[:ch.Pos] + ch.Text + view[ch.Pos+ch.Removed:]
	}
	if want := "bonjour monde"; view != want {
		t.Fatalf("the reported edits build %q, want %q", view, want)
	}
}

// Reporting must not change what applying does. Whatever the batches, a replica
// that watched and one that did not hold the same document.
func TestWatchingDoesNotChangeTheDocument(t *testing.T) {
	ops := sessionOps(t)
	watched, plain := NewComposite(2), NewComposite(2)
	if _, err := watched.ApplyChanges(ops...); err != nil {
		t.Fatal(err)
	}
	if err := plain.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(watched.Snapshot(), plain.Snapshot()) {
		t.Fatal("watching changed what was applied")
	}
	// And the collector is not left behind to gather a later batch's keys.
	cells, err := watched.Map("cells")
	if err != nil {
		t.Fatal(err)
	}
	if cells.touched != nil {
		t.Error("the key collector outlived the call that set it")
	}
}

// A batch the composite would refuse is refused before anything is applied, and
// reports what Apply would have reported.
func TestApplyChangesRejectsABadBatchWholesale(t *testing.T) {
	ops := sessionOps(t)
	c := NewComposite(2)
	bad := PartOps{Part: Part{Kind: PartMap, Name: ""}}
	changes, err := c.ApplyChanges(append(append([]PartOps{}, ops...), bad)...)
	if err == nil {
		t.Fatal("a batch naming no part was accepted")
	}
	if changes != nil {
		t.Fatalf("a rejected batch reported %+v", changes)
	}
	if len(c.Parts()) != 0 {
		t.Fatalf("a rejected batch applied %d parts", len(c.Parts()))
	}
}

// The two halves under the composite, on their own terms.
func TestMapAndListReportTheirOwnChanges(t *testing.T) {
	peer := NewMap(1)
	set, err := peer.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := peer.Delete("j")
	if err != nil {
		t.Fatal(err)
	}
	m := NewMap(2)
	keys, err := m.ApplyChanges(set, gone)
	if err != nil {
		t.Fatal(err)
	}
	// A deletion changes a key's presence, so it is reported like a write.
	if want := []string{"j", "k"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys are %q, want %q", keys, want)
	}
	if keys, err := m.ApplyChanges(set, gone); err != nil || keys != nil {
		t.Fatalf("applying twice reported %q, %v", keys, err)
	}
	if _, err := m.ApplyChanges(MapOp{}); err == nil {
		t.Error("an invalid operation was accepted")
	}

	// A write that loses to one already held changes nothing. The winner has to
	// be put in first: what the peer wrote above carries the lowest clock there
	// is, so nothing can sort below it.
	winner := MapOp{Kind: MapSet, ID: ID{Site: 5, Seq: 1}, Clock: 9, Key: "k", Value: []byte("new")}
	if keys, err := m.ApplyChanges(winner); err != nil || !reflect.DeepEqual(keys, []string{"k"}) {
		t.Fatalf("the winning write reported %q, %v", keys, err)
	}
	loser := MapOp{Kind: MapSet, ID: ID{Site: 3, Seq: 1}, Clock: 2, Key: "k", Value: []byte("old")}
	if keys, err := m.ApplyChanges(loser); err != nil || keys != nil {
		t.Fatalf("a losing write reported %q, %v", keys, err)
	}
	if got, ok := m.Get("k"); !ok || !bytes.Equal(got, []byte("new")) {
		t.Fatalf("the key holds %q, want the winner's %q", got, "new")
	}

	lpeer := NewList(1)
	ops, err := lpeer.Insert(0, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	l := NewList(2)
	moved, err := l.ApplyChanges(ops...)
	if err != nil || !moved {
		t.Fatalf("ApplyChanges = %v, %v; want a change", moved, err)
	}
	if moved, err := l.ApplyChanges(ops...); err != nil || moved {
		t.Fatalf("applying twice reported %v, %v", moved, err)
	}
	if _, err := l.ApplyChanges(ListOp{}); err == nil {
		t.Error("an invalid operation was accepted")
	}
}

func TestMergeKeys(t *testing.T) {
	tests := []struct {
		a, b, want []string
	}{
		{nil, []string{"b"}, []string{"b"}},
		{[]string{"a"}, nil, []string{"a"}},
		{[]string{"a", "c"}, []string{"b", "d"}, []string{"a", "b", "c", "d"}},
		{[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
		{[]string{"c"}, []string{"a"}, []string{"a", "c"}},
	}
	for _, tt := range tests {
		if got := mergeKeys(tt.a, tt.b); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("mergeKeys(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}
