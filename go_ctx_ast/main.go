package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	ctxType             string // "ctx", "*ctx", ""
	rAvailable          bool
	hasCtxWithoutCancel bool // <- track ctxWithoutCancel per function
}

// RewriteContent parses and rewrites Go source code string, replacing context.TODO()
// with ctx, r.Context(), or ctxWithoutCancel (inside goroutines) when in scope.
func RewriteContent(src string) (string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return "", err
	}

	lines := strings.Split(src, "\n")
	fnScopeStack := []scopeInfo{}

	// Collect all go statements (anonymous and non-anonymous)
	goFuncBlocks := map[int]int{}
	ast.Inspect(node, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		startLine := fset.Position(goStmt.Pos()).Line - 1
		endLine := fset.Position(goStmt.End()).Line - 1
		goFuncBlocks[startLine] = endLine
		return true
	})

	// Walk functions and literals
	var walkFuncs func(ast.Node)
	walkFuncs = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch fn := node.(type) {
			case *ast.FuncDecl:
				pushScope(&fnScopeStack, fn.Type.Params)
				processLines(&fnScopeStack, fn.Body, fset, &lines, goFuncBlocks)
				popScope(&fnScopeStack)
				return false
			case *ast.FuncLit:
				pushScope(&fnScopeStack, fn.Type.Params)
				processLines(&fnScopeStack, fn.Body, fset, &lines, goFuncBlocks)
				popScope(&fnScopeStack)
				return false
			}
			return true
		})
	}
	walkFuncs(node)

	return strings.Join(lines, "\n"), nil
}

func processLines(fnScopeStack *[]scopeInfo, block *ast.BlockStmt, fset *token.FileSet, lines *[]string, goFuncBlocks map[int]int) {
	if block == nil {
		return
	}

	currFuncScope := &(*fnScopeStack)[len(*fnScopeStack)-1]
	start := fset.Position(block.Lbrace).Line - 1
	end := fset.Position(block.Rbrace).Line - 1

	if hasCtxWithoutCancelDecl(lines, start, end) {
		currFuncScope.hasCtxWithoutCancel = true
	}

	depth := 0
	localCtxDepth := -1
	localRDepth := -1

	ctxWord := regexp.MustCompile(`\bctx\b`)
	for i := start; i <= end && i < len(*lines); i++ {
		line := (*lines)[i]
		depth += strings.Count(line, "{") - strings.Count(line, "}")

		// Detect local ctx
		if localCtxDepth == -1 {
			if ok, detectedType := checkLocalCtxDeclaration(line); ok {
				localCtxDepth = depth
				currFuncScope.ctxType = detectedType
			}
		}

		// Detect local r param
		if localRDepth == -1 {
			if ok := checkLocalRParam(line); ok {
				localRDepth = depth
				currFuncScope.rAvailable = true
			}
		}

		if endLine, ok := goFuncBlocks[i]; ok && currFuncScope.ctxType != "" {
			newI, newEndLine := processGoStatement(currFuncScope, lines, i, endLine, goFuncBlocks, ctxWord)
			i = newI
			end += newEndLine - endLine
		} else {
			(*lines)[i] = replaceCtxOrRInLine((*lines)[i], currFuncScope.ctxType, currFuncScope.rAvailable)
		}

		// Reset ctx/r on scope exit
		if localCtxDepth != -1 && depth < localCtxDepth {
			localCtxDepth = -1
			currFuncScope.ctxType = ""
		}
		if localRDepth != -1 && depth < localRDepth {
			localRDepth = -1
			currFuncScope.rAvailable = false
		}
	}
}

func processGoStatement(currFuncScope *scopeInfo, lines *[]string, start, end int, goFuncBlocks map[int]int, ctxWord *regexp.Regexp) (int, int) {
	line := (*lines)[start]
	trimmed := strings.TrimSpace(line)
	isAnonymous := strings.HasPrefix(trimmed, "go func")
	containsCtx := false

	for j := start; j <= end && j < len(*lines); j++ {
		if strings.Contains((*lines)[j], "context.TODO()") || ctxWord.MatchString((*lines)[j]) {
			containsCtx = true
			break
		}
	}

	if !containsCtx {
		return start, end
	}

	if isAnonymous {
		start, end = handleAnonymousGo(currFuncScope, lines, start, end, goFuncBlocks)
	} else {
		start, end = handleNonAnonymousGo(lines, start, end, goFuncBlocks)
	}

	return start, end
}

