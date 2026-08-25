// Command list_go_tests reports the test entries registered by cmd/go.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type discovery struct {
	Entries     []string `json:"entries"`
	HasTestMain bool     `json:"has_test_main"`
}

func main() {
	filenames := os.Args[1:]
	if len(filenames) > 0 && filenames[0] == "--" {
		filenames = filenames[1:]
	}
	if len(filenames) == 0 {
		fatalf("no Go test files provided")
	}

	entries := make(map[string]struct{})
	hasTestMain := false
	fileset := token.NewFileSet()
	for _, filename := range filenames {
		file, err := parser.ParseFile(
			fileset,
			filename,
			nil,
			parser.ParseComments|parser.SkipObjectResolution,
		)
		if err != nil {
			fatalf("parse %s: %v", filename, err)
		}

		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil {
				continue
			}
			name := function.Name.Name
			switch {
			case name == "TestMain":
				if isTestFunc(function, "T") {
					entries[name] = struct{}{}
					continue
				}
				if err := checkTestFunc(function, "M"); err != nil {
					fatalf("%s: %v", fileset.Position(function.Pos()), err)
				}
				hasTestMain = true
			case isTest(name, "Test"):
				if err := checkTestFunc(function, "T"); err != nil {
					fatalf("%s: %v", fileset.Position(function.Pos()), err)
				}
				entries[name] = struct{}{}
			case isTest(name, "Fuzz"):
				if err := checkTestFunc(function, "F"); err != nil {
					fatalf("%s: %v", fileset.Position(function.Pos()), err)
				}
				entries[name] = struct{}{}
			}
		}

		for _, example := range doc.Examples(file) {
			if example.Output == "" && !example.EmptyOutput {
				continue
			}
			entries["Example"+example.Name] = struct{}{}
		}
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := json.NewEncoder(os.Stdout).Encode(discovery{
		Entries:     names,
		HasTestMain: hasTestMain,
	}); err != nil {
		fatalf("encode discovery: %v", err)
	}
}

func isTestFunc(function *ast.FuncDecl, argument string) bool {
	if function.Type.Results != nil && len(function.Type.Results.List) > 0 ||
		function.Type.Params.List == nil ||
		len(function.Type.Params.List) != 1 ||
		len(function.Type.Params.List[0].Names) > 1 {
		return false
	}
	pointer, ok := function.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	if identifier, ok := pointer.X.(*ast.Ident); ok {
		return identifier.Name == argument
	}
	if selector, ok := pointer.X.(*ast.SelectorExpr); ok {
		return selector.Sel.Name == argument
	}
	return false
}

func isTest(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	runeValue, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(runeValue)
}

func checkTestFunc(function *ast.FuncDecl, argument string) error {
	if !isTestFunc(function, argument) {
		return fmt.Errorf(
			"wrong signature for %s, must be func(%s *testing.%s)",
			function.Name.Name,
			strings.ToLower(argument),
			argument,
		)
	}
	if function.Type.TypeParams != nil && function.Type.TypeParams.NumFields() > 0 {
		return fmt.Errorf("test function %s cannot have type parameters", function.Name.Name)
	}
	return nil
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "list-go-tests: "+format+"\n", arguments...)
	os.Exit(1)
}
