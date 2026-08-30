package structured

import (
	"github.com/go-crdt/crdt"
)

// Every replica in the chaos reaches a composite, and this is where. It is one
// line each rather than an interface each type has to grow, because collecting
// is something this harness does to a document and not something a document
// offers a caller.
func (r *blobReplica) composite() *crdt.Composite     { return r.b.doc }
func (r *blocksReplica) composite() *crdt.Composite   { return r.b.doc }
func (r *counterReplica) composite() *crdt.Composite  { return r.doc }
func (r *diagramReplica) composite() *crdt.Composite  { return r.d.doc }
func (r *documentReplica) composite() *crdt.Composite { return r.d.doc }
func (r *inkReplica) composite() *crdt.Composite      { return r.ink.doc }
func (r *multiReplica) composite() *crdt.Composite    { return r.doc }
func (r *proposalReplica) composite() *crdt.Composite { return r.p.doc }
func (r *richReplica) composite() *crdt.Composite     { return r.r.doc }
func (r *sequenceReplica) composite() *crdt.Composite { return r.doc }
func (r *setReplica) composite() *crdt.Composite      { return r.doc }
func (r *sheetReplica) composite() *crdt.Composite    { return r.s.doc }
func (r *treeReplica) composite() *crdt.Composite     { return r.doc }
func (r *undoReplica) composite() *crdt.Composite     { return r.doc }

// holdsComposite is what the chaos asks a replica for when it is about to
// collect. Every replica answers; the assertion that they do is in the harness,
// so a type added later without one is a failure and not a silent skip.
type holdsComposite interface {
	composite() *crdt.Composite
}

// meetOf is the element-wise minimum of two composite versions: the operations
// both of them have. A part one of them does not name is a part neither can be
// said to hold, so it is dropped rather than carried at the other's value.
func meetOf(a, b crdt.CompositeVersion) crdt.CompositeVersion {
	out := crdt.CompositeVersion{}
	for part, mine := range a {
		theirs, named := b[part]
		if !named {
			continue
		}
		vv := crdt.VersionVector{}
		for site, n := range mine {
			if t := theirs[site]; t < n {
				vv[site] = t
			} else {
				vv[site] = n
			}
		}
		out[part] = vv
	}
	return out
}
