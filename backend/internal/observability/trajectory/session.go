package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const SessionSchemaVersion = "tk-session/v1"

// SessionInput is populated only from a checksum-verified, authorized Bundle
// record. Evidence remains in qa-records.jsonl; projection never reads hot QA.
type SessionInput struct {
	RequestID                           string
	UserID, APIKeyID                    int64
	Platform, Endpoint, StableSessionID string
	Evidence                            json.RawMessage
	Metadata                            map[string]any
}

type SourceRef struct {
	RequestID      string `json:"request_id"`
	Path           string `json:"path"`
	ProjectionPath string `json:"projection_path,omitempty"`
}

type SessionTurn struct {
	Role      string          `json:"role"`
	Message   json.RawMessage `json:"message"`
	Source    SourceRef       `json:"source"`
	ToolLinks []ToolLink      `json:"tool_links,omitempty"`
}

type ToolLink struct {
	Kind   string     `json:"kind"`
	ID     string     `json:"id,omitempty"`
	Path   string     `json:"path"`
	Status string     `json:"status"`
	Call   *SourceRef `json:"call,omitempty"`
}

type SessionCall struct {
	RequestID        string                     `json:"request_id"`
	EvidenceFile     string                     `json:"evidence_file"`
	Metadata         map[string]any             `json:"metadata"`
	Parameters       map[string]json.RawMessage `json:"parameters,omitempty"`
	ResponseMetadata map[string]json.RawMessage `json:"response_metadata,omitempty"`
	Sidecars         map[string]json.RawMessage `json:"sidecars,omitempty"`
	Issues           []string                   `json:"issues,omitempty"`
}

type sessionHeader struct {
	SchemaVersion string    `json:"schema_version"`
	SessionID     string    `json:"session_id"`
	WireShape     WireShape `json:"wire_shape"`
	Association   string    `json:"association"`
}

type sessionState struct {
	Header         sessionHeader `json:"header"`
	ExpectedHash   string        `json:"expected_hash"`
	ExpectedLength int           `json:"expected_length"`
}

type prefixMatch struct {
	SessionID string `json:"session_id"`
	Length    int    `json:"length"`
}

// SessionExporter buffers indexes and fragments within a fixed budget, then
// spills to one disk store. Memory never grows with the day window or a session.
// The caller owns dir and removes it on success, failure or cancellation.
type SessionExporter struct {
	store             *sessionStore
	count             int
	historyCache      map[[32]byte]string
	historyCacheBytes int
}

func NewSessionExporter(dir string) (*SessionExporter, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &SessionExporter{store: newSessionStore(dir), historyCache: make(map[[32]byte]string)}, nil
}

func sessionHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	_ = json.Unmarshal(raw, &out)
	return out
}
func rawArray(raw json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	_ = json.Unmarshal(raw, &out)
	return out
}
func rawString(raw json.RawMessage) string {
	var out string
	_ = json.Unmarshal(raw, &out)
	return out
}
func rawJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func canonicalRaw(raw json.RawMessage) string {
	var value any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&value) != nil {
		return string(raw)
	}
	return string(rawJSON(value))
}

const sessionHistoryCacheBytes = 1 << 20

func (e *SessionExporter) canonicalMessage(raw json.RawMessage) string {
	key := sha256.Sum256(raw)
	if value, ok := e.historyCache[key]; ok {
		return value
	}
	value := canonicalRaw(raw)
	cost := len(value) + 160
	if cost <= sessionHistoryCacheBytes {
		if e.historyCacheBytes+cost > sessionHistoryCacheBytes {
			e.historyCache = make(map[[32]byte]string)
			e.historyCacheBytes = 0
		}
		e.historyCache[key] = value
		e.historyCacheBytes += cost
	}
	return value
}
func (e *SessionExporter) historyHashes(scope string, history []json.RawMessage) []string {
	hashes := make([]string, len(history))
	previous := scope
	for i, m := range history {
		previous = sessionHash(previous, e.canonicalMessage(m))
		hashes[i] = previous
	}
	return hashes
}
func (e *SessionExporter) Close() error {
	e.historyCache = nil
	e.historyCacheBytes = 0
	return e.store.close()
}
func (e *SessionExporter) read(ctx context.Context, key string, v any) (bool, error) {
	b, found, err := e.store.get(ctx, key)
	if err != nil || !found {
		return found, err
	}
	return true, json.Unmarshal(b, v)
}
func (e *SessionExporter) write(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.store.put(ctx, key, b)
}
func (e *SessionExporter) append(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.store.append(ctx, key, b)
}

