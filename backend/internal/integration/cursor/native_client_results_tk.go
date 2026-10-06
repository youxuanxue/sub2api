package cursor

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"google.golang.org/protobuf/proto"
)

// This adapter has no access to the client's filesystem or process state.
// Successful tool acknowledgements use native success conventions; they are
// not independent measurements of process exit status or filesystem metadata.
func nativeClientResults(exec *pb.ExecServerMessage, result AgentMessage) []*pb.AgentClientMessage {
	reply := &pb.ExecClientMessage{Id: exec.Id, ExecId: exec.ExecId}
	finish := func() []*pb.AgentClientMessage {
		return []*pb.AgentClientMessage{
			{ExecClientMessage: reply},
			{ExecClientControlMessage: &pb.ExecClientControlMessage{StreamClose: &pb.ExecClientStreamClose{Id: exec.Id}}},
		}
	}
	text := result.Text
	if len(text) > maxAgentFrame/2 {
		return execClientThrowAndClose(exec, "Client tool result exceeds the supported size.", "gateway_tool_unavailable")
	}
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
	case exec.McpArgs != nil:
		reply.McpResult = &pb.McpResult{Success: &pb.McpSuccess{IsError: result.IsError, Content: []*pb.McpToolResultContentItem{{Text: &pb.McpTextContent{Text: text}}}}}
	case read != nil:
		value := &pb.ReadResult{}
		if result.IsError {
			switch clientReadErrorText(text) {
			case "File not found", "File not found.", "Error: File does not exist.", "File does not exist.":
				value.FileNotFound = &pb.ReadFileNotFound{Path: read.Path}
			default:
				value.Error = &pb.ReadError{Path: read.Path, Error: text}
			}
		} else if content, ok := clientReadText(text); ok {
			ranged := read.Offset != nil || read.Limit != nil
			value.Success = &pb.ReadSuccess{Path: read.Path, Content: proto.String(content), RangeApplied: ranged}
			if !ranged {
				value.Success.FileSize = int64(len(content))
				value.Success.TotalLines = textLines(content)
			} // unknown total for a page
		} else {
			value.Error = &pb.ReadError{Path: read.Path, Error: "Client Read output is truncated or cannot be decoded losslessly."}
		}
		if exec.RedactedReadArgs != nil {
			reply.RedactedReadResult = value
		} else {
			reply.ReadResult = value
		}
	case exec.WriteArgs != nil:
		args := exec.WriteArgs
		if result.IsError {
			reply.WriteResult = &pb.WriteResult{Error: &pb.WriteError{Path: args.Path, Error: text}}
		} else {
			// The caller acknowledged these exact bytes. Never re-read the path.
			success := &pb.WriteSuccess{Path: args.Path, LinesCreated: textLines(args.FileText), FileSize: int32(len(args.FileText))}
			if args.ReturnFileContentAfterWrite {
				success.FileContentAfterWrite = proto.String(args.FileText)
			}
			reply.WriteResult = &pb.WriteResult{Success: success}
		}
	case shell != nil:
		if result.IsError {
			// A tool error does not distinguish refusal, spawn failure, timeout
			// or nonzero exit. Preserve it without fabricating that distinction.
			return execClientThrowAndClose(exec, text, "client_tool_error")
		}
		if strings.HasPrefix(text, "Command running in background with ID:") {
			return execClientThrowAndClose(exec, "Client output cannot confirm foreground completion; no process is managed by the gateway.\n"+text, "client_tool_result_unrepresentable")
		}
		if exec.ShellStreamArgs != nil {
			// A successful Bash tool_result acknowledges success. No PID, duration or
			// working-directory changes are invented from output text.
			return []*pb.AgentClientMessage{
				{ExecClientMessage: &pb.ExecClientMessage{Id: exec.Id, ExecId: exec.ExecId, ShellStream: &pb.ShellStream{Start: &pb.ShellStreamStart{}}}},
				{ExecClientMessage: &pb.ExecClientMessage{Id: exec.Id, ExecId: exec.ExecId, ShellStream: &pb.ShellStream{Stdout: &pb.ShellStreamStdout{Data: text}}}},
				{ExecClientMessage: &pb.ExecClientMessage{Id: exec.Id, ExecId: exec.ExecId, ShellStream: &pb.ShellStream{Exit: &pb.ShellStreamExit{Code: 0}}}},
				{ExecClientControlMessage: &pb.ExecClientControlMessage{StreamClose: &pb.ExecClientStreamClose{Id: exec.Id}}},
			}
		}
		value := &pb.ShellResult{Success: &pb.ShellSuccess{Command: shell.Command, WorkingDirectory: shell.WorkingDirectory, Stdout: text}}
		if exec.MiniSweAgentBashArgs != nil {
			reply.MiniSweAgentBashResult = value
		} else {
			reply.ShellResult = value
		}
	case exec.GrepArgs != nil:
		value := &pb.GrepResult{}
		if result.IsError {
			value.Error = &pb.GrepError{Error: text}
		} else if success, ok := clientGrepResult(exec.GrepArgs, text); ok {
			value.Success = success
		} else {
			value.Error = &pb.GrepError{Error: "Client Grep output cannot be represented without losing match information."}
		}
		reply.GrepResult = value
	case exec.PiReadArgs != nil:
		reply.PiReadResult = &pb.PiReadExecResult{}
		if result.IsError {
			reply.PiReadResult.Error = &pb.PiReadExecError{Error: text}
		} else {
			reply.PiReadResult.Success = &pb.PiReadExecSuccess{Output: text}
		}
	case exec.PiWriteArgs != nil:
		reply.PiWriteResult = &pb.PiWriteExecResult{}
		if result.IsError {
			reply.PiWriteResult.Error = &pb.PiWriteExecError{Error: text}
		} else {
			reply.PiWriteResult.Success = &pb.PiWriteExecSuccess{Output: text}
		}
	case exec.PiBashArgs != nil:
		reply.PiBashResult = &pb.PiBashExecResult{}
		if result.IsError {
			reply.PiBashResult.Error = &pb.PiBashExecError{Error: text}
		} else {
			reply.PiBashResult.Success = &pb.PiBashExecSuccess{Output: text}
		}
	case exec.PiGrepArgs != nil:
		reply.PiGrepResult = &pb.PiGrepExecResult{}
		if result.IsError {
			reply.PiGrepResult.Error = &pb.PiGrepExecError{Error: text}
		} else {
			reply.PiGrepResult.Success = &pb.PiGrepExecSuccess{Output: text}
		}
	default:
		return execClientThrowAndClose(exec, "The requested tool is unavailable through this gateway.", "gateway_tool_unavailable")
	}
	return finish()
}

