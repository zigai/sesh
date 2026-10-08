package internal_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const nativeModule = "github.com/zigai/sesh/"

type importBoundary struct {
	sources   []string
	forbidden []string
	allowed   []string
	reason    string
}

type architectureScan struct {
	packages   map[string]int
	violations []string
}

func TestProductionImportBoundaries(t *testing.T) {
	scan, err := scanArchitecture(os.DirFS(".."))
	if err != nil {
		t.Fatal(err)
	}

	for _, pkg := range []string{"cmd/sesh", "internal/snapshot/model", "internal/snapshot/store", "internal/integration/tmux", "internal/integration/process", "internal/integration/lifecycle"} {
		if scan.packages[pkg] == 0 {
			t.Errorf("architecture scan found no production source in %s", pkg)
		}
	}

	for _, violation := range scan.violations {
		t.Error(violation)
	}
}

func architectureBoundaries() []importBoundary {
	core := []string{"internal/snapshot/model", "internal/snapshot/store", "internal/integration"}

	workflows := []string{"agents", "app", "automation", "cli", "config", "daemon", "doctor", "remote", "session", "setup", "tui"}
	for index := range workflows {
		workflows[index] = nativeModule + "internal/" + workflows[index]
	}

	services := []string{"internal/agents", "internal/automation", "internal/config", "internal/daemon", "internal/doctor", "internal/remote", "internal/session", "internal/setup", "internal/tui"}

	return []importBoundary{
		{
			sources: services, forbidden: []string{nativeModule + "internal/app"},
			reason: "services are built from the workspace by the command line, not by each other",
		},
		{
			forbidden: []string{"testing", "github.com/zigai/gotmux/tmuxtest", nativeModule + "internal/testsupport", nativeModule + "test"},
			reason:    "production must not depend on test support",
		},
		{
			sources: core, forbidden: workflows,
			reason: "snapshot data, storage and integration adapters must not depend on application workflows",
		},
		{
			sources:   []string{"internal/snapshot/model"},
			forbidden: []string{nativeModule + "internal/snapshot", nativeModule + "internal/integration", "github.com/zigai/gotmux"},
			allowed:   []string{nativeModule + "internal/snapshot/model"},
			reason:    "snapshot values must not depend on persistence or runtime adapters",
		},
		{
			sources:   []string{"internal/snapshot/store"},
			forbidden: []string{nativeModule + "internal/snapshot", nativeModule + "internal/integration", "github.com/zigai/gotmux"},
			allowed:   []string{nativeModule + "internal/snapshot/model", nativeModule + "internal/snapshot/store"},
			reason:    "snapshot storage must not depend on capture, restore or runtime adapters",
		},
		{
			sources:   []string{"internal/integration"},
			forbidden: []string{nativeModule + "internal/snapshot"},
			allowed:   []string{nativeModule + "internal/snapshot/model"},
			reason:    "integration adapters may use snapshot values but not snapshot orchestration",
		},
	}
}

func scanArchitecture(source fs.FS) (architectureScan, error) {
	scan := architectureScan{packages: make(map[string]int)}
	rules := architectureBoundaries()

	err := fs.WalkDir(source, ".", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if skipArchitectureDirectory(filename) {
				return fs.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
			return nil
		}

		violations, err := checkArchitectureFile(source, filename, rules)
		if err != nil {
			return err
		}

		scan.packages[path.Dir(filename)]++
		scan.violations = append(scan.violations, violations...)

		return nil
	})
	if err != nil {
		return scan, fmt.Errorf("scan production imports: %w", err)
	}

	return scan, nil
}

func skipArchitectureDirectory(filename string) bool {
	if filename == "." {
		return false
	}

	if filename == "test" || filename == "dist" || filename == "internal/testsupport" {
		return true
	}

	name := path.Base(filename)

	return strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor"
}

func checkArchitectureFile(source fs.FS, filename string, rules []importBoundary) ([]string, error) {
	data, err := fs.ReadFile(source, filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	positions := token.NewFileSet()

	file, err := parser.ParseFile(positions, filename, data, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	var violations []string

	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("decode import in %s: %w", filename, err)
		}

		for _, rule := range rules {
			if rule.rejects(path.Dir(filename), imported) {
				violations = append(violations, fmt.Sprintf("%s: import %q violates %s", positions.Position(spec.Pos()), imported, rule.reason))
			}
		}
	}

	return violations, nil
}

func (rule importBoundary) rejects(source, imported string) bool {
	return (len(rule.sources) == 0 || withinAnyPackage(source, rule.sources)) &&
		withinAnyPackage(imported, rule.forbidden) && !withinAnyPackage(imported, rule.allowed)
}

func withinAnyPackage(candidate string, roots []string) bool {
	return slices.ContainsFunc(roots, func(root string) bool {
		return candidate == root || strings.HasPrefix(candidate, root+"/")
	})
}

