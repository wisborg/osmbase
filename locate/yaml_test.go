package locate

import (
	"reflect"
	"testing"
)

// A match reads the same in YAML as in JSON: every field under the same name,
// and its level and source as their names, which YAML takes from
// MarshalYAML as JSON takes them from MarshalJSON. Checked without a YAML
// library, which this module does not otherwise need.
func TestMatchInYAMLIsAsInJSON(t *testing.T) {
	ty := reflect.TypeOf(Match{})
	for i := range ty.NumField() {
		f := ty.Field(i)
		if j, ok := f.Tag.Lookup("json"); ok {
			if y, _ := f.Tag.Lookup("yaml"); y != j {
				t.Errorf("Match.%s is %q in JSON and %q in YAML", f.Name, j, y)
			}
		}
	}
	for _, c := range []struct {
		m    interface{ MarshalYAML() (any, error) }
		want string
	}{{Region, "region"}, {Street, "street"}, {Contained, "contained"}, {Near, "near"}} {
		if got, err := c.m.MarshalYAML(); err != nil || got != c.want {
			t.Errorf("%v in YAML: %v %v, want %q", c.m, got, err, c.want)
		}
	}
}
