package kiro

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrInvalidToolUse identifies upstream tool events that cannot be executed safely.
var ErrInvalidToolUse = errors.New("invalid Kiro tool event")

type toolUseState struct {
	name        string
	input       strings.Builder
	objectInput bool
}

type toolUseDecoder struct {
	pending   map[string]*toolUseState
	completed map[string]bool
}

func (d *toolUseDecoder) consume(payload []byte, callback *KiroStreamCallback) error {
	var event map[string]json.RawMessage
	if json.Unmarshal(payload, &event) != nil || event == nil {
		return fmt.Errorf("%w: malformed event JSON", ErrInvalidToolUse)
	}
	id, err := toolStringField(event, "toolUseId", "toolUseID", "tool_use_id", "id")
	if err != nil {
		return err
	}
	name, err := toolStringField(event, "name", "toolName", "tool_name")
	if err != nil {
		return err
	}
	var stop bool
	for _, key := range []string{"stop", "isStop", "done"} {
		if raw, ok := event[key]; ok {
			if string(raw) != "true" && string(raw) != "false" {
				return fmt.Errorf("%w: non-boolean stop", ErrInvalidToolUse)
			}
			stop = string(raw) == "true"
			break
		}
	}
	// Some upstream continuations omit identity. They are only unambiguous
	// while exactly one identified call remains open.
	if id == "" {
		if len(d.pending) != 1 {
			return fmt.Errorf("%w: missing or ambiguous tool identity", ErrInvalidToolUse)
		}
		for pendingID := range d.pending {
			id = pendingID
		}
	}
	if d.completed[id] {
		return fmt.Errorf("%w: duplicate completed tool", ErrInvalidToolUse)
	}
	state := d.pending[id]
	if state == nil {
		state = &toolUseState{}
		d.pending[id] = state
	}
	if name != "" {
		if state.name != "" && state.name != name {
			return fmt.Errorf("%w: tool name changed", ErrInvalidToolUse)
		}
		state.name = name
	}
	if raw, ok := event["input"]; ok {
		if state.objectInput {
			return fmt.Errorf("%w: input after complete object", ErrInvalidToolUse)
		}
		if len(raw) > 0 && raw[0] == '{' {
			if state.input.Len() != 0 {
				return fmt.Errorf("%w: object overwrites input fragments", ErrInvalidToolUse)
			}
			state.objectInput = true
			_, _ = state.input.Write(raw)
		} else {
			var fragment string
			if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &fragment) != nil {
				return fmt.Errorf("%w: input must be JSON fragments or an object", ErrInvalidToolUse)
			}
			_, _ = state.input.WriteString(fragment)
		}
	}
	if !stop {
		return nil
	}
	if strings.TrimSpace(state.name) == "" {
		return fmt.Errorf("%w: missing tool name", ErrInvalidToolUse)
	}
	decoder := json.NewDecoder(strings.NewReader(state.input.String()))
	decoder.UseNumber()
	var input map[string]any
	if decoder.Decode(&input) != nil || input == nil {
		return fmt.Errorf("%w: input is not a complete JSON object", ErrInvalidToolUse)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("%w: trailing input data", ErrInvalidToolUse)
	}
	delete(d.pending, id)
	d.completed[id] = true
	if callback.OnToolUse != nil {
		callback.OnToolUse(KiroToolUse{ToolUseID: id, Name: state.name, Input: input})
	}
	return nil
}

func toolStringField(event map[string]json.RawMessage, keys ...string) (string, error) {
	for _, key := range keys {
		if raw, ok := event[key]; ok {
			var value string
			if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
				return "", fmt.Errorf("%w: non-string identity", ErrInvalidToolUse)
			}
			if strings.TrimSpace(value) != "" {
				return value, nil
			}
		}
	}
	return "", nil
}
