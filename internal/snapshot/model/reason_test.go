package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryReasonHasProductionProducerOrValidator(t *testing.T) {
	modelDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	snapshotDir := filepath.Dir(modelDir)
	reasonPath := filepath.Join(modelDir, "reason.go")
	reasons := declaredReasons(t, reasonPath)

	if len(reasons) == 0 {
		t.Fatal("no reason constants declared")
	}

	err = filepath.WalkDir(snapshotDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() && entry.Name() == "testdata" {
			return filepath.SkipDir
		}

		if isProductionGoSource(path, entry) && path != reasonPath {
			markReasonUses(t, path, reasons)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, used := range reasons {
		if !used {
			t.Errorf("%s has no production producer or validator", name)
		}
	}
}

func isProductionGoSource(path string, entry fs.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

func declaredReasons(t *testing.T, path string) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	reasons := make(map[string]bool)

	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}

		for _, spec := range group.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				t.Fatalf("unexpected const spec %T", spec)
			}

			typ, ok := value.Type.(*ast.Ident)
			if !ok || typ.Name != "Reason" {
				continue
			}

			for _, name := range value.Names {
				reasons[name.Name] = false
			}
		}
	}

	return reasons
}

func markReasonUses(t *testing.T, path string, reasons map[string]bool) {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	ast.Inspect(file, func(node ast.Node) bool {
		name, ok := node.(*ast.Ident)
		if !ok {
			return true
		}

		if _, known := reasons[name.Name]; known {
			reasons[name.Name] = true
		}

		return true
	})
}
