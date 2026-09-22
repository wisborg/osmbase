package osmpbf

import (
	"fmt"
	"slices"

	"github.com/wisborg/osmbase/internal/protobuf"
)

// Features this reader implements, and so may see declared as required.
//
// OsmSchema-V0.6 is the element model every current file uses. DenseNodes is
// the packed node encoding; every extract in circulation sets it, and a file
// that does not is still readable because plain nodes are decoded too.
var supportedFeatures = []string{"OsmSchema-V0.6", "DenseNodes"}

// Header is an OSMHeader block's feature declarations.
//
// The bounding box, writing program and replication fields are skipped: the
// bounding box is a sint64 quadruple, and signed varints are part of the
// element decoding that has not landed yet.
type Header struct {
	// RequiredFeatures names what a reader must implement to read the rest of
	// the file correctly. The format is explicit that a reader encountering
	// one it does not implement must stop.
	RequiredFeatures []string

	// OptionalFeatures names properties a reader may exploit and is free to
	// ignore -- how the file is sorted, whether it carries replication
	// timestamps.
	OptionalFeatures []string
}

// DecodeHeader decodes an OSMHeader block and refuses a file whose required
// features this does not implement.
//
// The Header is returned alongside the error so a caller can say which feature
// was the problem.
//
// Refusing is the specification's rule, and it is the same rule this package
// applies to an unreadable codec and to a granularity of zero: the failure it
// prevents is a quiet wrong answer rather than a crash. An extract declaring
// HistoricalInformation carries every version of every element, including
// deleted ones. Read as though it were an ordinary extract, the boundary
// passes would accept a relation that was abolished years ago and draw the
// suburb it used to describe, with nothing anywhere reporting a problem.
func DecodeHeader(data []byte) (Header, error) {
	var h Header
	r := protobuf.New(data, "osmpbf", "a header block")
	for !r.Done() {
		field, wire, err := r.Tag()
		if err != nil {
			return h, err
		}
		switch {
		case field == 4 && wire == protobuf.WireBytes: // required_features
			v, err := r.Bytes("a required feature")
			if err != nil {
				return h, err
			}
			if len(h.RequiredFeatures) >= MaxFeatures {
				return h, fmt.Errorf("osmpbf: a header declares more than %d required features", MaxFeatures)
			}
			h.RequiredFeatures = append(h.RequiredFeatures, string(v))
		case field == 5 && wire == protobuf.WireBytes: // optional_features
			v, err := r.Bytes("an optional feature")
			if err != nil {
				return h, err
			}
			if len(h.OptionalFeatures) >= MaxFeatures {
				return h, fmt.Errorf("osmpbf: a header declares more than %d optional features", MaxFeatures)
			}
			h.OptionalFeatures = append(h.OptionalFeatures, string(v))
		default:
			if err := r.Skip(field, wire); err != nil {
				return h, err
			}
		}
	}

	for _, f := range h.RequiredFeatures {
		if !slices.Contains(supportedFeatures, f) {
			return h, fmt.Errorf("osmpbf: the file requires the feature %q, which this does not implement", f)
		}
	}
	return h, nil
}

// MaxFeatures caps the declarations in a header, for the same reason the
// primitive block caps its string table: the entries are unbounded in count
// and a header is bounded only in bytes.
const MaxFeatures = 64
