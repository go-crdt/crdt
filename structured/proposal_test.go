package structured

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

// body is the one text part these documents are made of, which is enough to
// show what a proposal does to a document people are editing.
func body(t *testing.T, doc *crdt.Composite) *crdt.Doc {
	t.Helper()
	text, err := doc.Text("text")
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func mustWrite(t *testing.T, doc *crdt.Composite, pos int, s string) {
	t.Helper()
	if _, err := body(t, doc).Insert(pos, s); err != nil {
		t.Fatal(err)
	}
}

func mustDraft(t *testing.T, p *Proposals, site crdt.SiteID) *Draft {
	t.Helper()
	d, err := p.Draft(site)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustPropose(t *testing.T, p *Proposals, title string, d *Draft) ProposalID {
	t.Helper()
	id, _, err := p.Put(title, d)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func listed(p *Proposals) string {
	parts := make([]string, 0, 4)
	for _, proposal := range p.List() {
		parts = append(parts, fmt.Sprintf("%s/%s/%d", proposal.Title, proposal.State, proposal.Author))
	}
	return strings.Join(parts, " ")
}

// A change is drafted, put up, read by somebody else, and accepted.
func TestAChangeIsDraftedAndAccepted(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	if docs.List() != nil || docs.Open() != nil {
		t.Fatalf("a fresh document has proposals: %q", listed(docs))
	}

	draft := mustDraft(t, docs, 2)
	if draft.Changed() {
		t.Fatal("a fresh draft has changed something")
	}
	mustWrite(t, draft.Composite(), 6, "beautiful ")
	if !draft.Changed() {
		t.Fatal("an edited draft says it has changed nothing")
	}
	id := mustPropose(t, docs, "an adjective", draft)

	// The document has not moved: a proposal is not a change to it.
	if got := body(t, docs.Composite()).String(); got != "Hello world" {
		t.Fatalf("putting a proposal up changed the document to %q", got)
	}
	proposal, ok := docs.Get(id)
	if !ok || proposal.Title != "an adjective" || proposal.State != Open || proposal.Author != 1 {
		t.Fatalf("Get = %+v, %v", proposal, ok)
	}
	if len(proposal.Ops) == 0 {
		t.Fatal("the proposal carries no operations")
	}
	if got := listed(docs); got != "an adjective/open/1" {
		t.Fatalf("List = %q", got)
	}

	// A reader can see what it would do without doing it.
	preview, err := docs.Preview(id, 9)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, preview).String(); got != "Hello beautiful world" {
		t.Fatalf("the preview reads %q", got)
	}
	if got := body(t, docs.Composite()).String(); got != "Hello world" {
		t.Fatalf("previewing changed the document to %q", got)
	}

	if _, err := docs.Accept(id); err != nil {
		t.Fatal(err)
	}
	if got := body(t, docs.Composite()).String(); got != "Hello beautiful world" {
		t.Fatalf("after accepting the document reads %q", got)
	}
	if got := listed(docs); got != "an adjective/accepted/1" {
		t.Fatalf("List = %q", got)
	}
	if docs.Open() != nil {
		t.Fatalf("an accepted proposal is still open: %q", listed(docs))
	}
}

// The claim this type exists for: accepting a proposal does not disturb what
// anything else in the document is anchored to.
//
// A comparison of two texts produces a difference, and applying a difference
// mints new characters — so every comment, mark and cursor hanging off the
// characters it replaced would be pointing at something that no longer exists.
// A proposal is made of the document's own operations instead.
func TestAcceptingLeavesEveryAnchorWhereItWas(t *testing.T) {
	docs := NewProposals(1)
	text := body(t, docs.Composite())
	mustWrite(t, docs.Composite(), 0, "Hello world")

	// Somebody has a comment on the 'w' of world, and something is emphasised.
	anchor, err := text.Anchor(6)
	if err != nil {
		t.Fatal(err)
	}
	rich := RichTextOf(docs.Composite())
	if _, err := rich.Doc().Insert(rich.Doc().Len(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := rich.Mark(6, 11, "comment", []byte("which world?"), ExpandNone); err != nil {
		t.Fatal(err)
	}

	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 6, "beautiful ")
	id := mustPropose(t, docs, "an adjective", draft)
	if _, err := docs.Accept(id); err != nil {
		t.Fatal(err)
	}

	if got := text.String(); got != "Hello beautiful world" {
		t.Fatalf("the document reads %q", got)
	}
	pos, ok := text.Position(anchor)
	if !ok {
		t.Fatal("the anchor names a character the document no longer has")
	}
	if pos != 16 {
		t.Fatalf("the anchor is at %d, want 16 — the same character, moved along", pos)
	}
	if !text.Visible(anchor) {
		t.Fatal("the anchored character was replaced rather than moved")
	}
	// And the comment still covers "world" and nothing else.
	if m := rich.MarksAt(16); string(m["comment"]) != "which world?" {
		t.Fatalf("the comment at the anchor is %v", m)
	}
	if m := rich.MarksAt(6); m != nil {
		t.Fatalf("the comment spread over the new text: %v", m)
	}
}

// A proposal is a replica that has not synced, so accepting it merges with
// whatever the document did while it was open. There is no rebase and nothing
// to settle.
func TestAcceptingMergesWithWhatHappenedMeanwhile(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")

	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 6, "beautiful ")
	id := mustPropose(t, docs, "an adjective", draft)

	// While the proposal sits there, the document moves on — at the other end
	// of the very sentence the proposal touches.
	mustWrite(t, docs.Composite(), 11, ", and goodbye")
	if got := body(t, docs.Composite()).String(); got != "Hello world, and goodbye" {
		t.Fatalf("the document reads %q", got)
	}

	if _, err := docs.Accept(id); err != nil {
		t.Fatal(err)
	}
	want := "Hello beautiful world, and goodbye"
	if got := body(t, docs.Composite()).String(); got != want {
		t.Fatalf("after accepting the document reads %q, want %q", got, want)
	}
}

// Two proposals raised against the same version, touching the same sentence,
// both accepted: two people editing offline is what this already is.
func TestTwoProposalsAgainstTheSameTextBothLand(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")

	first := mustDraft(t, docs, 2)
	mustWrite(t, first.Composite(), 6, "beautiful ")
	second := mustDraft(t, docs, 3)
	mustWrite(t, second.Composite(), 5, ",")

	a := mustPropose(t, docs, "an adjective", first)
	b := mustPropose(t, docs, "a comma", second)
	if _, err := docs.Accept(a); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Accept(b); err != nil {
		t.Fatal(err)
	}
	want := "Hello, beautiful world"
	if got := body(t, docs.Composite()).String(); got != want {
		t.Fatalf("both accepted reads %q, want %q", got, want)
	}
	if got := listed(docs); got != "an adjective/accepted/1 a comma/accepted/1" {
		t.Fatalf("List = %q", got)
	}
}

// Turning a proposal down costs nothing and leaves nothing, because its
// operations were never applied. That is not true of anything already in the
// document.
func TestWithdrawingLeavesTheDocumentAlone(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	before := body(t, docs.Composite()).String()

	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 0, "Goodbye. ")
	id := mustPropose(t, docs, "a farewell", draft)
	if _, err := docs.Withdraw(id); err != nil {
		t.Fatal(err)
	}
	if got := body(t, docs.Composite()).String(); got != before {
		t.Fatalf("withdrawing changed the document to %q", got)
	}
	if got := listed(docs); got != "a farewell/withdrawn/1" {
		t.Fatalf("List = %q", got)
	}
	// It is still readable, which is what a record of a decision is for.
	if proposal, ok := docs.Get(id); !ok || proposal.State != Withdrawn {
		t.Fatalf("Get = %+v, %v", proposal, ok)
	}
	// And forgetting it takes the record away without touching the document.
	if _, err := docs.Forget(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := docs.Get(id); ok {
		t.Fatal("a forgotten proposal is still readable")
	}
	if got := body(t, docs.Composite()).String(); got != before {
		t.Fatalf("forgetting changed the document to %q", got)
	}
}

// Accepting and withdrawing are not two values of one field. Accepting changed
// the document and withdrawing did not, so a withdrawal that arrives after an
// acceptance is a label on a change that is already in — and every replica
// reads it the same way whichever order the two arrive in.
func TestAcceptingWinsOverWithdrawingInEitherOrder(t *testing.T) {
	for _, order := range []string{"accept first", "withdraw first"} {
		ada := NewProposals(1)
		mustWrite(t, ada.Composite(), 0, "Hello world")
		draft := mustDraft(t, ada, 2)
		mustWrite(t, draft.Composite(), 6, "beautiful ")
		id := mustPropose(t, ada, "an adjective", draft)

		grace, err := LoadProposals(3, ada.Snapshot())
		if err != nil {
			t.Fatal(err)
		}

		var accept []crdt.PartOps
		var withdraw crdt.PartOps
		if order == "accept first" {
			accept, err = ada.Accept(id)
			if err != nil {
				t.Fatal(err)
			}
			withdraw, err = grace.Withdraw(id)
		} else {
			withdraw, err = grace.Withdraw(id)
			if err != nil {
				t.Fatal(err)
			}
			accept, err = ada.Accept(id)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := ada.Apply(withdraw); err != nil {
			t.Fatal(err)
		}
		if err := grace.Apply(accept...); err != nil {
			t.Fatal(err)
		}
		for name, p := range map[string]*Proposals{"ada": ada, "grace": grace} {
			proposal, _ := p.Get(id)
			if proposal.State != Accepted {
				t.Fatalf("%s (%s) reads %s, want accepted", name, order, proposal.State)
			}
			if got := body(t, p.Composite()).String(); got != "Hello beautiful world" {
				t.Fatalf("%s (%s) reads %q", name, order, got)
			}
		}
	}
}

// Accepting one that is already accepted applies the same operations again,
// which changes nothing; accepting a withdrawn one is somebody changing their
// mind, and the document is what says so.
func TestAcceptingTwiceAndAcceptingAWithdrawnOne(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 6, "beautiful ")
	id := mustPropose(t, docs, "an adjective", draft)

	if _, err := docs.Accept(id); err != nil {
		t.Fatal(err)
	}
	want := body(t, docs.Composite()).String()
	if _, err := docs.Accept(id); err != nil {
		t.Fatal(err)
	}
	if got := body(t, docs.Composite()).String(); got != want {
		t.Fatalf("accepting twice reads %q, want %q", got, want)
	}

	other := mustDraft(t, docs, 3)
	mustWrite(t, other.Composite(), 0, "PS. ")
	second := mustPropose(t, docs, "a postscript", other)
	if _, err := docs.Withdraw(second); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Accept(second); err != nil {
		t.Fatal(err)
	}
	if proposal, _ := docs.Get(second); proposal.State != Accepted {
		t.Fatalf("the withdrawn proposal reads %s after being accepted", proposal.State)
	}
	if got := body(t, docs.Composite()).String(); !strings.HasPrefix(got, "PS. ") {
		t.Fatalf("the document reads %q", got)
	}
}

// The proposals travel with the document, and every replica reads the same list
// in the same order.
func TestProposalsTravelWithTheDocument(t *testing.T) {
	ada := NewProposals(1)
	mustWrite(t, ada.Composite(), 0, "Hello world")

	for i, title := range []string{"first", "second", "third"} {
		draft := mustDraft(t, ada, crdt.SiteID(10+i))
		mustWrite(t, draft.Composite(), 0, strings.Repeat("x", i+1))
		mustPropose(t, ada, title, draft)
	}

	grace, err := LoadProposals(2, ada.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := listed(grace), listed(ada); got != want {
		t.Fatalf("grace reads %q, want %q", got, want)
	}
	if got := listed(grace); got != "first/open/1 second/open/1 third/open/1" {
		t.Fatalf("List = %q, want the order they were raised in", got)
	}
	if grace.Site() != 2 || grace.Pending() != 0 || grace.Composite() == nil || grace.Records() == nil {
		t.Fatalf("Site = %d, Pending = %d", grace.Site(), grace.Pending())
	}
	if got := grace.Version(); len(got) == 0 {
		t.Fatal("the reloaded document promises nothing")
	}
	if got := grace.OpsSince(grace.Version()); len(got) != 0 {
		t.Fatalf("%d operations owed to itself", len(got))
	}
	// And grace can accept one Ada raised.
	if _, err := grace.Accept(grace.List()[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := body(t, grace.Composite()).String(); got != "xHello world" {
		t.Fatalf("grace reads %q", got)
	}
	if _, err := LoadProposals(1, []byte("not a snapshot")); err == nil {
		t.Fatal("a snapshot that is not one loaded")
	}
}

// Everything a caller can get wrong, and what it is told.
func TestWhatProposalsRefuse(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	gone := ProposalID{Site: 7, Seq: 7}

	// A draft is a replica, so it cannot be the one already editing.
	if _, err := docs.Draft(1); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Draft with the document's own site = %v, want invalid", err)
	}

	// A draft that changed nothing is not a proposal.
	empty := mustDraft(t, docs, 2)
	if _, _, err := docs.Put("nothing at all", empty); !errors.Is(err, ErrNoChange) {
		t.Fatalf("Put of an untouched draft = %v, want no change", err)
	}

	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 0, "x")
	for _, bad := range []string{"", "\xff\xfe"} {
		if _, _, err := docs.Put(bad, draft); !errors.Is(err, crdt.ErrInvalidOp) {
			t.Fatalf("Put(%q) = %v, want invalid", bad, err)
		}
	}
	if _, _, err := docs.Put("no draft", nil); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatal("Put with no draft was accepted")
	}

	// And every operation on a proposal nobody has heard of.
	if _, ok := docs.Get(gone); ok {
		t.Fatal("a proposal nobody has heard of reads")
	}
	if _, err := docs.Accept(gone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Accept of a stranger = %v", err)
	}
	if _, err := docs.Withdraw(gone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Withdraw of a stranger = %v", err)
	}
	if _, err := docs.Forget(gone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Forget of a stranger = %v", err)
	}
	if _, err := docs.Preview(gone, 9); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Preview of a stranger = %v", err)
	}
	if got := Open.String() + " " + Accepted.String() + " " + Withdrawn.String() + " " + State(9).String(); got != "open accepted withdrawn open" {
		t.Fatalf("the states print as %q", got)
	}
}

// A map holds whatever key an applied operation names, so a record that is not
// a proposal has to read as one that is not there rather than as a proposal
// that would do nothing.
func TestARecordThatIsNotAProposal(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 0, "x")
	good := mustPropose(t, docs, "a real one", draft)

	recs := docs.Records()
	whole, _ := recs.GetField(good.key(), propOpsField)
	base, _ := recs.GetField(good.key(), propBaseField)

	bad := []struct {
		name   string
		key    string
		fields map[string][]byte
	}{
		{"no title", "9.1", map[string][]byte{propBaseField: base, propOpsField: whole}},
		{"no base", "9.2", map[string][]byte{propTitleField: []byte("t"), propOpsField: whole}},
		{"no operations", "9.3", map[string][]byte{propTitleField: []byte("t"), propBaseField: base}},
		{"a base that is not one", "9.4", map[string][]byte{
			propTitleField: []byte("t"), propBaseField: []byte("\xff\xff"), propOpsField: whole}},
		{"operations that are not any", "9.5", map[string][]byte{
			propTitleField: []byte("t"), propBaseField: base, propOpsField: []byte("\xff\xff")}},
	}
	for _, c := range bad {
		for field, value := range c.fields {
			if _, err := recs.SetField(c.key, field, value); err != nil {
				t.Fatal(err)
			}
		}
		id, _ := decodeID(c.key)
		if _, ok := docs.Get(ProposalID(id)); ok {
			t.Fatalf("%s reads as a proposal", c.name)
		}
	}
	// A record whose key is not an identity at all is not one either.
	if _, err := recs.SetField("not-an-identity", propTitleField, []byte("t")); err != nil {
		t.Fatal(err)
	}
	// The list is the one real proposal, and nothing else.
	if got := listed(docs); got != "a real one/open/1" {
		t.Fatalf("List = %q, want only the well-formed one", got)
	}
}

