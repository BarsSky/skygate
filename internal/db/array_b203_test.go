// v1.5.0+ (B203.1) — unit tests for the StringArray
// sql.Scanner + driver.Valuer used to scan / write
// PostgreSQL TEXT[] columns via the database/sql interface.
//
// The pre-B203.1 code stored replica_node_ids as []string,
// which the pgx v5 stdlib refuses to scan (it returns
// the PG array literal as a string). StringArray implements
// sql.Scanner to parse the literal, and driver.Valuer to
// write it back.

package db

import (
	"reflect"
	"testing"
)

func TestStringArray_Scan(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want StringArray
	}{
		{
			name: "nil src",
			in:   nil,
			want: StringArray{},
		},
		{
			name: "empty array",
			in:   "{}",
			want: StringArray{},
		},
		{
			name: "single element",
			in:   "{skyadmin}",
			want: StringArray{"skyadmin"},
		},
		{
			name: "two elements",
			in:   "{skyadmin,skyworker}",
			want: StringArray{"skyadmin", "skyworker"},
		},
		{
			name: "three elements",
			in:   "{a,b,c}",
			want: StringArray{"a", "b", "c"},
		},
		{
			name: "quoted with space",
			in:   `{"a b","c d"}`,
			want: StringArray{"a b", "c d"},
		},
		{
			name: "quoted with comma",
			in:   `{"a,b","c,d"}`,
			want: StringArray{"a,b", "c,d"},
		},
		{
			name: "byte slice input",
			in:   []byte("{x,y}"),
			want: StringArray{"x", "y"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got StringArray
			if err := got.Scan(c.in); err != nil {
				t.Fatalf("Scan(%#v): %v", c.in, err)
			}
			if !reflect.DeepEqual([]string(got), []string(c.want)) {
				t.Errorf("got %#v, want %#v", []string(got), []string(c.want))
			}
		})
	}
}

func TestStringArray_Scan_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
	}{
		{name: "not a string", in: 42},
		{name: "missing braces", in: "a,b,c"},
		{name: "only opening brace", in: "{a,b"},
		{name: "only closing brace", in: "a,b}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got StringArray
			if err := got.Scan(c.in); err == nil {
				t.Errorf("Scan(%#v) expected error, got nil", c.in)
			}
		})
	}
}

func TestStringArray_Value(t *testing.T) {
	cases := []struct {
		name string
		in   StringArray
		want string
	}{
		{
			name: "nil",
			in:   nil,
			want: "{}",
		},
		{
			name: "empty",
			in:   StringArray{},
			want: "{}",
		},
		{
			name: "single",
			in:   StringArray{"skyadmin"},
			want: "{skyadmin}",
		},
		{
			name: "multi",
			in:   StringArray{"skyadmin", "skyworker"},
			want: "{skyadmin,skyworker}",
		},
		{
			name: "needs quoting (space)",
			in:   StringArray{"a b"},
			want: `{"a b"}`,
		},
		{
			name: "needs quoting (comma)",
			in:   StringArray{"a,b"},
			want: `{"a,b"}`,
		},
		{
			name: "needs quoting (quote)",
			in:   StringArray{`a"b`},
			want: `{"a\"b"}`,
		},
		{
			name: "needs quoting (backslash)",
			in:   StringArray{`a\b`},
			want: `{"a\\b"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.in.Value()
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if s, ok := got.(string); !ok || s != c.want {
				t.Errorf("got %#v, want %q", got, c.want)
			}
		})
	}
}

// TestStringArray_Roundtrip — value returned by Value() must
// be parseable by Scan() back to the original slice. This
// pins the symmetry contract.
//
// Note: a nil StringArray round-trips to StringArray{}
// (empty, non-nil) because Value() always emits "{}" and
// Scan("{}") returns StringArray{}. The length is preserved;
// only the nil-vs-empty distinction is lost, which is
// semantically equivalent for []string.
func TestStringArray_Roundtrip(t *testing.T) {
	originals := []StringArray{
		nil,
		{},
		{"skyadmin"},
		{"skyadmin", "skyworker"},
		{"a b", "c d"},
		{`a"b`, `a\b`},
	}
	for _, orig := range originals {
		v, err := orig.Value()
		if err != nil {
			t.Fatalf("Value: %v", err)
		}
		var got StringArray
		if err := got.Scan(v); err != nil {
			t.Fatalf("Scan(%q): %v", v, err)
		}
		if len(got) != len(orig) {
			t.Errorf("length mismatch: orig=%d got=%d (orig=%#v got=%#v)", len(orig), len(got), []string(orig), []string(got))
		}
		// Compare element-by-element to ignore nil-vs-empty
		// distinction (semantically equivalent for []string).
		for i := range orig {
			if i >= len(got) {
				t.Errorf("orig[%d]=%q, got missing", i, orig[i])
				continue
			}
			if got[i] != orig[i] {
				t.Errorf("element %d: orig=%q got=%q", i, orig[i], got[i])
			}
		}
	}
}
