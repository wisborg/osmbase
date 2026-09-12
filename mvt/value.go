package mvt

import "strconv"

// ValueKind says which of a Value's fields carries its content.
//
// The zero value is deliberately not a usable kind: a Value that was never set
// is distinguishable from one holding the empty string or the number zero.
// That distinction is the whole reason tags are looked up through a map and
// tested for membership rather than compared against a zero value -- a feature
// with no "name" tag and a feature named "" are different things, and a style
// that cannot tell them apart will label one of them.
type ValueKind uint8

const (
	ValueNone ValueKind = iota
	ValueString
	ValueFloat
	ValueDouble
	ValueInt
	ValueUint
	ValueSint
	ValueBool
)

func (k ValueKind) String() string {
	switch k {
	case ValueNone:
		return "none"
	case ValueString:
		return "string"
	case ValueFloat:
		return "float"
	case ValueDouble:
		return "double"
	case ValueInt:
		return "int"
	case ValueUint:
		return "uint"
	case ValueSint:
		return "sint"
	case ValueBool:
		return "bool"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Value is one attribute value from a layer's value table.
//
// The wire format has seven value types and this keeps them apart rather than
// folding the five numeric ones into a float64. Two reasons: a uint64 above
// 2^53 does not survive the round trip through a float, and an encoder that
// writes zoom thresholds as sint and populations as uint is telling us
// something a collapsed type would throw away. Callers that only want a number
// have Float64, which does the folding where it is wanted and says when the
// value was not a number at all.
type Value struct {
	Kind   ValueKind
	Str    string
	Float  float32
	Double float64
	Int    int64
	Uint   uint64
	Sint   int64
	Bool   bool
}

// StringValue and the constructors below build a Value of each kind.
//
// They are the only way a Value is made anywhere in this module, including in
// the decoder: a kind and its field have to be set together, and a constructor
// per kind is what makes that impossible to get half right. Nothing should
// assign the fields directly, and nothing does.
func StringValue(s string) Value  { return Value{Kind: ValueString, Str: s} }
func FloatValue(f float32) Value  { return Value{Kind: ValueFloat, Float: f} }
func DoubleValue(f float64) Value { return Value{Kind: ValueDouble, Double: f} }
func IntValue(i int64) Value      { return Value{Kind: ValueInt, Int: i} }
func UintValue(u uint64) Value    { return Value{Kind: ValueUint, Uint: u} }
func SintValue(i int64) Value     { return Value{Kind: ValueSint, Sint: i} }
func BoolValue(b bool) Value      { return Value{Kind: ValueBool, Bool: b} }

// Text returns the value's string content and whether it had any.
//
// It does NOT stringify a number. A style asking for a name wants a name, and
// a layer whose "name" happens to be encoded as an integer is a tile worth
// noticing rather than one worth papering over.
func (v Value) Text() (string, bool) {
	if v.Kind != ValueString {
		return "", false
	}
	return v.Str, true
}

// Float64 returns the value as a float64 and whether it was numeric at all.
//
// Every numeric kind converts, because a caller comparing a minimum-zoom
// threshold does not care which of the five integer encodings the writer
// chose. A bool does not convert: 1 and 0 for true and false is a convention
// this decoder has no business inventing on a caller's behalf.
func (v Value) Float64() (float64, bool) {
	switch v.Kind {
	case ValueFloat:
		return float64(v.Float), true
	case ValueDouble:
		return v.Double, true
	case ValueInt:
		return float64(v.Int), true
	case ValueUint:
		return float64(v.Uint), true
	case ValueSint:
		return float64(v.Sint), true
	}
	return 0, false
}

// String renders the value for a human reading a diagnostic. It is not a
// serialisation and nothing should parse it.
func (v Value) String() string {
	switch v.Kind {
	case ValueString:
		return strconv.Quote(v.Str)
	case ValueFloat:
		return strconv.FormatFloat(float64(v.Float), 'g', -1, 32)
	case ValueDouble:
		return strconv.FormatFloat(v.Double, 'g', -1, 64)
	case ValueInt:
		return strconv.FormatInt(v.Int, 10)
	case ValueUint:
		return strconv.FormatUint(v.Uint, 10)
	case ValueSint:
		return strconv.FormatInt(v.Sint, 10)
	case ValueBool:
		return strconv.FormatBool(v.Bool)
	}
	return "<unset>"
}
