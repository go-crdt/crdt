package crdt

import (
	"encoding/binary"
	"sort"
	"testing"
)

// Where a snapshot's bytes go.
//
// docs/performance.md records that a real document encodes to 620 KB here and
// 109 KB in diamond-types, and attributes the gap to the text: "ours is 182 KB
// of characters written plainly, which already exceeds their whole document."
// That reading makes compressing the text look like the fix.
//
// It is worth checking before building anything, because if the text is 182 KB
// of 620 then something else is 438 KB — four times diamond-types' entire
// document — and compressing the text would leave most of the gap standing.
// This test is the check. It accounts for every byte Snapshot writes, by the
// field that wrote it, so the next decision is made against the shape of the
// cost rather than against a plausible story about it.
//
// It asserts only that the accounting is complete: the parts have to add up to
// the whole, or the numbers below are decoration. What the numbers *say* is for
// a person to read in the log.
func TestSnapshotBudget(t *testing.T) {
	patches, _ := loadTrace(t)

	d := New(1)
	replay(t, d, patches)

	var (
		header   int // magic, version, the version-vector table
		runCount int // the count of runs, and per-run text lengths and del counts
		ids      int // run identity: site and seq
		clocks   int
		origins  int
		text     int
		dels     int
		dups     int

		// The nine columns, so their length prefixes can be sized: version 5
		// writes each field all together and says how long it is.
		colSites, colSeqs, colClocks, colOSites, colOSeqs int
		colLengths, colText, colDelCounts, colDelFields   int

		delRanges, delChars                 int
		delGaps, delLens, delSites, delSeqs int

		// What the sequence number would have cost written in full, which is
		// what version 2 did and what made this worth changing.
		seqIfAbsolute int
		lastDelSeq    = map[SiteID]uint64{}

		// The next three fields down, and what they would cost written as steps
		// rather than in full. Same question the deletions answered, asked of
		// what is now above them.
		idAsStep     int // what version 3 spent: the sequence number in full
		clockAsStep  int // the clock in full
		clockFromSeq int // unused now; kept so the arithmetic below still reads
		originAsStep int // the origin's sequence number in full

		lastRunSeq    = map[SiteID]uint64{}
		lastOriginSeq = map[SiteID]uint64{}
		lastClock     uint64

		lastRunSeq2    = map[SiteID]uint64{}
		lastOriginSeq2 = map[SiteID]uint64{}
	)

	uvarint := func(v uint64) int {
		return len(binary.AppendUvarint(nil, v))
	}

	header += len(snapshotMagic) + 1
	sites := d.vv.sites()
	header += uvarint(uint64(len(sites)))
	for _, site := range sites {
		header += uvarint(uint64(site)) + uvarint(d.vv[site])
	}

	runs := d.runs()
	runCount += uvarint(uint64(len(runs)))
	for _, r := range runs {
		colSites += uvarint(uint64(r.id.Site))
		colSeqs += uvarint(zigzag(int64(r.id.Seq) - int64(lastRunSeq2[r.id.Site])))
		ids += uvarint(uint64(r.id.Site)) + uvarint(zigzag(int64(r.id.Seq)-int64(lastRunSeq2[r.id.Site])))
		lastRunSeq2[r.id.Site] = r.id.Seq
		colClocks += uvarint(r.clock - r.id.Seq)
		clocks += uvarint(r.clock - r.id.Seq)
		colOSites += uvarint(uint64(r.origin.Site))
		colOSeqs += uvarint(zigzag(int64(r.origin.Seq) - int64(lastOriginSeq2[r.origin.Site])))
		origins += uvarint(uint64(r.origin.Site)) + uvarint(zigzag(int64(r.origin.Seq)-int64(lastOriginSeq2[r.origin.Site])))
		lastOriginSeq2[r.origin.Site] = r.origin.Seq

		idAsStep += uvarint(uint64(r.id.Site)) + uvarint(r.id.Seq)
		clockAsStep += uvarint(r.clock)
		originAsStep += uvarint(uint64(r.origin.Site)) + uvarint(r.origin.Seq)
		_, _, _ = lastRunSeq, lastOriginSeq, lastClock
		_ = clockFromSeq
		colLengths += uvarint(uint64(len(r.text)))
		runCount += uvarint(uint64(len(r.text)))
		for _, ch := range r.text {
			text += uvarint(uint64(ch))
			colText += uvarint(uint64(ch))
		}
		colDelCounts += uvarint(uint64(len(r.dels)))
		runCount += uvarint(uint64(len(r.dels)))
		at := uint32(0)
		for _, del := range r.dels {
			delRanges++
			delChars += int(del.to - del.from)
			delGaps += uvarint(uint64(del.from - at))
			delLens += uvarint(uint64(del.to - del.from))
			delSites += uvarint(uint64(del.id.Site))
			delSeqs += uvarint(zigzag(int64(del.id.Seq) - int64(lastDelSeq[del.id.Site])))
			seqIfAbsolute += uvarint(del.id.Seq)
			dels += uvarint(uint64(del.from-at)) + uvarint(uint64(del.to-del.from))
			dels += uvarint(uint64(del.id.Site))
			dels += uvarint(zigzag(int64(del.id.Seq) - int64(lastDelSeq[del.id.Site])))
			colDelFields += uvarint(uint64(del.from-at)) + uvarint(uint64(del.to-del.from)) +
				uvarint(uint64(del.id.Site)) + uvarint(zigzag(int64(del.id.Seq)-int64(lastDelSeq[del.id.Site])))
			lastDelSeq[del.id.Site] = del.id.Seq
			at = del.to
		}
	}

	dupIDs := make([]ID, 0, len(d.dupDeletes))
	for delID := range d.dupDeletes {
		dupIDs = append(dupIDs, delID)
	}
	dups += uvarint(uint64(len(dupIDs)))
	for _, delID := range dupIDs {
		target := d.dupDeletes[delID]
		dups += uvarint(uint64(delID.Site)) + uvarint(delID.Seq)
		dups += uvarint(uint64(target.Site)) + uvarint(target.Seq)
	}

	// Version 5 length-prefixes each of the nine columns, which is what lets
	// whoever stores a snapshot take them apart and compress them one at a
	// time — worth three kilobytes against compressing the whole thing at
	// once. Those nine lengths are part of the file, so they are part of this
	// sum; the accounting was short by exactly them, and said so.
	for _, n := range []int{colSites, colSeqs, colClocks, colOSites, colOSeqs,
		colLengths, colText, colDelCounts, colDelFields} {
		runCount += uvarint(uint64(n))
	}

	total := len(d.Snapshot())
	parts := []struct {
		name string
		size int
	}{
		{"text", text},
		{"run identities (site, seq)", ids},
		{"origins (site, seq)", origins},
		{"clocks", clocks},
		{"deletions", dels},
		{"run and field lengths", runCount},
		{"duplicate deletes", dups},
		{"header and version vector", header},
	}
	sum := 0
	for _, p := range parts {
		sum += p.size
	}
	if sum != total {
		t.Fatalf("the accounting missed %d bytes: %d counted against a %d-byte snapshot",
			total-sum, sum, total)
	}

	sort.Slice(parts, func(i, j int) bool { return parts[i].size > parts[j].size })
	t.Logf("%d runs over %d characters and %d tombstones, in %d bytes:",
		len(runs), d.Len(), d.Tombstones(), total)
	for _, p := range parts {
		t.Logf("  %-28s %8d bytes  %5.1f%%", p.name, p.size, 100*float64(p.size)/float64(total))
	}
	t.Logf("  %-28s %8d bytes", "everything but the text", total-text)
	t.Logf("deletions in detail: %d ranges covering %d tombstones (%.2f per range), %.2f bytes each",
		delRanges, delChars, float64(delChars)/float64(delRanges), float64(dels)/float64(delRanges))
	t.Logf("  of which  gaps %d · lengths %d · sites %d · sequence numbers %d",
		delGaps, delLens, delSites, delSeqs)
	t.Logf("  the sequence numbers cost %d bytes as steps; written in full, as version 2 did, they would be %d (%+d)",
		delSeqs, seqIfAbsolute, delSeqs-seqIfAbsolute)
	t.Logf("the run header, and what version 3 spent on the same fields:")
	t.Logf("  run identities  %6d, written in full %6d (%+d)", ids, idAsStep, ids-idAsStep)
	t.Logf("  origins         %6d, written in full %6d (%+d)", origins, originAsStep, origins-originAsStep)
	t.Logf("  clocks          %6d, written in full %6d (%+d)", clocks, clockAsStep, clocks-clockAsStep)
}
