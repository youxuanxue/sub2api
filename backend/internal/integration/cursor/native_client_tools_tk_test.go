package cursor

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func nativeTestTool(name string, fields ...string) AgentTool {
	properties := map[string]any{}
	for _, key := range fields {
		kind := "string"
		if key == "offset" || key == "limit" || key == "timeout" || key == "head_limit" || key == "-C" {
			kind = "integer"
		}
		if key == "-i" || key == "multiline" {
			kind = "boolean"
		}
		properties[key] = map[string]any{"type": kind}
	}
	return AgentTool{Name: name, Schema: map[string]any{"type": "object", "properties": properties, "required": []any{fields[0]}, "additionalProperties": false}}
}

func TestNativeClientToolArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		exec *pb.ExecServerMessage
		tool AgentTool
		want string
	}{
		{"read", &pb.ExecServerMessage{ReadArgs: &pb.ReadArgs{Path: "/client/file", Offset: proto.Int32(4), Limit: proto.Uint32(9)}}, nativeTestTool("Read", "file_path", "offset", "limit"), `{"file_path":"/client/file","offset":4,"limit":9}`},
		{"redacted read", &pb.ExecServerMessage{RedactedReadArgs: &pb.ReadArgs{Path: "/client/file"}}, nativeTestTool("Read", "file_path"), `{"file_path":"/client/file"}`},
		{"empty write", &pb.ExecServerMessage{WriteArgs: &pb.WriteArgs{Path: "/client/file"}}, nativeTestTool("Write", "file_path", "content"), `{"file_path":"/client/file","content":""}`},
		{"grep", &pb.ExecServerMessage{GrepArgs: &pb.GrepArgs{Pattern: "hi", CaseInsensitive: proto.Bool(true), Context: proto.Int32(2)}}, nativeTestTool("Grep", "pattern", "output_mode", "-i", "-C"), `{"pattern":"hi","output_mode":"content","-i":true,"-C":2}`},
		{"grep explicit false flags", &pb.ExecServerMessage{GrepArgs: &pb.GrepArgs{Pattern: "hi", CaseInsensitive: proto.Bool(false), Multiline: proto.Bool(false)}}, nativeTestTool("Grep", "pattern", "output_mode", "-i", "multiline"), `{"pattern":"hi","output_mode":"content","-i":false,"multiline":false}`},
		{"pi read", &pb.ExecServerMessage{PiReadArgs: &pb.PiReadExecArgs{Path: "/client/file"}}, nativeTestTool("Read", "file_path"), `{"file_path":"/client/file"}`},
		{"pi write", &pb.ExecServerMessage{PiWriteArgs: &pb.PiWriteExecArgs{Path: "/client/file", Content: "hello"}}, nativeTestTool("Write", "file_path", "content"), `{"file_path":"/client/file","content":"hello"}`},
		{"pi grep", &pb.ExecServerMessage{PiGrepArgs: &pb.PiGrepExecArgs{Pattern: "hi", Limit: proto.Int32(3)}}, nativeTestTool("Grep", "pattern", "output_mode", "head_limit"), `{"pattern":"hi","output_mode":"content","head_limit":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.exec.Id, tc.exec.ExecId = 7, "cursor-private-id"
			call, ok := nativeClientToolCall(tc.exec, []AgentTool{tc.tool})
			require.True(t, ok)
			require.Equal(t, tc.tool.Name, call.Name)
			raw, err := json.Marshal(call.Arguments)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(raw))
			require.NotContains(t, call.ID, "cursor")
			require.NotContains(t, string(raw), "skip_approval")
			require.NotContains(t, string(raw), "dangerouslyDisableSandbox")
			_, ok = nativeClientToolCall(tc.exec, nil)
			require.False(t, ok, "tools absent or tool_choice=none must never hand off")
			_, ok = nativeClientToolCall(tc.exec, []AgentTool{tc.tool, tc.tool})
			require.False(t, ok, "ambiguous declaration must fail closed")
		})
	}
}

func TestNativeClientToolsFailClosed(t *testing.T) {
	tools := []AgentTool{nativeTestTool("Read", "file_path"), nativeTestTool("Write", "file_path", "content"), nativeTestTool("Bash", "command"), nativeTestTool("Grep", "pattern", "output_mode")}
	for _, exec := range []*pb.ExecServerMessage{
		{ReadArgs: &pb.ReadArgs{Path: "/x", Offset: proto.Int32(2)}}, // undeclared offset
		{ReadArgs: &pb.ReadArgs{Path: "/x", EncodingHint: proto.String("base64")}},
		{WriteArgs: &pb.WriteArgs{Path: "/x", FileBytes: []byte{1}}},
		{ShellArgs: &pb.ShellArgs{Command: "touch /x", IsBackground: true}},
		{ShellArgs: &pb.ShellArgs{Command: "touch /x", HardTimeout: proto.Int32(10)}},
		{ShellArgs: &pb.ShellArgs{Command: "touch /x", TimeoutBehavior: 3}},
		{ShellArgs: &pb.ShellArgs{Command: "x\x00y"}},
		{ReadArgs: &pb.ReadArgs{Path: ""}},
		{GrepArgs: &pb.GrepArgs{Pattern: "x", Sort: proto.String("path")}},
		{GrepArgs: &pb.GrepArgs{Pattern: "x", CaseInsensitive: proto.Bool(false)}}, // explicit defaults still need declarations
		{GrepArgs: &pb.GrepArgs{Pattern: "x", Multiline: proto.Bool(false)}},
		{PiGrepArgs: &pb.PiGrepExecArgs{Pattern: "x", Literal: proto.Bool(true)}},
		{PiBashArgs: &pb.PiBashExecArgs{Command: "x", Timeout: proto.Float64(-1)}},
		{ReadArgs: &pb.ReadArgs{Path: "/x"}, WriteArgs: &pb.WriteArgs{Path: "/x"}},
		{},
	} {
		exec.Id = 1
		_, ok := nativeClientToolCall(exec, tools)
		require.False(t, ok, "%v", exec)
	}
	read := &pb.ExecServerMessage{Id: 1, ReadArgs: &pb.ReadArgs{Path: "/x"}}
	for _, change := range []func(map[string]any){
		func(s map[string]any) { s["required"] = []string{"other"} },
		func(s map[string]any) {
			properties, ok := s["properties"].(map[string]any)
			require.True(t, ok)
			properties["file_path"] = map[string]any{"type": "integer"}
		},
		func(s map[string]any) {
			properties, ok := s["properties"].(map[string]any)
			require.True(t, ok)
			properties["file_path"] = map[string]any{"enum": []string{"/allowed"}}
		},
	} {
		tool := nativeTestTool("Read", "file_path")
		change(tool.Schema)
		_, ok := nativeClientToolCall(read, []AgentTool{tool})
		require.False(t, ok)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	tool := nativeTestTool("Read", "file_path")
	tool.Schema["$ref"] = server.URL + "/schema"
	_, ok := nativeClientToolCall(read, []AgentTool{tool})
	require.False(t, ok)
	require.Zero(t, requests.Load(), "schema validation must never fetch caller-controlled URLs")
}

// Architectural tripwire in addition to the runtime side-effect assertions.
// Local tool executors must not return via a copied community fallback.
func TestCursorAdapterCannotImportLocalExecutors(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range f.Imports {
			pkg, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			for _, forbidden := range []string{"os", "os/exec", "syscall", "unsafe", "plugin", "io/ioutil", "golang.org/x/sys"} {
				require.False(t, pkg == forbidden || strings.HasPrefix(pkg, forbidden+"/"), "%s imports executor-capable %s", path, pkg)
			}
		}
	}
}
