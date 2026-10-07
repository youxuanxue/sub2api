package cursor

// Native tools are translated to caller-owned tools, NEVER executed here.
// Results resume the original upstream connection; no executor exists here.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// nativeClientToolCall only recognizes explicit, schema-compatible declarations.
// It never invents a tool, relaxes client permissions, or dispatches an executor.
func nativeClientToolCall(exec *pb.ExecServerMessage, tools []AgentTool) (AgentToolCall, bool) {
	var call AgentToolCall
	if exec == nil {
		return call, false
	}
	// ExecServerMessage.message is a oneof upstream. Our protocol subset is not:
	// reject ambiguous frames instead of selecting a privileged interpretation.
	variants := 0
	for _, n := range protobufFieldNumbers(exec) {
		if n != 1 && n != 15 && n != 19 && n != 55 {
			variants++
		}
	}
	if variants != 1 {
		return call, false
	}
	args := map[string]any{}
	var name, wireID string
	read := exec.ReadArgs
	if read == nil {
		read = exec.RedactedReadArgs
	}
	switch {
	case read != nil:
		if !nativeTextEncoding(read.GetEncodingHint()) || read.GetOffset() < 0 {
			return call, false
		}
		name, wireID, args["file_path"] = "Read", read.ToolCallId, read.Path
		if read.Offset != nil {
			args["offset"] = read.GetOffset()
		}
		if read.Limit != nil {
			args["limit"] = read.GetLimit()
		}
	case exec.WriteArgs != nil:
		a := exec.WriteArgs
		if len(a.FileBytes) != 0 || !nativeTextEncoding(a.GetEncodingHint()) {
			return call, false
		}
		name, wireID = "Write", a.ToolCallId
		args["file_path"], args["content"] = a.Path, a.FileText
	case exec.GrepArgs != nil:
		a := exec.GrepArgs
		if a.Sort != nil || a.SortAscending != nil {
			return call, false // no equivalent in Claude Code's Grep schema
		}
		name, wireID, args["pattern"] = "Grep", a.ToolCallId, a.Pattern
		args["output_mode"] = "content" // native default differs from client Grep
		nativeArg(args, "path", a.Path)
		nativeArg(args, "glob", a.Glob)
		nativeArg(args, "output_mode", a.OutputMode)
		nativeArg(args, "-B", a.ContextBefore)
		nativeArg(args, "-A", a.ContextAfter)
		nativeArg(args, "-C", a.Context)
		nativeArg(args, "-i", a.CaseInsensitive)
		nativeArg(args, "type", a.Type)
		nativeArg(args, "head_limit", a.HeadLimit)
		nativeArg(args, "multiline", a.Multiline)
		nativeArg(args, "offset", a.Offset)
	case exec.PiReadArgs != nil:
		a := exec.PiReadArgs
		name, args["file_path"] = "Read", a.Path
		nativeArg(args, "offset", a.Offset)
		nativeArg(args, "limit", a.Limit)
	case exec.PiWriteArgs != nil:
		a := exec.PiWriteArgs
		name, args["file_path"], args["content"] = "Write", a.Path, a.Content
	case exec.PiGrepArgs != nil:
		a := exec.PiGrepArgs
		if a.GetLiteral() {
			return call, false // regex escaping is dialect-dependent
		}
		name, args["pattern"] = "Grep", a.Pattern
		args["output_mode"] = "content"
		nativeArg(args, "path", a.Path)
		nativeArg(args, "glob", a.Glob)
		nativeArg(args, "-i", a.IgnoreCase)
		nativeArg(args, "-C", a.Context)
		nativeArg(args, "head_limit", a.Limit)
	default:
		return call, false
	}
	if value, ok := args["file_path"].(string); ok && (strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0)) {
		return call, false
	}
	if wireID == "" {
		wireID = exec.ExecId
	}
	if wireID == "" {
		if exec.Id == 0 {
			return call, false
		}
		wireID = fmt.Sprint(exec.Id)
	}
	// Native IDs can carry provider identity. Use a stable, opaque public ID.
	digest := sha256.Sum256([]byte(wireID))
	call = AgentToolCall{ID: fmt.Sprintf("toolu_%s_%x", strings.ToLower(name), digest[:16]), Name: name, Arguments: args}
	matched := false
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if matched || !nativeClientSchemaAccepts(tool.Schema, args) {
			return AgentToolCall{}, false
		}
		matched = true
	}
	return call, matched
}

func nativeArg[T any](args map[string]any, name string, value *T) {
	if value != nil {
		args[name] = *value
	}
}

func nativeTextEncoding(value string) bool {
	return value == "" || strings.EqualFold(value, "utf8") || strings.EqualFold(value, "utf-8")
}

func nativeClientSchemaAccepts(schema map[string]any, args map[string]any) bool {
	if schema["type"] != "object" {
		return false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	// Even an open schema must explicitly declare the parameters we translate.
	for key := range args {
		if _, ok := properties[key]; !ok {
			return false
		}
	}
	return clientToolSchemaAccepts(schema, args)
}

// Untranslated client arguments follow the caller's schema, including open
// schemas and local references. The native adapter's property whitelist applies
// only to parameters it synthesizes, never to an unchanged MCP argument object.
func clientToolSchemaAccepts(schema map[string]any, args map[string]any) bool {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are unavailable")
	}
	const resource = "urn:gateway:client-tool-schema"
	if err := compiler.AddResource(resource, bytes.NewReader(encoded)); err != nil {
		return false
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return false
	}
	// Normalize generated numeric Go values to JSON types for the validator.
	encoded, err = json.Marshal(args)
	if err != nil {
		return false
	}
	var value any
	if json.Unmarshal(encoded, &value) != nil {
		return false
	}
	return compiled.Validate(value) == nil
}
