package structured

import (
	"sort"

	"github.com/go-crdt/crdt"
)

// Proposals are changes to a document that are not part of it yet: a suggested
// edit, a change put up for review, a branch somebody wants merged.
//
// # Why this is not a fork
//
// The obvious shape is a second copy of the document, edited on its own and
// reconciled later. Reconciling is where it falls apart. Two copies of a text
// can be compared, and what a comparison produces is a difference — insert
// this here, delete that — and applying a difference mints new characters. Every
// anchor, mark, comment and cursor hanging off the characters it replaced is
// then pointing at something that no longer exists, in a document nobody edited
// in the meantime. A review that accepts a wording change would take the
// comments off the paragraph around it.
//
// # What a proposal actually is
//
// A replica that has not synced. Nothing more.
//
// Operations here commute, so a replica that goes offline, edits, and comes back
// a week later needs no reconciliation: its operations merge with everything
// that happened while it was away, and every identity it wrote is the identity
// the rest of the document already knows. A proposal is that same replica, kept
// offline on purpose, with its operations written down where reviewers can read
// them and applied if somebody says yes.
//
// So accepting is [crdt.Composite.Apply] of the operations, and there is no
// rebase, no merge step and no conflict to settle: whatever the document did
// while the proposal was open, the proposal merges into it as concurrent work,
// because it is concurrent work. And nothing an anchor points at is disturbed,
// because the proposal is made of the document's own operations against the
// document's own identities rather than of a difference between two texts.
//
// Rejecting is free for the same reason: the operations were never applied, so
// there is nothing to take back. That is not true of anything already in the
// document, which can only be undone by [Undo] — a new edit that has the effect
// of the old one not having happened.
//
// # A draft is a replica, so it needs a site of its own
//
// [Proposals.Draft] hands back a working copy to edit, and it takes a
// [crdt.SiteID], for exactly the reason every replica takes one: two replicas
// minting operations under one site mint the same identity for different
// operations, and a document that received both would be holding two different
// things that claim to be the same. A draft is a replica. Give it its own site.
//
// # What it costs, and what it will not do
//
// A proposal is stored as one map value holding its operations, so it is one
// operation the size of the change. That is the right shape for a review — a
// wording, a paragraph, a renamed field — and the wrong one for a rewrite of the
// whole document, which should be a document.
//
// A proposal is a recorded set of operations, not a live session: this package
// does not carry two people typing into one draft at the same time. Two people
// can each hold the draft as a replica and exchange its operations — which is
// what a draft being a replica means — and what gets written down when it is put
// up is whatever the draft holds then.
type Proposals struct {
	doc  *crdt.Composite
	recs *RecordMap
}

// The part proposals live in. The name is constant and valid, so the error
// [crdt.Composite] returns for an invalid one cannot happen and is discarded.
var proposalsPart = crdt.Part{Kind: crdt.PartMap, Name: "proposals"}

// What a proposal records. A field that is absent is the ordinary case, so an
// open proposal writes nothing about being open.
const (
	propTitleField     = "\x00title"
	propOpsField       = "\x00ops"
	propBaseField      = "\x00base"
	propAcceptedField  = "\x00accepted"
	propWithdrawnField = "\x00withdrawn"
)

// A ProposalID names a proposal. It is the identity of the operation that
// created it, so it is unique across replicas, reload-safe, and carries the
// site that raised it.
type ProposalID crdt.ID

// String renders the identity in the "seq@site" notation the crdt package uses.
func (p ProposalID) String() string { return crdt.ID(p).String() }

func (p ProposalID) key() string { return encodeID(crdt.ID(p)) }

// A State is what has become of a proposal.
type State uint8

const (
	// Open is a proposal nobody has decided about.
	Open State = iota
	// Accepted is a proposal whose operations are in the document.
	Accepted
	// Withdrawn is a proposal somebody took back or turned down.
	Withdrawn
)

// String renders the state for diagnostics.
func (s State) String() string {
	switch s {
	case Accepted:
		return "accepted"
	case Withdrawn:
		return "withdrawn"
	default:
		return "open"
	}
}