// With no clock left a replica writes nothing and says so, and what a half-
// written proposal leaves is a record that does not read as a proposal.
func TestProposalsWithNoClockLeft(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 0, "x")
	id := mustPropose(t, docs, "a real one", draft)

	other := mustDraft(t, docs, 3)
	mustWrite(t, other.Composite(), 0, "y")

	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Key: "seed", Value: []byte("x")}
	if err := docs.Apply(crdt.PartOps{Part: proposalsPart, Map: []crdt.MapOp{top}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := docs.Put("no room", other); err == nil {
		t.Fatal("putting a proposal up with no clock left was accepted")
	}
	if _, err := docs.Accept(id); err == nil {
		t.Fatal("accepting with no clock left was accepted")
	}
	if _, err := docs.Withdraw(id); err == nil {
		t.Fatal("withdrawing with no clock left was accepted")
	}
	if _, err := docs.Forget(id); err == nil {
		t.Fatal("forgetting with no clock left was accepted")
	}
	// Accepting failed at the mark, which is written first, so the change is
	// not in the document and the proposal is still open.
	if got := body(t, docs.Composite()).String(); got != "Hello world" {
		t.Fatalf("a refused acceptance changed the document to %q", got)
	}
	if got := listed(docs); got != "a real one/open/1" {
		t.Fatalf("List = %q", got)
	}
}

// Half of Put, with one tick left: the identity is minted and the title cannot
// be written, so what is left does not read as a proposal.
func TestAProposalThatCouldNotBeWrittenIsNotOne(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	draft := mustDraft(t, docs, 2)
	mustWrite(t, draft.Composite(), 0, "x")

	nearlyTop := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock - 1, Key: "seed", Value: []byte("x")}
	if err := docs.Apply(crdt.PartOps{Part: proposalsPart, Map: []crdt.MapOp{nearlyTop}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := docs.Put("no room", draft); err == nil {
		t.Fatal("a half-written proposal was accepted")
	}
	if got := docs.List(); got != nil {
		t.Fatalf("List = %v, want nothing readable", got)
	}
	if got := body(t, docs.Composite()).String(); got != "Hello world" {
		t.Fatalf("the document reads %q", got)
	}
}

// A draft is a replica handed everything this one holds, so it holds the same
// document — down to the bytes, and with nothing waiting on anything.
func TestADraftHoldsTheSameDocument(t *testing.T) {
	docs := NewProposals(1)
	mustWrite(t, docs.Composite(), 0, "Hello world")
	rich := RichTextOf(docs.Composite())
	if _, err := rich.Mark(0, 5, "bold", nil, ExpandEnd); err != nil {
		t.Fatal(err)
	}
	labels := SetOf(mapPart(t, docs.Composite(), "labels"))
	if _, err := labels.Add("urgent"); err != nil {
		t.Fatal(err)
	}

	draft := mustDraft(t, docs, 2)
	if draft.Composite().Pending() != 0 {
		t.Fatalf("%d operations are waiting in a fresh draft", draft.Composite().Pending())
	}
	if got := body(t, draft.Composite()).String(); got != "Hello world" {
		t.Fatalf("the draft reads %q", got)
	}
	if !SetOf(mapPart(t, draft.Composite(), "labels")).Contains("urgent") {
		t.Fatal("the draft is missing a part of the document")
	}
	if !draft.Base().Equal(docs.Version()) {
		t.Fatal("the draft's base is not the document's version")
	}
	if got := len(draft.Base()); got != len(docs.Version()) {
		t.Fatalf("the draft holds %d parts, the document %d", got, len(docs.Version()))
	}
}

func mapPart(t *testing.T, doc *crdt.Composite, name string) *crdt.Map {
	t.Helper()
	m, err := doc.Map(name)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Two replicas that raise a proposal without seeing each other stamp them with
// the same clock, and the list is still an order — settled by site, the same on
// both.
func TestTwoProposalsRaisedAtOnceStillReadInOneOrder(t *testing.T) {
	ada := NewProposals(1)
	mustWrite(t, ada.Composite(), 0, "Hello world")
	grace, err := LoadProposals(2, ada.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	adaDraft := mustDraft(t, ada, 11)
	mustWrite(t, adaDraft.Composite(), 0, "A")
	graceDraft := mustDraft(t, grace, 12)
	mustWrite(t, graceDraft.Composite(), 0, "B")

	fromAda := putBatches(t, ada, "from ada", adaDraft)
	fromGrace := putBatches(t, grace, "from grace", graceDraft)
	if err := ada.Apply(fromGrace...); err != nil {
		t.Fatal(err)
	}
	if err := grace.Apply(fromAda...); err != nil {
		t.Fatal(err)
	}

	// Site 1 before site 2, on both, because the clocks are equal.
	want := "from ada/open/1 from grace/open/2"
	if got := listed(ada); got != want {
		t.Fatalf("ada lists %q, want %q", got, want)
	}
	if got := listed(grace); got != want {
		t.Fatalf("grace lists %q, want %q", got, want)
	}
	// And both are open, which is the only thing Open leaves in.
	if got := len(ada.Open()); got != 2 {
		t.Fatalf("%d open, want 2", got)
	}
	if got := ada.List()[0].ID.String(); got == "" {
		t.Fatal("a proposal identity prints as nothing")
	}
}

func putBatches(t *testing.T, p *Proposals, title string, d *Draft) []crdt.PartOps {
	t.Helper()
	_, batches, err := p.Put(title, d)
	if err != nil {
		t.Fatal(err)
	}
	return batches
}

// A document holding nothing but records that are not proposals lists nothing,
// rather than listing an empty proposal.
func TestADocumentOfRubbishListsNothing(t *testing.T) {
	docs := NewProposals(1)
	if _, err := docs.Records().SetField("9.1", propTitleField, []byte("t")); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Records().SetField("not-an-identity", propTitleField, []byte("t")); err != nil {
		t.Fatal(err)
	}
	if got := docs.List(); got != nil {
		t.Fatalf("List = %v, want nothing", got)
	}
	if got := docs.Open(); got != nil {
		t.Fatalf("Open = %v, want nothing", got)
	}
}
