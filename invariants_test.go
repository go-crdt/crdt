package crdt

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"unicode/utf16"
)

// What a document must be true of itself, whatever was done to it.
//
// Convergence tests ask whether two replicas agree. They cannot see a replica
// that is internally wrong in a way both replicas share, or one whose index has
// drifted from the text it indexes — which reads correctly until the walk that
// would have caught it takes a different path, and then reads wrongly, and
// converges wrongly, quietly.
//
// So this checks the document against itself: the counters against the blocks
// they count, the index against the list it indexes, and both against the text
// that comes out. It is cheap enough to run after every operation, which is
// where it is run.

// check reports every way the document disagrees with itself.
func check(t *testing.T, d *Doc, what string) {
	t.Helper()
	for _, bad := range inspect(d) {
		t.Errorf("%s: %s", what, bad)
	}
}

func inspect(d *Doc) []string {
	var bad []string
	say := func(format string, args ...any) { bad = append(bad, fmt.Sprintf(format, args...)) }

	d.flush() // the index lags by one block by design; ask it to catch up

	// --- the blocks, walked forwards
	var total, visible, sup int
	seen := map[*block]bool{}
	ids := map[ID]bool{}
	// The head is a sentinel for the root identity. It is in the index too — the
	// index orders the same blocks — so it counts as a block here and is
	// excluded from everything that is about characters.
	seen[d.head] = true
	prev := d.head
	for b := d.head.next; b != nil; b = b.next {
		if seen[b] {
			say("the block list loops at %v", b.id)
			break
		}
		seen[b] = true
		if b.prev != prev {
			say("block %v's prev points at %v, not %v", b.id, idOf(b.prev), idOf(prev))
		}
		if len(b.text) == 0 {
			say("block %v holds no characters", b.id)
		}
		// Deleted stretches ascend, do not overlap, and stay inside the run.
		at := 0
		for _, r := range b.dels {
			switch {
			case int(r.from) < at:
				say("block %v has deletions out of order at %d", b.id, r.from)
			case r.from >= r.to:
				say("block %v has an empty deletion [%d,%d)", b.id, r.from, r.to)
			case int(r.to) > len(b.text):
				say("block %v deletes past its end: [%d,%d) of %d", b.id, r.from, r.to, len(b.text))
			}
			at = int(r.to)
		}
		// Every character's identity is its own, exactly once in the document.
		for i := range b.text {
			id := b.idAt(i)
			if ids[id] {
				say("identity %v belongs to two characters", id)
			}
			ids[id] = true
			if got := d.vv[id.Site]; got < id.Seq {
				say("the version promises %d for site %d, and it holds %v",
					got, id.Site, id)
			}
		}
		var nsup int32
		for _, r := range b.text {
			if r > 0xFFFF {
				nsup++
			}
		}
		if nsup != b.nsup {
			say("block %v says %d supplementary characters and has %d", b.id, b.nsup, nsup)
		}
		total += len(b.text)
		visible += b.visibleFrom(0)
		for i, r := range b.text {
			if r > 0xFFFF && b.aliveAt(i) {
				sup++
			}
		}
		prev = b
	}
	if total != d.total {
		say("total is %d and the blocks hold %d", d.total, total)
	}
	if visible != d.visible {
		say("visible is %d and the blocks show %d", d.visible, visible)
	}
	if sup != d.sup {
		say("sup is %d and the blocks show %d", d.sup, sup)
	}
	if got := d.Len(); got != visible {
		say("Len is %d and the blocks show %d", got, visible)
	}
	if got := d.Tombstones(); got != total-visible {
		say("Tombstones is %d and the blocks hide %d", got, total-visible)
	}

	// --- the text that comes out
	text := []rune(d.String())
	if len(text) != visible {
		say("String has %d characters and %d are visible", len(text), visible)
	}
	if got, want := d.LenUTF16(), len(utf16.Encode(text)); got != want {
		say("LenUTF16 is %d and the text is %d units", got, want)
	}

	// --- the index over the same blocks
	if n := len(seen); n > 0 && d.tree == nil {
		say("%d blocks and no index", n)
	}
	if d.tree != nil && d.tree.up != nil {
		say("the index root has a parent")
	}
	indexed := map[*block]bool{}
	var order []*block
	var walk func(b *block, up *block) (vis, sups int32, height uint8, low *block)
	walk = func(b *block, up *block) (int32, int32, uint8, *block) {
		if b == nil {
			return 0, 0, 0, nil
		}
		if indexed[b] {
			say("block %v is in the index twice", b.id)
			return 0, 0, 0, nil
		}
		indexed[b] = true
		if b.up != up {
			say("block %v's parent is %v, not %v", b.id, idOf(b.up), idOf(up))
		}
		lv, ls, lh, llow := walk(b.left, b)
		order = append(order, b)
		rv, rs, rh, rlow := walk(b.right, b)

		own := int32(b.visibleFrom(0))
		if want := lv + rv + own; b.subVis != want {
			say("block %v says subVis %d and its subtree shows %d", b.id, b.subVis, want)
		}
		var ownSup int32
		for i, r := range b.text {
			if r > 0xFFFF && b.aliveAt(i) {
				ownSup++
			}
		}
		if want := ls + rs + ownSup; b.subSup != want {
			say("block %v says subSup %d and its subtree shows %d", b.id, b.subSup, want)
		}
		if want := max(lh, rh) + 1; b.height != want {
			say("block %v says height %d and its children are %d and %d", b.id, b.height, lh, rh)
		}
		if balance := int(lh) - int(rh); balance < -1 || balance > 1 {
			say("block %v is out of balance by %d", b.id, balance)
		}
		// subMin is the block of the subtree whose first character sorts
		// lowest by (clock, identity) — the order the integration walk steps
		// over runs by — not the one furthest left.
		low := b
		if llow != nil && sortsLower(llow, low) {
			low = llow
		}
		if rlow != nil && sortsLower(rlow, low) {
			low = rlow
		}
		if b.subMin != low {
			say("block %v says its lowest is %v and it is %v", b.id, idOf(b.subMin), idOf(low))
		}
		return lv + rv + own, ls + rs + ownSup, max(lh, rh) + 1, low
	}
	walk(d.tree, nil)

	if len(indexed) != len(seen) {
		say("the index holds %d blocks and the list holds %d", len(indexed), len(seen))
	}
	for b := range seen {
		if !indexed[b] {
			say("block %v is in the list and not in the index", b.id)
		}
	}
	// In-order over the index is the order of the list, sentinel first.
	i := 0
	for b := d.head; b != nil && i < len(order); b, i = b.next, i+1 {
		if order[i] != b {
			say("the index reads %v where the list reads %v", idOf(order[i]), idOf(b))
			break
		}
	}

	// --- the per-site index
	for site, blocks := range d.bySite {
		var last uint64
		for n, b := range blocks {
			if b.id.Site != site {
				say("bySite[%d] holds a block of site %d", site, b.id.Site)
			}
			if n > 0 && b.id.Seq <= last {
				say("bySite[%d] is out of order at %v", site, b.id)
			}
			last = b.id.Seq + uint64(len(b.text)) - 1
			if !seen[b] {
				say("bySite[%d] holds %v, which is not in the list", site, b.id)
			}
		}
	}

	// --- what a reader is entitled to: every offset resolves, both ways
	for pos := range visible {
		id, err := d.Anchor(pos)
		if err != nil {
			say("Anchor(%d) of %d visible: %v", pos, visible, err)
			continue
		}
		back, ok := d.Position(id)
		if !ok || back != pos {
			say("Anchor(%d) came back as %d, ok=%v", pos, back, ok)
		}
		if !d.Visible(id) {
			say("the character at %d is not visible", pos)
		}
	}
	return bad
}

