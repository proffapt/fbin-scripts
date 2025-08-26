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
	// baseline from function params
	baseCtxType    string // "", "ctx", "*ctx"
	baseRAvailable bool

	// currently active (may be shadowed by local ctx := ...)
	ctxType    string
	rAvailable bool

	hasBody   bool
	startLine int
	endLine   int
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
	addedLinesCount := 0
	var walkFuncs func(ast.Node)
	walkFuncs = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch fn := node.(type) {
			case *ast.FuncDecl:
				pushScope(&fnScopeStack, fset, fn, addedLinesCount)
				processFunction(&fnScopeStack, &lines, goFuncBlocks, &addedLinesCount)
				popScope(&fnScopeStack)
				return false
			case *ast.FuncLit:
				pushScope(&fnScopeStack, fset, fn, addedLinesCount)
				processFunction(&fnScopeStack, &lines, goFuncBlocks, &addedLinesCount)
				popScope(&fnScopeStack)
				return false
			}
			return true
		})
	}
	walkFuncs(node)

	return strings.Join(lines, "\n"), nil
}

func processFunction(fnScopeStack *[]scopeInfo, lines *[]string, goFuncBlocks map[int]int, addedLinesCount *int) {
	currFuncScope := &(*fnScopeStack)[len(*fnScopeStack)-1]
	if !currFuncScope.hasBody {
		return
	}

	start := currFuncScope.startLine
	end := currFuncScope.endLine

	depth := 0
	localCtxDepth := -1
	localRDepth := -1

	ogEnd := end
	ctxWord := regexp.MustCompile(`\bctx\b`)
	for i := start; i <= end && i < len(*lines); i++ {
		line := (*lines)[i]

		// Compute min depth reached on this line and final depth after the line
		d := depth
		minD := d
		for _, ch := range line {
			if ch == '}' {
				d--
				if d < minD {
					minD = d
				}
			} else if ch == '{' {
				d++
			}
		}

		// If we exited the local ctx/r scope anywhere on this line (e.g. `} else {`),
		// reset BEFORE doing any replacements on this line.
		if localCtxDepth != -1 && minD < localCtxDepth {
			localCtxDepth = -1
			currFuncScope.ctxType = currFuncScope.baseCtxType
		}
		if localRDepth != -1 && minD < localRDepth {
			localRDepth = -1
			currFuncScope.rAvailable = currFuncScope.baseRAvailable
		}

		// Detect new local ctx/r (depth at start-of-line)
		if localCtxDepth == -1 {
			if ok, detectedType := checkLocalCtxDeclaration(line); ok {
				localCtxDepth = depth
				currFuncScope.ctxType = detectedType
			}
		}
		if localRDepth == -1 {
			if ok := checkLocalRParam(line); ok {
				localRDepth = depth
				currFuncScope.rAvailable = true
			}
		}

		// Handle go-stmt or plain replacement using the (possibly reset) ctx/r
		if endLine, ok := goFuncBlocks[i]; ok && currFuncScope.ctxType != "" {
			newEndLine := processGoStatement(lines, i, endLine, goFuncBlocks, ctxWord)
			lenAddedLines := newEndLine - endLine
			i = newEndLine
			end += lenAddedLines
		} else {
			(*lines)[i] = replaceCtxOrRInLine((*lines)[i], currFuncScope.ctxType, currFuncScope.rAvailable)
		}

		// Commit final depth for this line
		depth = d

		// Safety: if the line ends with pure closing braces, also clear after the line
		if localCtxDepth != -1 && depth < localCtxDepth {
			localCtxDepth = -1
			currFuncScope.ctxType = currFuncScope.baseCtxType
		}
		if localRDepth != -1 && depth < localRDepth {
			localRDepth = -1
			currFuncScope.rAvailable = currFuncScope.baseRAvailable
		}
	}
	*addedLinesCount += end - ogEnd
}

func processGoStatement(lines *[]string, start, end int, goFuncBlocks map[int]int, ctxWord *regexp.Regexp) int {
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
		return end
	}

	if isAnonymous {
		end = handleAnonymousGo(lines, start, end, goFuncBlocks)
	} else {
		end = handleNonAnonymousGo(lines, start, end, goFuncBlocks)
	}

	return end
}