// A Proposal is one proposed change as it stands.
type Proposal struct {
	// ID names it.
	ID ProposalID
	// Title is what it was put up as.
	Title string
	// Author is the replica that put it up, which is the site the proposal's
	// identity was minted by. It is not the draft's site: a draft's site is a
	// second identity the same person holds so that its operations do not
	// collide with the ones their replica is making, and it says nothing about
	// who they are.
	Author crdt.SiteID
	// State is what has become of it.
	State State
	// Base is the version of the document the draft was taken from. It is
	// informational: the operations merge whatever the document has done
	// since, and what a reader wants it for is whether the change still means
	// what its author meant — a reworded sentence somebody has since deleted
	// merges cleanly and says nothing.
	Base crdt.CompositeVersion
	// Ops is what accepting it would apply.
	Ops []crdt.PartOps
}

// A Draft is a working copy of a document, and a replica of it: edit the
// composite, then hand the draft to [Proposals.Put].
type Draft struct {
	doc  *crdt.Composite
	base crdt.CompositeVersion
}

// Composite returns the working copy to edit. It is an ordinary document —
// wrap it in whatever this package's types the real one is read through.
func (d *Draft) Composite() *crdt.Composite { return d.doc }

// Base returns the version the draft was taken at.
func (d *Draft) Base() crdt.CompositeVersion { return d.base }

// Changed reports whether the draft has anything in it the document did not.
func (d *Draft) Changed() bool { return len(d.doc.OpsSince(d.base)) > 0 }

// NewProposals returns an empty document with proposals on it, this site can
// edit.
func NewProposals(site crdt.SiteID) *Proposals { return ProposalsOf(crdt.NewComposite(site)) }

// ProposalsOf reads a composite as a document that can carry proposals. The
// proposals are one part of it; the rest of the document is whatever else it
// holds.
func ProposalsOf(doc *crdt.Composite) *Proposals {
	m, _ := doc.Map(proposalsPart.Name)
	return &Proposals{doc: doc, recs: RecordsOf(m)}
}

// LoadProposals rebuilds one from a snapshot, to be edited as site.
func LoadProposals(site crdt.SiteID, snapshot []byte) (*Proposals, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return ProposalsOf(doc), nil
}

// Composite returns the document underneath, which is what is snapshotted and
// what operations are applied to.
func (p *Proposals) Composite() *crdt.Composite { return p.doc }

// Records returns the record map the proposals live in.
func (p *Proposals) Records() *RecordMap { return p.recs }

// Site returns the replica this document edits as.
func (p *Proposals) Site() crdt.SiteID { return p.doc.Site() }

// Draft returns a working copy of the document as it stands, to be edited as
// site.
//
// site must be one no other replica is using, for the reason every replica's
// must be: a draft is a replica, and two replicas sharing a site mint one
// identity for two different operations. Passing the document's own site is
// refused, because that is the one collision this package can see coming.
func (p *Proposals) Draft(site crdt.SiteID) (*Draft, error) {
	if site == p.doc.Site() {
		return nil, crdt.ErrInvalidOp
	}
	doc := crdt.NewComposite(site)
	// A replica that holds nothing, handed everything this one holds. That is
	// what a draft is, said in operations rather than described. The error is
	// dropped because these operations validated when they were made and Apply
	// makes the same check, so it is not a branch any test could reach — the
	// composite drops the same one for the same reason.
	_ = doc.Apply(p.doc.OpsSince(nil)...)
	return &Draft{doc: doc, base: doc.Version()}, nil
}

// Put writes a draft up as a proposal, and returns its identity.
//
// A draft that changes nothing is [ErrNoChange] rather than an empty proposal:
// there is nothing for a reviewer to look at and nothing for accepting to do.
func (p *Proposals) Put(title string, d *Draft) (ProposalID, []crdt.PartOps, error) {
	if !validName(title) || d == nil {
		return ProposalID{}, nil, crdt.ErrInvalidOp
	}
	ops := d.doc.OpsSince(d.base)
	if len(ops) == 0 {
		return ProposalID{}, nil, ErrNoChange
	}
	// Both errors are dropped, and neither is reachable: AppendPartOps refuses
	// a batch that does not validate, and OpsSince only produces ones that do;
	// MarshalBinary refuses a part that could not name anything or a sequence
	// number above the ceiling, and a version taken from a live document has
	// neither.
	encoded, _ := crdt.AppendPartOps(nil, ops)
	base, _ := d.base.MarshalBinary()

	id, mint, err := mintID(p.recs.Map())
	if err != nil {
		return ProposalID{}, nil, err
	}
	proposal := ProposalID(id)
	writes := []crdt.MapOp{mint}
	for _, field := range []struct {
		name  string
		value []byte
	}{
		{propTitleField, []byte(title)},
		{propBaseField, base},
		{propOpsField, encoded},
	} {
		op, err := p.recs.SetField(proposal.key(), field.name, field.value)
		if err != nil {
			// Written so far and no further. What that leaves is a proposal
			// missing a field, which reads as no proposal at all — see the
			// decoding in Get, which wants all three.
			return ProposalID{}, nil, err
		}
		writes = append(writes, op)
	}
	return proposal, []crdt.PartOps{{Part: proposalsPart, Map: writes}}, nil
}

