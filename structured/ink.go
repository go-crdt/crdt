package structured

import (
	"encoding/binary"
	"math"

	"github.com/go-crdt/crdt"
)

// Ink is what is drawn by hand: the strokes of a whiteboard, an annotation over
// a figure, a signature.
//
// # What a stroke needs that the other types do not give it
//
// A stroke is a list of points, and it arrives one point at a time while
// somebody is drawing it. Held as one value — a [Sequence] item whose contents
// are the whole path — every point sent rewrites the path: a stroke of four
// hundred points costs four hundred operations whose average size is two
// hundred points, and the person watching sees the line redrawn rather than
// extended.
//
// So the points are a sequence of their own, appended to, and each point says
// which stroke it belongs to.
//
// # Why one stream of points and not one per stroke
//
// A [crdt.Composite] part per stroke would keep each stroke's points together
// and read more simply. It would also put a part in the document for every
// stroke anybody ever drew, and a part cannot be taken out again: the version
// two replicas exchange to find out what they are missing carries one entry per
// part, so a whiteboard would come to spend more on saying what it has than on
// what was drawn.
//
// One stream costs a stroke identity on every point — a dozen bytes — and keeps
// the document two parts wide however much is drawn on it.
//
// # What is where
//
//   - The strokes are a [Sequence], so the order they are drawn in is the order
//     they are painted in, and a stroke can be moved above or below another
//     without being redrawn. Anything a stroke carries — its colour, its width,
//     the pen it was drawn with — is a field on the item.
//   - The points are a [crdt.List], appended to as the pen moves.
type Ink struct {
	doc     *crdt.Composite
	strokes *Sequence
	points  *crdt.List
}

// The two parts. The names are constant and valid, so the errors
// [crdt.Composite] returns for an invalid name cannot happen and are discarded.
var (
	strokesPart = crdt.Part{Kind: crdt.PartMap, Name: "strokes"}
	pointsPart  = crdt.Part{Kind: crdt.PartList, Name: "points"}
)

// A StrokeID names a stroke. It is a [Sequence] item, so the sequence's own
// ordering and fields apply to it.
type StrokeID = ItemID

// A Point is one sample of a pen: where it was, and how hard it was pressed.
//
// Pressure is whatever the input device reports, normally between zero and one,
// and is carried rather than interpreted. A device with no pressure sensor
// should send one.
type Point struct {
	X, Y     float32
	Pressure float32
}

// NewInk returns an empty drawing this site can draw on.
func NewInk(site crdt.SiteID) *Ink { return bindInk(crdt.NewComposite(site)) }

// InkOf reads a composite as a drawing, for a document that holds one among
// other parts.
func InkOf(doc *crdt.Composite) *Ink { return bindInk(doc) }

func bindInk(doc *crdt.Composite) *Ink {
	strokes, _ := doc.Map(strokesPart.Name)
	points, _ := doc.List(pointsPart.Name)
	return &Ink{doc: doc, strokes: SequenceOf(strokes), points: points}
}

// LoadInk rebuilds a drawing from a snapshot, to be drawn on as site.
func LoadInk(site crdt.SiteID, snapshot []byte) (*Ink, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bindInk(doc), nil
}

// Composite returns the document underneath.
func (i *Ink) Composite() *crdt.Composite { return i.doc }

// Strokes returns the sequence the strokes live in, for the order they are
// painted in and whatever each of them carries.
func (i *Ink) Strokes() *Sequence { return i.strokes }

// Site returns the replica this drawing draws as.
func (i *Ink) Site() crdt.SiteID { return i.doc.Site() }

// Begin starts a stroke, above every stroke already drawn, and returns it with
// no points yet.
func (i *Ink) Begin() (StrokeID, []crdt.PartOps, error) {
	var after StrokeID
	if drawn := i.strokes.Items(); len(drawn) > 0 {
		after = drawn[len(drawn)-1]
	}
	stroke, ops, err := i.strokes.Insert(after, nil)
	if err != nil {
		return StrokeID{}, nil, err
	}
	return stroke, []crdt.PartOps{{Part: strokesPart, Map: ops}}, nil
}

// Extend adds points to the end of a stroke.
//
// It is one operation however many points are given, so a pen that reports
// several samples between two frames costs one operation rather than several.
func (i *Ink) Extend(stroke StrokeID, pts ...Point) (crdt.PartOps, error) {
	if stroke.IsStart() || !i.strokes.Records().HasRecord(stroke.key()) {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	if len(pts) == 0 {
		return crdt.PartOps{}, ErrNoChange
	}
	values := make([][]byte, 0, len(pts))
	for _, p := range pts {
		if !finite(p.X) || !finite(p.Y) || !finite(p.Pressure) {
			// A coordinate that is not a number is a fault upstream, and one
			// stored is one every replica has to carry and paint around.
			return crdt.PartOps{}, crdt.ErrInvalidOp
		}
		values = append(values, encodePoint(stroke, p))
	}
	ops, err := i.points.Insert(i.points.Len(), values...)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: pointsPart, List: ops}, nil
}

