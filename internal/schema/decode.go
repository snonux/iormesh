package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// ErrInvalid wraps every validation failure, so callers can tell a bad
// record (skip or report it) from an I/O error (stop).
var ErrInvalid = errors.New("schema: invalid")

// jsonField is one struct field as the wire format sees it.
type jsonField struct {
	typ      reflect.Type
	required bool // no omitempty: must be present and not null
}

// DecodeEvent parses one NDJSON line strictly and validates the result.
// Strictness is the point: a reader that silently drops or folds an
// unexpected key would hide a schema mismatch until an LLM draws the
// wrong conclusion. See checkKeys for what "strictly" covers.
func DecodeEvent(data []byte) (Event, error) {
	var ev Event
	if err := checkKeys(data, reflect.TypeFor[Event]()); err != nil {
		return Event{}, err
	}
	if err := decodeStrict(data, &ev); err != nil {
		return Event{}, err
	}
	if err := ev.Validate(); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// checkKeys walks raw against struct type t before encoding/json sees it,
// because encoding/json alone is too lenient in three ways that matter
// here: it matches keys case-insensitively ("Comm" overwrites "comm"),
// it cannot tell a missing number from a zero one, and it decodes null as
// the zero value. So: every key must equal a json tag name exactly, every
// field without omitempty must be present and non-null, and the same
// rules apply to nested structs and slices of structs. Map fields (Attrs)
// are free-form and not descended into.
func checkKeys(raw []byte, t reflect.Type) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if obj == nil {
		return invalidf("%s: not a JSON object", t.Name())
	}
	fields := jsonFields(t)
	// Sorted so the reported violation is the same on every run.
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		f, ok := fields[k]
		if !ok {
			return invalidf("%s: unknown key %q (keys are case-sensitive)", t.Name(), k)
		}
		if err := checkNested(obj[k], f.typ); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !fields[name].required {
			continue
		}
		v, ok := obj[name]
		if !ok {
			return invalidf("%s: missing required field %q", t.Name(), name)
		}
		if isNull(v) {
			return invalidf("%s: required field %q is null", t.Name(), name)
		}
	}
	return nil
}

// checkNested applies checkKeys to a struct-valued or struct-slice-valued
// field. A null optional struct is the same as an absent one.
func checkNested(raw json.RawMessage, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isNull(raw) {
		return nil
	}
	switch {
	case t.Kind() == reflect.Struct:
		return checkKeys(raw, t)
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct:
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		for _, e := range elems {
			if err := checkKeys(e, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields maps each json tag name of struct type t to its field. The
// tags are the single source of truth for required-ness, so appending an
// optional (omitempty) field never needs a second list updated.
func jsonFields(t reflect.Type) map[string]jsonField {
	fields := make(map[string]jsonField, t.NumField())
	for i := range t.NumField() {
		sf := t.Field(i)
		name, opts, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = jsonField{typ: sf.Type, required: !strings.Contains(opts, "omitempty")}
	}
	return fields
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// decodeStrict unmarshals exactly one JSON value into v, rejecting
// unknown fields and trailing data.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after JSON value", ErrInvalid)
	}
	return nil
}