// Get returns one proposal.
//
// A record missing any of the three things a proposal is made of — a title, a
// base and its operations — is not one, and reads as absent rather than as a
// proposal that would do nothing. A map holds whatever key an applied operation
// names, so that is a state a peer can put this replica in.
func (p *Proposals) Get(id ProposalID) (Proposal, bool) {
	title, ok1 := p.recs.GetField(id.key(), propTitleField)
	rawBase, ok2 := p.recs.GetField(id.key(), propBaseField)
	rawOps, ok3 := p.recs.GetField(id.key(), propOpsField)
	if !ok1 || !ok2 || !ok3 {
		return Proposal{}, false
	}
	var base crdt.CompositeVersion
	if err := base.UnmarshalBinary(rawBase); err != nil {
		return Proposal{}, false
	}
	ops, err := crdt.ParsePartOps(rawOps)
	if err != nil {
		return Proposal{}, false
	}
	return Proposal{
		ID:     id,
		Title:  string(title),
		Author: crdt.ID(id).Site,
		State:  p.stateOf(id),
		Base:   base,
		Ops:    ops,
	}, true
}

// stateOf resolves what became of a proposal.
//
// Acceptance and withdrawal are two fields rather than one, and acceptance wins
// over withdrawal whichever was written last. They are not two values of one
// thing: accepting changed the document and withdrawing did not, so a
// withdrawal that arrives after an acceptance is a label on a change that is
// already in. Saying so is a rule about the state rather than an arbitration
// between two writes, and it reads the same on every replica.
func (p *Proposals) stateOf(id ProposalID) State {
	if _, ok := p.recs.GetField(id.key(), propAcceptedField); ok {
		return Accepted
	}
	if _, ok := p.recs.GetField(id.key(), propWithdrawnField); ok {
		return Withdrawn
	}
	return Open
}

