package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type TestCase struct {
	name     string
	input    string
	expected string
}

var testCases = []TestCase{
	// Function parameters
	{
		name: "func def 1",
		input: `
package main

import "context"

func main(ctx context.Context) {
	someFunc(context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	someFunc(ctx)
}
`,
	},
	{
		name: "pointer func def 1",
		input: `
package main

import "context"

func main(ctx *context.Context) {
	someFunc(context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx *context.Context) {
	someFunc(*ctx)
}
`,
	},

	// Local variable declarations
	{
		name: "declaration 1",
		input: `
package main

import "context"

func main() {
	ctx := context.Background()
	b := context.TODO()
	_ = b
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := context.Background()
	b := ctx
	_ = b
}
`,
	},
	{
		name: "declaration 2",
		input: `
package main

import "context"

func main() {
	a := context.TODO()
	ctx := context.Background()
	b := context.TODO()
	_ = a
	_ = b
}
`,
		expected: `
package main

import "context"

func main() {
	a := context.TODO()
	ctx := context.Background()
	b := ctx
	_ = a
	_ = b
}
`,
	},

	// Scope cases
	{
		name: "scope 1",
		input: `
package main

import "context"

func main() {
	if something() {
		ctx := context.Background()
		someFunc(context.TODO())
	}
	someFunc(context.TODO())
}
`,
		expected: `
package main

import "context"

func main() {
	if something() {
		ctx := context.Background()
		someFunc(ctx)
	}
	someFunc(context.TODO())
}
`,
	},

	// Function arguments and return
	{
		name: "function arguments and return",
		input: `
package main

import "context"
import "fmt"

func main() {
	ctx := context.Background()
	fmt.Println(context.TODO())
	useContext(context.TODO())
}

func useContext(c context.Context) string {
	if c == nil {
		return "nil"
	}
	return "ok"
}
`,
		expected: `
package main

import "context"
import "fmt"

func main() {
	ctx := context.Background()
	fmt.Println(ctx)
	useContext(ctx)
}

func useContext(c context.Context) string {
	if c == nil {
		return "nil"
	}
	return "ok"
}
`,
	},

	// Struct and map literals
	{
		name: "struct literal and map literal",
		input: `
package main

import "context"

func main() {
	ctx := context.Background()
	type Config struct { C context.Context }
	cfg := Config{C: context.TODO()}
	m := map[string]context.Context{"x": context.TODO()}
	_ = cfg
	_ = m
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := context.Background()
	type Config struct { C context.Context }
	cfg := Config{C: ctx}
	m := map[string]context.Context{"x": ctx}
	_ = cfg
	_ = m
}
`,
	},

	// Nested scope
	{
		name: "nested scope",
		input: `
package main

import "context"

func main() {
	if something() {
		ctx := context.Background()
		if somethingElse() {
			a := context.TODO()
			b := context.TODO()
		}
		c := context.TODO()
	}
	d := context.TODO()
}
`,
		expected: `
package main

import "context"

func main() {
	if something() {
		ctx := context.Background()
		if somethingElse() {
			a := ctx
			b := ctx
		}
		c := ctx
	}
	d := context.TODO()
}
`,
	},

	// Shadowed ctx
	{
		name: "shadowed ctx",
		input: `
package main

import "context"

func main() {
	ctx := context.Background()
	a := context.TODO()
	if something() {
		ctx := context.TODO()
		b := context.TODO()
	}
	c := context.TODO()
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := context.Background()
	a := ctx
	if something() {
		ctx := context.TODO()
		b := ctx
	}
	c := ctx
}
`,
	},

	// Pointer local ctx
	{
		name: "pointer ctx local",
		input: `
package main

import "context"

func main() {
	ctx := &context.Background()
	a := context.TODO()
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := &context.Background()
	a := *ctx
}
`,
	},

	// Closures
	{
		name: "closure",
		input: `
package main

import "context"

func main() {
	ctx := context.Background()
	f := func() {
		fmt.Println(context.TODO())
	}
	f()
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := context.Background()
	f := func() {
		fmt.Println(ctx)
	}
	f()
}
`,
	},

	// After other code
	{
		name: "ctx after other code",
		input: `
package main

import "context"

func main() {
	a := 42
	ctx := context.Background()
	b := context.TODO()
}
`,
		expected: `
package main

import "context"

func main() {
	a := 42
	ctx := context.Background()
	b := ctx
}
`,
	},

	// Multiple functions
	{
		name: "multiple functions",
		input: `
package main

import "context"

func f1(ctx context.Context) {
	a := context.TODO()
}
func f2() {
	ctx := context.Background()
	b := context.TODO()
}
`,
		expected: `
package main

import "context"

func f1(ctx context.Context) {
	a := ctx
}
func f2() {
	ctx := context.Background()
	b := ctx
}
`,
	},

	// Multiple args in call
	{
		name: "function call multiple args",
		input: `
package main

import "context"

func main(ctx context.Context) {
	doSomething(ctx, context.TODO(), context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	doSomething(ctx, ctx, ctx)
}
`,
	},

	// Nested struct and slice literals
	{
		name: "nested literals",
		input: `
package main

import "context"

func main() {
	ctx := context.Background()
	cfg := Config{
		C: context.TODO(),
		Subs: []SubConfig{
			{C: context.TODO()},
		},
	}
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := context.Background()
	cfg := Config{
		C: ctx,
		Subs: []SubConfig{
			{C: ctx},
		},
	}
}
`,
	},

	// Map literals
	{
		name: "map literal multiple values",
		input: `
package main

import "context"

func main() {
	ctx := context.Background()
	m := map[string]context.Context{
		"req": context.TODO(),
		"rsp": context.TODO(),
	}
}
`,
		expected: `
package main

import "context"

func main() {
	ctx := context.Background()
	m := map[string]context.Context{
		"req": ctx,
		"rsp": ctx,
	}
}
`,
	},

	// Returns
	{
		name: "return statements",
		input: `
package main

import "context"

func f(ctx context.Context) context.Context {
	return context.TODO()
}
`,
		expected: `
package main

import "context"

func f(ctx context.Context) context.Context {
	return ctx
}
`,
	},

	// Context in method calls
	{
		name: "method calls",
		input: `
package main

import "context"

type Service interface {
	DoSomething(ctx context.Context)
}

func (s *Server) Serve(ctx *context.Context) {
	s.DoSomething(context.TODO())
}
`,
		expected: `
package main

import "context"

type Service interface {
	DoSomething(ctx context.Context)
}

func (s *Server) Serve(ctx *context.Context) {
	s.DoSomething(*ctx)
}
`,
	},

	// Don't replace inside comments or string literals
	{
		name: "comments and strings",
		input: `
package main

import "context"

func main(ctx context.Context) {
	fmt.Println("context.TODO() should not be replaced")
	// context.TODO() inside comment
	doSomething(context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	fmt.Println("context.TODO() should not be replaced")
	// context.TODO() inside comment
	doSomething(ctx)
}
`,
	},
}

func TestContextReplacement(t *testing.T) {
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir, err := ioutil.TempDir("", "ctx_test")
			assert.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			filePath := filepath.Join(tmpDir, "test.go")
			err = ioutil.WriteFile(filePath, []byte(tc.input), 0644)
			assert.NoError(t, err)

			newContent, err := RewriteContent(tc.input)
			assert.NoError(t, err)

			actual := normalizeCode(newContent)
			expected := normalizeCode(tc.expected)

			assert.Equal(t, expected, actual, "replacement failed")
		})
	}
}

// normalizeCode trims spaces and newlines for stable comparison
func normalizeCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.ReplaceAll(code, "\r\n", "\n")
	lines := strings.Split(code, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}