func idOf(b *block) any {
	if b == nil {
		return "nothing"
	}
	return b.id
}

// A document is checked against itself after every operation, over a schedule
// nobody chose.
func TestADocumentAgreesWithItself(t *testing.T) {
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 991))
		d := New(1)
		peer := New(2)
		for step := range 60 {
			what := fmt.Sprintf("seed %d step %d", seed, step)
			n := d.Len()
			switch {
			case n == 0 || rng.IntN(3) > 0:
				at := rng.IntN(n + 1)
				s := string(runeFor(rng))
				if _, err := d.Insert(at, s); err != nil {
					t.Fatalf("%s: Insert(%d, %q): %v", what, at, s, err)
				}
			case rng.IntN(4) == 0:
				// The peer edits too, so the document integrates work it did
				// not make — which is the path that builds the index by a
				// different road.
				if err := peer.Apply(must(d.OpsSince(peer.Version()))...); err != nil {
					t.Fatalf("%s: peer catching up: %v", what, err)
				}
				at := rng.IntN(peer.Len() + 1)
				ops, err := peer.Insert(at, string(runeFor(rng)))
				if err != nil {
					t.Fatalf("%s: peer insert: %v", what, err)
				}
				if err := d.Apply(ops...); err != nil {
					t.Fatalf("%s: applying the peer's: %v", what, err)
				}
			default:
				at := rng.IntN(n)
				if _, err := d.Delete(at, 1+rng.IntN(n-at)); err != nil {
					t.Fatalf("%s: Delete: %v", what, err)
				}
			}
			check(t, d, what)
			if t.Failed() {
				t.FailNow()
			}
		}
	}
}

