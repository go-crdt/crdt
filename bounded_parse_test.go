package crdt

import (
	"errors"
	"testing"
)

// A caller may bound how many operations a message is allowed to claim, and the
// bound is refused before anything is reserved for it.
//
// The counted headers already refuse a claim larger than the remaining bytes could
// hold. What they cannot refuse is a message that is honest and enormous: the worst
// claim they permit still reserves sixteen to twenty-four times the input, so a
// gibibyte of real operations is sixteen to twenty-four gibibytes of records. See
// [ErrTooManyOps] and go-crdt/collab#169.
//
// Every row checks the boundary as well as the refusal, because an off-by-one here
// would either refuse a message the caller allowed or admit one more than it did.
func TestParsingRefusesMoreOperationsThanAllowed(t *testing.T) {
	// Three messages of three operations each, one per kind.
	text := func() []byte {
		d := New(7)
		ops, err := d.Insert(0, "abc")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := AppendOps(nil, ops)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()
	list := func() []byte {
		l := NewList(7)
		var ops []ListOp
		for _, v := range []string{"a", "b", "c"} {
			got, err := l.Insert(l.Len(), []byte(v))
			if err != nil {
				t.Fatal(err)
			}
			ops = append(ops, got...)
		}
		raw, err := AppendListOps(nil, ops)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()
	mapMsg := func() []byte {
		m := NewMap(7)
		var ops []MapOp
		for _, k := range []string{"a", "b", "c"} {
			got, err := m.Set(k, []byte("v"))
			if err != nil {
				t.Fatal(err)
			}
			ops = append(ops, got)
		}
		raw, err := AppendMapOps(nil, ops)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()

	for _, c := range []struct {
		kind  string
		parse func([]byte, int) (int, error)
		msg   []byte
	}{{
		"text", func(b []byte, max int) (int, error) {
			ops, err := ParseOpsLimit(b, max)
			return len(ops), err
		}, text,
	}, {
		"list", func(b []byte, max int) (int, error) {
			ops, err := ParseListOpsLimit(b, max)
			return len(ops), err
		}, list,
	}, {
		"map", func(b []byte, max int) (int, error) {
			ops, err := ParseMapOpsLimit(b, max)
			return len(ops), err
		}, mapMsg,
	}} {
		t.Run(c.kind, func(t *testing.T) {
			if _, err := c.parse(c.msg, 2); !errors.Is(err, ErrTooManyOps) {
				t.Errorf("a limit of 2 on three operations gave %v, want ErrTooManyOps", err)
			}
			// Exactly the limit is allowed: the bound is what the caller permits,
			// not one less.
			got, err := c.parse(c.msg, 3)
			if err != nil {
				t.Errorf("a limit of 3 on three operations refused them: %v", err)
			} else if got != 3 {
				t.Errorf("parsed %d operations, want 3", got)
			}
			// Zero is unlimited, and is what the unbounded entry point passes.
			if _, err := c.parse(c.msg, 0); err != nil {
				t.Errorf("a limit of zero refused the message: %v", err)
			}
		})
	}
}

// A composite's bound is for the MESSAGE, not for each batch.
//
// That is the claim ParsePartOpsLimit makes, and it is the one worth a test: a
// thousand batches of a thousand operations costs what one of a million costs, so a
// per-batch bound would refuse the second while waving the first through.
func TestACompositeBoundCoversTheWholeMessage(t *testing.T) {
	c := NewComposite(7)
	body, err := c.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := body.Insert(0, "abc"); err != nil {
		t.Fatal(err)
	}
	cells, err := c.Map("cells")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"x", "y"} {
		if _, err := cells.Set(k, []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := AppendPartOps(nil, c.OpsSince(nil))
	if err != nil {
		t.Fatal(err)
	}

	// Five operations across two batches: three characters and two keys. Neither
	// batch alone exceeds four, so a per-batch bound of four would admit the
	// message; a message bound of four must refuse it.
	if _, err := ParsePartOpsLimit(raw, 4); !errors.Is(err, ErrTooManyOps) {
		t.Errorf("a message bound of 4 admitted five operations across two batches: %v", err)
	}
	batches, err := ParsePartOpsLimit(raw, 5)
	if err != nil {
		t.Fatalf("a message bound of 5 refused five operations: %v", err)
	}
	total := 0
	for _, b := range batches {
		total += len(b.Text) + len(b.List) + len(b.Map)
	}
	if total != 5 {
		t.Errorf("parsed %d operations across %d batches, want 5", total, len(batches))
	}
	if _, err := ParsePartOpsLimit(raw, 0); err != nil {
		t.Errorf("a limit of zero refused the message: %v", err)
	}
	// And the unbounded entry point is still unbounded.
	if _, err := ParsePartOps(raw); err != nil {
		t.Errorf("ParsePartOps refused a message it used to accept: %v", err)
	}
}
