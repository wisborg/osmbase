package osmbasetest

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/wisborg/osmbase/mvt"
)

// Tag is one attribute of a feature in a synthetic tile.
//
// Tags are an ORDERED slice rather than a map because the bytes depend on the
// order: keys and values go into per-layer tables in first-appearance order,
// and a map would decide that order by whatever the runtime felt like, making
// one fixture encode to several different byte strings. A test comparing bytes
// would then fail about one run in two.
type Tag struct {
	Key   string
	Value mvt.Value
}

// FeatureSpec describes one feature to encode.
type FeatureSpec struct {
	// ID is written only when HasID is set. The field is optional in the
	// schema and 0 is a legal identifier, so a fixture has to be able to say
	// "feature zero" and "no identifier" separately.
	ID    uint64
	HasID bool

	Type mvt.GeomType
	Tags []Tag

	// Geometry is encoded VERBATIM. Rings keep the winding and the point order
	// they are given, and no polygon is normalised on the way in. That is what
	// makes it possible to hand the decoder the same polygon wound both ways
	// and check that what comes back is the same either way -- a builder that
	// tidied up first could not express the input the test needs.
	Geometry mvt.Geometry

	// Extra is appended to the encoded feature verbatim. It is how a fixture
	// carries a field this decoder does not know about, or writes a repeated
	// field one value at a time instead of packing it.
	Extra []byte

	// RawGeometry, when non-nil, is written as the feature's geometry instead
	// of encoding Geometry. It is the way to build a command stream a correct
	// encoder would never emit: a truncated parameter list, an unknown command
	// id, a ring with no ClosePath.
	RawGeometry []uint32
}

// LayerSpec describes one layer to encode.
type LayerSpec struct {
	Name string
	// Version is written as 2 when left at 0, which is the version every real
	// tile declares.
	Version uint32
	// Extent is written as mvt.DefaultExtent when left at 0. It is written
	// explicitly either way, so a fixture for the schema's default-when-absent
	// behaviour needs OmitExtent.
	Extent uint32
	// OmitExtent leaves the extent field out entirely, which is how a decoder
	// is tested for applying the schema's default of 4096.
	OmitExtent bool
	Features   []FeatureSpec

	// Extra is appended to the encoded layer verbatim, for the same reason as
	// FeatureSpec.Extra.
	Extra []byte
}

// TileSpec describes a whole vector tile.
type TileSpec struct {
	Layers []LayerSpec

	// Extra is appended to the encoded tile verbatim.
	Extra []byte
}

// BuildTile encodes a vector tile.
//
// Like the archive builder, this is written from the schema rather than by
// running the decoder backwards, and its output is deterministic.
func BuildTile(spec TileSpec) ([]byte, error) {
	var out []byte
	for _, l := range spec.Layers {
		body, err := buildLayer(l)
		if err != nil {
			return nil, err
		}
		out = appendTagged(out, 3, 2)
		out = appendBytes(out, body)
	}
	return append(out, spec.Extra...), nil
}

func buildLayer(spec LayerSpec) ([]byte, error) {
	version := spec.Version
	if version == 0 {
		version = 2
	}
	extent := spec.Extent
	if extent == 0 {
		extent = mvt.DefaultExtent
	}

	// The key and value tables are built in first-appearance order, so the
	// encoding of a fixture is a function of the fixture alone.
	var (
		keys     []string
		values   []mvt.Value
		keyIndex = map[string]int{}
		// mvt.Value is comparable -- seven scalar fields and a string -- so it
		// is its own map key. An earlier version keyed on Value.String(),
		// which that method's own documentation says is for a human reading a
		// diagnostic and that nothing should parse; a fixture package whose
		// justification is independence from the code under test has no
		// business depending on the formatting of the code under test.
		//
		// A NaN value never equals itself, so repeated NaNs get one table
		// entry each rather than being folded into one. That is deterministic,
		// and it is arguably the more honest encoding: two NaNs are not known
		// to be the same value.
		valIndex = map[mvt.Value]int{}
	)
	intern := func(t Tag) (uint32, uint32) {
		ki, ok := keyIndex[t.Key]
		if !ok {
			ki = len(keys)
			keyIndex[t.Key] = ki
			keys = append(keys, t.Key)
		}
		vi, ok := valIndex[t.Value]
		if !ok {
			vi = len(values)
			valIndex[t.Value] = vi
			values = append(values, t.Value)
		}
		return uint32(ki), uint32(vi)
	}

	features := make([][]byte, 0, len(spec.Features))
	for i, f := range spec.Features {
		body, err := buildFeature(f, intern)
		if err != nil {
			return nil, fmt.Errorf("osmbasetest: layer %q feature %d: %w", spec.Name, i, err)
		}
		features = append(features, body)
	}

	var out []byte
	out = appendTagged(out, 1, 2)
	out = appendBytes(out, []byte(spec.Name))
	for _, f := range features {
		out = appendTagged(out, 2, 2)
		out = appendBytes(out, f)
	}
	for _, k := range keys {
		out = appendTagged(out, 3, 2)
		out = appendBytes(out, []byte(k))
	}
	for _, v := range values {
		body, err := buildValue(v)
		if err != nil {
			return nil, fmt.Errorf("osmbasetest: layer %q: %w", spec.Name, err)
		}
		out = appendTagged(out, 4, 2)
		out = appendBytes(out, body)
	}
	if !spec.OmitExtent {
		out = appendTagged(out, 5, 0)
		out = binary.AppendUvarint(out, uint64(extent))
	}
	out = appendTagged(out, 15, 0)
	out = binary.AppendUvarint(out, uint64(version))
	return append(out, spec.Extra...), nil
}