func finite(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0)
}

// Erase takes a stroke out of the drawing.
//
// Its points stay until [Ink.Sweep] takes them, because erasing happens while
// somebody is drawing and has to cost one operation rather than one per point.
func (i *Ink) Erase(stroke StrokeID) (crdt.PartOps, error) {
	ops, err := i.strokes.Remove(stroke)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: strokesPart, Map: ops}, nil
}

// Points returns the points of a stroke, in the order they were drawn.
func (i *Ink) Points(stroke StrokeID) []Point {
	var out []Point
	for _, value := range i.points.Values() {
		at, p, ok := decodePoint(value)
		if ok && at == stroke {
			out = append(out, p)
		}
	}
	return out
}

// Paths returns every stroke that is still drawn, in the order they are painted
// in, with its points.
//
// It is one walk of the points rather than one per stroke, which is what a
// repaint wants: [Ink.Points] asked for each of five hundred strokes walks the
// whole drawing five hundred times.
func (i *Ink) Paths() [][]Point {
	drawn := i.strokes.Items()
	where := make(map[StrokeID]int, len(drawn))
	for at, stroke := range drawn {
		where[stroke] = at
	}
	paths := make([][]Point, len(drawn))
	for _, value := range i.points.Values() {
		stroke, p, ok := decodePoint(value)
		if !ok {
			continue
		}
		at, live := where[stroke]
		if !live {
			continue // a stroke that was erased, or one whose strokes part has
			// not arrived yet: its points are not painted either way
		}
		paths[at] = append(paths[at], p)
	}
	return paths
}

// Sweep removes the points of strokes that are no longer drawn.
//
// It is the counterpart of [Ink.Erase] being one operation: erasing says the
// stroke is gone, and this is what takes the bytes away, when the drawing is
// quiet rather than while somebody is drawing on it.
//
// It has the hazard [Blobs.Sweep] has and for the same reason: a peer may be
// extending a stroke this replica has been told to erase, and those points
// arrive after the sweep and are swept by the next one. Nothing is corrupted —
// the stroke is erased, which is what was asked.
func (i *Ink) Sweep() (crdt.PartOps, error) {
	live := map[StrokeID]bool{}
	for _, stroke := range i.strokes.Items() {
		live[stroke] = true
	}
	// Backwards, so that deleting a run does not move the runs still to be
	// found, and a run at a time rather than a point at a time — which does not
	// save operations, because a list deletion is one operation per element
	// whatever it is asked for, but does mean the position of each run is found
	// once instead of once per point in it.
	values := i.points.Values()
	var ops []crdt.ListOp
	for at := len(values) - 1; at >= 0; at-- {
		stroke, _, ok := decodePoint(values[at])
		if ok && live[stroke] {
			continue
		}
		end := at + 1
		for at > 0 {
			prev, _, ok := decodePoint(values[at-1])
			if ok && live[prev] {
				break
			}
			at--
		}
		got, err := i.points.Delete(at, end-at)
		if err != nil {
			return crdt.PartOps{}, err
		}
		ops = append(ops, got...)
	}
	if len(ops) == 0 {
		return crdt.PartOps{}, ErrNoChange
	}
	return crdt.PartOps{Part: pointsPart, List: ops}, nil
}

func encodePoint(stroke StrokeID, p Point) []byte {
	out := binary.AppendUvarint(nil, uint64(stroke.Site))
	out = binary.AppendUvarint(out, stroke.Seq)
	for _, f := range [3]float32{p.X, p.Y, p.Pressure} {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(f))
	}
	return out
}

func decodePoint(value []byte) (StrokeID, Point, bool) {
	site, used := binary.Uvarint(value)
	if used <= 0 {
		return StrokeID{}, Point{}, false
	}
	rest := value[used:]
	seq, used := binary.Uvarint(rest)
	if used <= 0 || seq == 0 {
		return StrokeID{}, Point{}, false
	}
	rest = rest[used:]
	if len(rest) != 12 {
		return StrokeID{}, Point{}, false
	}
	var f [3]float32
	for k := range f {
		f[k] = math.Float32frombits(binary.LittleEndian.Uint32(rest[k*4:]))
	}
	stroke := StrokeID{Site: crdt.SiteID(site), Seq: seq}
	return stroke, Point{X: f[0], Y: f[1], Pressure: f[2]}, true
}

// Snapshot encodes the whole drawing.
func (i *Ink) Snapshot() []byte { return i.doc.Snapshot() }

// Version returns what this replica holds.
func (i *Ink) Version() crdt.CompositeVersion { return i.doc.Version() }

// OpsSince returns the operations a peer at v has not seen.
func (i *Ink) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return i.doc.OpsSince(v) }

// Apply integrates operations from peers.
func (i *Ink) Apply(batches ...crdt.PartOps) error { return i.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting.
func (i *Ink) Pending() int { return i.doc.Pending() }
