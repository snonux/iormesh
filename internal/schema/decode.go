package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrInvalid wraps every validation failure, so callers can tell a bad
// record (skip or report it) from an I/O error (stop).
var ErrInvalid = errors.New("schema: invalid")

// eventRequiredKeys are the Event fields without omitempty. encoding/json
// cannot tell a missing number from a zero one, so Decode checks key
// presence itself; TestEventRequiredKeysMatchTags keeps this list in step
// with the struct tags.
var eventRequiredKeys = []string{
	"schema_version", "project", "seq", "boot_ns", "wall_ns", "host_id",
	"pid", "pid_incarnation", "tid", "comm", "event", "fd", "fd_gen",
	"ret", "latency_ns", "bytes", "args", "attribution",
}

// DecodeEvent parses one NDJSON line strictly — unknown keys, missing
// required keys and trailing data are errors — and validates the result.
// Strictness is the point: a reader that silently drops an unknown key
// would hide a schema mismatch until an LLM draws the wrong conclusion.
func DecodeEvent(data []byte) (Event, error) {
	var ev Event
	if err := requireKeys(data, eventRequiredKeys); err != nil {
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

// requireKeys fails when the top-level JSON object lacks any of keys.
func requireKeys(data []byte, keys []string) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if obj == nil {
		return fmt.Errorf("%w: not a JSON object", ErrInvalid)
	}
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("%w: missing required field %q", ErrInvalid, k)
		}
	}
	return nil
}

// decodeStrict unmarshals exactly one JSON value into v, rejecting
// unknown fields at every nesting level.
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
