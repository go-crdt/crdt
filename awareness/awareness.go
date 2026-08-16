// Package awareness tracks who else has a document open and where their cursor
// is — the coloured carets and name labels a collaborative editor shows.
//
// Awareness is deliberately not part of the CRDT. It is ephemeral: it is never
// persisted, it is not part of the document's history, and a peer's state is
// simply dropped when the peer leaves. Merging it needs nothing stronger than
// last-writer-wins per peer, so it carries a counter of its own rather than
// borrowing the document's.
//
// Offsets are rune positions in the document text as the publishing peer saw
// it. A concurrent edit can leave them briefly stale; a renderer should clamp
// them to the current length rather than trust them.
package awareness

import (
	"encoding/binary"
	"errors"
	"sort"

	"github.com/go-crdt/crdt"
)

// ErrMalformed reports bytes that are not a valid encoded update.
var ErrMalformed = errors.New("awareness: malformed encoding")

// A Cursor is a peer's selection. Anchor is where the selection started and
// Head is where it ends — Head may precede Anchor when selecting backwards, and
// the two are equal for a plain caret.
type Cursor struct {
	Anchor int
	Head   int
}

// A Peer is one participant's published state.
type Peer struct {
	Site   crdt.SiteID
	Cursor Cursor
	// Meta carries presentation details the editor chooses — typically a display
	// name and a colour. The package neither interprets nor requires it.
	Meta map[string]string

	clock uint64
	gone  bool
}

// An Update is one peer's state as it travels between replicas. Updates are
// idempotent and may arrive in any order: each carries the publishing peer's
// counter, and a lower counter than the receiver already holds is ignored.
type Update struct {
	Site   crdt.SiteID
	Clock  uint64
	Gone   bool
	Cursor Cursor
	Meta   map[string]string
}

// A Registry is one replica's view of every participant, including itself. It
// is not safe for concurrent use.
//
// The zero Registry is unusable — construct one with [New].
type Registry struct {
	peers map[crdt.SiteID]*Peer
}

// New returns an empty registry.
func New() *Registry { return &Registry{peers: map[crdt.SiteID]*Peer{}} }

// Publish records this replica's own state and returns the update to broadcast.
// Each call advances the site's counter, so a later publication always wins over
// an earlier one.
func (r *Registry) Publish(site crdt.SiteID, cursor Cursor, meta map[string]string) Update {
	p := r.peers[site]
	if p == nil {
		p = &Peer{Site: site}
		r.peers[site] = p
	}
	p.clock++
	p.gone = false
	p.Cursor = cursor
	p.Meta = cloneMeta(meta)
	return Update{Site: site, Clock: p.clock, Cursor: cursor, Meta: cloneMeta(meta)}
}

// Leave marks a peer as gone and returns the update announcing it. The peer's
// counter is kept so that an update still in flight cannot resurrect it.
func (r *Registry) Leave(site crdt.SiteID) Update {
	p := r.peers[site]
	if p == nil {
		p = &Peer{Site: site}
		r.peers[site] = p
	}
	p.clock++
	p.gone = true
	p.Cursor = Cursor{}
	p.Meta = nil
	return Update{Site: site, Clock: p.clock, Gone: true}
}

// Apply merges a peer's update and reports whether it changed anything. A stale
// or duplicate update changes nothing and returns false.
func (r *Registry) Apply(u Update) bool {
	if u.Clock == 0 {
		return false
	}
	p := r.peers[u.Site]
	if p != nil && u.Clock <= p.clock {
		return false
	}
	if p == nil {
		p = &Peer{Site: u.Site}
		r.peers[u.Site] = p
	}
	p.clock = u.Clock
	p.gone = u.Gone
	p.Cursor = u.Cursor
	p.Meta = cloneMeta(u.Meta)
	return true
}

// Peers returns the participants still present, ordered by site so the result
// is stable across calls and across replicas.
func (r *Registry) Peers() []Peer {
	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		if p.gone {
			continue
		}
		out = append(out, Peer{Site: p.Site, Cursor: p.Cursor, Meta: cloneMeta(p.Meta), clock: p.clock})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

// State returns the updates that reproduce this registry's view, which is what a
// joining peer needs in order to see everyone already present.
func (r *Registry) State() []Update {
	peers := r.Peers()
	out := make([]Update, 0, len(peers))
	for _, p := range peers {
		out = append(out, Update{Site: p.Site, Clock: p.clock, Cursor: p.Cursor, Meta: p.Meta})
	}
	return out
}

func cloneMeta(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// MarshalBinary encodes the update. Metadata keys are written in sorted order,
// so the same state always produces the same bytes.
func (u Update) MarshalBinary() ([]byte, error) {
	out := binary.AppendUvarint(nil, uint64(u.Site))
	out = binary.AppendUvarint(out, u.Clock)
	if u.Gone {
		return append(out, 1), nil
	}
	out = append(out, 0)
	out = binary.AppendVarint(out, int64(u.Cursor.Anchor))
	out = binary.AppendVarint(out, int64(u.Cursor.Head))

	keys := make([]string, 0, len(u.Meta))
	for k := range u.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out = binary.AppendUvarint(out, uint64(len(keys)))
	for _, k := range keys {
		out = appendString(out, k)
		out = appendString(out, u.Meta[k])
	}
	return out, nil
}

func appendString(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// UnmarshalBinary decodes an update written by MarshalBinary.
func (u *Update) UnmarshalBinary(data []byte) error {
	d := decoder{buf: data}
	site, ok1 := d.uvarint()
	clock, ok2 := d.uvarint()
	gone, ok3 := d.byteValue()
	if !ok1 || !ok2 || !ok3 || gone > 1 {
		return ErrMalformed
	}
	out := Update{Site: crdt.SiteID(site), Clock: clock, Gone: gone == 1}
	if out.Gone {
		if len(d.buf) != 0 {
			return ErrMalformed
		}
		*u = out
		return nil
	}
	anchor, ok1 := d.varint()
	head, ok2 := d.varint()
	count, ok3 := d.uvarint()
	if !ok1 || !ok2 || !ok3 || count > uint64(len(d.buf)) {
		return ErrMalformed
	}
	out.Cursor = Cursor{Anchor: int(anchor), Head: int(head)}
	if count > 0 {
		out.Meta = make(map[string]string, count)
	}
	for range count {
		k, ok1 := d.text()
		v, ok2 := d.text()
		if !ok1 || !ok2 {
			return ErrMalformed
		}
		out.Meta[k] = v
	}
	if len(d.buf) != 0 {
		return ErrMalformed
	}
	*u = out
	return nil
}

type decoder struct{ buf []byte }

func (d *decoder) byteValue() (byte, bool) {
	if len(d.buf) == 0 {
		return 0, false
	}
	b := d.buf[0]
	d.buf = d.buf[1:]
	return b, true
}

func (d *decoder) uvarint() (uint64, bool) {
	v, used := binary.Uvarint(d.buf)
	if used <= 0 {
		return 0, false
	}
	d.buf = d.buf[used:]
	return v, true
}

func (d *decoder) varint() (int64, bool) {
	v, used := binary.Varint(d.buf)
	if used <= 0 {
		return 0, false
	}
	d.buf = d.buf[used:]
	return v, true
}

func (d *decoder) text() (string, bool) {
	n, ok := d.uvarint()
	if !ok || n > uint64(len(d.buf)) {
		return "", false
	}
	s := string(d.buf[:n])
	d.buf = d.buf[n:]
	return s, true
}
