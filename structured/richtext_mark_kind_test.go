package structured

import "testing"

// A mark kind this build does not know is refused, for the reason crdt refuses
// an operation kind it does not know: a reserved number has to come back as
// something this build declined, not as something it guessed at.
//
// Here the guess would not be harmless. Further down, the only question asked
// of a decoded mark is `m.kind == markAdd`; everything else is treated as a
// REMOVAL. So an unknown kind, accepted, takes formatting away on this build
// while a later one that knows the kind does something else with it -- two
// replicas of one document showing different text, from bytes both accepted.
//
// Nothing tested the line that refuses it: deleting it leaves the whole suite
// green, which is how a mutation run over structured found it.
func TestAnUnknownMarkKindIsRefused(t *testing.T) {
	// The control first: a mark this package wrote is read back, so what
	// follows is about the kind byte and not about an encoding that was never
	// valid.
	want := mark{kind: markAdd, expand: ExpandNone, name: "b", value: []byte("1")}
	encoded := encodeMark(want)
	got, ok := decodeMark(encoded)
	if !ok || got.kind != want.kind || got.name != want.name {
		t.Fatalf("a mark this package encoded read back as %+v ok=%v", got, ok)
	}
	if removal, ok := decodeMark(encodeMark(mark{kind: markRemove, name: "b"})); !ok || removal.kind != markRemove {
		t.Fatalf("a removal read back as %+v ok=%v", removal, ok)
	}

	for _, kind := range []byte{0, 3, 9, 255} {
		altered := append([]byte(nil), encoded...)
		altered[0] = kind
		if m, ok := decodeMark(altered); ok {
			t.Errorf("a mark of kind %d was accepted as %+v: an unknown kind is read "+
				"as a removal below, so accepting it takes formatting away here and "+
				"leaves a build that knows the kind holding different text", kind, m)
		}
	}
}