// runeFor picks from the widths that behave differently: one byte, several
// bytes in one UTF-16 unit, and a supplementary character, which is two.
func runeFor(rng *rand.Rand) rune {
	switch rng.IntN(6) {
	case 0:
		return '\u00e9' // two bytes, one UTF-16 unit
	case 1:
		return '\U0001F600' // supplementary: two UTF-16 units
	case 2:
		return '\u4e2d' // three bytes, one UTF-16 unit
	default:
		return rune('a' + rng.IntN(26))
	}
}

// The same, on a document built only out of a peer's operations delivered in an
// order nobody chose — which is how a real replica's index gets built.
func TestADocumentBuiltFromOperationsAgreesWithItself(t *testing.T) {
	source := New(1)
	for range 40 {
		if _, err := source.Insert(source.Len(), "the quick brown fox "); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Delete(100, 200); err != nil {
		t.Fatal(err)
	}
	ops := must(source.OpsSince(nil))

	for seed := range uint64(20) {
		rng := rand.New(rand.NewPCG(seed, 7))
		shuffled := append([]Op(nil), ops...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })

		d := New(2)
		for i, op := range shuffled {
			if err := d.Apply(op); err != nil {
				t.Fatalf("seed %d op %d: %v", seed, i, err)
			}
		}
		check(t, d, fmt.Sprintf("seed %d, rebuilt from %d operations", seed, len(ops)))
		if d.Pending() != 0 {
			t.Fatalf("seed %d: %d operations still parked", seed, d.Pending())
		}
		if d.String() != source.String() {
			t.Fatalf("seed %d: rebuilt document differs", seed)
		}
	}
}

// The same question of a list: does it agree with itself?
func inspectList(l *List) []string {
	var bad []string
	say := func(format string, args ...any) { bad = append(bad, fmt.Sprintf(format, args...)) }

	present := 0
	ids := map[ID]bool{}
	for i, e := range l.elements {
		if ids[e.id] {
			say("identity %v belongs to two elements", e.id)
		}
		ids[e.id] = true
		if at, ok := l.byID[e.id]; !ok {
			say("element %v is not in the index", e.id)
		} else if at != i {
			say("the index puts %v at %d and it is at %d", e.id, at, i)
		}
		if e.delID.IsRoot() {
			present++
			if len(e.value) == 0 {
				say("present element %v holds no value", e.id)
			}
		}
		if got := l.vv[e.id.Site]; got < e.id.Seq {
			say("the version promises %d for site %d and it holds %v", got, e.id.Site, e.id)
		}
		if e.clock > MaxClock {
			say("element %v has a clock above the ceiling: %d", e.id, e.clock)
		}
	}
	if present != l.present {
		say("present is %d and the elements show %d", l.present, present)
	}
	if got := l.Len(); got != present {
		say("Len is %d and the elements show %d", got, present)
	}
	if got := l.Tombstones(); got != len(l.elements)-present {
		say("Tombstones is %d and the elements hide %d", got, len(l.elements)-present)
	}
	if got := len(l.Values()); got != present {
		say("Values has %d and %d are present", got, present)
	}
	if len(l.byID) != len(l.elements) {
		say("the index holds %d and the list holds %d", len(l.byID), len(l.elements))
	}
	// Every element is readable at the offset the list says it is.
	values := l.Values()
	at := 0
	for _, e := range l.elements {
		if e.delID.IsRoot() {
			got, err := l.Get(at)
			if err != nil || string(got) != string(values[at]) {
				say("Get(%d) disagrees with Values: %v", at, err)
			}
			at++
		}
	}
	return bad
}

