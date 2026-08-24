package structured

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"sort"

	"github.com/go-crdt/crdt"
)

// Blobs holds the files a document refers to but is not made of: the figures of
// a paper, an image pasted into a page, a font, a recorded clip.
//
// # Why a map value is not one
//
// A file could be one value in a [crdt.Map], and for a small one that is the
// right answer. For a two-megabyte figure it is not, because the operation that
// writes it is two megabytes: it cannot be sent as it is read, it cannot be
// resumed if the connection drops halfway, a peer that already has the same
// figure under another name receives it again, and changing one corner of the
// image sends the whole thing a second time.
//
// # How this one works
//
// A file is cut into chunks, and each chunk is stored under the hash of its own
// bytes. What a name refers to is a manifest: the length of the file and the
// hashes of its chunks, in order.
//
// Every property this type has follows from that one decision:
//
//   - A file is as many operations as it has chunks, so it is sent as it is
//     read and a peer that stops halfway has a prefix rather than nothing.
//   - Two replicas storing the same chunk write the same key and the same
//     bytes, so there is no conflict to resolve, ever. The same figure under
//     two names, in two documents, or added twice by two people is stored once.
//   - Changing part of a file rewrites only the chunks that changed, if the
//     chunker cuts on the content rather than on a count; see [Chunker].
//   - A chunk arrives verified or not at all. The key says what the bytes must
//     hash to, so a peer cannot quietly put something else there.
//
// What a name points at is one value, so two replicas replacing the same file
// at once is a conflict [crdt.Map] settles: one of the two whole files wins,
// which is what replacing a file means. Neither replica ends up with a mixture.
type Blobs struct {
	doc      *crdt.Composite
	chunks   *crdt.Map
	manifest *crdt.Map
}

// The two parts. The names are constant and valid, so the errors
// [crdt.Composite] returns for an invalid name cannot happen and are discarded.
var (
	chunksPart   = crdt.Part{Kind: crdt.PartMap, Name: "chunks"}
	manifestPart = crdt.Part{Kind: crdt.PartMap, Name: "blobs"}
)

// A Chunker cuts a file into the pieces it is stored as.
//
// The pieces are what deduplication and repeated sending work in terms of, so
// where the cuts fall is what decides whether changing one corner of an image
// rewrites one chunk or all of them. [FixedChunks] cuts every n bytes, which is
// enough for a file that is written once and never edited and is the wrong
// answer for one that is: inserting a single byte at the front moves every
// later boundary and nothing matches any more.
//
// A chunker that cuts on the content — one that hashes a sliding window and cuts
// where the hash has some shape, so a boundary depends on the bytes around it
// and not on how far into the file it is — makes an edit rewrite only the chunks
// it touched. This package does not carry one, because it carries nothing: the
// crdt module has no dependencies and this is not the thing to spend the first
// one on. Pass whichever you have.
type Chunker func(data []byte) [][]byte

// FixedChunks cuts every size bytes. A size of zero or less is one chunk.
func FixedChunks(size int) Chunker {
	return func(data []byte) [][]byte {
		if size <= 0 || len(data) <= size {
			return [][]byte{data}
		}
		out := make([][]byte, 0, (len(data)+size-1)/size)
		for at := 0; at < len(data); at += size {
			end := min(at+size, len(data))
			out = append(out, data[at:end])
		}
		return out
	}
}

// DefaultChunkSize is what [Blobs.Put] cuts at. Sixty-four kilobytes makes a
// two-megabyte figure thirty-two operations rather than one, which is small
// enough to send between two keystrokes and large enough that the hash beside
// each is a rounding error against it.
const DefaultChunkSize = 64 << 10

// NewBlobs returns an empty store this site can write to.
func NewBlobs(site crdt.SiteID) *Blobs { return bindBlobs(crdt.NewComposite(site)) }

// BlobsOf reads a composite as a blob store, for a document that holds one among
// other parts — the usual case, since the files belong to the document that
// refers to them.
func BlobsOf(doc *crdt.Composite) *Blobs { return bindBlobs(doc) }

func bindBlobs(doc *crdt.Composite) *Blobs {
	chunks, _ := doc.Map(chunksPart.Name)
	manifest, _ := doc.Map(manifestPart.Name)
	return &Blobs{doc: doc, chunks: chunks, manifest: manifest}
}

// LoadBlobs rebuilds a store from a snapshot, to be written as site.
func LoadBlobs(site crdt.SiteID, snapshot []byte) (*Blobs, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bindBlobs(doc), nil
}

// Composite returns the document underneath, which is what is snapshotted and
// what operations are applied to.
func (b *Blobs) Composite() *crdt.Composite { return b.doc }

