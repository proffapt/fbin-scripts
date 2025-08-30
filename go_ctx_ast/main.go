package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"

	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

var (
	flagNoGoroutines bool
	flagDryRun       bool
)

type ctxKind int

const (
	ctxNone    ctxKind = iota
	ctxValue           // context.Context
	ctxPointer         // *context.Context
)

// scopeFrame represents the availability of ctx and r at/after certain positions.
// Availability positions are token.Pos values within the file's FileSet.
type scopeFrame struct {
	// ctxKind and ctxAvailPos indicate whether `ctx` is available (value or pointer)
	// and from which position onward (the identifier position).
	ctxKind     ctxKind
	ctxAvailPos token.Pos

	// rPresent and rAvailPos indicate whether `r` (type *http.Request) is available.
	rPresent  bool
	rAvailPos token.Pos
}

// skipInterval marks ranges (pos..end) inside which we must not rewrite (anonymous goroutine bodies).
type skipInterval struct {
	start token.Pos
	end   token.Pos
}

func init() {
	flag.BoolVar(&flagNoGoroutines, "no-goroutines", false, "Skip rewriting inside goroutines")
	flag.BoolVar(&flagDryRun, "dry-run", false, "Print replacements but do not write files")
}

func main() {
	log.SetFlags(0)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <file-or-dir>...\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	var files []string

	// Collect files
	for _, arg := range flag.Args() {
		info, err := os.Stat(arg)
		if err != nil {
			log.Fatalf("stat %s: %v", arg, err)
		}

		if info.IsDir() {
			// Recursively collect .go files
			err := filepath.WalkDir(arg, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && strings.HasSuffix(path, ".go") {
					files = append(files, path)
				}
				return nil
			})
			if err != nil {
				log.Fatalf("walk %s: %v", arg, err)
			}
		} else if strings.HasSuffix(arg, ".go") {
			files = append(files, arg)
		} else {
			log.Fatalf("argument %s is not a Go file or directory", arg)
		}
	}

	if len(files) == 0 {
		log.Fatal("no Go files found")
	}

	// Build file patterns for packages.Load
	var patterns []string
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			log.Fatalf("abs %s: %v", f, err)
		}
		patterns = append(patterns, "file="+abs)
	}

	cfg := &packages.Config{
		Mode:  packages.LoadSyntax, // parse + type-check + syntax
		Dir:   ".",                 // module root
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		log.Fatalf("packages.Load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		log.Fatal("packages had errors")
	}

	// Process each file individually
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			filename := pkg.Fset.File(file.Pos()).Name()
			if !strings.HasSuffix(filename, ".go") {
				continue
			}
			if err := processFile(pkg, file, filename); err != nil {
				log.Printf("[ERROR] %s: %v", filename, err)
			} else {
				log.Printf("[OK] %s processed", filename)
			}
		}
	}
}

// isContextType recognizes context.Context and pointer to it.
func isContextType(t types.Type) (ctxKind, bool) {
	switch u := t.(type) {
	case *types.Named:
		if u.Obj().Pkg() != nil && u.Obj().Pkg().Path() == "context" && u.Obj().Name() == "Context" {
			return ctxValue, true
		}
	case *types.Pointer:
		if kind, ok := isContextType(u.Elem()); ok {
			// if the element is context.Context, treat as pointer kind
			_ = kind
			return ctxPointer, true
		}
	}
	return ctxNone, false
}

// isRequestPtrType detects *http.Request
func isRequestPtrType(t types.Type) bool {
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	if named, ok := ptr.Elem().(*types.Named); ok {
		if named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "net/http" && named.Obj().Name() == "Request" {
			return true
		}
	}
	return false
}

