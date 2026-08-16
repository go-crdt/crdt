package awareness

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/go-crdt/crdt"
)

func TestPublishAndSee(t *testing.T) {
	r := New()
	if got := r.Peers(); len(got) != 0 {
		t.Fatalf("a new registry lists %d peers, want none", len(got))
	}

	u := r.Publish(1, Cursor{Anchor: 2, Head: 5}, map[string]string{"name": "ada"})
	if u.Site != 1 || u.Clock != 1 || u.Gone {
		t.Fatalf("Publish returned %+v, want site 1 at clock 1, present", u)
	}
	peers := r.Peers()
	if len(peers) != 1 || peers[0].Cursor != (Cursor{Anchor: 2, Head: 5}) {
		t.Fatalf("Peers() = %+v, want the published cursor", peers)
	}
	if peers[0].Meta["name"] != "ada" {
		t.Fatalf("Peers()[0].Meta = %v, want name=ada", peers[0].Meta)
	}

	// Publishing again must supersede, not accumulate.
	again := r.Publish(1, Cursor{Anchor: 7, Head: 7}, nil)
	if again.Clock != 2 {
		t.Fatalf("second Publish clock = %d, want 2", again.Clock)
	}
	if peers := r.Peers(); len(peers) != 1 || peers[0].Cursor.Head != 7 || peers[0].Meta != nil {
		t.Fatalf("Peers() = %+v, want a single peer at 7 with no metadata", peers)
	}
}

// The registry hands out copies: a caller mutating what it published, or what it
// read back, must not reach inside.
func TestMetadataIsCopied(t *testing.T) {
	r := New()
	meta := map[string]string{"colour": "teal"}
	u := r.Publish(1, Cursor{}, meta)
	meta["colour"] = "red"

	if got := r.Peers()[0].Meta["colour"]; got != "teal" {
		t.Errorf("the registry kept a reference to the caller's map: colour = %q", got)
	}
	if got := u.Meta["colour"]; got != "teal" {
		t.Errorf("the update kept a reference to the caller's map: colour = %q", got)
	}
	r.Peers()[0].Meta["colour"] = "green"
	if got := r.Peers()[0].Meta["colour"]; got != "teal" {
		t.Errorf("Peers() exposed the registry's own map: colour = %q", got)
	}
}

func TestApplyMergesAndRejectsStale(t *testing.T) {
	local, remote := New(), New()
	first := remote.Publish(2, Cursor{Anchor: 1, Head: 1}, nil)
	second := remote.Publish(2, Cursor{Anchor: 4, Head: 4}, nil)

	if !local.Apply(second) {
		t.Fatal("Apply of a fresh update reported no change")
	}
	if local.Apply(first) {
		t.Fatal("Apply of a superseded update reported a change")
	}
	if local.Apply(second) {
		t.Fatal("Apply of a duplicate reported a change")
	}
	if got := local.Peers()[0].Cursor.Head; got != 4 {
		t.Fatalf("cursor = %d, want 4: an out-of-order update won", got)
	}
	if local.Apply(Update{Site: 2, Clock: 0}) {
		t.Fatal("Apply of an unpublished update reported a change")
	}
}

func TestLeave(t *testing.T) {
	r := New()
	r.Publish(1, Cursor{}, nil)
	r.Publish(2, Cursor{}, nil)

	gone := r.Leave(1)
	if !gone.Gone || gone.Clock != 2 {
		t.Fatalf("Leave returned %+v, want gone at clock 2", gone)
	}
	peers := r.Peers()
	if len(peers) != 1 || peers[0].Site != 2 {
		t.Fatalf("Peers() = %+v, want only site 2", peers)
	}

	// A departure keeps the counter, so an update still in flight from before it
	// cannot bring the peer back.
	if r.Apply(Update{Site: 1, Clock: 1, Cursor: Cursor{Head: 3}}) {
		t.Fatal("an update from before the departure was accepted")
	}
	if len(r.Peers()) != 1 {
		t.Fatal("a departed peer came back")
	}
	// A later update does bring them back, which is what a reconnection is.
	if !r.Apply(Update{Site: 1, Clock: 3, Cursor: Cursor{Head: 3}}) {
		t.Fatal("a reconnection was rejected")
	}
	if len(r.Peers()) != 2 {
		t.Fatal("a reconnecting peer was not listed")
	}
}

// Leaving a site the registry never saw still has to produce an announcement,
// because that is what a server does when a connection drops before the client
// ever published.
func TestLeaveUnknownSite(t *testing.T) {
	r := New()
	u := r.Leave(42)
	if !u.Gone || u.Site != 42 || u.Clock != 1 {
		t.Fatalf("Leave(42) = %+v, want gone at clock 1", u)
	}
	if got := r.Peers(); len(got) != 0 {
		t.Fatalf("Peers() = %+v, want none", got)
	}
}

func TestApplyOfDeparture(t *testing.T) {
	r := New()
	r.Apply(Update{Site: 3, Clock: 1, Cursor: Cursor{Head: 2}})
	if !r.Apply(Update{Site: 3, Clock: 2, Gone: true}) {
		t.Fatal("Apply of a departure reported no change")
	}
	if got := r.Peers(); len(got) != 0 {
		t.Fatalf("Peers() = %+v, want none", got)
	}
}

