// Copyright (c) the go-crdt/crdt authors
//
// SPDX-License-Identifier: BSD-3-Clause

package crdt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// doc.go says, under Determinism, that this package "never reads the wall clock
// and never draws random numbers, so the same Doc compiled to js/wasm behaves
// exactly as it does on a server".
//
// That was true and nothing held it. It is not a property a test can pin by
// exercising the code, because what it forbids is an absence: a `time.Now()`
// added to a new file tomorrow breaks it, every test still passes, and the
// sentence above quietly becomes false. The convergence suite would not notice
// either — two replicas that both read the clock can still agree with each
// other, and only disagree with a replay of themselves.
//
// So this reads the source instead. It is the one kind of claim where that is
// the only honest check.
func TestTheSourceNeverReadsTheClockOrDrawsRandomNumbers(t *testing.T) {
	// time.Duration and time.Time as types are fine and deterministic; it is
	// reading the clock that is not. The rule is written to match the sentence
	// rather than to be easy, so that a legitimate duration field does not one
	// day force somebody to weaken it.
	forbiddenCalls := map[string][]string{
		"time": {"Now", "Since", "Until"},
	}
	forbiddenImports := map[string]string{
		"math/rand":    "draws random numbers",
		"math/rand/v2": "draws random numbers",
		"crypto/rand":  "draws random numbers",
	}

	fset := token.NewFileSet()
	var found []string
	walked := 0

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", ".git":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		walked++
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		// The local name matters: an import may be renamed, and a rule that
		// only knows the path would miss `clock "time"`.
		localOf := map[string]string{}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if why, bad := forbiddenImports[p]; bad {
				found = append(found, fset.Position(imp.Pos()).String()+": imports "+p+", which "+why)
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			localOf[name] = p
		}

		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			for _, fn := range forbiddenCalls[localOf[pkg.Name]] {
				if sel.Sel.Name == fn {
					found = append(found, fset.Position(sel.Pos()).String()+
						": calls "+localOf[pkg.Name]+"."+fn+", which reads the wall clock")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}

	// A check that read nothing would pass for the wrong reason, which is the
	// failure this repository keeps finding elsewhere.
	if walked < 20 {
		t.Fatalf("only %d non-test Go files were read; this check is not looking where it thinks", walked)
	}
	for _, f := range found {
		t.Errorf("doc.go claims this package never reads the wall clock and never draws random numbers:\n  %s", f)
	}
	t.Logf("%d non-test Go files read, none reads the clock or draws random numbers", walked)
}
