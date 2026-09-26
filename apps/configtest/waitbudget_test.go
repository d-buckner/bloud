// SPDX-License-Identifier: AGPL-3.0-only

package configtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// AssertWaitBudgetsReachable is the guard against reintroducing the failure
// this refactor removed.
//
// An app's readiness wait is only honest if the framework lets it run for as
// long as it declares. The orchestrator cancels every PostStart at
// DefaultPostStartBudget, which is appclient.MaxWaitBudget by construction.
// A wait declared longer than that can never expire on its own: the framework
// kills it first, and the declared number is decoration. That is exactly how
// Timeout(5 * time.Minute) ended up meaning nothing.
//
// So: no app may declare a wait budget above MaxWaitBudget. The check reads
// the source rather than trusting a comment, and it fails on a budget it
// cannot evaluate instead of skipping it, so the rule cannot be dodged by
// writing the duration in a shape this file does not understand.
func TestAssertWaitBudgetsReachable(t *testing.T) {
	root := appsRoot(t)

	type finding struct {
		pos     string
		budget  time.Duration
	}
	var findings []finding

	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Within" {
				return true
			}
			if len(call.Args) != 1 {
				t.Errorf("%s: Within() takes exactly one argument", fset.Position(call.Pos()))
				return true
			}
			d, ok := evalDurationExpr(call.Args[0])
			if !ok {
				t.Errorf("%s: cannot evaluate the Within() budget %s; extend evalDurationExpr rather than let this rule skip a wait",
					fset.Position(call.Pos()), exprString(call.Args[0]))
				return true
			}
			findings = append(findings, finding{pos: fset.Position(call.Pos()).String(), budget: d})
			if d > appclient.MaxWaitBudget {
				t.Errorf("%s: wait budget %s exceeds appclient.MaxWaitBudget %s; the framework cancels PostStart at that ceiling, so this wait can never expire on its own. Raise MaxWaitBudget (and DefaultPostStartBudget with it) deliberately, or lower the wait.",
					fset.Position(call.Pos()), d, appclient.MaxWaitBudget)
			}
			return true
		})
		return nil
	})
	requireNoWalkErr(t, err)

	if len(findings) == 0 {
		t.Fatalf("no Within() wait budgets found under %s; the harness is not looking at the right tree", root)
	}
	t.Logf("checked %d declared wait budgets against MaxWaitBudget %s", len(findings), appclient.MaxWaitBudget)
	for _, f := range findings {
		t.Logf("  %s: %s", f.pos, f.budget)
	}
}

// appsRoot resolves the apps/ directory from the harness package dir.
func appsRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, ".."))
	if _, err := os.Stat(filepath.Join(root, "registry.go")); err != nil {
		t.Fatalf("apps root %q does not look like the apps tree: %v", root, err)
	}
	return root
}

func requireNoWalkErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("walk apps tree: %v", err)
	}
}

// evalDurationExpr evaluates the duration expressions apps actually write in
// a wait budget: a time unit, an integer multiple of one, and parentheses
// around either. Anything else reports false so the caller fails loudly.
func evalDurationExpr(e ast.Expr) (time.Duration, bool) {
	switch v := e.(type) {
	case *ast.ParenExpr:
		return evalDurationExpr(v.X)
	case *ast.SelectorExpr:
		if id, ok := v.X.(*ast.Ident); ok && id.Name == "time" {
			return unitDuration(v.Sel.Name)
		}
		return 0, false
	case *ast.BinaryExpr:
		if v.Op != token.MUL {
			return 0, false
		}
		// <int> * <unit> or <unit> * <int>
		if n, ok := intLit(v.X); ok {
			if u, ok := evalDurationExpr(v.Y); ok {
				return time.Duration(n) * u, true
			}
		}
		if n, ok := intLit(v.Y); ok {
			if u, ok := evalDurationExpr(v.X); ok {
				return time.Duration(n) * u, true
			}
		}
		// <unit> * <unit> is not meaningful; fall through.
		return 0, false
	}
	return 0, false
}

func intLit(e ast.Expr) (int, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.Atoi(lit.Value)
	if err != nil {
		return 0, false
	}
	return n, true
}

func unitDuration(name string) (time.Duration, bool) {
	switch name {
	case "Nanosecond":
		return time.Nanosecond, true
	case "Microsecond":
		return time.Microsecond, true
	case "Millisecond":
		return time.Millisecond, true
	case "Second":
		return time.Second, true
	case "Minute":
		return time.Minute, true
	case "Hour":
		return time.Hour, true
	}
	return 0, false
}

// exprString renders an argument for the error message.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.BinaryExpr:
		return exprString(v.X) + " " + v.Op.String() + " " + exprString(v.Y)
	case *ast.ParenExpr:
		return "(" + exprString(v.X) + ")"
	}
	return "<expr>"
}
