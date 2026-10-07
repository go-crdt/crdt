package crdt

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// binary.Uvarint and binary.Varint return a COUNT that is zero when the buffer
// is too short and NEGATIVE when the encoding overflows sixty-four bits. The
// negative is the dangerous one: the next line is usually buf[n:], and a
// negative index panics rather than erring.
//
// Measured, on structured.decodePoint with its first check deleted: twelve
// bytes of continuation bits give "slice bounds out of range [-11:]". A peer's
// ink operation carries those bytes, so that is a remote panic rather than a
// refused message.
//
// Every one of these in this module checks its count today -- twenty of them,
// counted by this test. The rule is cheap to keep and expensive to notice
// having lost, so it is written down here rather than left to review.
//
// The check does not have to be the next LINE: frame.uvarint in the sibling
// repository puts a four-line comment between the call and the if, which is
// why this walks the syntax tree instead of a window of lines.
func TestEveryVarintCountIsCheckedBeforeItIsUsed(t *testing.T) {
	var decodes, checked int
	var unchecked []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			// A statement list is not always a BlockStmt: the body of a case in
			// a switch, and of a comm clause in a select, are bare []ast.Stmt.
			// Two of the twenty decodes here live in a case, and an earlier
			// version of this test walked only blocks and never saw them --
			// they were correct, and that is not the point: the instrument was
			// blind where it claimed to look.
			var list []ast.Stmt
			switch v := n.(type) {
			case *ast.BlockStmt:
				list = v.List
			case *ast.CaseClause:
				list = v.Body
			case *ast.CommClause:
				list = v.Body
			default:
				return true
			}
			for i, stmt := range list {
				name, isDecode := varintCount(stmt)
				if !isDecode {
					continue
				}
				decodes++
				// The next STATEMENT must be an `if` whose CONDITION tests the
				// count. "Mentions it somewhere" is not enough and was the
				// first version of this: deleting the guard leaves
				// `rest = rest[used:]` as the next statement, which mentions
				// the count and would have satisfied it -- the test passed over
				// exactly the shape it exists to refuse. Both break-checks
				// returned green before this was fixed.
				if i+1 < len(list) && tests(list[i+1], name) {
					checked++
					continue
				}
				unchecked = append(unchecked,
					fmt.Sprintf("%s:%d", path, fset.Position(stmt.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A scan that read nothing reports nothing wrong.
	if decodes < 15 {
		t.Fatalf("found %d varint decodes, which is too few to be this module: "+
			"the scan is broken, not the source", decodes)
	}
	t.Logf("%d varint decodes, %d followed immediately by a test of the count", decodes, checked)
	for _, where := range unchecked {
		t.Errorf("%s decodes a varint and does not test the count in the next statement: "+
			"the count is NEGATIVE on an overflowing encoding, and the usual next move "+
			"is buf[n:], which panics rather than erring", where)
	}
}

// varintCount reports the name a binary.Uvarint or binary.Varint call assigns
// its count to.
func varintCount(stmt ast.Stmt) (string, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
		return "", false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Uvarint" && sel.Sel.Name != "Varint") {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "binary" {
		return "", false
	}
	count, ok := assign.Lhs[1].(*ast.Ident)
	if !ok {
		return "", false
	}
	return count.Name, true
}

// tests reports whether a statement is an `if` whose condition names the
// identifier. Anything else -- including a use of it as an index -- is not a
// test of it.
func tests(stmt ast.Stmt, name string) bool {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(ifs.Cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}
