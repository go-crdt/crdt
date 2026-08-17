package crdt

import (
	"errors"
	"hash/fnv"
	"testing"
)

func TestIDString(t *testing.T) {
	tests := []struct {
		id   ID
		want string
	}{
		{ID{}, "root"},
		{ID{Site: 4, Seq: 12}, "12@4"},
		{ID{Seq: 1}, "1@0"},
	}
	for _, tt := range tests {
		if got := tt.id.String(); got != tt.want {
			t.Errorf("ID%+v.String() = %q, want %q", tt.id, got, tt.want)
		}
	}
}

// A sequence number of zero never names an operation, so it means the root
// whatever site is attached to it. Anything looser lets a decoder mistake
// {site, 0} for a real operation, which is how a hostile snapshot smuggles in a
// deletion no replica can reproduce.
func TestIDIsRoot(t *testing.T) {
	for _, id := range []ID{{}, {Site: 1}, {Site: 1 << 40}} {
		if !id.IsRoot() {
			t.Errorf("ID%+v is not reported as the root", id)
		}
	}
	for _, id := range []ID{{Seq: 1}, {Site: 1, Seq: 1}} {
		if id.IsRoot() {
			t.Errorf("ID%+v is reported as the root", id)
		}
	}
}

func TestIDWellFormed(t *testing.T) {
	for _, id := range []ID{{}, {Seq: 1}, {Site: 3, Seq: 7}} {
		if !id.wellFormed() {
			t.Errorf("ID%+v is rejected as ill-formed", id)
		}
	}
	// A site with no sequence number is neither the root nor an operation.
	for _, id := range []ID{{Site: 1}, {Site: 48}} {
		if id.wellFormed() {
			t.Errorf("ID%+v is accepted as well formed", id)
		}
	}
}

// A site identity has to be derivable from bytes the caller already holds, the
// same way on every platform, because js/wasm has no usable source of one.
func TestDeriveSiteID(t *testing.T) {
	if got, want := DeriveSiteID([]byte("session-a")), DeriveSiteID([]byte("session-a")); got != want {
		t.Errorf("DeriveSiteID is not deterministic: %d then %d", want, got)
	}
	if DeriveSiteID([]byte("session-a")) == DeriveSiteID([]byte("session-b")) {
		t.Error("two different inputs derived the same site")
	}
	// The hash is FNV-1a, checked against the standard library rather than
	// against numbers written down here: a typo in the constants would still
	// look random, and every replica in a session has to agree on it.
	for _, in := range [][]byte{nil, []byte("a"), []byte("session-a"), []byte("\x00\xff")} {
		h := fnv.New64a()
		h.Write(in)
		if got, want := DeriveSiteID(in), SiteID(h.Sum64()); got != want {
			t.Errorf("DeriveSiteID(%q) = %#x, want the FNV-1a value %#x", in, got, want)
		}
	}
}

func TestBeforeIsAStrictTotalOrder(t *testing.T) {
	tests := []struct {
		name           string
		aClock, bClock uint64
		a, b           ID
		want           bool
	}{
		{"lower clock sorts first", 1, 2, ID{Site: 9, Seq: 1}, ID{Site: 1, Seq: 1}, true},
		{"higher clock does not", 2, 1, ID{Site: 1, Seq: 1}, ID{Site: 9, Seq: 1}, false},
		{"site breaks a tie", 5, 5, ID{Site: 1, Seq: 7}, ID{Site: 2, Seq: 1}, true},
		{"site breaks a tie the other way", 5, 5, ID{Site: 2, Seq: 1}, ID{Site: 1, Seq: 7}, false},
		{"identical is not before", 5, 5, ID{Site: 1, Seq: 1}, ID{Site: 1, Seq: 1}, false},
		// Only a peer's bytes reach these two: a site's clock advances at least
		// once per operation, so nothing this package mints ever ties here.
		{"the sequence number breaks what site cannot", 5, 5, ID{Site: 1, Seq: 1}, ID{Site: 1, Seq: 2}, true},
		{"and the other way", 5, 5, ID{Site: 1, Seq: 2}, ID{Site: 1, Seq: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := before(tt.aClock, tt.a, tt.bClock, tt.b); got != tt.want {
				t.Errorf("before(%d, %v, %d, %v) = %v, want %v",
					tt.aClock, tt.a, tt.bClock, tt.b, got, tt.want)
			}
		})
	}
}

// The property the ordering has to have, rather than a handful of cases: over
// every pair drawn from a set that includes the ties only a peer can produce,
// exactly one of a<b, b<a and a==b holds, and the order is transitive. An
// ordering that fails this is not merely wrong somewhere — it makes the two
// walks that place a character free to disagree, and a document that cannot be
// reloaded is what that looked like.
func TestBeforeIsTrichotomousAndTransitive(t *testing.T) {
	type stamp struct {
		clock uint64
		id    ID
	}
	var all []stamp
	for _, clock := range []uint64{4, 5} {
		for _, site := range []SiteID{1, 2} {
			for _, seq := range []uint64{1, 2} {
				all = append(all, stamp{clock, ID{Site: site, Seq: seq}})
			}
		}
	}
	same := func(x, y stamp) bool { return x.clock == y.clock && x.id == y.id }
	for _, a := range all {
		for _, b := range all {
			ab := before(a.clock, a.id, b.clock, b.id)
			ba := before(b.clock, b.id, a.clock, a.id)
			switch {
			case same(a, b):
				if ab || ba {
					t.Errorf("%v and %v are the same operation but compare ordered", a, b)
				}
			case ab == ba:
				t.Errorf("%v and %v are distinct but neither sorts first (or both do)", a, b)
			}
			for _, c := range all {
				if ab && before(b.clock, b.id, c.clock, c.id) && !before(a.clock, a.id, c.clock, c.id) {
					t.Errorf("not transitive: %v < %v < %v but not %v < %v", a, b, c, a, c)
				}
			}
		}
	}
}

