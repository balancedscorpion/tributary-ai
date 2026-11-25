package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuccessfulOutputLiteralsCarryResultMetadata(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob Go files: %v", err)
	}
	fset := token.NewFileSet()
	var problems []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if isLegacyOutputHelper(fn.Name.Name) {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isOutputCompositeLiteral(lit) || !outputLiteralIsSuccessful(lit) {
					return true
				}
				if !outputLiteralHasKey(lit, "ResultMetadata") {
					pos := fset.Position(lit.Pos())
					problems = append(problems, pos.String())
				}
				return true
			})
		}
	}
	if len(problems) > 0 {
		t.Fatalf("successful Output literals must set Output.ResultMetadata directly:\n%s", strings.Join(problems, "\n"))
	}
}

func TestLegacyOutputHelpersAreNotUsedByToolImplementations(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob Go files: %v", err)
	}
	fset := token.NewFileSet()
	var problems []string
	for _, path := range files {
		if path == "types.go" || strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		parsed, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calledFunctionName(call.Fun)
			if name == "JSONOK" || name == "TextOK" {
				problems = append(problems, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	if len(problems) > 0 {
		t.Fatalf("tool implementations must use JSONOKWithMetadata or explicit Output.ResultMetadata instead of legacy helpers:\n%s", strings.Join(problems, "\n"))
	}
}

func TestWeakToolMetadataHelpersAreNotReintroduced(t *testing.T) {
	t.Parallel()

	banned := []string{
		"atlassianResultMetadata",
		"atlassianOutput",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob Go files: %v", err)
	}
	var problems []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, name := range banned {
			if strings.Contains(string(src), name) {
				problems = append(problems, path+":"+name)
			}
		}
	}
	if len(problems) > 0 {
		t.Fatalf("weak ad hoc tool metadata helpers must not be reintroduced; use catalog-backed metadata helpers instead:\n%s", strings.Join(problems, "\n"))
	}
}

func isLegacyOutputHelper(name string) bool {
	switch name {
	case "TextOK", "JSONOK":
		return true
	default:
		return false
	}
}

func isOutputCompositeLiteral(lit *ast.CompositeLit) bool {
	ident, ok := lit.Type.(*ast.Ident)
	return ok && ident.Name == "Output"
}

func outputLiteralIsSuccessful(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "OK" {
			continue
		}
		value, ok := kv.Value.(*ast.Ident)
		return ok && value.Name == "true"
	}
	return false
}

func outputLiteralHasKey(lit *ast.CompositeLit, want string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == want {
			return true
		}
	}
	return false
}

func calledFunctionName(expr ast.Expr) string {
	switch fn := expr.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	default:
		return ""
	}
}