var numberedReadLine = regexp.MustCompile(`^\s*([0-9]+)[→\t](.*)$`)

// The first :line: delimiter ends the path; later delimiters belong to content.
var grepContentLine = regexp.MustCompile(`^(.+?):([0-9]+):(.*)$`)

func textLines(text string) int32 {
	if text == "" {
		return 0
	}
	return int32(strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1)
}

func clientReadErrorText(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<tool_use_error>") && strings.HasSuffix(text, "</tool_use_error>") {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<tool_use_error>"), "</tool_use_error>"))
	}
	if strings.HasPrefix(text, "File does not exist. Note: your current working directory is ") {
		return "File does not exist."
	}
	return text
}

func clientReadText(text string) (string, bool) {
	// Decode numbered file lines separately from the CLI's unnumbered reminder
	// suffix. A tag in numbered file content is data and must never be stripped.
	for _, marker := range []string{"[Output truncated", "[... truncated", "lines truncated"} {
		if strings.Contains(text, marker) {
			return "", false
		}
	}
	lines := strings.Split(text, "\n")
	first := numberedReadLine.FindStringSubmatch(lines[0])
	if first == nil {
		return text, !strings.Contains(text, "<system-reminder>") && !strings.Contains(text, "<tool_use_error>")
	}
	expected, err := strconv.Atoi(first[1])
	if err != nil {
		return "", false
	}
	decoded := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			decoded = append(decoded, "")
			break
		}
		match := numberedReadLine.FindStringSubmatch(line)
		if match == nil {
			suffix := strings.TrimSpace(strings.Join(lines[i:], "\n"))
			if strings.HasPrefix(suffix, "<system-reminder>") && strings.HasSuffix(suffix, "</system-reminder>") && strings.Count(suffix, "<system-reminder>") == 1 && strings.Count(suffix, "</system-reminder>") == 1 {
				return strings.Join(decoded, "\n"), true
			}
			return "", false
		}
		number, err := strconv.Atoi(match[1])
		if err != nil || number != expected+i {
			return "", false
		}
		decoded = append(decoded, match[2])
	}
	return strings.Join(decoded, "\n"), true
}

func clientGrepResult(args *pb.GrepArgs, text string) (*pb.GrepSuccess, bool) {
	mode := args.GetOutputMode()
	if mode == "" {
		mode = "content"
	}
	value := &pb.GrepUnionResult{}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" || text == "No matches found" {
		lines = nil
	}
	switch mode {
	case "files_with_matches":
		value.Files = &pb.GrepFilesResult{Files: lines, TotalFiles: int32(len(lines)), HeadLimitApplied: args.HeadLimit, OffsetApplied: args.Offset}
	case "count":
		counts := &pb.GrepCountResult{HeadLimitApplied: args.HeadLimit, OffsetApplied: args.Offset}
		for _, line := range lines {
			pos := strings.LastIndex(line, ":")
			if pos <= 0 {
				return nil, false
			}
			n, err := strconv.ParseInt(line[pos+1:], 10, 32)
			if err != nil || n < 0 || n > int64(math.MaxInt32-counts.TotalMatches) {
				return nil, false
			}
			counts.Counts = append(counts.Counts, &pb.GrepFileCount{File: line[:pos], Count: int32(n)})
			counts.TotalMatches += int32(n)
		}
		counts.TotalFiles = int32(len(counts.Counts))
		value.Count = counts
	case "content":
		content := &pb.GrepContentResult{HeadLimitApplied: args.HeadLimit, OffsetApplied: args.Offset}
		for _, line := range lines {
			match := grepContentLine.FindStringSubmatch(line)
			if match == nil {
				return nil, false
			}
			n, err := strconv.ParseInt(match[2], 10, 32)
			if err != nil || n < 1 {
				return nil, false
			}
			content.Matches = append(content.Matches, &pb.GrepFileMatch{File: match[1], Matches: []*pb.GrepContentMatch{{LineNumber: int32(n), Content: match[3]}}})
		}
		content.TotalLines = int32(len(lines))
		content.TotalMatchedLines = int32(len(lines))
		value.Content = content
	default:
		return nil, false
	}
	// The exact upstream truncation total is unavailable when the client applies
	// a limit; preserve the applied window rather than asserting an exhaustive scan.
	limited := args.HeadLimit != nil || args.Offset != nil
	if value.Content != nil {
		value.Content.ClientTruncated = limited
	}
	if value.Count != nil {
		value.Count.ClientTruncated = limited
	}
	if value.Files != nil {
		value.Files.ClientTruncated = limited
	}
	return &pb.GrepSuccess{Pattern: args.Pattern, Path: args.GetPath(), OutputMode: mode, WorkspaceResults: map[string]*pb.GrepUnionResult{args.GetPath(): value}}, true
}