func TestIDLess(t *testing.T) {
	tests := []struct {
		a, b ID
		want bool
	}{
		{ID{Site: 1, Seq: 5}, ID{Site: 2, Seq: 1}, true},
		{ID{Site: 2, Seq: 1}, ID{Site: 1, Seq: 5}, false},
		{ID{Site: 1, Seq: 1}, ID{Site: 1, Seq: 2}, true},
		{ID{Site: 1, Seq: 2}, ID{Site: 1, Seq: 1}, false},
		{ID{Site: 1, Seq: 1}, ID{Site: 1, Seq: 1}, false},
	}
	for _, tt := range tests {
		if got := idLess(tt.a, tt.b); got != tt.want {
			t.Errorf("idLess(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSortIDs(t *testing.T) {
	ids := []ID{{Site: 2, Seq: 1}, {Site: 1, Seq: 9}, {Site: 2, Seq: 0}, {Site: 1, Seq: 2}}
	sortIDs(ids)
	want := []ID{{Site: 1, Seq: 2}, {Site: 1, Seq: 9}, {Site: 2, Seq: 0}, {Site: 2, Seq: 1}}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("sortIDs gave %v, want %v", ids, want)
		}
	}
	sortIDs(nil) // must not panic
}

func TestVersionVector(t *testing.T) {
	var empty VersionVector
	if got := empty.Get(3); got != 0 {
		t.Errorf("Get on a nil vector = %d, want 0", got)
	}
	if !empty.Includes(ID{Site: 3}) {
		t.Error("the root of an unknown site should count as included")
	}
	if empty.Includes(ID{Site: 3, Seq: 1}) {
		t.Error("a nil vector reported an operation as applied")
	}

	v := VersionVector{1: 4, 2: 7}
	if got, want := v.Get(2), uint64(7); got != want {
		t.Errorf("Get(2) = %d, want %d", got, want)
	}
	if !v.Includes(ID{Site: 1, Seq: 4}) || v.Includes(ID{Site: 1, Seq: 5}) {
		t.Error("Includes disagrees with the recorded sequence number")
	}

	clone := v.Clone()
	if !clone.Equal(v) {
		t.Fatalf("Clone() = %v, want %v", clone, v)
	}
	clone[1] = 99
	if v[1] != 4 {
		t.Error("Clone shares storage with the original")
	}
	if got := empty.Clone(); got == nil || len(got) != 0 {
		t.Errorf("Clone of a nil vector = %v, want an empty writable vector", got)
	}
}

func TestVersionVectorEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b VersionVector
		want bool
	}{
		{"both empty", nil, VersionVector{}, true},
		{"same entries", VersionVector{1: 2}, VersionVector{1: 2}, true},
		{"zero entries do not count", VersionVector{1: 2, 3: 0}, VersionVector{1: 2}, true},
		{"differing sequence", VersionVector{1: 2}, VersionVector{1: 3}, false},
		{"missing on the right", VersionVector{1: 2, 2: 1}, VersionVector{1: 2}, false},
		{"missing on the left", VersionVector{1: 2}, VersionVector{1: 2, 2: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVersionVectorSitesAreSortedAndSkipZero(t *testing.T) {
	got := VersionVector{5: 1, 1: 2, 3: 0, 2: 9}.sites()
	want := []SiteID{1, 2, 5}
	if len(got) != len(want) {
		t.Fatalf("sites() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sites() = %v, want %v", got, want)
		}
	}
}

func TestVersionVectorRoundTrip(t *testing.T) {
	for _, want := range []VersionVector{
		nil,
		{},
		{1: 4},
		{9: 1, 2: 300, 1 << 40: 1 << 40},
	} {
		data, err := want.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary(%v): %v", want, err)
		}
		var got VersionVector
		if err := got.UnmarshalBinary(data); err != nil {
			t.Fatalf("UnmarshalBinary(%v): %v", want, err)
		}
		if !got.Equal(want) {
			t.Errorf("round trip gave %v, want %v", got, want)
		}
	}
}

// The encoding is deterministic, which is what lets a peer compare or cache one.
func TestVersionVectorEncodingIsStable(t *testing.T) {
	v := VersionVector{7: 2, 1: 9, 4: 1, 3: 0}
	first, err := v.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := v.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatal("the same vector encoded differently across calls")
		}
	}
	// A site recorded with a zero sequence number is absent, and must not be
	// written, or two replicas in the same state would disagree on the bytes.
	other := VersionVector{7: 2, 1: 9, 4: 1}
	if data, err := other.MarshalBinary(); err != nil || string(data) != string(first) {
		t.Fatalf("a zero entry changed the encoding (err %v)", err)
	}
}

func TestVersionVectorRejectsMalformed(t *testing.T) {
	valid, err := VersionVector{1: 2, 3: 4}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"more entries than bytes", []byte{0x40, 1, 1}},
		{"sequence number of zero", []byte{0x01, 0x01, 0x00}},
		{"the same site twice", []byte{0x02, 0x01, 0x01, 0x01, 0x02}},
		{"trailing bytes", append(append([]byte{}, valid...), 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v VersionVector
			if err := v.UnmarshalBinary(tt.data); !errors.Is(err, ErrMalformed) {
				t.Errorf("UnmarshalBinary() = %v, want ErrMalformed", err)
			}
		})
	}
	for n := range len(valid) {
		var v VersionVector
		if err := v.UnmarshalBinary(valid[:n]); err == nil {
			t.Errorf("UnmarshalBinary(%d of %d bytes) succeeded, want an error", n, len(valid))
		}
	}
}
