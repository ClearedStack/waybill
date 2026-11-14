// Package strictjson decodes JSON and refuses anything ambiguous: unknown
// fields, duplicate keys, deep nesting and trailing data.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// MaxDepth is the deepest nesting of objects and arrays accepted.
const MaxDepth = 32

var (
	ErrDuplicateKey = errors.New("strictjson: duplicate key")
	ErrTooDeep      = errors.New("strictjson: nested too deeply")
	ErrTrailing     = errors.New("strictjson: data after the value")
)

// Decode reads exactly one JSON value from data into v. Numbers are kept as
// json.Number so no precision is lost.
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walk(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrTrailing
	}

	dec = json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// walk consumes one value and checks nesting depth and duplicate keys.
func walk(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= MaxDepth {
		return ErrTooDeep
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if seen[key] {
				return ErrDuplicateKey
			}
			seen[key] = true
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // closing delimiter
	return err
}