// List returns every proposal in the order they were raised.
//
// The order is the (clock, site) one the map resolves its own writes by, taken
// from the write of the title — so it is a causal order where there is one, and
// settled by site where two replicas raised a proposal without seeing each
// other. Every replica reads the same list.
func (p *Proposals) List() []Proposal {
	keys := p.recs.Records()
	if len(keys) == 0 {
		return nil
	}
	out := make([]Proposal, 0, len(keys))
	raised := make(map[ProposalID]stamp, len(keys))
	for _, key := range keys {
		id, ok := decodeThing(key)
		if !ok {
			continue
		}
		proposal, ok := p.Get(ProposalID(id))
		if !ok {
			continue
		}
		// Get succeeded, so the title is a live key and has a stamp.
		clock, site, _ := p.recs.Map().Stamp(fieldKey(ProposalID(id).key(), propTitleField))
		raised[ProposalID(id)] = stamp{clock: clock, site: site}
		out = append(out, proposal)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := raised[out[i].ID], raised[out[j].ID]
		if a.clock != b.clock {
			return a.clock < b.clock
		}
		return a.site < b.site
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

// a stamp is when a write happened, in the only terms this package has for it.
type stamp struct {
	clock uint64
	site  crdt.SiteID
}

// Open returns the proposals nobody has decided about, oldest first.
func (p *Proposals) Open() []Proposal {
	var out []Proposal
	for _, proposal := range p.List() {
		if proposal.State == Open {
			out = append(out, proposal)
		}
	}
	return out
}

// Preview returns the document as it would read with a proposal in it, without
// putting it in: a copy, loaded as site, with the operations applied.
//
// site is what the copy would edit as if the caller went on to edit it, and is
// subject to the same rule [Proposals.Draft] states. Nothing here writes to it.
func (p *Proposals) Preview(id ProposalID, site crdt.SiteID) (*crdt.Composite, error) {
	proposal, ok := p.Get(id)
	if !ok {
		return nil, crdt.ErrInvalidOp
	}
	doc := crdt.NewComposite(site)
	// Neither error is reachable. The first is the one [Proposals.Draft]
	// drops. The second is refused only for a batch that does not validate,
	// and the operations came back through ParsePartOps, which refuses a part
	// that could not name anything and fills the one slice its kind allows —
	// which is the whole of what Apply checks.
	_ = doc.Apply(p.doc.OpsSince(nil)...)
	_ = doc.Apply(proposal.Ops...)
	return doc, nil
}

// Accept applies a proposal's operations to the document and records that it
// was accepted.
//
// There is no rebase and nothing to settle. The operations merge with whatever
// the document has done since the draft was taken, exactly as a replica coming
// back from a week offline does, because that is what they are.
//
// Accepting one that is already accepted applies the operations again, which
// changes nothing — they are the same operations — and writes the field again.
// Accepting a withdrawn one is allowed and makes it accepted: somebody changed
// their mind, and the document is what says so.
func (p *Proposals) Accept(id ProposalID) ([]crdt.PartOps, error) {
	proposal, ok := p.Get(id)
	if !ok {
		return nil, crdt.ErrInvalidOp
	}
	// The mark goes first. Neither write can be taken back, so whichever fails
	// second leaves the other done: this way a failure leaves a proposal marked
	// accepted whose operations are not in yet, which accepting again puts
	// right. The other way round leaves the change in the document with nothing
	// saying where it came from.
	mark, err := p.recs.SetField(id.key(), propAcceptedField, nil)
	if err != nil {
		return nil, err
	}
	// Dropped for the reason [Proposals.Preview] gives: operations that came
	// back through ParsePartOps have already passed the check Apply makes.
	_ = p.doc.Apply(proposal.Ops...)
	out := append([]crdt.PartOps(nil), proposal.Ops...)
	return append(out, crdt.PartOps{Part: proposalsPart, Map: []crdt.MapOp{mark}}), nil
}

// Withdraw records that a proposal was taken back or turned down. Its
// operations were never applied, so there is nothing to take out of the
// document; withdrawing costs one write and leaves the proposal readable, which
// is what a record of a decision is for.
//
// A withdrawal of a proposal somebody else has already accepted is a label. See
// [Proposals.Accept].
func (p *Proposals) Withdraw(id ProposalID) (crdt.PartOps, error) {
	if _, ok := p.Get(id); !ok {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	op, err := p.recs.SetField(id.key(), propWithdrawnField, nil)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: proposalsPart, Map: []crdt.MapOp{op}}, nil
}

// Forget takes a proposal's record out of the document, which is what an
// accepted one is once nobody needs to read what it was: its operations are in
// the document, and the record is a copy of them.
//
// It does not take the change out. Nothing does; see [Undo].
func (p *Proposals) Forget(id ProposalID) ([]crdt.PartOps, error) {
	if _, ok := p.Get(id); !ok {
		return nil, crdt.ErrInvalidOp
	}
	ops, err := p.recs.DeleteRecord(id.key())
	if err != nil {
		return nil, err
	}
	return []crdt.PartOps{{Part: proposalsPart, Map: ops}}, nil
}

// Snapshot returns the document as bytes, proposals included.
func (p *Proposals) Snapshot() []byte { return p.doc.Snapshot() }

// Version returns what this replica holds.
func (p *Proposals) Version() crdt.CompositeVersion { return p.doc.Version() }

// OpsSince returns the operations a peer at v has not seen.
func (p *Proposals) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return p.doc.OpsSince(v) }

// Apply takes operations from a peer.
func (p *Proposals) Apply(batches ...crdt.PartOps) error { return p.doc.Apply(batches...) }

// Pending reports how many operations are held back waiting for ones they
// depend on.
func (p *Proposals) Pending() int { return p.doc.Pending() }