func processFile(pkg *packages.Package, file *ast.File, filename string) error {
	fset := pkg.Fset
	info := pkg.TypesInfo

	// First pass: find goroutine skips:
	// - anonymous func literals in `go func(...) { ... }(...)` (skip their body only)
	// - resolved functions invoked via `go someFunc(...)` or `go pkg.Func(...)` (skip whole target function)
	skipRanges := []skipInterval{}
	skipFuncs := map[*types.Func]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		gs, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		call := gs.Call
		// anonymous literal
		if funLit, ok := call.Fun.(*ast.FuncLit); ok {
			if funLit.Body != nil {
				skipRanges = append(skipRanges, skipInterval{start: funLit.Body.Lbrace, end: funLit.Body.Rbrace})
			}
			return true
		}
		// named or selector: try resolve the function object and mark skipFuncs
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if obj := info.Uses[fn]; obj != nil {
				if tf, ok := obj.(*types.Func); ok {
					skipFuncs[tf] = true
				}
			}
		case *ast.SelectorExpr:
			// selector.Sel is an Ident; Try to look up Uses for the Sel
			if sel := fn.Sel; sel != nil {
				if obj := info.Uses[sel]; obj != nil {
					if tf, ok := obj.(*types.Func); ok {
						skipFuncs[tf] = true
					}
				}
			}
		}
		return true
	})

	// Helper: test if pos lies inside any skipRange
	insideSkipRange := func(pos token.Pos) bool {
		for _, r := range skipRanges {
			if pos >= r.start && pos <= r.end {
				return true
			}
		}
		return false
	}

	// frame stack for scoping; each frame inherits parent's values on push
	var frameStack []scopeFrame
	pushFrame := func(copyFrom *scopeFrame) {
		if copyFrom == nil {
			frameStack = append(frameStack, scopeFrame{})
			return
		}
		frameStack = append(frameStack, *copyFrom)
	}
	popFrame := func() {
		if len(frameStack) == 0 {
			return
		}
		frameStack = frameStack[:len(frameStack)-1]
	}
	currentFrame := func() *scopeFrame {
		if len(frameStack) == 0 {
			return nil
		}
		return &frameStack[len(frameStack)-1]
	}

	// funcStack to know if current function is one that should be skipped entirely (because it's invoked by `go` elsewhere)
	type funcCtx struct {
		fnObj     *types.Func
		skipWhole bool
	}
	var funcStack []funcCtx

	// We'll collect whether we made changes
	var replaced bool

	// Use astutil.Apply to walk and potentially replace nodes
	newFile := astutil.Apply(file,
		// pre
		func(c *astutil.Cursor) bool {
			n := c.Node()
			if n == nil {
				return true
			}

			switch node := n.(type) {
			case *ast.FuncDecl:
				// entering a function decl: push new frame (inheriting nothing)
				pushFrame(nil)

				// determine if this function is one of the skipFuncs
				var fnObj *types.Func
				if node.Name != nil {
					if obj := info.Defs[node.Name]; obj != nil {
						if f, ok := obj.(*types.Func); ok {
							fnObj = f
						}
					}
				}
				skip := fnObj != nil && skipFuncs[fnObj]
				funcStack = append(funcStack, funcCtx{fnObj: fnObj, skipWhole: skip})

				// Inspect params to fill baseline availability
				if node.Type != nil && node.Type.Params != nil {
					fr := currentFrame()
					for _, fld := range node.Type.Params.List {
						for _, nm := range fld.Names {
							if nm == nil {
								continue
							}
							// try to get the type from info.Defs (for param id) or Types map
							var t types.Type
							if obj := info.Defs[nm]; obj != nil {
								t = obj.Type()
							} else if tv := info.Types[nm]; tv.Type != nil {
								t = tv.Type
							}
							if t == nil {
								// sometimes the type is on the field.Type (use typeOf expression)
								if fld.Type != nil {
									if tv := info.TypeOf(fld.Type); tv != nil {
										t = tv
									}
								}
							}
							if t == nil {
								continue
							}
							if nm.Name == "ctx" {
								if kind, ok := isContextType(t); ok {
									fr.ctxKind = kind
									fr.ctxAvailPos = nm.Pos()
								}
							} else if nm.Name == "r" {
								if isRequestPtrType(t) {
									fr.rPresent = true
									fr.rAvailPos = nm.Pos()
								}
							}
						}
					}
				}
				return true

			case *ast.FuncLit:
				// entering a function literal: push new frame (inheriting nothing)
				pushFrame(nil)

				// func literal params
				if node.Type != nil && node.Type.Params != nil {
					fr := currentFrame()
					for _, fld := range node.Type.Params.List {
						for _, nm := range fld.Names {
							if nm == nil {
								continue
							}
							var t types.Type
							if obj := info.Defs[nm]; obj != nil {
								t = obj.Type()
							} else if tv := info.Types[nm]; tv.Type != nil {
								t = tv.Type
							}
							if t == nil && fld.Type != nil {
								if tv := info.TypeOf(fld.Type); tv != nil {
									t = tv
								}
							}
							if t == nil {
								continue
							}
							if nm.Name == "ctx" {
								if kind, ok := isContextType(t); ok {
									fr.ctxKind = kind
									fr.ctxAvailPos = nm.Pos()
								}
							} else if nm.Name == "r" {
								if isRequestPtrType(t) {
									fr.rPresent = true
									fr.rAvailPos = nm.Pos()
								}
							}
						}
					}
				}
				// For func literals, we can't easily map to a types.Func object for skipWhole detection.
				// However, we already recorded anonymous goroutine bodies as skipRanges earlier.
				funcStack = append(funcStack, funcCtx{fnObj: nil, skipWhole: false})
				return true

			case *ast.BlockStmt:
				// push a child frame that inherits the parent frame
				var copyFrom *scopeFrame
				if cur := currentFrame(); cur != nil {
					copyFrom = cur
				}
				pushFrame(copyFrom)
				return true

			case *ast.AssignStmt:
				// handle `:=` new declarations for ctx and r
				if node.Tok == token.DEFINE {
					for _, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok || id == nil {
							continue
						}
						// Try to get the declared object's type via info.Defs (should be present for :=)
						var t types.Type
						if obj := info.Defs[id]; obj != nil {
							t = obj.Type()
						} else if tv := info.Types[id]; tv.Type != nil {
							t = tv.Type
						}
						// as fallback, attempt to get type from the corresponding RHS expr (best-effort)
						if t == nil {
							// find index of id in Lhs to map rhs
							for idx, lhsExpr := range node.Lhs {
								if lhsExpr == id && idx < len(node.Rhs) {
									if rhsT := info.TypeOf(node.Rhs[idx]); rhsT != nil {
										t = rhsT
									}
									break
								}
							}
						}
						if t == nil {
							continue
						}
						fr := currentFrame()
						if id.Name == "ctx" {
							if kind, ok := isContextType(t); ok {
								fr.ctxKind = kind
								fr.ctxAvailPos = id.Pos()
							}
						} else if id.Name == "r" {
							if isRequestPtrType(t) {
								fr.rPresent = true
								fr.rAvailPos = id.Pos()
							}
						}
					}
				}
				return true

			case *ast.ValueSpec:
				// var declarations: var ctx context.Context or var ctx = something
				for _, id := range node.Names {
					if id == nil {
						continue
					}
					if id.Name != "ctx" && id.Name != "r" {
						continue
					}
					var t types.Type
					if obj := info.Defs[id]; obj != nil {
						t = obj.Type()
					} else if node.Type != nil {
						if tv := info.TypeOf(node.Type); tv != nil {
							t = tv
						}
					} else {
						// try initializer
						for _, val := range node.Values {
							if tv := info.TypeOf(val); tv != nil {
								t = tv
								break
							}
						}
					}
					if t == nil {
						continue
					}
					fr := currentFrame()
					if id.Name == "ctx" {
						if kind, ok := isContextType(t); ok {
							fr.ctxKind = kind
							fr.ctxAvailPos = id.Pos()
						}
					} else if id.Name == "r" {
						if isRequestPtrType(t) {
							fr.rPresent = true
							fr.rAvailPos = id.Pos()
						}
					}
				}
				return true

			case *ast.CallExpr:
				// We only rewrite context.TODO() call expressions.
				// But first -- skip cases:
				//  - if the containing function is flagged skipWhole (because it is invoked via `go target(...)`)
				//  - if this call is inside an anonymous goroutine body and -no-goroutines is set (skipRanges)
				// Determine if current function is skipWhole
				if len(funcStack) > 0 && funcStack[len(funcStack)-1].skipWhole {
					return true
				}
				// If -no-goroutines passed, skip any call that is inside skipRanges
				if flagNoGoroutines {
					if insideSkipRange(node.Lparen) {
						return true
					}
				}
				// Check selector expression: context.TODO
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identX, ok := sel.X.(*ast.Ident)
				if !ok || identX.Name != "context" || sel.Sel == nil || sel.Sel.Name != "TODO" {
					return true
				}
				// TODO() must have zero args
				if len(node.Args) != 0 {
					return true
				}
				// Now find current frame and decide replacement
				fr := currentFrame()
				if fr == nil {
					return true
				}
				pos := node.Pos()
				// Decide replacement in priority:
				// 1) ctx (if ctxKind != ctxNone and node pos >= ctxAvailPos)
				// 2) *ctx if pointer
				// 3) r.Context() (if rPresent and pos >= rAvailPos)
				var repl ast.Expr
				var replStr string
				if fr.ctxKind != ctxNone && pos >= fr.ctxAvailPos {
					if fr.ctxKind == ctxValue {
						repl = ast.NewIdent("ctx")
						replStr = "ctx"
					} else {
						// *ctx: represent as '(*ctx)'? In expressions `*ctx` is unary; we'll use unary expr.
						repl = &ast.UnaryExpr{Op: token.MUL, X: ast.NewIdent("ctx")}
						replStr = "*ctx"
					}
				} else if fr.rPresent && pos >= fr.rAvailPos {
					repl = &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   ast.NewIdent("r"),
							Sel: ast.NewIdent("Context"),
						},
					}
					replStr = "r.Context()"
				} else {
					// nothing in scope -> leave as-is
					return true
				}

				// Replace node
				cReplace(c, repl)
				p := fset.Position(pos)
				if flagDryRun {
					fmt.Printf("[DRY] %s:%d: context.TODO() -> %s\n", p.Filename, p.Line, replStr)
				} else {
					fmt.Printf("✅ %s:%d: replaced context.TODO() → %s\n", p.Filename, p.Line, replStr)
				}
				replaced = true

				// do not visit children of replaced node
				return false
			}
			return true
		},
		// post
		func(c *astutil.Cursor) bool {
			switch c.Node().(type) {
			case *ast.BlockStmt:
				popFrame()
			case *ast.FuncDecl:
				// pop funcStack and frame
				if len(funcStack) > 0 {
					funcStack = funcStack[:len(funcStack)-1]
				}
				popFrame()
			case *ast.FuncLit:
				if len(funcStack) > 0 {
					funcStack = funcStack[:len(funcStack)-1]
				}
				popFrame()
			}
			return true
		})

	// newFile is an *ast.File (astutil.Apply returns interface{}); we don't need the returned value beyond writing
	if newFile == nil {
		return fmt.Errorf("internal rewrite returned nil AST")
	}

	if !replaced {
		// nothing to change
		return nil
	}

	// Write AST back to file preserving comments + layout (unless dry-run)
	if flagDryRun {
		return nil
	}
	if err := writeFile(fset, file, filename); err != nil {
		return fmt.Errorf("writeFile: %w", err)
	}
	return nil
}

// cReplace centralizes cursor.Replace (wrapped to satisfy type expectations)
func cReplace(c *astutil.Cursor, repl ast.Expr) *astutil.Cursor {
	c.Replace(repl)
	return c
}

// writeFile preserves comments + formatting
func writeFile(fset *token.FileSet, f *ast.File, path string) error {
	fOut, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fOut.Close()

	cfg := &printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 8}
	return cfg.Fprint(fOut, fset, f)
}
