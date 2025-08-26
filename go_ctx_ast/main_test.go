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
	{
		name: "go routine with multi-line non-anon literal",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go func(userID string) {
		userObj, err := users.Get(context.TODO(), userID)
		if err != nil {
			errorHandler.ReportToSentryWithoutRequest(err)
		}
		usersutil.UpdateUserSource(userID, userObj.Source, map[string]interface{}{})
	}(userID)
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func(userID string) {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		userObj, err := users.Get(ctxWithoutCancel, userID)
		if err != nil {
			errorHandler.ReportToSentryWithoutRequest(err)
		}
		usersutil.UpdateUserSource(userID, userObj.Source, map[string]interface{}{})
	}(userID)
}
`,
	},
	{
		name: "go routine with multi-line non-anon literal",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go s.AuditRepository.LogTemporalSignal(ctx, nil, coremodels.TemporalSignalLog{
		SignalName: signalName,
		UserID:     userID,
		WorkflowID: workflowID,
		SignalData: signalData,
	})
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		s.AuditRepository.LogTemporalSignal(ctxWithoutCancel, nil, coremodels.TemporalSignalLog{
			SignalName: signalName,
			UserID:     userID,
			WorkflowID: workflowID,
			SignalData: signalData,
		})
	}()
}
`,
	},
	{
		name: "multiple functions with and without context",
		input: `
package main

import "context"

func processA(ctx context.Context) {
		doA(context.TODO())
}

func processB(ctx context.Context) {
		doB(context.TODO())
}
func processC() {
		doC(context.TODO())
}

func main(ctx context.Context) {
	processA(ctx)
	processB(ctx)
	processC(ctx)
}
`,
		expected: `
package main

import "context"

func processA(ctx context.Context) {
		doA(ctx)
}

func processB(ctx context.Context) {
		doB(ctx)
}
func processC() {
		doC(context.TODO())
}

func main(ctx context.Context) {
	processA(ctx)
	processB(ctx)
	processC(ctx)
}
`,
	},
	{
		name: "multiple functions with multiple go routines",
		input: `
package main

import "context"

func processA(ctx context.Context) {
	go func() {
		task1(context.TODO())
	}()
	go func() {
		task2(context.TODO())
	}()

	go func() {
		task2(context.TODO())
	}()
}

func processB(ctx context.Context) {
	go func() {
		task3(context.TODO())
	}()
	go func() {
		task4(context.TODO())
	}()
}

func processC(ctx context.Context) {
	go func() {
		task3(context.TODO())
	}()
	go func() {
		task4(context.TODO())
	}()
	go func() {
		task4(context.TODO())
	}()
}

func main(ctx context.Context) {
	processA(ctx)
	processB(ctx)
	processC(ctx)
}
`,
		expected: `
package main

import "context"

func processA(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task1(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task2(ctxWithoutCancel)
	}()

	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task2(ctxWithoutCancel)
	}()
}

func processB(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task3(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task4(ctxWithoutCancel)
	}()
}

func processC(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task3(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task4(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		task4(ctxWithoutCancel)
	}()
}

func main(ctx context.Context) {
	processA(ctx)
	processB(ctx)
	processC(ctx)
}
`,
	},

	{
		name: "multiple anonymous go routines",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		someFunc1(context.TODO())
	}()
	go func() {
		someFunc2(context.TODO())
	}()
	go func() {
		someFunc3(context.TODO())
	}()
	
	someshit(a, b)
	go func() {
		someFunc3(context.TODO())
	}()
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc1(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc2(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc3(ctxWithoutCancel)
	}()
	
	someshit(a, b)
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc3(ctxWithoutCancel)
	}()
}
`,
	},
	// Function parameters
	{
		name: "go routine 1",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go someFunc(context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc(ctxWithoutCancel)
	}()
}
`,
	},

	// Multiple go tracer in same function
	{
		name: "multiple go routines",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go someFunc1(ctx)

	go someFunc2(ctx)
	go someFunc3(context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc1(ctxWithoutCancel)
	}()

	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc2(ctxWithoutCancel)
	}()
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc3(ctxWithoutCancel)
	}()
}
`,
	},

	{
		name: "go routine with ctx parameter in func signature",
		input: `
package main

import (
	"context"
)

func main(ctx context.Context) {
	go func(ctx context.Context, kycObj *lenderservice.KYCDocumentstructsDetails) {
		defer wg.Done()
		mediObj, err := media.Get(ctx, kycObj.MediaID)
		if err != nil {
			logger.WithLoanApplication(loanApplicationID).Warn(err)
			return
		}
		if mediObj.MediaID == "" {
			logger.WithLoanApplication(loanApplicationID).Warn("media not found")
			return
		}
		kycObj.Path = mediObj.Path
	}(ctx, kycObj)
}
`,
		expected: `
