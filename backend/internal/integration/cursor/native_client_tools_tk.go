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
	"math"
	"strings"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Leave a delivery margin inside the retained run's client-wait TTL. Upstream
// process-lifetime limits (often 24h) must not become client Bash timeouts.
const maxNativeClientTimeoutMS = 120000

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
	shell := exec.ShellArgs
	if shell == nil {
		shell = exec.ShellStreamArgs
	}
	if shell == nil {
		shell = exec.MiniSweAgentBashArgs
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
	case shell != nil:
		// Background process ownership and approval bypass have no safe client
		// equivalent here. Do not forward skip_approval or sandbox settings.
		if shell.IsBackground || shell.TimeoutBehavior < 0 || shell.TimeoutBehavior > 2 || shell.GetHardTimeout() < 0 || shell.Timeout < 0 {
			return call, false
		}
		name, wireID = "Bash", shell.ToolCallId
		command := shell.Command
		if strings.TrimSpace(command) == "" || strings.ContainsRune(command+shell.WorkingDirectory, 0) {
			return call, false
		}
		if shell.WorkingDirectory != "" {
			// This is a parameter for the CLIENT's Bash tool, never a local shell.
			command = "cd -- '" + strings.ReplaceAll(shell.WorkingDirectory, "'", "'\"'\"'") + "' && (\n" + command + "\n)"
		}
		args["command"] = command
		timeout := shell.Timeout
		// BACKGROUND is a soft-deadline preference, not an already-backgrounded
		// process. The client may complete in the foreground or return its own
		// timeout error; the relay never takes ownership of a process handle.
		if shell.TimeoutBehavior == 2 && timeout == 0 {
			timeout = 30000
		}
		if hard := shell.GetHardTimeout(); hard > 0 && (timeout == 0 || hard < timeout) {
			timeout = hard
		}
		if timeout > 0 {
			args["timeout"] = min(timeout, int32(maxNativeClientTimeoutMS))
		}
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
	case exec.PiBashArgs != nil:
		a := exec.PiBashArgs
		name, args["command"] = "Bash", a.Command
		if a.Timeout != nil {
			ms := a.GetTimeout() * 1000 // Pi uses seconds; Bash uses milliseconds.
			if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 || ms > math.MaxInt32 || math.Trunc(ms) != ms {
				return call, false
			}
			args["timeout"] = min(int32(ms), int32(maxNativeClientTimeoutMS))
		}
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
	for _, key := range []string{"file_path", "command"} {
		if value, ok := args[key].(string); ok && (strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0)) {
			return call, false
		}
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