func TestImportBoundariesRejectForbiddenDependencies(t *testing.T) {
	tests := []struct {
		name, filename, imported, reason string
	}{
		{"entrypoint testing", "cmd/sesh/main.go", "testing", "test support"},
		{"testing subpackage", "internal/cli/command.go", "testing/fstest", "test support"},
		{"published fixtures", "internal/session/session.go", "github.com/zigai/gotmux/tmuxtest", "test support"},
		{"native fixtures", "internal/cli/command.go", nativeModule + "internal/testsupport/process", "test support"},
		{"integration fixtures", "internal/cli/command.go", nativeModule + "test/integration/fixture", "test support"},
		{"model to store", "internal/snapshot/model/codec.go", nativeModule + "internal/snapshot/store", "snapshot values"},
		{"model to gotmux", "internal/snapshot/model/codec.go", "github.com/zigai/gotmux/tmux", "snapshot values"},
		{"store to capture", "internal/snapshot/store/read.go", nativeModule + "internal/snapshot/capture", "snapshot storage"},
		{"store to process", "internal/snapshot/store/read.go", nativeModule + "internal/integration/process", "snapshot storage"},
		{"linux to cli", "internal/integration/process/read_linux.go", nativeModule + "internal/cli", "application workflows"},
		{"darwin to tui", "internal/integration/process/read_darwin.go", nativeModule + "internal/tui/whichkey", "application workflows"},
		{"windows to session", "internal/integration/process/read_windows.go", nativeModule + "internal/session", "application workflows"},
		{"adapter to engine", "internal/integration/tmux/target.go", nativeModule + "internal/snapshot/engine", "snapshot orchestration"},
		{"generated production", "internal/integration/tmux/generated.go", nativeModule + "internal/snapshot/restore", "snapshot orchestration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := "//go:build architecture_fixture\n\n// Code generated by fixture. DO NOT EDIT.\npackage fixture\nimport alias " + strconv.Quote(test.imported) + "\n"

			scan, err := scanArchitecture(fstest.MapFS{test.filename: &fstest.MapFile{Data: []byte(source)}})
			if err != nil {
				t.Fatal(err)
			}

			if len(scan.violations) != 1 {
				t.Fatalf("violations = %v, want one forbidden dependency", scan.violations)
			}

			for _, detail := range []string{test.filename + ":", strconv.Quote(test.imported), test.reason} {
				if !strings.Contains(scan.violations[0], detail) {
					t.Errorf("diagnostic %q omits %q", scan.violations[0], detail)
				}
			}
		})
	}
}

func TestImportBoundariesAllowDomainDependenciesAndTestFixtures(t *testing.T) {
	source := fstest.MapFS{
		"cmd/sesh/main.go":                           {Data: []byte("package main\nimport `github.com/zigai/sesh/internal/cli`")},
		"internal/snapshot/model/codec.go":           {Data: []byte("package model\nimport \"github.com/zigai/sesh/internal/strictjson\"")},
		"internal/snapshot/store/repository.go":      {Data: []byte("package store\nimport (\"github.com/zigai/sesh/internal/snapshot/model\"; \"github.com/zigai/sesh/internal/privatefs\")")},
		"internal/integration/tmux/target_darwin.go": {Data: []byte("package tmux\nimport (\"github.com/zigai/sesh/internal/snapshot/model\"; \"github.com/zigai/sesh/internal/integration/process\"; \"github.com/zigai/gotmux/tmux\")")},
		"internal/snapshot/capture/capture.go":       {Data: []byte("package capture\nimport \"github.com/zigai/sesh/internal/agents\"")},
		"internal/cli/command.go":                    {Data: []byte("package cli\nimport (\"example.com/testing/client\"; \"github.com/zigai/sesh/internal/testsupporting\")")},
	}
	for _, filename := range []string{"internal/cli/command_test.go", "internal/cli/testdata/bad.go", "internal/testsupport/fixture.go", "test/integration/fixture/main.go", "vendor/dependency/main.go", ".agents/reference/main.go", "dist/source/main.go"} {
		source[filename] = &fstest.MapFile{Data: []byte("not Go source; fixture must not be parsed")}
	}

	scan, err := scanArchitecture(source)
	if err != nil {
		t.Fatal(err)
	}

	if len(scan.violations) != 0 {
		t.Fatalf("legitimate dependencies were rejected: %v", scan.violations)
	}
}

func TestImportScanReportsMalformedProductionSource(t *testing.T) {
	_, err := scanArchitecture(fstest.MapFS{"internal/snapshot/model/broken.go": {Data: []byte("not Go source")}})
	if err == nil || !strings.Contains(err.Error(), "internal/snapshot/model/broken.go") {
		t.Fatalf("parse error = %v, want the malformed production filename", err)
	}
}
