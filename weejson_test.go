package whorwe

import (
	"bytes"
	"reflect"
	"strconv"
	"testing"
	"unicode/utf8"
)

var parseJSONObjectTestCases = []struct {
	in   []byte
	want map[string]any
	read int64
	err  bool
}{
	{in: []byte(`{}`), want: map[string]any{}, read: 2},
	{in: []byte(`{"a":1}`), want: map[string]any{"a": 1}, read: 6},
	{in: []byte(`{"a":1,"b":2}`), want: map[string]any{"a": 1, "b": 2}, read: 12},
	{in: []byte(`{"a":[1,2,3]}`), want: map[string]any{"a": []any{1, 2, 3}}, read: 13},
	{in: []byte(`{`), err: true},
	{in: []byte(`"a":`), err: true},
	{in: []byte(`:1123`), err: true},
	{in: []byte(`{{}`), err: true},
	{in: []byte(`{"a":1,}`), err: true},
}

func TestParseJSONObject(t *testing.T) {
	for i, tc := range parseJSONObjectTestCases {
		got, read, err := parseJSONObject(tc.in)
		if tc.err && err == nil {
			t.Errorf("test %d: expected error, got nil", i)
			continue
		}
		if !tc.err && err != nil {
			t.Errorf("test %d: unexpected error: %v", i, err)
			continue
		}
		if read != tc.read {
			t.Errorf("test %d: read %d, want %d", i, read, tc.read)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("test %d: got %v, want %v", i, got, tc.want)
		}
	}
}

func FuzzParseJSONObject(f *testing.F) {
	for _, tc := range parseJSONObjectTestCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _ = parseJSONObject(b)
	})
}

var parseJSONArrayTestCases = []struct {
	in   []byte
	want []any
	read int64
	err  bool
}{
	{in: []byte(`[]`), want: []any{}, read: 2},
	{in: []byte(`[1]`), want: []any{1}, read: 3},
	{in: []byte(`[1,2,3]`), want: []any{1, 2, 3}, read: 7},
	{in: []byte(`[1,"2", [3]]`), want: []any{1, "2", []any{3}}, read: 13},
	{in: []byte(`[1,2,3,]`), err: true},
	{in: []byte(`[1,2,3`), err: true},
}

func TestParseJSONArray(t *testing.T) {
	for i, tc := range parseJSONArrayTestCases {
		got, read, err := parseJSONArray(tc.in)
		if tc.err && err == nil {
			t.Errorf("test %d: expected error, got nil", i)
			continue
		}
		if !tc.err && err != nil {
			t.Errorf("test %d: unexpected error: %v", i, err)
			continue
		}
		if read != tc.read {
			t.Errorf("test %d: read %d, want %d", i, read, tc.read)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("test %d: got %v, want %v", i, got, tc.want)
		}
	}
}

func FuzzParseJSONArray(f *testing.F) {
	for _, tc := range parseJSONArrayTestCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _ = parseJSONArray(b)
	})
}

var parseJSONStringTestCases = []struct {
	in   []byte
	want string
	read int64
	err  bool
}{
	{in: []byte(`""`), want: "", read: 2},
	{in: []byte(`"\n"`), want: "\n", read: 4},
	{in: []byte(` "\""`), want: "\"", read: 5},
	{in: []byte(`"\t \\"`), want: "\t \\", read: 7},
	{in: []byte(`"\\\\"`), want: `\\`, read: 6},
	{in: []byte{'"', 0xFE, 0xFE, 0xFF, 0xFF, '"'}, want: "\uFFFD\uFFFD\uFFFD\uFFFD", read: 8},
	{in: []byte(`"\u0061a"`), want: "aa", read: 8},
	{in: []byte(`"\u0159\u0170"`), want: "řŰ", read: 12},
	{in: []byte(`"\uD800\uDC00"`), want: "\U00010000", read: 14},
	{in: []byte(`"\uD800"`), want: "\uFFFD", read: 8},
	{in: []byte(` ::`), err: true},
	{in: []byte(`"`), err: true},
	{in: []byte("\"0\xE5"), err: true},
	{in: []byte(`"\u000"`), err: true},
	{in: []byte(`"\u00MF"`), err: true},
	{in: []byte(`"\uD800\uDC0"`), err: true},
}

func TestParseJSONString(t *testing.T) {
	for i, tc := range parseJSONStringTestCases {
		t.Run("#"+strconv.Itoa(i), func(t *testing.T) {
			got, read, err := parseJSONString(tc.in)
			if tc.err && err == nil {
				t.Errorf("want err for parseJSONString(%s), got nil", tc.in)
			}
			if !tc.err {
				if err != nil {
					t.Errorf("parseJSONString(%s) unexpected error: %s", tc.in, err.Error())
				}
				if tc.want != got {
					t.Errorf("parseJSONString(%s) = %s, want %s", tc.in, got, tc.want)
				}
				if tc.read != read {
					t.Errorf("parseJSONString(%s) read %d, want %d", tc.in, read, tc.read)
				}
			}
		})
	}
}