package main

import (
	"context"
)

func main(ctx context.Context) {
	go func(ctx context.Context, kycObj *lenderservice.KYCDocumentstructsDetails) {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		defer wg.Done()
		mediObj, err := media.Get(ctxWithoutCancel, kycObj.MediaID)
		if err != nil {
			logger.WithLoanApplication(loanApplicationID).Warn(err)
			return
		}
		if mediObj.MediaID == "" {
			logger.WithLoanApplication(loanApplicationID).Warn("media not found")
			return
		}
		kycObj.Path = mediObj.Path
	}(ctx, kycObj)
}
`,
	},
	{
		name: "go routine with multi-line struct literal",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		s.AuditRepository.LogTemporalSignal(ctx, nil, coremodels.TemporalSignalLog{
			SignalName: signalName,
			UserID:     userID,
			WorkflowID: workflowID,
			SignalData: signalData,
		})
	}()
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		s.AuditRepository.LogTemporalSignal(ctxWithoutCancel, nil, coremodels.TemporalSignalLog{
			SignalName: signalName,
			UserID:     userID,
			WorkflowID: workflowID,
			SignalData: signalData,
		})
	}()
}
`,
	},
	// Function parameters
	{
		name: "go routine 1",
		input: `
package main

import "context"

func main(ctx context.Context) {
	go someFunc(context.TODO())
}
`,
		expected: `
package main

import "context"

func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc(ctxWithoutCancel)
	}()
}
`,
	},
	{
		name: "go routine 2",
		input: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	go someFunc(context.TODO())
  doingSomething(b)
	go someFunc2(context.TODO())
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc(ctxWithoutCancel)
	}()
  doingSomething(b)
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		someFunc2(ctxWithoutCancel)
	}()
	 }
	 `,
	},
	{
		name: "go routine 3",
		input: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	go func() {
		doingSomething(context.TODO())
	}()
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		doingSomething(ctxWithoutCancel)
	}()
	 }
	 `,
	},
	{
		name: "go routine 4",
		input: `
	 package main

	 import "context"

	 func main() {
	go func() {
		ctx := context.Background()
		doingSomething(ctx)
	}()
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main() {
	go func() {
		ctx := context.Background()
		doingSomething(ctx)
	}()
	 }
	 `,
	},
	{
		name: "go routine 5",
		input: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(context.TODO())
	go func() {
		doingSomething(ctx)
		doingSomething2(ctx)
	}()
	someFunc(ctx)
	someFunc2(context.TODO())
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(ctx)
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		doingSomething(ctxWithoutCancel)
		doingSomething2(ctxWithoutCancel)
	}()
	someFunc(ctx)
	someFunc2(ctx)
	 }
	 `,
	},
	{
		name: "go routine 6",
		input: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(context.TODO())
	go func() {
		doingSomething(a)
	}()
	someFunc(ctx)
	someFunc2(context.TODO())
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(ctx)
	go func() {
		doingSomething(a)
	}()
	someFunc(ctx)
	someFunc2(ctx)
	 }
	 `,
	},
	{
		name: "go routine 7",
		input: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(context.TODO())
	go func() {
		ctx := context.Background()
		doingSomething(ctx)
	}()
	someFunc(ctx)
	someFunc2(context.TODO())
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(ctx)
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()


		doingSomething(ctxWithoutCancel)
	}()
	someFunc(ctx)
	someFunc2(ctx)
	 }
	 `,
	},
	{
		name: "go routine 8",
		input: `
	 package main

	 import "context"

	 func main() {
	someFunc(context.TODO())
	go func() {
		ctx := context.Background()
		doingSomething(ctx)
	}()
	someFunc2(context.TODO())
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main() {
	someFunc(context.TODO())
	go func() {
		ctx := context.Background()
		doingSomething(ctx)
	}()
	someFunc2(context.TODO())
	 }
	 `,
	},
	{
		name: "go routine 9",
		input: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(context.TODO())
	go func() {
		doingSomething(ctx)
	}()
	someFunc2(context.TODO())
	go func() {
		doingSomething(ctx)
	}()
	 }
	 `,
		expected: `
	 package main

	 import "context"

	 func main(ctx context.Context) {
	someFunc(ctx)
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		doingSomething(ctxWithoutCancel)
	}()
	someFunc2(ctx)
	go func() {
		span, ctxWithoutCancel := tracer.StartOtelChildSpan(
			context.WithoutCancel(ctx),
			tracer.ChildSpanInfo{OperationName: "go-routine"},
		)
		defer span.End()

		doingSomething(ctxWithoutCancel)
	}()
	 }
	 `,
	},
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
