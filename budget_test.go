package crdt

import (
	"bytes"
	"compress/gzip"
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
// column that wrote it, so the next decision is made against the shape of the
// cost rather than against a plausible story about it. It is what found the
// three columns holding one value each that version 8 was written for, and it
// is what says what they cost now.
//
// It asserts only that the accounting is complete: the parts have to add up to
// the whole, or the numbers below are decoration. What the numbers *say* is for
// a person to read in the log.
func TestSnapshotBudget(t *testing.T) {
	patches, _ := loadTrace(t)

	d := New(1)
	replay(t, d, patches)

	uvarint := func(v uint64) int {
		return len(binary.AppendUvarint(nil, v))
	}
	plain := func(vs []uint64) int {
		n := 0
		for _, v := range vs {
			n += uvarint(v)
		}
		return n
	}

	header := len(snapshotMagic) + 1
	sites := d.vv.sites()
	header += uvarint(uint64(len(sites)))
	for _, site := range sites {
		header += uvarint(uint64(site)) + uvarint(d.vv[site])
	}
	// Version 6 carries two tables here, both of them always empty: they were
	// written for a collection that was withdrawn. One byte each, and one byte
	// unaccounted for here is the whole of what this test exists to catch.
	header += uvarint(0) + uvarint(0)

	runs := d.runs()
	// The count of runs, and then a length and an encoding byte for each of the
	// twelve columns. Both are part of the file, so both are part of this sum.
	framing := uvarint(uint64(len(runs)))

	var (
		runSites, seqs, clocks, oSites, oSeqs, lengths, text, delCounts []uint64
		delGaps, delSpans, delSites, delSeqs                            []uint64

		lastDelSeq    = map[SiteID]uint64{}
		lastRunSeq    = map[SiteID]uint64{}
		lastOriginSeq = map[SiteID]uint64{}

		delChars int

		// What each stepped field would have cost written in full, which is
		// what the version below it did and what made the step worth having.
		seqIfAbsolute  int
		idAsStep       int
		clockAsStep    int
		originAsStep   int
		lastRunSeq2    = map[SiteID]uint64{}
		lastOriginSeq2 = map[SiteID]uint64{}
	)
	for _, r := range runs {
		runSites = append(runSites, uint64(r.id.Site))
		seqs = append(seqs, zigzag(int64(r.id.Seq)-int64(lastRunSeq[r.id.Site])))
		lastRunSeq[r.id.Site] = r.id.Seq
		clocks = append(clocks, r.clock-r.id.Seq)
		oSites = append(oSites, uint64(r.origin.Site))
		oSeqs = append(oSeqs, zigzag(int64(r.origin.Seq)-int64(lastOriginSeq[r.origin.Site])))
		lastOriginSeq[r.origin.Site] = r.origin.Seq
		lengths = append(lengths, uint64(len(r.text)))
		for _, ch := range r.text {
			text = append(text, uint64(ch))
		}
		delCounts = append(delCounts, uint64(len(r.dels)))

		idAsStep += uvarint(uint64(r.id.Site)) + uvarint(r.id.Seq)
		clockAsStep += uvarint(r.clock)
		originAsStep += uvarint(uint64(r.origin.Site)) + uvarint(r.origin.Seq)
		_, _ = lastRunSeq2, lastOriginSeq2

		at := uint32(0)
		for _, del := range r.dels {
			delChars += int(del.to - del.from)
			delGaps = append(delGaps, uint64(del.from-at))
			delSpans = append(delSpans, uint64(del.to-del.from))
			delSites = append(delSites, uint64(del.id.Site))
			delSeqs = append(delSeqs, zigzag(int64(del.id.Seq)-int64(lastDelSeq[del.id.Site])))
			seqIfAbsolute += uvarint(del.id.Seq)
			lastDelSeq[del.id.Site] = del.id.Seq
			at = del.to
		}
	}

	cols := []struct {
		name string
		vs   []uint64
	}{
		{"run sites", runSites},
		{"run sequence steps", seqs},
		{"clocks", clocks},
		{"origin sites", oSites},
		{"origin sequence steps", oSeqs},
		{"run lengths", lengths},
		{"text", text},
		{"deletion counts", delCounts},
		{"deletion gaps", delGaps},
		{"deletion spans", delSpans},
		{"deletion sites", delSites},
		{"deletion sequence steps", delSeqs},
	}

	parts := []struct {
		name  string
		size  int
		were  int
		count int
	}{}
	for _, c := range cols {
		size := len(groupColumn(c.vs))
		framing += uvarint(uint64(size+1)) + 1
		parts = append(parts, struct {
			name  string
			size  int
			were  int
			count int
		}{c.name, size, plain(c.vs), len(c.vs)})
	}

	dupIDs := make([]ID, 0, len(d.dupDeletes))
	for delID := range d.dupDeletes {
		dupIDs = append(dupIDs, delID)
	}
	dups := uvarint(uint64(len(dupIDs)))
	for _, delID := range dupIDs {
		target := d.dupDeletes[delID]
		dups += uvarint(uint64(delID.Site)) + uvarint(delID.Seq)
		dups += uvarint(uint64(target.Site)) + uvarint(target.Seq)
	}

	total := len(d.Snapshot())

	sum := header + framing + dups
	wasPlain := 0
	for _, p := range parts {
		sum += p.size
		wasPlain += p.were
	}
	if sum != total {
		t.Fatalf("the accounting missed %d bytes: %d counted against a %d-byte snapshot",
			total-sum, sum, total)
	}

	// The same document through gzip -6, which is what a store or a transport
	// applies on top. docs/comparison reports this for every other
	// implementation, so that the size table can compare like with like: a
	// format that has already removed its own redundancy has little left to give
	// here, and the ratio is how much a general-purpose compressor still finds.
	var squeezed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&squeezed, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(d.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("gzip -6: %d bytes, %.2fx", squeezed.Len(), float64(total)/float64(squeezed.Len()))

	t.Logf("%d runs over %d characters and %d tombstones, in %d bytes:",
		len(runs), d.Len(), d.Tombstones(), total)
	sort.Slice(parts, func(i, j int) bool { return parts[i].size > parts[j].size })
	for _, p := range parts {
		t.Logf("  %-24s %8d bytes  %5.1f%%   over %7d values, %8d written plainly",
			p.name, p.size, 100*float64(p.size)/float64(total), p.count, p.were)
	}
	t.Logf("  %-24s %8d bytes", "header and version vector", header)
	t.Logf("  %-24s %8d bytes", "counts, lengths, encodings", framing)
	t.Logf("  %-24s %8d bytes", "duplicate deletes", dups)
	t.Logf("the columns cost %d bytes; one uvarint per value, as version 6 wrote them, is %d",
		total-header-framing-dups, wasPlain)
	t.Logf("deletions: %d ranges covering %d tombstones (%.2f per range)",
		len(delGaps), delChars, float64(delChars)/float64(len(delGaps)))
	t.Logf("  the sequence steps cost %d bytes plainly; written in full, as version 2 did, %d",
		plain(delSeqs), seqIfAbsolute)
	t.Logf("the run header, and what version 3 spent on the same fields:")
	t.Logf("  run identities  %6d, written in full %6d",
		plain(runSites)+plain(seqs), idAsStep)
	t.Logf("  origins         %6d, written in full %6d",
		plain(oSites)+plain(oSeqs), originAsStep)
	t.Logf("  clocks          %6d, written in full %6d", plain(clocks), clockAsStep)
}
