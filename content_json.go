package cloud

import (
	"bytes"
	"encoding/json"
)

// Keep the original wire representation separately from the decoded baseline:
// encoding/json otherwise conflates absent fields, nulls, and scalar zeros.
type objectState struct {
	original map[string]json.RawMessage
	baseline map[string]json.RawMessage
}

func objectFields(value any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func decodeObject(data []byte, value any) (objectState, map[string]json.RawMessage, error) {
	var state objectState
	if err := json.Unmarshal(data, value); err != nil {
		return state, nil, err
	}
	if err := json.Unmarshal(data, &state.original); err != nil {
		return state, nil, err
	}
	baseline, err := objectFields(value)
	if err != nil {
		return state, nil, err
	}
	state.baseline = baseline
	extra := make(map[string]json.RawMessage)
	for key, raw := range state.original {
		if _, known := baseline[key]; !known {
			extra[key] = raw
		}
	}
	return state, extra, nil
}

func encodeObject(value any, extra map[string]json.RawMessage, state objectState) ([]byte, error) {
	fields, err := objectFields(value)
	if err != nil {
		return nil, err
	}
	result := make(map[string]json.RawMessage, len(fields)+len(extra))
	for key, raw := range extra {
		if _, known := fields[key]; !known {
			result[key] = raw
		}
	}
	unchanged := len(extra) == 0
	for key, raw := range fields {
		if state.baseline != nil && bytes.Equal(raw, state.baseline[key]) {
			if original, exists := state.original[key]; exists {
				result[key] = original
			}
			continue
		}
		unchanged = false
		result[key] = raw
	}
	if state.baseline != nil && state.original == nil && unchanged {
		return []byte("null"), nil
	}
	return json.Marshal(result)
}
