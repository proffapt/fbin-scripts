package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	doTodo := flag.Bool("todo", false, "Replace context.TODO()")
	doBackground := flag.Bool("background", false, "Replace context.Background()")
	flag.Parse()

	if !*doTodo && !*doBackground {
		fmt.Println("Usage: go run main.go [--todo] [--background] <file_or_dir> [...]")
		os.Exit(1)
	}

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Provide at least one Go file or directory")
		os.Exit(1)
	}

	for _, path := range args {
		filepath.WalkDir(path, func(fp string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(fp) != ".go" {
				return nil
			}
			processFile(fp, *doTodo, *doBackground)
			return nil
		})
	}
	fmt.Println("Done")
}

func processFile(filename string, doTodo, doBackground bool) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("Error opening file:", filename, err)
		return
	}
	defer file.Close()

	var output []string
	scanner := bufio.NewScanner(file)
	reTodo := regexp.MustCompile(`\bcontext\.TODO\(\)`)
	reBg := regexp.MustCompile(`\bcontext\.Background\(\)`)
	reLocalCtx := regexp.MustCompile(`\bctx\b\s*[:=]`) // detect ctx := or ctx =

	inFunc := false
	braceCount := 0
	ctxAvailable := false
	rAvailable := false

	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := scanner.Text()
		trim := strings.TrimSpace(line)

		// Detect function start
		if !inFunc && strings.HasPrefix(trim, "func ") && strings.Contains(trim, "(") && strings.Contains(trim, ")") {
			inFunc = true
			braceCount = strings.Count(line, "{") - strings.Count(line, "}")

			ctxAvailable = false
			rAvailable = false

			// Check function parameters
			start := strings.Index(trim, "(")
			end := strings.Index(trim, ")")
			if start >= 0 && end > start {
				params := trim[start+1 : end]
				for _, p := range strings.Split(params, ",") {
					p = strings.TrimSpace(p)
					if strings.HasPrefix(p, "ctx") || strings.Contains(p, "ctx ") {
						ctxAvailable = true
					}
					if strings.HasPrefix(p, "r") || strings.Contains(p, "r ") {
						rAvailable = true
					}
				}
			}
		} else if inFunc {
			// Update brace count
			braceCount += strings.Count(line, "{") - strings.Count(line, "}")
			if braceCount <= 0 {
				inFunc = false
				ctxAvailable = false
				rAvailable = false
			}
		}

		newLine := line

		// Detect local ctx declaration before replacement
		if inFunc && reLocalCtx.MatchString(trim) {
			ctxAvailable = true
		}

		// Only replace inside a function
		if inFunc {
			// Replace context.TODO()
			if doTodo && reTodo.MatchString(line) {
				if ctxAvailable {
					newLine = reTodo.ReplaceAllString(line, "ctx")
					fmt.Printf("%s:%d → replaced context.TODO() with ctx\n", filename, lineNum)
				} else if rAvailable {
					newLine = reTodo.ReplaceAllString(line, "r.Context()")
					fmt.Printf("%s:%d → replaced context.TODO() with r.Context()\n", filename, lineNum)
				}
			}

			// Replace context.Background()
			if doBackground && reBg.MatchString(line) {
				if ctxAvailable {
					newLine = reBg.ReplaceAllString(line, "ctx")
					fmt.Printf("%s:%d → replaced context.Background() with ctx\n", filename, lineNum)
				} else if rAvailable {
					newLine = reBg.ReplaceAllString(line, "r.Context()")
					fmt.Printf("%s:%d → replaced context.Background() with r.Context()\n", filename, lineNum)
				}
			}
		}

		output = append(output, newLine)
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading file:", filename, err)
		return
	}

	// Write back without adding extra newline at EOF
	content := strings.Join(output, "\n")
	if err := os.WriteFile(filename, []byte(content+"\n"), 0644); err != nil {
		fmt.Println("Error writing file:", filename, err)
	}
}
