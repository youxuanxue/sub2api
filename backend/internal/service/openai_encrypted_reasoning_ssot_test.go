package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const observeOpenAIResponsesEventName = "observeOpenAIResponsesEvent"
const stashOpenAIEncryptedReasoningName = "stashOpenAIEncryptedReasoningFromSSE"

func TestEncryptedReasoningStashHasSingleProductionCaller(t *testing.T) {
	callers := collectIdentCallers(t, stashOpenAIEncryptedReasoningName)
	if len(callers) != 1 || callers[0] != observeOpenAIResponsesEventName {
		t.Fatalf("stashOpenAIEncryptedReasoningFromSSE 只能被 %s 调用，实际生产调用方=%v", observeOpenAIResponsesEventName, callers)
	}
}

func TestOpenAIResponsesClientEventLoopsMustObserveEncryptedReasoning(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "openai_encrypted_reasoning_qa.go" {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calls := collectCallNames(fn.Body)
			if !callsObserver(calls) && callsParser(calls) && callsClientSink(fn.Body) {
				missing = append(missing, name+":"+fn.Name.Name)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("这些会把 OpenAI Responses 事件写给客户端的函数没有调用 %s: %s", observeOpenAIResponsesEventName, strings.Join(missing, ", "))
	}
}

func collectIdentCallers(t *testing.T, target string) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	var callers []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || ident.Name != target {
					return true
				}
				if _, exists := seen[fn.Name.Name]; exists {
					return true
				}
				seen[fn.Name.Name] = struct{}{}
				callers = append(callers, fn.Name.Name)
				return true
			})
		}
	}
	return callers
}

func collectCallNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			out[fun.Name] = true
		case *ast.SelectorExpr:
			out[fun.Sel.Name] = true
		}
		return true
	})
	return out
}

func callsObserver(calls map[string]bool) bool {
	return calls[observeOpenAIResponsesEventName] || calls["observeOpenAIResponsesSSEBody"] || calls["ObserveOpenAI"]
}

func callsParser(calls map[string]bool) bool {
	return calls["extractOpenAISSEDataLine"] ||
		calls["parseOpenAIWSEventEnvelope"] ||
		calls["forEachOpenAISSEDataPayload"] ||
		calls["ObserveOpenAI"]
}

func callsClientSink(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			switch fun.Name {
			case "writePendingString", "emitStreamMessage", "writeClientMessage", "writeOpenAICompactSSEBridge":
				found = true
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name == "Data" {
				found = true
				return true
			}
			if fun.Sel.Name == "Write" || fun.Sel.Name == "WriteString" {
				if x, ok := fun.X.(*ast.SelectorExpr); ok && x.Sel.Name == "Writer" {
					found = true
				}
			}
		}
		return true
	})
	return found
}
