package awareness

import "testing"

// Awareness updates arrive from other participants, so the decoder is a trust
// boundary like every other in this module.
func FuzzUpdate(f *testing.F) {
	seed, err := Update{
		Site:   7,
		Clock:  3,
		Cursor: Cursor{Anchor: -1, Head: 12},
		Meta:   map[string]string{"name": "ada", "colour": "teal"},
	}.MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	gone, err := Update{Site: 1, Clock: 1, Gone: true}.MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(gone)
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		var u Update
		if err := u.UnmarshalBinary(data); err != nil {
			return
		}
		// Anything accepted must survive a round trip unchanged, and must be
		// something a registry can merge without losing it.
		encoded, err := u.MarshalBinary()
		if err != nil {
			t.Fatalf("re-encoding an accepted update failed: %v", err)
		}
		var again Update
		if err := again.UnmarshalBinary(encoded); err != nil {
			t.Fatalf("a re-encoded update no longer decodes: %v", err)
		}
		if again.Site != u.Site || again.Clock != u.Clock || again.Gone != u.Gone || again.Cursor != u.Cursor {
			t.Fatalf("round trip changed the update: %+v, want %+v", again, u)
		}
		if len(again.Meta) != len(u.Meta) {
			t.Fatalf("round trip changed the metadata: %v, want %v", again.Meta, u.Meta)
		}
		for k, v := range u.Meta {
			if again.Meta[k] != v {
				t.Fatalf("round trip changed the metadata: %v, want %v", again.Meta, u.Meta)
			}
		}
		r := New()
		if r.Apply(u) && r.Apply(u) {
			t.Fatal("the same update was accepted twice")
		}
	})
}