// Site returns the replica this store writes as.
func (b *Blobs) Site() crdt.SiteID { return b.doc.Site() }

// Put stores data under name, cut at [DefaultChunkSize].
func (b *Blobs) Put(name string, data []byte) ([]crdt.PartOps, error) {
	return b.PutWith(name, data, FixedChunks(DefaultChunkSize))
}

// PutWith stores data under name, cut by the given chunker.
//
// The batches come back in the order they should be sent: every chunk, and the
// manifest last. A peer that receives a prefix of them holds chunks it cannot
// yet assemble, which is what makes the transfer resumable — nothing refers to
// them until the manifest arrives, and [Blobs.Missing] says what is still to
// come.
//
// A chunk already stored is not written again, so putting a file twice costs one
// operation and putting a file that shares chunks with one already there costs
// only what is new.
func (b *Blobs) PutWith(name string, data []byte, cut Chunker) ([]crdt.PartOps, error) {
	if name == "" {
		return nil, crdt.ErrInvalidOp
	}
	if cut == nil {
		cut = FixedChunks(DefaultChunkSize)
	}
	pieces := cut(data)
	// A chunker may hand back nothing for empty input, or one empty piece. Both
	// mean a file of no bytes, which is a file and has a manifest of no chunks.
	keys := make([]string, 0, len(pieces))
	var ops []crdt.PartOps
	total := 0
	for _, piece := range pieces {
		if len(piece) == 0 {
			continue
		}
		total += len(piece)
		sum := sha256.Sum256(piece)
		digest := string(sum[:])
		keys = append(keys, digest)
		// Already here, and holding what its key says it must: this is the
		// deduplication. Checking that it is present is not enough — a peer can
		// have written something else under the key, and skipping it then would
		// make putting the file again the one thing that cannot repair it.
		if held, ok := b.chunks.Get(chunkKey(digest)); ok && hashesTo(held, digest) {
			continue
		}
		op, err := b.chunks.Set(chunkKey(digest), piece)
		if err != nil {
			return nil, err
		}
		ops = append(ops, crdt.PartOps{Part: chunksPart, Map: []crdt.MapOp{op}})
	}
	if total != len(data) {
		// The chunker did not hand back what it was given. Storing the manifest
		// would name a file that cannot be reassembled.
		return nil, crdt.ErrInvalidOp
	}
	op, err := b.manifest.Set(name, encodeManifest(total, keys))
	if err != nil {
		return nil, err
	}
	return append(ops, crdt.PartOps{Part: manifestPart, Map: []crdt.MapOp{op}}), nil
}

// Get returns the file stored under name.
//
// It returns false while any of the file's chunks has not arrived, rather than a
// hole where one should be: half a figure is not a figure. Use [Blobs.Missing]
// to tell "not here" from "not here yet".
//
// A chunk whose bytes do not hash to the key they are under is treated as not
// having arrived. A peer can write anything into the map; it cannot make this
// hand back bytes nobody asked for.
func (b *Blobs) Get(name string) ([]byte, bool) {
	total, keys, ok := b.read(name)
	if !ok {
		return nil, false
	}
	// No capacity is reserved from total. It is the manifest's word for how
	// long the file is, and a manifest is bytes a peer wrote: reserving on it
	// let ten bytes ask for an exabyte. The loop below appends only chunks that
	// are present and hash to their own digest, so what this grows to is
	// bounded by bytes this replica really holds, and the length is checked
	// against total afterwards.
	var out []byte
	for _, digest := range keys {
		piece, held := b.chunks.Get(chunkKey(digest))
		if !held || !hashesTo(piece, digest) {
			return nil, false
		}
		out = append(out, piece...)
	}
	if len(out) != total {
		// The manifest says one length and its chunks another, which is a
		// manifest this package did not write.
		return nil, false
	}
	return out, true
}

// Size returns how long the file under name is, and whether there is one. It
// answers before the chunks have arrived, because the manifest carries the
// length — which is what lets a caller show a figure's dimensions, or a
// progress bar, while it is still coming.
func (b *Blobs) Size(name string) (int, bool) {
	total, _, ok := b.read(name)
	return total, ok
}

// Missing returns how many of a file's chunks have not arrived, or have arrived
// as something other than what their key says they must be.
func (b *Blobs) Missing(name string) int {
	_, keys, ok := b.read(name)
	if !ok {
		return 0
	}
	missing := 0
	for _, digest := range keys {
		piece, held := b.chunks.Get(chunkKey(digest))
		if !held || !hashesTo(piece, digest) {
			missing++
		}
	}
	return missing
}

