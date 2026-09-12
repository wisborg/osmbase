// Package mvt decodes Mapbox Vector Tile 2.1 bytes into layers, features,
// attributes and geometry.
//
// It decodes and nothing else. Coordinates come out in the tile's own integer
// space, exactly as the producer wrote them -- no projection, no scaling to
// pixels, no clipping to the tile edge. Where those numbers end up on a screen
// depends on a view the decoder knows nothing about, and a decoder that
// guessed would be a decoder that had to be undone.
//
// The one thing it does NOT pass through untouched is polygon ring
// orientation, which is normalised from the computed signed area rather than
// trusted. The rasterizer this library draws with sums winding and takes the
// absolute value, so a hole wound the same way as its exterior fills solid
// instead of cutting a hole -- silently, and into a picture that still looks
// like a map. See normalisePolygons and docs/architecture.md, trap T13.
//
// The protobuf decoding is hand written. google.golang.org/protobuf would be a
// module in the go.sum and the hand-maintained NOTICE of every public program
// that renders a map here, for a schema of six messages that has not changed
// since 2016. See docs/architecture.md, "Zero third-party modules".
package mvt

import (
	"fmt"
	"math"
)

// Field numbers from the vector tile schema. They are the whole schema, so
// they are written here rather than generated.
const (
	tileLayers = 3

	layerName     = 1
	layerFeatures = 2
	layerKeys     = 3
	layerValues   = 4
	layerExtent   = 5
	layerVersion  = 15

	featureID       = 1
	featureTags     = 2
	featureType     = 3
	featureGeometry = 4

	valueString = 1
	valueFloat  = 2
	valueDouble = 3
	valueInt    = 4
	valueUint   = 5
	valueSint   = 6
	valueBool   = 7
)

// DefaultExtent is the tile-local coordinate range a layer has when it does
// not say. Every layer in practice says 4096 anyway; the default exists
// because the schema gives the field one.
const DefaultExtent = 4096

// GeomType is what a feature's geometry represents.
type GeomType uint8

const (
	GeomUnknown GeomType = 0
	GeomPoint   GeomType = 1
	// GeomLineString covers a single line and a multi-line alike; the parts
	// are in Geometry.Lines.
	GeomLineString GeomType = 2
	// GeomPolygon covers a single polygon and a multipolygon alike; the parts
	// are in Geometry.Polygons.
	GeomPolygon GeomType = 3
)

func (t GeomType) String() string {
	switch t {
	case GeomUnknown:
		return "unknown"
	case GeomPoint:
		return "point"
	case GeomLineString:
		return "linestring"
	case GeomPolygon:
		return "polygon"
	}
	return fmt.Sprintf("geomtype(%d)", uint8(t))
}

// Tile is a decoded vector tile.
type Tile struct {
	Layers []Layer
}

// Layer returns the layer with the given name and whether the tile has one.
//
// A style asks for layers by name, and a tile legitimately lacks most of them:
// there is no water layer inland and no building layer on a trail. That is the
// correct picture of the place rather than missing data, so this reports
// absence as a boolean and not as an error. See docs/architecture.md, trap T9.
func (t Tile) Layer(name string) (Layer, bool) {
	for _, l := range t.Layers {
		if l.Name == name {
			return l, true
		}
	}
	return Layer{}, false
}

// Layer is one named layer of a tile.
type Layer struct {
	Name string
	// Version is the schema version the producer declared: 1 or 2.
	Version uint32
	// Extent is the width and height of the tile in its own coordinates, so a
	// feature's Points are in [0, Extent) apart from the buffer that spills
	// past the edge. It is per LAYER, not per tile: a building layer at 4096
	// and a landcover layer at 512 in one tile is legal and happens.
	Extent   uint32
	Features []Feature
}

// Feature is one geometry with its attributes.
type Feature struct {
	// ID is the producer's identifier for the feature, and HasID says whether
	// there was one. The field is optional, and 0 is a legal ID, so the flag
	// is the only way to tell "feature zero" from "no identifier".
	ID    uint64
	HasID bool

	Type GeomType

	// Tags are the feature's attributes, already resolved against the layer's
	// key and value tables.
	//
	// Absence is membership, not a zero value: ask with the two-result form
	// and read the value only when the key was there. A missing "name" and a
	// name of "" are different features, and so are a road with no declared
	// minimum zoom and one declared at zero.
	//
	// Iteration order over a map is randomised by the runtime, so anything
	// whose result must be reproducible -- which here is everything, because
	// the same tile must draw the same picture every time -- has to look tags
	// up by key rather than range over them.
	Tags map[string]Value

	Geometry Geometry
}

// Tag returns the value of one attribute and whether the feature carries it.
func (f Feature) Tag(key string) (Value, bool) {
	v, ok := f.Tags[key]
	return v, ok
}

