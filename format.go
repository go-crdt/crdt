package crdt

// A Format is one of the snapshot encodings this package writes. Each has a
// version of its own, moving at its own pace: a change to how a text is written
// does not disturb a map.
type Format uint8

// The four snapshot encodings. A composite snapshot embeds one of each of the
// other three, so reading a composite means reading whatever it contains.
const (
	FormatText Format = iota + 1
	FormatList
	FormatMap
	FormatComposite
)

// String names a format, and says so for one this build does not know rather
// than printing a bare number.
func (f Format) String() string {
	switch f {
	case FormatText:
		return "text"
	case FormatList:
		return "list"
	case FormatMap:
		return "map"
	case FormatComposite:
		return "composite"
	}
	return "unknown format"
}

// Reads reports the versions of a format this build can load, ascending.
//
// A set rather than a range, because the versions are not contiguous and
// assuming they were is a mistake this package has already made: version 7 of a
// text is reserved for the purge and refused here, so this build reads 1 to 6
// and 8. A peer told "up to 8" would send a 7 and be refused.
//
// Empty for a format this build does not know, which is how a peer built later
// can name one and be understood to have said something rather than nothing.
//
// # What it is for
//
// A snapshot travels: a joining participant loads one the server sends, and a
// federated link adopts one from the server it follows. Neither can negotiate --
// a reader knows the version byte or refuses the bytes -- so the only way to
// avoid sending something unreadable is to have been told what the other side
// reads. This is the half of that a peer can say about itself.
//
// The result is freshly allocated; a caller may keep it.
func Reads(f Format) []byte {
	switch f {
	case FormatText:
		return append([]byte(nil), textFormats...)
	case FormatList:
		return upTo(listVersion)
	case FormatMap:
		return upTo(mapVersion)
	case FormatComposite:
		return upTo(compositeSnapshotVersion)
	}
	return nil
}

// upTo is every version from one to highest, for the formats that have no gaps
// in them. Written once rather than three times so that a format acquiring a gap
// has to say so here rather than being described wrongly by a shared helper.
func upTo(highest byte) []byte {
	out := make([]byte, 0, highest)
	for v := byte(1); v <= highest; v++ {
		out = append(out, v)
	}
	return out
}

// Writes reports the version of a format this build produces.
//
// It is not always the highest version read: a format ships its reader first and
// its writer a release later, so that a peer meets nothing it cannot understand.
//
// Nor is it always what a given document writes. A text is version 9 here and
// version 8 in the bytes of every document that has not purged, because the
// purge's fields are written only by a document that has any -- see
// [Doc.formatVersion]. The higher number is the honest answer to a sender's
// question all the same: what a peer has to be able to read is the most this
// build might send it, and a peer told 8 would be sent a 9 the first time
// somebody called [Doc.Purge].
//
// Zero for a format this build does not know.
func Writes(f Format) byte {
	switch f {
	case FormatText:
		return snapshotVersion
	case FormatList:
		return listVersion
	case FormatMap:
		return mapVersion
	case FormatComposite:
		return compositeSnapshotVersion
	}
	return 0
}

// Formats reports every format this build knows, in order, so a peer can say
// what it reads without a list of its own to keep in step.
func Formats() []Format {
	return []Format{FormatText, FormatList, FormatMap, FormatComposite}
}
