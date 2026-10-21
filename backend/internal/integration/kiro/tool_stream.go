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
	sawInput    bool
}

type toolUseDecoder struct {
	tools     []KiroToolWrapper
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
	// Keep a single anonymous prefix private until its real ID arrives.
	if id == "" {
		switch len(d.pending) {
		case 0:
			if name == "" || stop {
				return fmt.Errorf("%w: missing tool identity", ErrInvalidToolUse)
			}
		case 1:
			if d.pending[""] != nil && name != "" {
				return fmt.Errorf("%w: repeated anonymous tool start", ErrInvalidToolUse)
			}
			for pendingID := range d.pending {
				id = pendingID
			}
		default:
			return fmt.Errorf("%w: ambiguous tool identity", ErrInvalidToolUse)
		}
	} else if anonymous := d.pending[""]; anonymous != nil {
		if len(d.pending) != 1 || d.pending[id] != nil || (name != "" && anonymous.name != name) {
			return fmt.Errorf("%w: ambiguous late tool identity", ErrInvalidToolUse)
		}
		delete(d.pending, "")
		d.pending[id] = anonymous
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
		state.sawInput = true
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
	if state.input.Len() > maxKiroEventFrame {
		return fmt.Errorf("%w: tool input too large", ErrInvalidToolUse)
	}
	if !stop {
		return nil
	}
	if id == "" {
		return fmt.Errorf("%w: completed tool without ID", ErrInvalidToolUse)
	}
	if strings.TrimSpace(state.name) == "" {
		return fmt.Errorf("%w: missing tool name", ErrInvalidToolUse)
	}
	if !state.sawInput && toolAcceptsEmptyInput(d.tools, state.name) {
		_, _ = state.input.WriteString("{}")
	}
	if !validKiroJSON([]byte(state.input.String())) {
		return fmt.Errorf("%w: invalid input JSON or Unicode", ErrInvalidToolUse)
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