// Decode decodes one vector tile.
//
// The returned strings and geometry do not alias data, but decoding does read
// it in place, so data must not be modified while Decode runs.
//
// Decoding is strict about structure and lenient about content. A field number
// this decoder does not know is skipped, because a producer is allowed to add
// one; a length that runs past the end of the buffer, a geometry command that
// does not exist, or a tag index that names a key the layer does not have is
// an error, because every one of those produces a feature that looks real and
// is not.
//
// ZERO BYTES DECODE TO A TILE WITH NO LAYERS AND NO ERROR. A protobuf message
// with no fields is legal and an archive can legitimately hold one, so there is
// nothing here to reject -- but it means this function cannot distinguish an
// empty tile from a file that was truncated to nothing or never written. That
// distinction belongs to whatever stores tiles, which knows how many bytes it
// expected; it is recorded here because the ambiguity is real and a caller
// should not discover it by finding a blank map.
func Decode(data []byte) (Tile, error) {
	var tile Tile
	// A set beside the slice, rather than a scan of the slice per layer. The
	// scan made decoding quadratic in the number of layers, which is invisible
	// on the ten a real tile has and is minutes of wall time on a tile built
	// to have tens of thousands -- inside the render path, where no context
	// deadline reaches.
	seen := map[string]struct{}{}
	r := protoReader{b: data, what: "the tile"}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return Tile{}, err
		}
		if field != tileLayers {
			if err := r.skip(field, wire); err != nil {
				return Tile{}, err
			}
			continue
		}
		if wire != wireBytes {
			return Tile{}, fmt.Errorf("mvt: the tile has a layer with wire type %d, and a layer is a length-delimited message", wire)
		}
		payload, err := r.bytes("a layer")
		if err != nil {
			return Tile{}, err
		}
		layer, err := decodeLayer(payload)
		if err != nil {
			return Tile{}, err
		}
		if _, clash := seen[layer.Name]; clash {
			return Tile{}, fmt.Errorf("mvt: the tile has two layers named %q, so a style asking for it by name would get an arbitrary one", layer.Name)
		}
		seen[layer.Name] = struct{}{}
		tile.Layers = append(tile.Layers, layer)
	}
	return tile, nil
}

func decodeLayer(data []byte) (Layer, error) {
	layer := Layer{Extent: DefaultExtent}
	var (
		keys []string
		// Values and features are both held as raw bytes until the whole
		// layer has been read. The features because the schema does not
		// require the key and value tables to precede the features that index
		// into them; the values so that a failure can name the layer it was
		// in, which is the difference between a fixable report and hunting
		// through twenty layers for one bad byte.
		valuePayloads [][]byte
		features      [][]byte
		sawVersion    bool
	)
	r := protoReader{b: data, what: "a layer"}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return Layer{}, err
		}
		switch {
		case field == layerName && wire == wireBytes:
			b, err := r.bytes("the layer name")
			if err != nil {
				return Layer{}, err
			}
			layer.Name = string(b)
		case field == layerFeatures && wire == wireBytes:
			b, err := r.bytes("a feature")
			if err != nil {
				return Layer{}, err
			}
			// Features are held back until the key and value tables are
			// known: the schema does not require the tables to precede the
			// features that index into them.
			features = append(features, b)
		case field == layerKeys && wire == wireBytes:
			b, err := r.bytes("a key")
			if err != nil {
				return Layer{}, err
			}
			keys = append(keys, string(b))
		case field == layerValues && wire == wireBytes:
			b, err := r.bytes("a value")
			if err != nil {
				return Layer{}, err
			}
			valuePayloads = append(valuePayloads, b)
		case field == layerExtent && wire == wireVarint:
			v, err := r.uvarint("the extent")
			if err != nil {
				return Layer{}, err
			}
			if v == 0 || v > 0xffffffff {
				return Layer{}, fmt.Errorf("mvt: a layer declares an extent of %d, and a tile has to be at least one unit across", v)
			}
			layer.Extent = uint32(v)
		case field == layerVersion && wire == wireVarint:
			v, err := r.uvarint("the version")
			if err != nil {
				return Layer{}, err
			}
			if v > 0xffffffff {
				return Layer{}, fmt.Errorf("mvt: a layer declares version %d", v)
			}
			layer.Version = uint32(v)
			sawVersion = true
		default:
			if err := r.skip(field, wire); err != nil {
				return Layer{}, err
			}
		}
	}
	if layer.Name == "" {
		return Layer{}, fmt.Errorf("mvt: a layer has no name, and a style can only ask for layers by name")
	}
	if !sawVersion {
		return Layer{}, fmt.Errorf("mvt: layer %q declares no version, and the field is required", layer.Name)
	}
	if layer.Version != 1 && layer.Version != 2 {
		return Layer{}, fmt.Errorf("mvt: layer %q declares version %d, and this decoder implements versions 1 and 2", layer.Name, layer.Version)
	}

	values := make([]Value, 0, len(valuePayloads))
	for i, payload := range valuePayloads {
		v, err := decodeValue(payload)
		if err != nil {
			return Layer{}, fmt.Errorf("mvt: layer %q, value %d of %d: %w", layer.Name, i, len(valuePayloads), err)
		}
		values = append(values, v)
	}

	layer.Features = make([]Feature, 0, len(features))
	for i, payload := range features {
		f, err := decodeFeature(payload, keys, values, layer.Extent)
		if err != nil {
			return Layer{}, fmt.Errorf("mvt: layer %q, feature %d of %d: %w", layer.Name, i, len(features), err)
		}
		layer.Features = append(layer.Features, f)
	}
	return layer, nil
}

