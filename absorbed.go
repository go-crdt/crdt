package crdt

// The operations a call to Apply integrated, which are not the operations it
// was given.
//
// Apply parks an operation whose causal predecessors have not arrived and
// releases it later when they do, and it ignores one it already had. So a batch
// can integrate nothing now and a great deal three batches later, and what it
// releases was in no batch anybody sent.
//
// Anything that passes operations on — a server relaying a participant's edit
// to the others, a bridge between two transports — needs exactly this set and
// not the batch it was handed. Passing on more than was integrated is how two
// replicas that follow each other talk for ever; passing on less is how a peer
// ends up missing an operation nobody will ever send it again, because
// everybody else has it by then.
//
// The replica knows without looking: it integrated them. Working the same set
// out afterwards means asking what it holds that a version does not, which
// walks the whole document — 383µs on sixty thousand operations against 90ns
// for a version that is current — and turns a relay into something quadratic in
// its own history.

// ApplyAbsorbed is [Doc.Apply], and also reports the operations it integrated,
// including any that had been parked waiting for them.
//
// The order is the order they were integrated in, which is a causal order: an
// operation appears after the one it was waiting for.
func (d *Doc) ApplyAbsorbed(ops ...Op) ([]Op, error) {
	var absorbed []Op
	if _, err := d.applyWith(false, ops, &absorbed); err != nil {
		return nil, err
	}
	return absorbed, nil
}

// ApplyAbsorbed is [List.Apply], and also reports the operations it integrated,
// including any that had been parked waiting for them.
func (l *List) ApplyAbsorbed(ops ...ListOp) ([]ListOp, error) {
	var absorbed []ListOp
	if _, err := l.applyWith(false, ops, &absorbed); err != nil {
		return nil, err
	}
	return absorbed, nil
}

// ApplyAbsorbed is [Map.Apply], and also reports the operations it integrated,
// including any that had been parked waiting for them.
func (m *Map) ApplyAbsorbed(ops ...MapOp) ([]MapOp, error) {
	var absorbed []MapOp
	if _, err := m.applyWith(false, ops, &absorbed); err != nil {
		return nil, err
	}
	return absorbed, nil
}

// ApplyAbsorbed is [Composite.Apply], and also reports what it integrated, per
// part, including operations that had been parked waiting for them.
//
// A part that integrated nothing is not in the result, so an empty result means
// this replica learned nothing — which is exactly when a relay has nothing to
// pass on and a loop between two replicas has to stop.
func (c *Composite) ApplyAbsorbed(batches ...PartOps) ([]PartOps, error) {
	for _, b := range batches {
		if err := b.validate(); err != nil {
			return nil, err
		}
	}
	var out []PartOps
	for _, b := range batches {
		// Each part validates with the same function that has just passed
		// here, so these cannot fail; the errors are dropped rather than turned
		// into branches no input can reach, as [Composite.Apply] does.
		got := PartOps{Part: b.Part}
		switch b.Part.Kind {
		case PartText:
			got.Text, _ = c.text(b.Part.Name).ApplyAbsorbed(b.Text...)
		case PartList:
			got.List, _ = c.list(b.Part.Name).ApplyAbsorbed(b.List...)
		default:
			got.Map, _ = c.mapPart(b.Part.Name).ApplyAbsorbed(b.Map...)
		}
		if len(got.Text)+len(got.List)+len(got.Map) > 0 {
			out = append(out, got)
		}
	}
	return out, nil
}
