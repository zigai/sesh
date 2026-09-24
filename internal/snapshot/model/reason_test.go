package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestEveryReasonHasProductionProducerOrValidator(t *testing.T) {
	modelDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	snapshotDir := filepath.Dir(modelDir)
	reasons := declaredReasons(t, filepath.Join(modelDir, "reason.go"))

	if len(reasons) != 25 {
		t.Fatalf("reason constants = %d, want 25", len(reasons))
	}

	paths := []string{
		filepath.Join(modelDir, "validate.go"),
		filepath.Join(modelDir, "settings.go"),
		filepath.Join(snapshotDir, "capture", "observe.go"),
		filepath.Join(snapshotDir, "capture", "graph.go"),
		filepath.Join(snapshotDir, "capture", "applications.go"),
		filepath.Join(snapshotDir, "restore", "plan.go"),
		filepath.Join(snapshotDir, "restore", "execute.go"),
		filepath.Join(snapshotDir, "restore", "scoped.go"),
	}

	for _, path := range paths {
		markReasonUses(t, path, reasons)
	}

	for name, used := range reasons {
		if !used {
			t.Errorf("%s has no production producer or validator", name)
		}
	}
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