func buildFeature(spec FeatureSpec, intern func(Tag) (uint32, uint32)) ([]byte, error) {
	var out []byte
	if spec.HasID {
		out = appendTagged(out, 1, 0)
		out = binary.AppendUvarint(out, spec.ID)
	}
	if len(spec.Tags) > 0 {
		var packed []byte
		for _, t := range spec.Tags {
			ki, vi := intern(t)
			packed = binary.AppendUvarint(packed, uint64(ki))
			packed = binary.AppendUvarint(packed, uint64(vi))
		}
		out = appendTagged(out, 2, 2)
		out = appendBytes(out, packed)
	}
	out = appendTagged(out, 3, 0)
	out = binary.AppendUvarint(out, uint64(spec.Type))

	geom := spec.RawGeometry
	if geom == nil {
		var err error
		geom, err = EncodeGeometry(spec.Type, spec.Geometry)
		if err != nil {
			return nil, err
		}
	}
	if len(geom) > 0 {
		var packed []byte
		for _, g := range geom {
			packed = binary.AppendUvarint(packed, uint64(g))
		}
		out = appendTagged(out, 4, 2)
		out = appendBytes(out, packed)
	}
	return append(out, spec.Extra...), nil
}

// EncodeGeometry turns geometry into the command stream the schema defines.
//
// The cursor persists across the whole feature and ClosePath does not move it,
// so each ring's opening MoveTo is a delta from the last point of the ring
// before it. Encoding it any other way produces a tile that a lenient decoder
// still draws, displaced.
func EncodeGeometry(t mvt.GeomType, g mvt.Geometry) ([]uint32, error) {
	var (
		out    []uint32
		cx, cy int32
	)
	moveTo := func(p mvt.Point) {
		out = append(out, command(1, 1), zigzag32(p.X-cx), zigzag32(p.Y-cy))
		cx, cy = p.X, p.Y
	}
	lineTo := func(pts []mvt.Point) {
		out = append(out, command(2, len(pts)))
		for _, p := range pts {
			out = append(out, zigzag32(p.X-cx), zigzag32(p.Y-cy))
			cx, cy = p.X, p.Y
		}
	}

	switch t {
	case mvt.GeomUnknown:
		return nil, nil
	case mvt.GeomPoint:
		if len(g.Points) == 0 {
			return nil, nil
		}
		out = append(out, command(1, len(g.Points)))
		for _, p := range g.Points {
			out = append(out, zigzag32(p.X-cx), zigzag32(p.Y-cy))
			cx, cy = p.X, p.Y
		}
	case mvt.GeomLineString:
		for _, line := range g.Lines {
			if len(line) < 2 {
				return nil, fmt.Errorf("osmbasetest: a linestring part has %d points", len(line))
			}
			moveTo(line[0])
			lineTo(line[1:])
		}
	case mvt.GeomPolygon:
		for _, poly := range g.Polygons {
			for _, ring := range append([]mvt.Ring{poly.Exterior}, poly.Holes...) {
				if len(ring) < 3 {
					return nil, fmt.Errorf("osmbasetest: a polygon ring has %d points", len(ring))
				}
				moveTo(ring[0])
				lineTo(ring[1:])
				out = append(out, command(7, 1))
			}
		}
	default:
		return nil, fmt.Errorf("osmbasetest: geometry type %d is not one the schema defines; use RawGeometry", uint8(t))
	}
	return out, nil
}

func buildValue(v mvt.Value) ([]byte, error) {
	var out []byte
	switch v.Kind {
	case mvt.ValueString:
		out = appendTagged(out, 1, 2)
		out = appendBytes(out, []byte(v.Str))
	case mvt.ValueFloat:
		out = appendTagged(out, 2, 5)
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v.Float))
	case mvt.ValueDouble:
		out = appendTagged(out, 3, 1)
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(v.Double))
	case mvt.ValueInt:
		out = appendTagged(out, 4, 0)
		out = binary.AppendUvarint(out, uint64(v.Int))
	case mvt.ValueUint:
		out = appendTagged(out, 5, 0)
		out = binary.AppendUvarint(out, v.Uint)
	case mvt.ValueSint:
		out = appendTagged(out, 6, 0)
		out = binary.AppendUvarint(out, zigzag64(v.Sint))
	case mvt.ValueBool:
		out = appendTagged(out, 7, 0)
		b := uint64(0)
		if v.Bool {
			b = 1
		}
		out = binary.AppendUvarint(out, b)
	default:
		return nil, fmt.Errorf("value of kind %s carries nothing to encode", v.Kind)
	}
	return out, nil
}

// command builds a command integer: the command id in the low three bits and
// the repeat count above them.
func command(id uint32, count int) uint32 { return id | uint32(count)<<3 }

// zigzag32 encodes a geometry parameter and zigzag64 an sint attribute value,
// interleaving the two signs onto the naturals. They are named for their width
// and written together for the same reason mvt's decoding pair is: one rule,
// two spellings, and a shift at the wrong width is invisible except at the
// extremes.
func zigzag32(v int32) uint32 { return uint32(v<<1) ^ uint32(v>>31) }

func zigzag64(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }

// appendTagged writes a protobuf field key.
func appendTagged(b []byte, field, wire int) []byte {
	return binary.AppendUvarint(b, uint64(field)<<3|uint64(wire))
}

// appendBytes writes a length-delimited payload.
func appendBytes(b, payload []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(payload)))
	return append(b, payload...)
}
