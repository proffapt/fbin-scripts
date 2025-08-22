package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: goap [file-or-directory ...]")
		os.Exit(1)
	}

	args := os.Args[1:]
	for _, path := range args {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Printf("Error accessing %s: %v\n", path, err)
			continue
		}

		if info.IsDir() {
			if err := RewriteDir(path); err != nil {
				fmt.Printf("Error processing directory %s: %v\n", path, err)
			} else {
				fmt.Printf("Processed directory successfully: %s\n", path)
			}
		} else {
			if err := RewriteFile(path); err != nil {
				fmt.Printf("Error processing file %s: %v\n", path, err)
			} else {
				fmt.Printf("Processed file successfully: %s\n", path)
			}
		}
	}
}

// scopeInfo tracks ctx and r availability in a function scope
type scopeInfo struct {
	ctxType    string // "ctx", "*ctx", ""
	rAvailable bool
}

// RewriteContent parses and rewrites Go source code string, replacing context.TODO()
// with ctx or r.Context() only when in scope.
func RewriteContent(src string) (string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return "", err
	}

	lines := strings.Split(src, "\n")
	scopeStack := []scopeInfo{}

	var walkFuncs func(ast.Node)
	walkFuncs = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch fn := node.(type) {
			case *ast.FuncDecl:
				pushScope(&scopeStack, fn.Type.Params)
				processLines(&scopeStack, fn.Body, fset, lines)
				popScope(&scopeStack)
				return false
			case *ast.FuncLit:
				pushScope(&scopeStack, fn.Type.Params)
				processLines(&scopeStack, fn.Body, fset, lines)
				popScope(&scopeStack)
				return false
			}
			return true
		})
	}

	walkFuncs(node)
	return strings.Join(lines, "\n"), nil
}

// pushScope adds a new function scope based on parameters
func pushScope(stack *[]scopeInfo, params *ast.FieldList) {
	s := scopeInfo{}
	if params != nil {
		for _, param := range params.List {
			for _, name := range param.Names {
				if name.Name == "ctx" {
					typ := exprToString(param.Type)
					if typ == "context.Context" {
						s.ctxType = "ctx"
					}
					if typ == "*context.Context" {
						s.ctxType = "*ctx"
					}
				}
				if name.Name == "r" {
					typ := exprToString(param.Type)
					if typ == "*http.Request" {
						s.rAvailable = true
					}
				}
			}
		}
	}
	*stack = append(*stack, s)
}

// popScope removes the top-most scope
func popScope(stack *[]scopeInfo) {
	if len(*stack) > 0 {
		*stack = (*stack)[:len(*stack)-1]
	}
}

// processLines rewrites lines inside a function body
func processLines(stack *[]scopeInfo, block *ast.BlockStmt, fset *token.FileSet, lines []string) {
	if block == nil {
		return
	}

	start := fset.Position(block.Lbrace).Line - 1
	end := fset.Position(block.Rbrace).Line - 1

	depth := 0
	localCtxDepth := -1
	localRDepth := -1

	for i := start; i <= end && i < len(lines); i++ {
		line := lines[i]
		depth += strings.Count(line, "{") - strings.Count(line, "}")

		// Detect local ctx declaration
		if localCtxDepth == -1 {
			if ok, detectedType := checkLocalCtxDeclaration(line); ok {
				localCtxDepth = depth
				(*stack)[len(*stack)-1].ctxType = detectedType
			}
		}

		// Detect local r parameter (anonymous function param)
		if localRDepth == -1 {
			if ok := checkLocalRParam(line); ok {
				localRDepth = depth
				(*stack)[len(*stack)-1].rAvailable = true
			}
		}

		// Replace context.TODO() based on current scope
		lines[i] = replaceCtxOrRInLine(line, (*stack)[len(*stack)-1].ctxType, (*stack)[len(*stack)-1].rAvailable)

		// Reset local ctx/r when leaving scope
		if localCtxDepth != -1 && depth < localCtxDepth {
			localCtxDepth = -1
			(*stack)[len(*stack)-1].ctxType = ""
		}
		if localRDepth != -1 && depth < localRDepth {
			localRDepth = -1
			(*stack)[len(*stack)-1].rAvailable = false
		}
	}
}

// checkLocalRParam returns true if line contains r as a function param (simplified detection)
func checkLocalRParam(line string) bool {
	rParamRegex := regexp.MustCompile(`func\s*\([^)]*r\s+\*http\.Request[^)]*\)`)
	return rParamRegex.MatchString(line)
}

// replaceCtxOrRInLine replaces context.TODO() with ctx or r.Context() based on scope
func replaceCtxOrRInLine(line string, ctxType string, rAvailable bool) string {
	if regexp.MustCompile(`\bctx\s*:=`).MatchString(line) {
		return line
	}

	var result strings.Builder
	inDoubleQuotes, inSingleQuotes, inBackticks := false, false, false
	i := 0

	for i < len(line) {
		c := line[i]
		if !inDoubleQuotes && !inSingleQuotes && !inBackticks && i+1 < len(line) && line[i] == '/' && line[i+1] == '/' {
			result.WriteString(line[i:])
			break
		}

		if !inSingleQuotes && !inBackticks && c == '"' {
			inDoubleQuotes = !inDoubleQuotes
			result.WriteByte(c)
			i++
			continue
		}
		if !inDoubleQuotes && !inBackticks && c == '\'' {
			inSingleQuotes = !inSingleQuotes
			result.WriteByte(c)
			i++
			continue
		}
		if !inDoubleQuotes && !inSingleQuotes && c == '`' {
			inBackticks = !inBackticks
			result.WriteByte(c)
			i++
			continue
		}

		if !inDoubleQuotes && !inSingleQuotes && !inBackticks && strings.HasPrefix(line[i:], "context.TODO()") {
			if ctxType != "" {
				result.WriteString(ctxType)
			} else if rAvailable {
				result.WriteString("r.Context()")
			} else {
				result.WriteString("context.TODO()")
			}
			i += len("context.TODO()")
			continue
		}

		result.WriteByte(c)
		i++
	}

	return result.String()
}

// checkLocalCtxDeclaration returns whether the line declares a local ctx variable
func checkLocalCtxDeclaration(line string) (bool, string) {
	ctxDeclRegex := regexp.MustCompile(`\bctx\s*:=\s*(\&)?`)
	matches := ctxDeclRegex.FindStringSubmatch(line)
	if len(matches) == 0 {
		return false, ""
	}
	if matches[1] == "&" {
		return true, "*ctx"
	}
	return true, "ctx"
}

// detectCtxType checks if a function has ctx param and returns its type string
func detectCtxType(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, param := range fn.Type.Params.List {
		for _, name := range param.Names {
			if name.Name == "ctx" {
				typ := exprToString(param.Type)
				if typ == "context.Context" {
					return "ctx"
				}
				if typ == "*context.Context" {
					return "*ctx"
				}
			}
		}
	}
	return ""
}

// exprToString converts ast.Expr into a string for type matching
func exprToString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	default:
		return fmt.Sprintf("%T", e)
	}
}

// RewriteFile loads a file, rewrites its contents, and saves it back
func RewriteFile(filename string) error {
	srcBytes, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	newContent, err := RewriteContent(string(srcBytes))
	if err != nil {
		return err
	}
	return os.WriteFile(filename, []byte(newContent), 0644)
}

// RewriteDir recursively rewrites all Go files in the given directory
func RewriteDir(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".go" {
			return RewriteFile(path)
		}
		return nil
	})
}