func decodeFeature(data []byte, keys []string, values []Value, extent uint32) (Feature, error) {
	var (
		f       Feature
		tags    []uint32
		geom    []uint32
		sawType bool
	)
	r := protoReader{b: data, what: "a feature"}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return Feature{}, err
		}
		switch {
		case field == featureID && wire == wireVarint:
			v, err := r.uvarint("the feature id")
			if err != nil {
				return Feature{}, err
			}
			f.ID, f.HasID = v, true
		case field == featureTags:
			tags, err = r.packedUint32(tags, "the tags", wire)
			if err != nil {
				return Feature{}, err
			}
		case field == featureType && wire == wireVarint:
			v, err := r.uvarint("the geometry type")
			if err != nil {
				return Feature{}, err
			}
			if v > uint64(GeomPolygon) {
				return Feature{}, fmt.Errorf("geometry type %d is not one of the four the schema defines", v)
			}
			f.Type, sawType = GeomType(v), true
		case field == featureGeometry:
			geom, err = r.packedUint32(geom, "the geometry", wire)
			if err != nil {
				return Feature{}, err
			}
		default:
			if err := r.skip(field, wire); err != nil {
				return Feature{}, err
			}
		}
	}
	if !sawType {
		// An absent type is UNKNOWN by protobuf's rules, and an unknown
		// geometry is one this decoder can read and cannot interpret. It is
		// not an error, and the geometry below decodes to nothing.
		f.Type = GeomUnknown
	}

	if len(tags)%2 != 0 {
		return Feature{}, fmt.Errorf("has %d tag indices, and they come in key/value pairs", len(tags))
	}
	if len(tags) > 0 {
		f.Tags = make(map[string]Value, len(tags)/2)
	}
	for i := 0; i < len(tags); i += 2 {
		ki, vi := int(tags[i]), int(tags[i+1])
		if ki >= len(keys) {
			return Feature{}, fmt.Errorf("refers to key %d and the layer has %d keys", ki, len(keys))
		}
		if vi >= len(values) {
			return Feature{}, fmt.Errorf("refers to value %d and the layer has %d values", vi, len(values))
		}
		f.Tags[keys[ki]] = values[vi]
	}

	g, err := decodeGeometry(f.Type, geom, extent)
	if err != nil {
		return Feature{}, fmt.Errorf("%s geometry: %w", f.Type, err)
	}
	f.Geometry = g
	return f, nil
}

func decodeValue(data []byte) (Value, error) {
	var v Value
	r := protoReader{b: data, what: "a value"}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return Value{}, err
		}
		switch {
		case field == valueString && wire == wireBytes:
			b, err := r.bytes("a string value")
			if err != nil {
				return Value{}, err
			}
			v = StringValue(string(b))
		case field == valueFloat && wire == wireFixed32:
			u, err := r.fixed32("a float value")
			if err != nil {
				return Value{}, err
			}
			v = FloatValue(math.Float32frombits(u))
		case field == valueDouble && wire == wireFixed64:
			u, err := r.fixed64("a double value")
			if err != nil {
				return Value{}, err
			}
			v = DoubleValue(math.Float64frombits(u))
		case field == valueInt && wire == wireVarint:
			u, err := r.uvarint("an int value")
			if err != nil {
				return Value{}, err
			}
			v = IntValue(int64(u))
		case field == valueUint && wire == wireVarint:
			u, err := r.uvarint("a uint value")
			if err != nil {
				return Value{}, err
			}
			v = UintValue(u)
		case field == valueSint && wire == wireVarint:
			u, err := r.uvarint("an sint value")
			if err != nil {
				return Value{}, err
			}
			v = SintValue(unzigzag64(u))
		case field == valueBool && wire == wireVarint:
			u, err := r.uvarint("a bool value")
			if err != nil {
				return Value{}, err
			}
			v = BoolValue(u != 0)
		default:
			if err := r.skip(field, wire); err != nil {
				return Value{}, err
			}
		}
	}
	if v.Kind == ValueNone {
		return Value{}, fmt.Errorf("carries none of the seven types the schema defines")
	}
	return v, nil
}
