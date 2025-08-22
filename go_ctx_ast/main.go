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
		fmt.Println("Usage: goap <file-or-directory> [file-or-directory ...]")
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

// RewriteContent parses and rewrites Go source code string, replacing context.TODO()
// with ctx only when ctx is in scope (as a function parameter or local variable).
func RewriteContent(src string) (string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return "", err
	}

	lines := strings.Split(src, "\n")

	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		// Detect ctx parameter
		ctxType := detectCtxType(fn)
		ctxAvailable := ctxType != "" // true if function param exists

		start := fset.Position(fn.Body.Lbrace).Line - 1
		end := fset.Position(fn.Body.Rbrace).Line - 1

		depth := 0
		ctxDepth := -1 // -1 means ctx not declared yet

		for i := start; i <= end && i < len(lines); i++ {
			line := lines[i]

			// Update brace depth
			depth += strings.Count(line, "{")
			depth -= strings.Count(line, "}")

			// Detect local ctx variable
			if ctxDepth == -1 {
				if ok, detectedType := checkLocalCtxDeclaration(line); ok {
					ctxDepth = depth
					ctxAvailable = true
					ctxType = detectedType // "ctx" or "*ctx"
				}
			}

			// Replace context.TODO() only if ctx is available and in scope
			if ctxAvailable && (ctxDepth == -1 || depth >= ctxDepth) {
				lines[i] = replaceCtxInLine(line, ctxType)
			}
		}
	}

	return strings.Join(lines, "\n"), nil
}

// replaceCtxInLine replaces context.TODO() with ctx, skipping comments and quoted strings
func replaceCtxInLine(line string, ctxType string) string {
	// Skip lines that declare ctx itself
	ctxDeclRegex := regexp.MustCompile(`\bctx\s*:=`)
	if ctxDeclRegex.MatchString(line) {
		return line
	}

	var result strings.Builder
	inDoubleQuotes := false
	inSingleQuotes := false
	inBackticks := false
	i := 0

	for i < len(line) {
		c := line[i]

		// Check for comment start
		if !inDoubleQuotes && !inSingleQuotes && !inBackticks && i+1 < len(line) && line[i] == '/' && line[i+1] == '/' {
			// Append the rest of the line as-is (comment)
			result.WriteString(line[i:])
			break
		}

		// Toggle quote states
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

		// Check for context.TODO() outside quotes/comments
		if !inDoubleQuotes && !inSingleQuotes && !inBackticks && strings.HasPrefix(line[i:], "context.TODO()") {
			switch ctxType {
			case "ctx":
				result.WriteString("ctx")
			case "*ctx":
				result.WriteString("*ctx")
			default:
				result.WriteString("context.TODO()")
			}
			i += len("context.TODO()")
			continue
		}

		// Normal character
		result.WriteByte(c)
		i++
	}

	return result.String()
}

// checkLocalCtxDeclaration returns whether the line declares a local ctx variable
// and the type string: "ctx" or "*ctx"
func checkLocalCtxDeclaration(line string) (bool, string) {
	// Match lines like:
	// ctx := context.Background()
	// ctx := &context.Background()
	// ctx := someOtherFunc()
	// ctx := &someOtherFunc()
	var ctxDeclRegex = regexp.MustCompile(`\bctx\s*:=\s*(\&)?`)
	matches := ctxDeclRegex.FindStringSubmatch(line)
	if len(matches) == 0 {
		return false, ""
	}

	// If first capturing group is "&", it's a pointer
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
