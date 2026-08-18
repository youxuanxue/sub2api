//go:build ignore

// export_agent_contract_routes extracts the concrete Gin route inventory from
// the routes package without importing or starting the application.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Source string `json:"source"`
}

type functionInfo struct {
	decl   *ast.FuncDecl
	params []string
	source string
}

type analyzer struct {
	functions map[string]*functionInfo
	routes    map[string]route
	visiting  map[string]bool
}

var httpMethods = map[string]string{
	"GET": "GET", "POST": "POST", "PUT": "PUT", "DELETE": "DELETE",
	"PATCH": "PATCH", "HEAD": "HEAD", "OPTIONS": "OPTIONS",
	"MethodGet": "GET", "MethodPost": "POST", "MethodPut": "PUT",
	"MethodDelete": "DELETE", "MethodPatch": "PATCH", "MethodHead": "HEAD",
	"MethodOptions": "OPTIONS",
}

func main() {
	routesDir := flag.String("routes-dir", "", "directory containing the Go routes package")
	repoRoot := flag.String("repo-root", "", "repository root used for source paths")
	flag.Parse()
	if *routesDir == "" || *repoRoot == "" {
		fmt.Fprintln(os.Stderr, "--routes-dir and --repo-root are required")
		os.Exit(2)
	}

	a, err := loadAnalyzer(*routesDir, *repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	entry := a.functions["__server_registerRoutes"]
	if entry == nil || len(entry.params) == 0 {
		fmt.Fprintln(os.Stderr, "backend/internal/server/router.go: registerRoutes entrypoint not found")
		os.Exit(1)
	}
	a.walkFunction("__server_registerRoutes", map[string]string{entry.params[0]: ""})

	routes := make([]route, 0, len(a.routes))
	for _, item := range a.routes {
		routes = append(routes, item)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Source < routes[j].Source
	})
	if err := json.NewEncoder(os.Stdout).Encode(routes); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadAnalyzer(routesDir, repoRoot string) (*analyzer, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(routesDir)
	if err != nil {
		return nil, err
	}
	a := &analyzer{
		functions: make(map[string]*functionInfo),
		routes:    make(map[string]route),
		visiting:  make(map[string]bool),
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filename := filepath.Join(routesDir, entry.Name())
		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filename, err)
		}
		rel, err := filepath.Rel(repoRoot, filename)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			params := make([]string, 0)
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						params = append(params, name.Name)
					}
				}
			}
			a.functions[fn.Name.Name] = &functionInfo{
				decl: fn, params: params, source: filepath.ToSlash(rel),
			}
		}
	}
	if err := a.loadServerEntrypoint(repoRoot, fset); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *analyzer) loadServerEntrypoint(repoRoot string, fset *token.FileSet) error {
	filename := filepath.Join(repoRoot, "backend", "internal", "server", "router.go")
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return fmt.Errorf("parse %s: %w", filename, err)
	}
	rel, err := filepath.Rel(repoRoot, filename)
	if err != nil {
		return err
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || fn.Name.Name != "registerRoutes" {
			continue
		}
		params := make([]string, 0)
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					params = append(params, name.Name)
				}
			}
		}
		a.functions["__server_registerRoutes"] = &functionInfo{
			decl: fn, params: params, source: filepath.ToSlash(rel),
		}
		return nil
	}
	return fmt.Errorf("%s: registerRoutes function not found", filename)
}

func (a *analyzer) walkFunction(name string, env map[string]string) {
	fn := a.functions[name]
	if fn == nil {
		return
	}
	visitKey := name + "\x00" + environmentKey(env)
	if a.visiting[visitKey] {
		return
	}
	a.visiting[visitKey] = true
	defer delete(a.visiting, visitKey)
	a.walkBlock(fn.decl.Body, cloneEnv(env), fn.source)
}

func (a *analyzer) walkBlock(block *ast.BlockStmt, env map[string]string, source string) {
	if block == nil {
		return
	}
	for _, stmt := range block.List {
		switch node := stmt.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				if i >= len(node.Rhs) {
					break
				}
				name, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if value, ok := evalRouteExpr(node.Rhs[i], env); ok {
					env[name.Name] = value
				}
			}
		case *ast.DeclStmt:
			decl, ok := node.Decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range decl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range valueSpec.Names {
					if i < len(valueSpec.Values) {
						if value, ok := evalRouteExpr(valueSpec.Values[i], env); ok {
							env[name.Name] = value
						}
					}
				}
			}
		case *ast.ExprStmt:
			a.walkCall(node.X, env, source)
		case *ast.IfStmt:
			branchEnv := cloneEnv(env)
			if node.Init != nil {
				a.walkBlock(&ast.BlockStmt{List: []ast.Stmt{node.Init}}, branchEnv, source)
			}
			a.walkBlock(node.Body, branchEnv, source)
			a.walkElse(node.Else, cloneEnv(env), source)
		case *ast.ForStmt:
			a.walkBlock(node.Body, cloneEnv(env), source)
		case *ast.RangeStmt:
			a.walkBlock(node.Body, cloneEnv(env), source)
		case *ast.SwitchStmt:
			a.walkCaseClauses(node.Body, env, source)
		case *ast.TypeSwitchStmt:
			a.walkCaseClauses(node.Body, env, source)
		case *ast.SelectStmt:
			a.walkCaseClauses(node.Body, env, source)
		case *ast.BlockStmt:
			a.walkBlock(node, cloneEnv(env), source)
		}
	}
}