func handleAnonymousGo(lines *[]string, start, end int, goFuncBlocks map[int]int) int {
	for start <= end && !strings.Contains((*lines)[start], "{") {
		start++
	}
	if start > end {
		return end
	}

	leadingTabs := (*lines)[start][:len((*lines)[start])-len(strings.TrimLeft((*lines)[start], " \t"))]
	alreadyDeclared := hasCtxWithoutCancelDecl(lines, start+1, end+1)

	if !alreadyDeclared {
		insertLines := []string{
			leadingTabs + "\tspan, ctxWithoutCancel := tracer.StartOtelChildSpan(",
			leadingTabs + "\t\tcontext.WithoutCancel(ctx),",
			leadingTabs + "\t\ttracer.ChildSpanInfo{OperationName: \"go-routine\"},",
			leadingTabs + "\t)",
			leadingTabs + "\tdefer span.End()",
			"",
		}
		*lines = append((*lines)[:start+1], append(insertLines, (*lines)[start+1:]...)...)

		// Shift indices of all future go statements AFTER this start line
		shiftGoFuncBlocks(goFuncBlocks, start+1, len(insertLines))
		end += len(insertLines)
	}

	// Replace context.TODO() inside the goroutine body
	for j := start + 1; j <= end && j < len(*lines); j++ {
		if strings.Contains((*lines)[j], "ctx := context.Background()") {
			(*lines)[j] = ""
		}
		if !strings.Contains((*lines)[j], "context.WithoutCancel(") {
			(*lines)[j] = strings.ReplaceAll((*lines)[j], "context.TODO()", "ctxWithoutCancel")
			if !strings.Contains((*lines)[j], "}(ctx") {
				(*lines)[j] = replaceStandaloneCtx((*lines)[j], "ctxWithoutCancel")
			}
		}
	}

	start = end // mark as fully processed
	return end
}

func handleNonAnonymousGo(lines *[]string, start, end int, goFuncBlocks map[int]int) int {
	line := (*lines)[start]
	leadingTabs := line[:len(line)-len(strings.TrimLeft(line, " \t"))]

	// Insert go func() { and ctxWithoutCancel declaration
	openBlock := []string{
		leadingTabs + "go func() {",
		leadingTabs + "\tspan, ctxWithoutCancel := tracer.StartOtelChildSpan(",
		leadingTabs + "\t\tcontext.WithoutCancel(ctx),",
		leadingTabs + "\t\ttracer.ChildSpanInfo{OperationName: \"go-routine\"},",
		leadingTabs + "\t)",
		leadingTabs + "\tdefer span.End()",
		"",
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
	end = end + len(closeBlock)
	start = end

	return end
}

// shiftGoFuncBlocks reliably shifts only future keys
func shiftGoFuncBlocks(goFuncBlocks map[int]int, insertAt, addedLines int) {
	newMap := make(map[int]int, len(goFuncBlocks))
	for startLine, oldEnd := range goFuncBlocks {
		if startLine > insertAt {
			newMap[startLine+addedLines] = oldEnd + addedLines
		} else if startLine <= insertAt && oldEnd > insertAt {
			// current block contains insert point
			newMap[startLine] = oldEnd + addedLines
		} else {
			newMap[startLine] = oldEnd
		}
	}
	// replace original map
	for k := range goFuncBlocks {
		delete(goFuncBlocks, k)
	}
	for k, v := range newMap {
		goFuncBlocks[k] = v
	}
}

// pushScope adds a new function scope based on parameters
func pushScope(stack *[]scopeInfo, fset *token.FileSet, fnNode ast.Node, addedLinesCount int) {
	s := scopeInfo{}
	var params *ast.FieldList

	switch fn := fnNode.(type) {
	case *ast.FuncDecl:
		params = fn.Type.Params
		if fn.Body != nil {
			s.hasBody = true
			s.startLine = addedLinesCount + fset.Position(fn.Body.Lbrace).Line - 1
			s.endLine = addedLinesCount + fset.Position(fn.Body.Rbrace).Line - 1
		}
	case *ast.FuncLit:
		params = fn.Type.Params
		if fn.Body != nil {
			s.hasBody = true
			s.startLine = addedLinesCount + fset.Position(fn.Body.Lbrace).Line - 1
			s.endLine = addedLinesCount + fset.Position(fn.Body.Rbrace).Line - 1
		}
	default:
		return
	}

	if params != nil {
		for _, param := range params.List {
			for _, name := range param.Names {
				if name.Name == "ctx" {
					typ := exprToString(param.Type)
					if typ == "context.Context" {
						s.baseCtxType = "ctx"
					}
					if typ == "*context.Context" {
						s.baseCtxType = "*ctx"
					}
				}
				if name.Name == "r" {
					if exprToString(param.Type) == "*http.Request" {
						s.baseRAvailable = true
					}
				}
			}
		}
	}

	// start active state from baseline
	s.ctxType = s.baseCtxType
	s.rAvailable = s.baseRAvailable

	*stack = append(*stack, s)
}

// popScope removes the top-most scope
func popScope(stack *[]scopeInfo) {
	if len(*stack) > 0 {
		*stack = (*stack)[:len(*stack)-1]
	}
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

// checkLocalRParam returns true if line contains r as a function param (simplified detection)
func checkLocalRParam(line string) bool {
	rParamRegex := regexp.MustCompile(`func\s*\([^)]*r\s+\*http\.Request[^)]*\)`)
	return rParamRegex.MatchString(line)
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
