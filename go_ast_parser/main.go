package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Println("Usage: go run main.go <file.go> <functionName>")
		return
	}

	fileName := os.Args[1]
	funcName := os.Args[2]

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, fileName, nil, 0)
	if err != nil {
		panic(err)
	}

	// Walk the AST
	ast.Inspect(node, func(n ast.Node) bool {
		// Look for function declarations
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName {
			return true
		}

		// Walk inside the function body to find call expressions
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// Function being called (could be selector or identifier)
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				fmt.Println(fun.Name)
			case *ast.SelectorExpr:
				fmt.Printf("%s.%s\n", exprToString(fun.X), fun.Sel.Name)
			}
			return true
		})

		return false
	})
}

// Helper to get string from expressions
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", exprToString(e.X), e.Sel.Name)
	default:
		return ""
	}
}