func (e *SessionExporter) Add(ctx context.Context, input SessionInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.RequestID == "" {
		return errors.New("session export requires a request id")
	}
	// Request identity must be unique, including when the archive contains duplicates.
	key := "record:" + sessionHash(input.RequestID)
	_, seen, err := e.store.get(ctx, key)
	if err != nil {
		return err
	}
	if seen {
		return errors.New("session export duplicate request identity")
	}
	if err = e.store.put(ctx, key, []byte{}); err != nil {
		return err
	}
	shape := wireShapeFor(input.Platform, input.Endpoint)
	scope := sessionHash(fmt.Sprint(input.UserID), fmt.Sprint(input.APIKeyID), string(shape))
	blob := rawObject(input.Evidence)
	req := rawObject(blob["request"])
	resp := rawObject(blob["response"])
	request := rawObject(req["body"])
	history := requestHistory(shape, request)
	responsesScalar := shape == WireOpenAIResponses && rawArray(request["input"]) == nil
	hashes := e.historyHashes(scope, history)
	call := SessionCall{RequestID: input.RequestID, EvidenceFile: "qa-records.jsonl", Metadata: input.Metadata}
	if call.Metadata == nil {
		call.Metadata = map[string]any{}
	}
	call.Metadata["source_path"] = "/detail/evidence"
	if blob == nil {
		call.Issues = append(call.Issues, "missing_or_invalid_evidence")
	}
	if request == nil {
		call.Issues = append(call.Issues, "missing_or_invalid_request")
	}
	if status, ok := call.Metadata["status_code"].(int); ok && status >= 400 {
		call.Issues = append(call.Issues, "http_error")
	}
	if shape == WireUnknown {
		call.Issues = append(call.Issues, "unsupported_wire_shape")
	}
	if rawString(input.Evidence) == "***" {
		call.Issues = append(call.Issues, "redacted_evidence")
	}
	if len(blob["redactions"]) > 0 {
		call.Metadata["redactions"] = blob["redactions"]
	}
	if string(req["upstream_divergent"]) == "true" {
		call.Issues = append(call.Issues, "upstream_request_divergent")
		call.Metadata["upstream_request_path"] = "/detail/evidence/request/upstream_body"
	}
	call.Parameters = request
	delete(call.Parameters, historyField(shape))
	response, responseIssues := sessionResponse(shape, resp["body"], blob["stream"])
	call.Issues = append(call.Issues, responseIssues...)
	call.ResponseMetadata = rawObject(response)
	for _, k := range []string{"content", "output", "choices", "candidates"} {
		delete(call.ResponseMetadata, k)
	}
	for _, k := range []string{"internal_thinking_blocks", "encrypted_reasoning"} {
		if v, ok := resp[k]; ok {
			if call.Sidecars == nil {
				call.Sidecars = map[string]json.RawMessage{}
			}
			call.Sidecars[k] = v
		}
	}
	state := sessionState{}
	start := 0
	if input.StableSessionID != "" {
		found, readErr := e.read(ctx, "stable:"+sessionHash(scope, input.StableSessionID), &state)
		if readErr != nil {
			return readErr
		}
		if found && state.ExpectedLength > 0 && state.ExpectedLength <= len(hashes) && hashes[state.ExpectedLength-1] == state.ExpectedHash {
			start = state.ExpectedLength
		} else if found {
			call.Issues = append(call.Issues, "history_boundary")
		}
	} else {
		var match prefixMatch
		ambiguous := false
		for i, key := range hashes {
			var candidate prefixMatch
			found, readErr := e.read(ctx, "prefix:"+key, &candidate)
			if readErr != nil {
				return readErr
			}
			if !found {
				continue
			}
			if candidate.SessionID == "" || (match.SessionID != "" && match.SessionID != candidate.SessionID) {
				ambiguous = true
				break
			}
			match = candidate
			match.Length = i + 1
		}
		if ambiguous {
			call.Issues = append(call.Issues, "ambiguous_history")
		} else if match.SessionID != "" {
			found, readErr := e.read(ctx, "state:"+match.SessionID, &state)
			if readErr != nil {
				return readErr
			}
			if !found {
				return errors.New("session prefix references missing state")
			}
			// A fork/retry must not append behind a newer response.
			if state.ExpectedLength == match.Length && hashes[match.Length-1] == state.ExpectedHash {
				start = match.Length
			} else {
				state = sessionState{}
				call.Issues = append(call.Issues, "history_branch")
			}
		}
	}
	if state.Header.SessionID == "" {
		association := "verified_history"
		if input.StableSessionID != "" {
			association = "synth_session_id"
		} else {
			call.Issues = append(call.Issues, "inferred_session_boundary")
		}
		state.Header = sessionHeader{SchemaVersion: SessionSchemaVersion, SessionID: sessionHash(scope, input.RequestID), WireShape: shape, Association: association}
		if err = e.append(ctx, "sessions", state.Header); err != nil {
			return err
		}
		e.count++
	}
	sid := state.Header.SessionID
	for i := start; i < len(history); i++ {
		turn := SessionTurn{Role: messageRole(shape, history[i]), Message: history[i], Source: SourceRef{RequestID: input.RequestID, Path: fmt.Sprintf("/detail/evidence/request/body/%s/%d", historyField(shape), i)}}
		if responsesScalar {
			turn.Source.Path = "/detail/evidence/request/body/input"
		}
		if err = e.appendTurn(ctx, sid, &turn); err != nil {
			return err
		}
	}
	outputs := responseMessages(shape, response)
	for i, m := range outputs {
		path := "/detail/evidence/response/body"
		if shape == WireOpenAIResponses {
			path += fmt.Sprintf("/output/%d", i)
		}
		turn := SessionTurn{Role: "assistant", Message: m, Source: SourceRef{RequestID: input.RequestID, Path: path}}
		if len(responseMessages(shape, resp["body"])) == 0 {
			turn.Source.Path = "/detail/evidence/stream/chunks"
			turn.Source.ProjectionPath = strings.TrimPrefix(path, "/detail/evidence/response/body")
		}
		if err = e.appendTurn(ctx, sid, &turn); err != nil {
			return err
		}
	}
	if err = e.append(ctx, "calls:"+sid, call); err != nil {
		return err
	}
	outputHistory := responseHistory(shape, response)
	// Extend the already hashed request instead of parsing and hashing it twice.
	if len(outputHistory) > 0 && len(responseIssues) == 0 && len(history) > 0 && !containsIssue(call.Issues, "http_error") {
		tail := e.historyHashes(hashes[len(hashes)-1], outputHistory)
		state.ExpectedLength = len(hashes) + len(tail)
		state.ExpectedHash = tail[len(tail)-1]
		var existing prefixMatch
		found, readErr := e.read(ctx, "prefix:"+state.ExpectedHash, &existing)
		if readErr != nil {
			return readErr
		}
		candidate := prefixMatch{SessionID: sid, Length: state.ExpectedLength}
		if found && existing.SessionID != sid {
			candidate.SessionID = ""
		}
		if err = e.write(ctx, "prefix:"+state.ExpectedHash, candidate); err != nil {
			return err
		}
	} else {
		state.ExpectedHash = ""
		state.ExpectedLength = 0
	}
	if err = e.write(ctx, "state:"+sid, state); err != nil {
		return err
	}
	if input.StableSessionID != "" {
		return e.write(ctx, "stable:"+sessionHash(scope, input.StableSessionID), state)
	}
	return nil
}