func TestPeersAreOrderedBySite(t *testing.T) {
	r := New()
	for _, site := range []crdt.SiteID{9, 2, 5, 1} {
		r.Publish(site, Cursor{}, nil)
	}
	r.Leave(5)
	want := []crdt.SiteID{1, 2, 9}
	got := r.Peers()
	if len(got) != len(want) {
		t.Fatalf("Peers() = %+v, want sites %v", got, want)
	}
	for i := range want {
		if got[i].Site != want[i] {
			t.Fatalf("Peers() = %+v, want sites %v", got, want)
		}
	}
}

// A joining client is sent the registry's state; replaying it must reproduce the
// same view, counters included, so later updates are compared against the right
// baseline.
func TestStateReproducesTheView(t *testing.T) {
	origin := New()
	origin.Publish(1, Cursor{Anchor: 1, Head: 2}, map[string]string{"name": "ada"})
	origin.Publish(2, Cursor{Anchor: 3, Head: 3}, nil)
	origin.Publish(1, Cursor{Anchor: 4, Head: 4}, nil)
	origin.Leave(2)

	joiner := New()
	for _, u := range origin.State() {
		if !joiner.Apply(u) {
			t.Fatalf("Apply of state update %+v reported no change", u)
		}
	}
	got, want := joiner.Peers(), origin.Peers()
	if len(got) != len(want) {
		t.Fatalf("Peers() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Site != want[i].Site || got[i].Cursor != want[i].Cursor {
			t.Fatalf("Peers() = %+v, want %+v", got, want)
		}
	}
	// The counters travelled too, so the state is not re-applied on top of itself.
	for _, u := range origin.State() {
		if joiner.Apply(u) {
			t.Fatalf("replaying state update %+v changed the view a second time", u)
		}
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	for _, want := range []Update{
		{Site: 7, Clock: 3, Cursor: Cursor{Anchor: -2, Head: 40}, Meta: map[string]string{"name": "ada", "colour": "teal"}},
		{Site: 0, Clock: 1},
		{Site: 9, Clock: 2, Gone: true},
	} {
		data, err := want.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary(%+v): %v", want, err)
		}
		var got Update
		if err := got.UnmarshalBinary(data); err != nil {
			t.Fatalf("UnmarshalBinary(%+v): %v", want, err)
		}
		if got.Site != want.Site || got.Clock != want.Clock || got.Gone != want.Gone || got.Cursor != want.Cursor {
			t.Errorf("round trip gave %+v, want %+v", got, want)
		}
		if len(got.Meta) != len(want.Meta) {
			t.Errorf("round trip gave metadata %v, want %v", got.Meta, want.Meta)
		}
		for k, v := range want.Meta {
			if got.Meta[k] != v {
				t.Errorf("round trip gave metadata %v, want %v", got.Meta, want.Meta)
			}
		}
	}
}

// The encoding is deterministic, which is what lets a caller compare or cache
// updates by their bytes.
func TestUpdateEncodingIsStable(t *testing.T) {
	u := Update{Site: 1, Clock: 1, Meta: map[string]string{"z": "1", "a": "2", "m": "3"}}
	first, err := u.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := u.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatal("the same update encoded differently across calls")
		}
	}
}

func TestUpdateRejectsMalformed(t *testing.T) {
	full := Update{Site: 7, Clock: 3, Cursor: Cursor{Anchor: 1, Head: 2}, Meta: map[string]string{"k": "v"}}
	valid, err := full.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	goneData, err := Update{Site: 1, Clock: 1, Gone: true}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"presence flag out of range", []byte{1, 1, 2}},
		{"trailing bytes after a departure", append(append([]byte{}, goneData...), 0)},
		{"trailing bytes", append(append([]byte{}, valid...), 0)},
		{"more metadata than bytes", func() []byte {
			b := binary.AppendUvarint(nil, 1)
			b = binary.AppendUvarint(b, 1)
			b = append(b, 0)
			b = binary.AppendVarint(b, 0)
			b = binary.AppendVarint(b, 0)
			return binary.AppendUvarint(b, 1<<20)
		}()},
		{"metadata value longer than the message", func() []byte {
			b := binary.AppendUvarint(nil, 1)
			b = binary.AppendUvarint(b, 1)
			b = append(b, 0)
			b = binary.AppendVarint(b, 0)
			b = binary.AppendVarint(b, 0)
			b = binary.AppendUvarint(b, 1)
			b = binary.AppendUvarint(b, 1)
			b = append(b, 'k')
			return binary.AppendUvarint(b, 99)
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Update
			if err := got.UnmarshalBinary(tt.data); !errors.Is(err, ErrMalformed) {
				t.Errorf("UnmarshalBinary() = %v, want ErrMalformed", err)
			}
		})
	}

	for _, data := range [][]byte{valid, goneData} {
		for n := range len(data) {
			var got Update
			if err := got.UnmarshalBinary(data[:n]); !errors.Is(err, ErrMalformed) {
				t.Errorf("UnmarshalBinary(%d of %d bytes) = %v, want ErrMalformed", n, len(data), err)
			}
		}
	}
}