// Names returns the files stored, in order.
func (b *Blobs) Names() []string {
	names := b.manifest.Keys()
	sort.Strings(names)
	return names
}

// Remove takes a name away. The chunks stay, because another name may share
// them; see [Blobs.Sweep].
func (b *Blobs) Remove(name string) (crdt.PartOps, error) {
	if _, held := b.manifest.Get(name); !held {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	op, err := b.manifest.Delete(name)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: manifestPart, Map: []crdt.MapOp{op}}, nil
}

// Stored returns how many distinct chunks are held, which is what the store
// costs rather than the sum of the sizes of the files in it.
func (b *Blobs) Stored() int { return b.chunks.Len() }

// Sweep removes every chunk no name refers to.
//
// It is not safe to run while a peer may be storing something. A peer that has
// just put a file whose chunks this replica also had would have written no chunk
// operations for them — they were already here — and sweeping them leaves that
// peer's manifest naming chunks nobody holds. What that costs is bounded and
// visible: [Blobs.Missing] reports it, and putting the file again restores
// exactly the chunks that went. It is not corruption, but it is work, so sweep
// when a document is quiet rather than as a matter of course.
func (b *Blobs) Sweep() ([]crdt.PartOps, error) {
	wanted := map[string]bool{}
	for _, name := range b.Names() {
		_, keys, ok := b.read(name)
		if !ok {
			continue
		}
		for _, digest := range keys {
			wanted[chunkKey(digest)] = true
		}
	}
	var ops []crdt.MapOp
	for _, key := range b.chunks.Keys() {
		if wanted[key] {
			continue
		}
		op, err := b.chunks.Delete(key)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return nil, nil
	}
	return []crdt.PartOps{{Part: chunksPart, Map: ops}}, nil
}

// read returns what a name refers to. A manifest this package did not write
// reads as no file at all rather than as a file of nonsense.
func (b *Blobs) read(name string) (total int, keys []string, ok bool) {
	value, held := b.manifest.Get(name)
	if !held {
		return 0, nil, false
	}
	return decodeManifest(value)
}

func hashesTo(piece []byte, digest string) bool {
	sum := sha256.Sum256(piece)
	return string(sum[:]) == digest
}

// chunkKey is what a digest is stored under. A [crdt.Map] key has to be valid
// UTF-8 — it travels in a text field and is compared as a string — and a hash is
// arbitrary bytes, so it is written in base64: forty-three characters for
// thirty-two bytes, against sixty-four for hexadecimal. The manifest keeps the
// digests as they are, so a file's list of chunks stays as short as the hashes.
func chunkKey(digest string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(digest))
}

func encodeManifest(total int, keys []string) []byte {
	out := binary.AppendUvarint(nil, uint64(total))
	out = binary.AppendUvarint(out, uint64(len(keys)))
	for _, key := range keys {
		out = append(out, key...)
	}
	return out
}

func decodeManifest(value []byte) (total int, keys []string, ok bool) {
	size, used := binary.Uvarint(value)
	if used <= 0 {
		return 0, nil, false
	}
	rest := value[used:]
	count, used := binary.Uvarint(rest)
	if used <= 0 {
		return 0, nil, false
	}
	rest = rest[used:]
	// Divide rather than multiply. count*sha256.Size is uint64 arithmetic and
	// wraps: count = 1<<59 makes it zero, so a ten-byte manifest with no
	// digests at all satisfied the equality and then asked for a slice of
	// 1<<59 strings. Comparing against len(rest)/sha256.Size cannot wrap,
	// because len(rest) is a real length.
	if count > uint64(len(rest))/sha256.Size || uint64(len(rest)) != count*sha256.Size {
		return 0, nil, false
	}
	// A file of no bytes has no chunks, and a file of some bytes has some: a
	// manifest saying otherwise cannot be assembled and is not read.
	if (size == 0) != (count == 0) {
		return 0, nil, false
	}
	keys = make([]string, 0, count)
	for at := 0; at < len(rest); at += sha256.Size {
		keys = append(keys, string(rest[at:at+sha256.Size]))
	}
	return int(size), keys, true
}

// Snapshot encodes the whole store.
func (b *Blobs) Snapshot() []byte { return b.doc.Snapshot() }

// Version returns what this replica holds.
func (b *Blobs) Version() crdt.CompositeVersion { return b.doc.Version() }

// OpsSince returns the operations a peer at v has not seen.
func (b *Blobs) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return b.doc.OpsSince(v) }

// Apply integrates operations from peers.
func (b *Blobs) Apply(batches ...crdt.PartOps) error { return b.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting.
func (b *Blobs) Pending() int { return b.doc.Pending() }