func FuzzParseJSONString(f *testing.F) {
	for _, tc := range parseJSONStringTestCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if out, _, err := parseJSONString(b); err == nil && !utf8.ValidString(out) {
			t.Errorf("parseJSONString(%s) = %s, invalid string", b, out)
		}
	})
}

var parseJSONInt64TestCases = []struct {
	in   []byte
	want int64
	read int64
	err  bool
}{
	{in: []byte("1235"), want: 1235, read: 4},
	{in: []byte("0"), want: 0, read: 1},
	{in: []byte("5012313123131231"), want: 5012313123131231, read: 16},
	{in: []byte("-1023"), want: -1023, read: 5},
	{in: []byte("-023"), err: true},
	{in: []byte(" 123"), err: true},
	{in: []byte("1231"), err: true},
}

func TestParseJSONInt64(t *testing.T) {
	for i, tc := range parseJSONInt64TestCases {
		t.Run("#"+strconv.Itoa(i), func(t *testing.T) {
			got, read, err := parseJSONInt64(tc.in)
			if tc.err && err == nil {
				t.Errorf("want err for parseJSONInt64(%s), got nil", tc.in)
			}
			if !tc.err {
				if err != nil {
					t.Errorf("parseJSONInt64(%s) unexpected error: %s", tc.in, err.Error())
				}
				if tc.want != got {
					t.Errorf("parseJSONInt64(%s) = %d, want %d", tc.in, got, tc.want)
				}
				if tc.read != read {
					t.Errorf("parseJSONInt64(%s) read %d, want %d", tc.in, read, tc.read)
				}
			}
		})
	}
}

func FuzzParseJSONInt64(f *testing.F) {
	for _, tc := range parseJSONInt64TestCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if out, _, err := parseJSONInt64(b); err == nil &&
			!bytes.Contains(b, []byte(strconv.FormatInt(out, 10))) {
			t.Errorf("parseJSONInt64(%s) = %d, %v", b, out, err)
		}
	})
}

var parseJSONBooleanTestCases = []struct {
	in   []byte
	want bool
	read int64
	err  bool
}{
	{in: []byte("true "), want: true, read: 4},
	{in: []byte("false  "), want: false, read: 5},
	{in: []byte(" false"), err: true},
	{in: []byte("foo"), err: true},
}

func TestParseJSONBoolean(t *testing.T) {
	for i, tc := range parseJSONBooleanTestCases {
		t.Run("#"+strconv.Itoa(i), func(t *testing.T) {
			got, read, err := parseJSONBoolean(tc.in)
			if tc.err && err == nil {
				t.Errorf("want err for parseJSONBoolean(%s), got nil", tc.in)
			}
			if !tc.err {
				if err != nil {
					t.Errorf("parseJSONBoolean(%s) unexpected error: %s", tc.in, err.Error())
				}
				if tc.want != got {
					t.Errorf("parseJSONBoolean(%s) = %t, want %t", tc.in, got, tc.want)
				}
				if tc.read != read {
					t.Errorf("parseJSONBoolean(%s) read %d, want %d", tc.in, read, tc.read)
				}
			}
		})
	}
}

func FuzzParseJSONBoolean(f *testing.F) {
	for _, tc := range parseJSONBooleanTestCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if out, _, err := parseJSONBoolean(b); err == nil &&
			!bytes.Contains(b, []byte(strconv.FormatBool(out))) {
			t.Errorf("parseJSONBoolean(%s) = %t, %v", b, out, err)
		}
	})
}

var parseJSONNullTestCases = []struct {
	in   []byte
	read int64
	err  bool
}{
	{in: []byte("null "), read: 4},
	{in: []byte("null  "), read: 4},
	{in: []byte(" null"), err: true},
	{in: []byte("foo"), err: true},
}

func TestParseJSONNull(t *testing.T) {
	for i, tc := range parseJSONNullTestCases {
		t.Run("#"+strconv.Itoa(i), func(t *testing.T) {
			read, err := parseJSONNull(tc.in)
			if tc.err && err == nil {
				t.Errorf("want err for parseJSONNull(%s), got nil", tc.in)
			}
			if !tc.err {
				if err != nil {
					t.Errorf("parseJSONNull(%s) unexpected error: %s", tc.in, err.Error())
				}
				if tc.read != read {
					t.Errorf("parseJSONNull(%s) read %d, want %d", tc.in, read, tc.read)
				}
			}
		})
	}
}
