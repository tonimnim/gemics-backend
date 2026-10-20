package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every constant SQL statement in the API prepares against the migrated schema,
// as pgx prepares it before running it. A statement that reuses a parameter at
// two types, or leaves one untyped, fails here rather than as a 503 on the one
// path that runs it.
func TestIntegrationEveryConstantStatementPrepares(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	statements := constantSQLStatements(t, "../..")
	if len(statements) < 400 {
		t.Fatalf("found %d constant statements; the source walk is broken", len(statements))
	}
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, statement := range statements {
		if _, err := conn.Conn().Prepare(t.Context(), "", statement.sql); err != nil {
			t.Errorf("%s: %v", statement.at, err)
		}
	}
}

type constantSQLStatement struct {
	at  string
	sql string
}

// A route pattern such as "DELETE /v1/me" is not a statement.
var sqlStatementStart = regexp.MustCompile(`^\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\s+[^/\s]`)

// constantSQLStatements returns the SQL statements written as string literals,
// constants, or concatenations of the two, in the non-test sources under root.
// A constant is judged where it is used, since some are shared prefixes.
// Statements assembled at run time are left out.
func constantSQLStatements(t *testing.T, root string) []constantSQLStatement {
	t.Helper()
	fset := token.NewFileSet()
	packages := map[string][]*ast.File{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		packages[filepath.Dir(path)] = append(packages[filepath.Dir(path)], file)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var statements []constantSQLStatement
	seen := map[string]bool{}
	for _, files := range packages {
		consts := map[string]ast.Expr{}
		for _, file := range files {
			for _, decl := range file.Decls {
				if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
					for _, spec := range gen.Specs {
						value := spec.(*ast.ValueSpec)
						for i, name := range value.Names {
							if i < len(value.Values) {
								consts[name.Name] = value.Values[i]
							}
						}
					}
				}
			}
		}
		var text func(ast.Expr) (string, bool)
		text = func(expr ast.Expr) (string, bool) {
			switch e := expr.(type) {
			case *ast.BasicLit:
				if e.Kind == token.STRING {
					value, err := strconv.Unquote(e.Value)
					return value, err == nil
				}
			case *ast.ParenExpr:
				return text(e.X)
			case *ast.Ident:
				if value, ok := consts[e.Name]; ok {
					return text(value)
				}
			case *ast.BinaryExpr:
				if e.Op == token.ADD {
					left, ok := text(e.X)
					right, rightOK := text(e.Y)
					return left + right, ok && rightOK
				}
			}
			return "", false
		}
		// A concatenation is judged whole: its literal pieces are fragments.
		var concatenation func(ast.Expr) bool
		concatenation = func(expr ast.Expr) bool {
			e, ok := expr.(*ast.BinaryExpr)
			if !ok || e.Op != token.ADD {
				return false
			}
			for _, side := range []ast.Expr{e.X, e.Y} {
				lit, literal := side.(*ast.BasicLit)
				ident, named := side.(*ast.Ident)
				if (literal && lit.Kind == token.STRING) || (named && consts[ident.Name] != nil) || concatenation(side) {
					return true
				}
			}
			return false
		}
		for _, file := range files {
			ast.Inspect(file, func(node ast.Node) bool {
				if gen, ok := node.(*ast.GenDecl); ok && gen.Tok == token.CONST {
					return false
				}
				expr, ok := node.(ast.Expr)
				if !ok {
					return true
				}
				_, literal := expr.(*ast.BasicLit)
				ident, named := expr.(*ast.Ident)
				named = named && consts[ident.Name] != nil
				whole := concatenation(expr)
				if !literal && !named && !whole {
					return true
				}
				if sql, ok := text(expr); ok && sqlStatementStart.MatchString(sql) && !seen[sql] {
					seen[sql] = true
					statements = append(statements, constantSQLStatement{fset.Position(expr.Pos()).String(), sql})
				}
				return !whole
			})
		}
	}
	return statements
}
