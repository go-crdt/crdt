package structured

import "strings"

// Ordering siblings, without an ordered list per parent.
//
// A tree needs to say what comes before what among the children of one node,
// and a node changes parents. Keeping a [crdt.List] per parent would mean
// moving a node was a delete from one list and an insert into another — two
// operations that can be split by a concurrent move, leaving the node in both
// lists or in neither.
//
// So a node carries its order as a value instead: a rank, and siblings are read
// in order of it. A rank is a string, compared as bytes, and between any two of
// them there is always another — which is what makes "put this between those
// two" a single write of one field rather than a change to a shared list.
//
// Ranks are never equal on purpose, but two replicas can mint the same one at
// the same place at the same time. That is why the order is (rank, identity)
// and not rank alone: the identity is unique, so the order is total, and both
// replicas read the pair the same way round.

// rankDigits is the alphabet ranks are written in, in ascending byte order, so
// that comparing two ranks as strings compares them as numbers.
const rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// rankDigit is where c sits in the alphabet. A character that is not in it can
// only have come from a peer writing a rank by hand; it reads as the lowest
// rather than being refused, because a strange order is recoverable and a
// refused operation is a replica that has stopped converging.
func rankDigit(c byte) int {
	if i := strings.IndexByte(rankDigits, c); i >= 0 {
		return i
	}
	return 0
}

// rankBetween returns a rank strictly between lo and hi.
//
// An empty lo means the start of the order and an empty hi means the end, so
// rankBetween("", "") is the rank of the first child of a node.
func rankBetween(lo, hi string) string {
	if hi != "" && lo >= hi {
		// Not an ordering this can answer. Rather than refuse — which would
		// make an insert fail because a peer sent a rank out of order — it
		// answers below hi, which is where the caller asked to be.
		lo = ""
	}
	// What lo and hi share is shared by everything between them, so it is
	// carried across and the question asked again about the rest.
	n := 0
	for n < len(lo) && n < len(hi) && lo[n] == hi[n] {
		n++
	}
	if n > 0 {
		return lo[:n] + rankBetween(lo[n:], hi[n:])
	}

	loDigit := 0
	if lo != "" {
		loDigit = rankDigit(lo[0])
	}
	hiDigit := len(rankDigits)
	if hi != "" {
		hiDigit = rankDigit(hi[0])
	}
	if hiDigit-loDigit > 1 {
		return string(rankDigits[(loDigit+hiDigit)/2])
	}

	// The two are adjacent digits, so there is no room at this length and the
	// answer is one character longer. Descending under hi's digit only works if
	// hi has something after it to stay below; otherwise the answer descends
	// under lo's, where there is always room above.
	if len(hi) > 1 {
		return hi[:1] + rankBetween("", hi[1:])
	}
	rest := ""
	if lo != "" {
		rest = lo[1:]
	}
	return string(rankDigits[loDigit]) + rankBetween(rest, "")
}