func handleAnonymousGo(currFuncScope *scopeInfo, lines *[]string, start, end int, goFuncBlocks map[int]int) (int, int) {
	openBraceLine := start
	for openBraceLine <= end && !strings.Contains((*lines)[openBraceLine], "{") {
		openBraceLine++
	}

	if openBraceLine > end {
		return start, end
	}

	leadingTabs := (*lines)[openBraceLine][:len((*lines)[openBraceLine])-len(strings.TrimLeft((*lines)[openBraceLine], " \t"))]

	// Check if the func has ctx as parameter
	hasCtxParam := regexp.MustCompile(`func\s*\([^)]*ctx\s+context\.Context`).MatchString((*lines)[start])

	if hasCtxParam {
		if !currFuncScope.hasCtxWithoutCancel {
			// Insert before goroutine
			insertLine := leadingTabs + "ctxWithoutCancel := context.WithoutCancel(ctx)"
			*lines = append((*lines)[:start], append([]string{insertLine}, (*lines)[start:]...)...)
			shiftGoFuncBlocks(goFuncBlocks, start, 1)
			end++

			// Replace ctx usage only if we declared
			(*lines)[end] = replaceStandaloneCtx((*lines)[end], "ctxWithoutCancel")

			currFuncScope.hasCtxWithoutCancel = true // mark declared for function
		}
	} else {
		alreadyDeclared := hasCtxWithoutCancelDecl(lines, openBraceLine+1, end+1)

		if !alreadyDeclared {
			insertLine := leadingTabs + "\tctxWithoutCancel := context.WithoutCancel(ctx)"
			*lines = append((*lines)[:openBraceLine+1], append([]string{insertLine}, (*lines)[openBraceLine+1:]...)...)
			shiftGoFuncBlocks(goFuncBlocks, start, 1)
			end++

			for j := openBraceLine + 1; j <= end && j < len(*lines); j++ {
				if strings.Contains((*lines)[j], "ctx := context.Background()") {
					(*lines)[j] = ""
				}
				if !strings.Contains((*lines)[j], "ctxWithoutCancel :=") {
					(*lines)[j] = strings.ReplaceAll((*lines)[j], "context.TODO()", "ctxWithoutCancel")
					(*lines)[j] = replaceStandaloneCtx((*lines)[j], "ctxWithoutCancel")
				}
			}
		}
	}

	return start, end
}

func hasCtxWithoutCancelDecl(lines *[]string, start, end int) bool {
	ctxDeclRegex := regexp.MustCompile(`\bctxWithoutCancel\s*:=`)
	for i := start; i <= end && i < len(*lines); i++ {
		if ctxDeclRegex.MatchString((*lines)[i]) {
			return true
		}
	}
	return false
}

func handleNonAnonymousGo(lines *[]string, start, end int, goFuncBlocks map[int]int) (int, int) {
	line := (*lines)[start]
	leadingTabs := line[:len(line)-len(strings.TrimLeft(line, " \t"))]

	// Insert go func() { and ctxWithoutCancel declaration
	openBlock := []string{
		leadingTabs + "go func() {",
		leadingTabs + "\tctxWithoutCancel := context.WithoutCancel(ctx)",
	}
	*lines = append((*lines)[:start], append(openBlock, (*lines)[start:]...)...)

	// Shift indices due to insertion
	start += len(openBlock)
	end += len(openBlock)

	// Process the original call line
	line = (*lines)[start]
	trimmed := strings.TrimSpace(line)
	trimmed = strings.ReplaceAll(trimmed, "context.TODO()", "ctxWithoutCancel")
	trimmed = replaceStandaloneCtx(trimmed, "ctxWithoutCancel")
	(*lines)[start] = leadingTabs + "\t" + strings.TrimSpace(strings.TrimPrefix(trimmed, "go "))

	// Handle multi-line calls
	openParens := strings.Count((*lines)[start], "(") - strings.Count((*lines)[start], ")")
	for j := start + 1; j <= end && openParens > 0; j++ {
		line := (*lines)[j]
		line = strings.ReplaceAll(line, "context.TODO()", "ctxWithoutCancel")
		line = replaceStandaloneCtx(line, "ctxWithoutCancel")
		(*lines)[j] = leadingTabs + line
		openParens += strings.Count(line, "(") - strings.Count(line, ")")
		if openParens <= 0 {
			break
		}
	}

	// Close the goroutine
	closeBlock := []string{
		leadingTabs + "}()",
	}
	*lines = append((*lines)[:end+1], append(closeBlock, (*lines)[end+1:]...)...)

	// Shift goFuncBlocks map for inserted lines
	shiftGoFuncBlocks(goFuncBlocks, start-len(openBlock), len(openBlock)+len(closeBlock))

	// Return updated indices
	start = end + len(closeBlock) - 1
	end = end + len(closeBlock)

	return start, end
}

func shiftGoFuncBlocks(goFuncBlocks map[int]int, insertAt, addedLines int) {
	keys := make([]int, 0, len(goFuncBlocks))
	for k := range goFuncBlocks {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	for _, startLine := range keys {
		oldEnd := goFuncBlocks[startLine]
		if startLine > insertAt {
			goFuncBlocks[startLine+addedLines] = oldEnd + addedLines
			delete(goFuncBlocks, startLine)
		} else if startLine < insertAt && oldEnd >= insertAt {
			goFuncBlocks[startLine] = oldEnd + addedLines
		}
	}
}

func replaceStandaloneCtx(line string, replacement string) string {
	ctxRegex := regexp.MustCompile(`\bctx\b`)
	return ctxRegex.ReplaceAllStringFunc(line, func(match string) string {
		idx := strings.Index(line, match)
		if idx > 0 && line[idx-1] == '.' {
			// part of a selector, do not replace
			return match
		}
		return replacement
	})
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