func (e *SessionExporter) appendTurn(ctx context.Context, sid string, turn *SessionTurn) error {
	turn.ToolLinks = toolLinks(turn.Message)
	for i := range turn.ToolLinks {
		link := &turn.ToolLinks[i]
		if link.ID == "" {
			link.Status = "unresolved"
			continue
		}
		key := "tool:" + sessionHash(sid, link.ID)
		var source SourceRef
		found, err := e.read(ctx, key, &source)
		if err != nil {
			return err
		}
		if link.Kind == "call" {
			source = turn.Source
			if source.Path == "/detail/evidence/stream/chunks" {
				source.ProjectionPath += link.Path
			} else {
				source.Path += link.Path
			}
			if found {
				source = SourceRef{}
				link.Status = "ambiguous"
			} else {
				link.Status = "observed"
			}
			if err = e.write(ctx, key, source); err != nil {
				return err
			}
		} else if found && source.RequestID != "" {
			link.Status = "matched"
			link.Call = &source
		} else {
			link.Status = "unresolved"
		}
	}
	return e.append(ctx, "turns:"+sid, turn)
}

func (e *SessionExporter) WriteTo(ctx context.Context, w io.Writer) (int, error) {
	err := e.store.each(ctx, "sessions", func(header []byte) error {
		var h sessionHeader
		if err := json.Unmarshal(header, &h); err != nil {
			return err
		}
		if _, err := w.Write(header[:len(header)-1]); err != nil {
			return err
		}
		for _, kind := range []string{"calls", "turns"} {
			if _, err := fmt.Fprintf(w, ",%q:[", kind); err != nil {
				return err
			}
			first := true
			if err := e.store.each(ctx, kind+":"+h.SessionID, func(raw []byte) error {
				if !first {
					if _, err := io.WriteString(w, ","); err != nil {
						return err
					}
				}
				first = false
				_, err := w.Write(raw)
				return err
			}); err != nil {
				return err
			}
			if _, err := io.WriteString(w, "]"); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "}\n")
		return err
	})
	if err != nil {
		return 0, err
	}
	return e.count, nil
}