// And of a map.
func inspectMap(m *Map) []string {
	var bad []string
	say := func(format string, args ...any) { bad = append(bad, fmt.Sprintf(format, args...)) }

	live := 0
	held := map[ID]string{}
	for key, rec := range m.records {
		if !rec.dead {
			live++
		}
		if prev, twice := held[rec.id]; twice {
			say("operation %v is claimed by %q and %q", rec.id, prev, key)
		}
		held[rec.id] = key
		if got := m.vv[rec.id.Site]; got < rec.id.Seq {
			say("the version promises %d for site %d and it holds %v", got, rec.id.Site, rec.id)
		}
		if rec.clock < rec.id.Seq {
			say("record %q has clock %d below its own sequence %d", key, rec.clock, rec.id.Seq)
		}
		if rec.clock > MaxClock {
			say("record %q has a clock above the ceiling: %d", key, rec.clock)
		}
		value, ok := m.Get(key)
		if ok == rec.dead {
			say("record %q is dead=%v and Get says ok=%v", key, rec.dead, ok)
		}
		if ok && string(value) != string(rec.value) {
			say("Get(%q) is %q and the record holds %q", key, value, rec.value)
		}
		if _, _, ok := m.Stamp(key); ok == rec.dead {
			say("record %q is dead=%v and Stamp says ok=%v", key, rec.dead, ok)
		}
	}
	if live != m.live {
		say("live is %d and the records show %d", m.live, live)
	}
	if got := len(m.Keys()); got != live {
		say("Keys has %d and %d are live", got, live)
	}
	if got := m.Tombstones(); got != len(m.records)-live {
		say("Tombstones is %d and the records hide %d", got, len(m.records)-live)
	}
	return bad
}

// A list is checked against itself after every operation, and against a peer's
// operations arriving in an order nobody chose.
func TestAListAgreesWithItself(t *testing.T) {
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 17))
		l := NewList(1)
		peer := NewList(2)
		for step := range 50 {
			what := fmt.Sprintf("seed %d step %d", seed, step)
			n := l.Len()
			switch {
			case n == 0 || rng.IntN(3) > 0:
				if _, err := l.Insert(rng.IntN(n+1), []byte(fmt.Sprint(rng.IntN(1000)))); err != nil {
					t.Fatalf("%s: Insert: %v", what, err)
				}
			case rng.IntN(3) == 0:
				if err := peer.Apply(must(l.OpsSince(peer.Version()))...); err != nil {
					t.Fatalf("%s: peer catching up: %v", what, err)
				}
				ops, err := peer.Insert(rng.IntN(peer.Len()+1), []byte("peer"))
				if err != nil {
					t.Fatalf("%s: peer insert: %v", what, err)
				}
				if err := l.Apply(ops...); err != nil {
					t.Fatalf("%s: applying the peer's: %v", what, err)
				}
			default:
				at := rng.IntN(n)
				if _, err := l.Delete(at, 1+rng.IntN(n-at)); err != nil {
					t.Fatalf("%s: Delete: %v", what, err)
				}
			}
			for _, complaint := range inspectList(l) {
				t.Fatalf("%s: %s", what, complaint)
			}
		}
	}
}

// And a map.
func TestAMapAgreesWithItself(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e"}
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 23))
		m := NewMap(1)
		peer := NewMap(2)
		for step := range 50 {
			what := fmt.Sprintf("seed %d step %d", seed, step)
			key := keys[rng.IntN(len(keys))]
			switch rng.IntN(4) {
			case 0:
				if _, err := m.Delete(key); err != nil {
					t.Fatalf("%s: Delete: %v", what, err)
				}
			case 1:
				if err := peer.Apply(m.OpsSince(peer.Version())...); err != nil {
					t.Fatalf("%s: peer catching up: %v", what, err)
				}
				op, err := peer.Set(key, []byte("peer"))
				if err != nil {
					t.Fatalf("%s: peer set: %v", what, err)
				}
				if err := m.Apply(op); err != nil {
					t.Fatalf("%s: applying the peer's: %v", what, err)
				}
			default:
				if _, err := m.Set(key, []byte(fmt.Sprint(rng.IntN(100)))); err != nil {
					t.Fatalf("%s: Set: %v", what, err)
				}
			}
			for _, complaint := range inspectMap(m) {
				t.Fatalf("%s: %s", what, complaint)
			}
		}
	}
}
