package whorwe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// parseJSONValue parses a JSON value from the given byte slice.
// It returns the parsed value, the number of bytes read, and an error.
func parseJSONValue(r []byte) (value any, read int64, err error) {
	if len(r) == 0 {
		err = errors.New("unexpected end of input")
		return
	}

	idx := findNextNonSpaceIndex(r)
	if idx >= len(r) {
		err = errors.New("unexpected end of input")
		return
	}

	r = r[idx:]
	switch r[0] {
	case '"':
		// String.
		value, read, err = parseJSONString(r[i:])
	case '{':
		// Object.
		value, read, err = parseJSONObject(r[i:])
	case '[':
		// Array.
		value, read, err = parseJSONArray(r[i:])
	case 't', 'f':
		// Boolean.
		value, read, err = parseJSONBool(r[i:])
	case 'n':
		// Null.
		read, err = parseJSONNull(r[i:])
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-':
		// Number.
		value, read, err = parseJSONNumber(r[i:])
	default:
		err = errors.New("unexpected character " + string(b))
	}
	return
}

// parseJSONObject parses a JSON object from the given byte slice.
// It returns the parsed object, the number of bytes read, and an error.
func parseJSONObject(r []byte) (map[string]any, int64, error) {
	object := make(map[string]any)
	reader := bytes.NewReader(r)
	for {
		// Skip over opening brace.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if idx >= len(r) {
			return nil, 0, errors.New("expected {, got end of input")
		} else if r[idx] != '{' {
			return nil, 0, errors.New("expected {, got " + string(r[idx]))
		} else if err := reader.Seek(idx+1, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Skip whitespace between opening brace and key.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if err := reader.Seek(idx, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Read key.
		key, err := parseJSONString(r[reader.Size()-reader.Len():])
		if err != nil {
			return nil, 0, err
		}
		// Advance over key.
		if _, err := reader.Seek(int64(len(key)+2), io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Skip whitespace between key and colon.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if err := reader.Seek(idx, io.SeekCurrent); err != nil {
			return nil, 0, err
		}
		if b, err := reader.ReadByte(); err != nil {
			return nil, 0, err
		} else if b != ':' {
			return nil, 0, errors.New("expected :, got " + string(b))
		}

		// Skip whitespace between colon and value.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if err := reader.Seek(idx, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Read value.
		value, read, err := parseJSONValue(r[reader.Size()-reader.Len():])
		if err != nil {
			return nil, 0, err
		}
		object[key] = value

		// Advance over value.
		if _, err := reader.Seek(read, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Skip whitespace between value and comma.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if err := reader.Seek(idx, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		if isBrace, err := readBraceOrComma('}', reader); err != nil {
			return nil, 0, err
		} else if isBrace {
			// End of object.
			break
		}
	}
	return object, int64(reader.Size() - reader.Len()), nil
}

// parseJSONArray parses a JSON array from the given byte slice.
// It returns the parsed array, the number of bytes read, and an error.
func parseJSONArray(r []byte) ([]any, int64, error) {
	if l := len(r); l < 2 {
		return nil, 0, errors.New("unexpected end of input")
	} else if l == 2 {
		if bytes.Equal(r, []byte(`[]`)) {
			return nil, 2, nil
		}
		return nil, 0, errors.New("invalid array")
	}

	if c := r[0]; c != '[' {
		return nil, 0, errors.New(`expected [ got ` + string(c))
	}
	// Advance over opening brace.
	r = r[1:]

	var array []any
	reader := bytes.NewReader(r)
	for {
		// Skip whitespace.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if err := reader.Seek(idx, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Read value.
		value, read, err := parseJSONValue(r[reader.Size()-reader.Len():])
		if err != nil {
			return nil, 0, err
		}
		array = append(array, value)

		// Advance over value.
		if _, err := reader.Seek(read, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		// Skip whitespace between value and comma.
		idx := findNextNonSpaceIndex(r[reader.Size()-reader.Len():])
		if err := reader.Seek(idx, io.SeekCurrent); err != nil {
			return nil, 0, err
		}

		if isBrace, err := readBraceOrComma(']', reader); err != nil {
			return nil, 0, err
		} else if isBrace {
			// End of array.
			break
		}
	}
	return array, int64(reader.Size() - reader.Len()), nil
}

// parseJSONString reads a JSON string from the given byte slice.
// It returns the parsed string, the number of bytes read, and an error.
func parseJSONString(r []byte) (string, []byte, error) {
	// Smallest valid string is `""`.
	if l := len(r); l < 2 {
		return "", 0, errors.New("unexpected end of input")
	} else if l == 2 {
		if bytes.Equal(r, []byte(`""`)) {
			return "", 2, nil
		}
		return "", 0, errors.New("invalid string")
	}

	if c := r[0]; c != '"' {
		return "", 0, errors.New(`expected " got ` + string(c))
	}
	// Advance over opening quote.
	r = r[1:]

	var value strings.Builder
	var inEsc bool
	var inUEsc bool
	var strEnds bool
	reader := bytes.NewReader(r)
	for {
		// Parse unicode escape sequences.
		if inUEsc {
			maybeRune := make([]byte, 4)
			n, err := reader.Read(maybeRune)
			if err != nil || n != 4 {
				return "", 0, fmt.Errorf(
					"invalid unicode escape sequence \\u%s: %w",
					string(maybeRune),
					err,
				)
			}
			prn, err := strconv.ParseUint(string(maybeRune), 16, 32)
			if err != nil {
				return "", 0, fmt.Errorf(
					"invalid unicode escape sequence \\u%s: %w",
					string(maybeRune),
					err,
				)
			}
			rn := rune(prn)
			if !utf16.IsSurrogate(rn) {
				value.WriteRune(rn)
				inUEsc = false
				continue
			}
			// rn maybe a high surrogate; read the low surrogate.
			maybeRune = make([]byte, 6)
			n, err = reader.Read(maybeRune)
			if err != nil || n != 6 || maybeRune[0] != '\\' || maybeRune[1] != 'u' {
				// Not a valid UTF-16 surrogate pair.
				if _, err := reader.Seek(int64(-n), io.SeekCurrent); err != nil {
					return "", 0, err
				}
				// Invalid low surrogate; write the replacement character.
				value.WriteRune(utf8.RuneError)
			} else {
				rn1, err := strconv.ParseUint(string(maybeRune[2:]), 16, 32)
				if err != nil {
					return "", 0, fmt.Errorf("invalid unicode escape sequence %s: %w", string(maybeRune), err)
				}
				// Check if rn and rn1 are valid UTF-16 surrogate pairs.
				if dec := utf16.DecodeRune(rn, rune(rn1)); dec != utf8.RuneError {
					n = utf8.EncodeRune(maybeRune, dec)
					// Write the decoded rune.
					value.Write(maybeRune[:n])
				}
			}
			inUEsc = false
			continue
		}

		if inEsc {
			b, err := reader.ReadByte()
			if err != nil {
				return "", 0, err
			}
			switch b {
			case 'b':
				value.WriteByte('\b')
			case 'f':
				value.WriteByte('\f')
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			case 'u':
				inUEsc = true
			case '/':
				value.WriteByte('/')
			case '\\':
				value.WriteByte('\\')
			case '"':
				value.WriteByte('"')
			default:
				return "", 0, errors.New("unexpected character in escape sequence " + string(b))
			}
			inEsc = false
			continue
		} else {
			rn, _, err := reader.ReadRune()
			if err != nil {
				if err == io.EOF {
					break
				}
				return "", 0, err
			}
			if rn == '\\' {
				inEsc = true
				continue
			}
			if rn == '"' {
				// String ends on un-escaped quote.
				strEnds = true
				break
			}
			value.WriteRune(rn)
		}
	}
	if !strEnds {
		return "", nil, errors.New("unexpected end of input")
	}
	return value.String(), int64(reader.Size() - reader.Len()), nil
}

// parseJSONInt64 reads a 64 bit integer from the given byte slice.
// It returns the parsed integer, the number of bytes read, and an error.
func parseJSONInt64(r []byte) (int64, int64, error) {
	var num strings.Builder
	var read int64
	for i, b := range r {
		// int64 max is 19 digits long.
		if num.Len() == 20 {
			return 0, 0, errors.New("number too large")
		}
		if i == 0 && b == '-' {
			if r[1] == '0' {
				return 0, 0, errors.New("invalid number")
			}
			num.WriteByte(b)
			continue
		}
		if strings.ContainsRune("0123456789", rune(b)) {
			num.WriteByte(b)
		} else {
			read = int64(i) + 1
			break
		}
	}
	n, err := strconv.ParseInt(num.String(), 10, 64)
	return int64(n), read, err
}

// parseJSONBoolean reads a boolean from the given byte slice.
// It returns the parsed boolean, the number of bytes read, and an error.
func parseJSONBoolean(r []byte) (bool, int64, error) {
	if bytes.HasPrefix(r, []byte("true")) {
		return true, 4, nil
	}
	if bytes.HasPrefix(r, []byte("false")) {
		return false, 5, nil
	}
	return false, 0, errors.New("unable to parse boolean value")
}

// parseJSONNull returns 4 as the first result and nil as the second result
// if the given byte slice starts with a JSON null.
func parseJSONNull(r []byte) (int64, error) {
	if bytes.HasPrefix(r, []byte("null")) {
		return 4, nil
	}
	return 0, errors.New("invalid null")
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// findNextNonSpaceIndex returns the index of the first non-space character in the given byte slice.
func findNextNonSpaceIndex(r []byte) (i int64) {
	for len(r) > 0 && isSpace(r[0]) {
		i++
	}
	return
}

// readBraceOrComma reads a comma or brace from the given reader.
// If the second result is nil, the first result indicates whether a brace was read.
// If the byte read is a comma, it returns false as the first result.
// If the byte read is a brace, it returns true as the first result.
func readBraceOrComma(r io.Reader, brace byte) (bool, error) {
	if b, err := reader.ReadByte(); err != nil {
		return nil, err
	} else if b == brace {
		return true, nil
	} else if b != ',' {
		return nil, errors.New("expected , or ], got " + string(b))
	}
	return false, nil
}