func historyField(shape WireShape) string {
	switch shape {
	case WireAnthropicMessages, WireOpenAIChat:
		return "messages"
	case WireOpenAIResponses:
		return "input"
	case WireGemini:
		return "contents"
	}
	return ""
}
func requestHistory(shape WireShape, request map[string]json.RawMessage) []json.RawMessage {
	raw := request[historyField(shape)]
	if shape == WireOpenAIResponses && rawString(raw) != "" {
		return []json.RawMessage{rawJSON(map[string]any{"role": "user", "content": rawString(raw)})}
	}
	return rawArray(raw)
}
func messageRole(shape WireShape, raw json.RawMessage) string {
	m := rawObject(raw)
	role := rawString(m["role"])
	if role == "model" {
		return "assistant"
	}
	if role != "" {
		return role
	}
	switch rawString(m["type"]) {
	case "function_call_output":
		return "tool"
	case "function_call", "reasoning", "message":
		return "assistant"
	}
	return "unknown"
}
func responseMessages(shape WireShape, response json.RawMessage) []json.RawMessage {
	m := rawObject(response)
	if len(m) == 0 {
		return nil
	}
	switch shape {
	case WireAnthropicMessages:
		if _, ok := m["content"]; ok {
			return []json.RawMessage{response}
		}
	case WireOpenAIResponses:
		return rawArray(m["output"])
	case WireOpenAIChat, WireGemini:
		return []json.RawMessage{response}
	}
	return nil
}
func responseHistory(shape WireShape, response json.RawMessage) []json.RawMessage {
	m := rawObject(response)
	switch shape {
	case WireAnthropicMessages:
		if content, ok := m["content"]; ok {
			return []json.RawMessage{rawJSON(map[string]any{"role": "assistant", "content": content})}
		}
	case WireOpenAIResponses:
		return rawArray(m["output"])
	case WireOpenAIChat:
		choices := rawArray(m["choices"])
		if len(choices) == 1 {
			msg := rawObject(choices[0])["message"]
			if len(msg) > 0 {
				return []json.RawMessage{msg}
			}
		}
	case WireGemini:
		candidates := rawArray(m["candidates"])
		if len(candidates) == 1 {
			msg := rawObject(candidates[0])["content"]
			if len(msg) > 0 {
				return []json.RawMessage{msg}
			}
		}
	}
	return nil
}
func toolLinks(raw json.RawMessage) []ToolLink {
	var links []ToolLink
	var visit func(json.RawMessage, string)
	visit = func(raw json.RawMessage, path string) {
		m := rawObject(raw)
		if m == nil {
			return
		}
		add := func(kind, id string) { links = append(links, ToolLink{Kind: kind, ID: id, Path: path}) }
		switch rawString(m["type"]) {
		case "tool_use":
			add("call", rawString(m["id"]))
		case "tool_result":
			add("result", rawString(m["tool_use_id"]))
		case "function_call":
			add("call", rawString(m["call_id"]))
		case "function_call_output":
			add("result", rawString(m["call_id"]))
		}
		if rawString(m["role"]) == "tool" {
			add("result", rawString(m["tool_call_id"]))
		}
		if call := rawObject(m["functionCall"]); call != nil {
			add("call", rawString(call["id"]))
		}
		if result := rawObject(m["functionResponse"]); result != nil {
			add("result", rawString(result["id"]))
		}
		for _, key := range []string{"content", "parts", "output", "choices", "candidates"} {
			for i, v := range rawArray(m[key]) {
				visit(v, fmt.Sprintf("%s/%s/%d", path, key, i))
			}
		}
		for _, key := range []string{"message", "content"} {
			if rawObject(m[key]) != nil {
				visit(m[key], path+"/"+key)
			}
		}
		for i, call := range rawArray(m["tool_calls"]) {
			cm := rawObject(call)
			links = append(links, ToolLink{Kind: "call", ID: rawString(cm["id"]), Path: fmt.Sprintf("%s/tool_calls/%d", path, i)})
		}
	}
	visit(raw, "")
	return links
}

func containsIssue(issues []string, target string) bool {
	for _, issue := range issues {
		if issue == target {
			return true
		}
	}
	return false
}