func (a *analyzer) walkElse(stmt ast.Stmt, env map[string]string, source string) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		a.walkBlock(node, env, source)
	case *ast.IfStmt:
		a.walkBlock(&ast.BlockStmt{List: []ast.Stmt{node}}, env, source)
	}
}

func (a *analyzer) walkCaseClauses(block *ast.BlockStmt, env map[string]string, source string) {
	if block == nil {
		return
	}
	for _, stmt := range block.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		a.walkBlock(&ast.BlockStmt{List: clause.Body}, cloneEnv(env), source)
	}
}

func (a *analyzer) walkCall(expr ast.Expr, env map[string]string, source string) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		if method, ok := httpMethods[selector.Sel.Name]; ok && len(call.Args) > 0 {
			base, baseOK := evalRouteExpr(selector.X, env)
			leaf, leafOK := stringLiteral(call.Args[0])
			if baseOK && leafOK {
				a.addRoute(method, joinRoute(base, leaf), source)
			}
			return
		}
		if selector.Sel.Name == "Handle" && len(call.Args) > 1 {
			base, baseOK := evalRouteExpr(selector.X, env)
			method, methodOK := methodExpr(call.Args[0])
			leaf, leafOK := stringLiteral(call.Args[1])
			if baseOK && methodOK && leafOK {
				a.addRoute(method, joinRoute(base, leaf), source)
			}
			return
		}
		if selector.Sel.Name == "Register" && len(call.Args) > 1 {
			base, baseOK := evalRouteExpr(selector.X, env)
			method, methodOK := methodExpr(call.Args[0])
			leaf, leafOK := stringLiteral(call.Args[1])
			if baseOK && methodOK && leafOK {
				a.addRoute(method, joinRoute(base, leaf), source)
			}
			return
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "routes" {
			a.walkCallee(selector.Sel.Name, call.Args, env)
			return
		}
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return
	}
	a.walkCallee(ident.Name, call.Args, env)
}

func (a *analyzer) walkCallee(name string, args []ast.Expr, env map[string]string) {
	callee := a.functions[name]
	if callee == nil {
		return
	}
	childEnv := make(map[string]string)
	for i, arg := range args {
		if i >= len(callee.params) {
			break
		}
		if value, ok := evalRouteExpr(arg, env); ok {
			childEnv[callee.params[i]] = value
		}
	}
	if len(childEnv) > 0 {
		a.walkFunction(name, childEnv)
	}
}

func (a *analyzer) addRoute(method, path, source string) {
	key := method + "\x00" + path
	item := route{Method: method, Path: path, Source: source}
	current, exists := a.routes[key]
	if !exists || item.Source < current.Source {
		a.routes[key] = item
	}
}

func evalRouteExpr(expr ast.Expr, env map[string]string) (string, bool) {
	switch node := expr.(type) {
	case *ast.Ident:
		value, ok := env[node.Name]
		return value, ok
	case *ast.ParenExpr:
		return evalRouteExpr(node.X, env)
	case *ast.CallExpr:
		if selector, ok := node.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Group" && len(node.Args) > 0 {
			base, baseOK := evalRouteExpr(selector.X, env)
			leaf, leafOK := stringLiteral(node.Args[0])
			if baseOK && leafOK {
				return joinRoute(base, leaf), true
			}
		}
		if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "newTerminalRouteRegistrar" && len(node.Args) > 0 {
			return evalRouteExpr(node.Args[0], env)
		}
	}
	return "", false
}

func methodExpr(expr ast.Expr) (string, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		value, ok := stringLiteral(node)
		if !ok {
			return "", false
		}
		method, ok := httpMethods[strings.ToUpper(value)]
		return method, ok
	case *ast.SelectorExpr:
		method, ok := httpMethods[node.Sel.Name]
		return method, ok
	case *ast.Ident:
		method, ok := httpMethods[node.Name]
		return method, ok
	}
	return "", false
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func joinRoute(base, leaf string) string {
	if base == "" {
		base = "/"
	}
	if leaf == "" {
		if base == "" {
			return "/"
		}
		return normalizeRoute(base)
	}
	return normalizeRoute(strings.TrimRight(base, "/") + "/" + strings.TrimLeft(leaf, "/"))
}

func normalizeRoute(value string) string {
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	for strings.Contains(value, "//") {
		value = strings.ReplaceAll(value, "//", "/")
	}
	if len(value) > 1 {
		value = strings.TrimRight(value, "/")
	}
	return value
}

func cloneEnv(env map[string]string) map[string]string {
	copy := make(map[string]string, len(env))
	for key, value := range env {
		copy[key] = value
	}
	return copy
}

func environmentKey(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, key+"="+env[key])
	}
	return strings.Join(parts, "\x00")
}
