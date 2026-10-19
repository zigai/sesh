package internal_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const nativeModule = "github.com/zigai/sesh/"

const (
	layerBase layer = iota
	layerModel
	layerStorage
	layerAdapter
	layerConfig
	layerDomain
	layerContract
	layerService
	layerComposition
	layerSurface
)

const testSupportReason = "production must not depend on test support"

var layerNames = []string{"base", "model", "storage", "adapter", "config", "domain", "contract", "service", "composition", "surface"}

var packageLayers = map[string]layer{
	"internal/strictjson": layerBase,
	"internal/privatefs":  layerBase,
	"internal/errorstep":  layerBase,
	"internal/logging":    layerBase,
	"internal/git":        layerBase,
	"internal/display":    layerBase,

	"internal/snapshot/model": layerModel,
	"internal/snapshot/store": layerStorage,
	"internal/integration":    layerAdapter,
	"internal/config":         layerConfig,

	"internal/session":          layerDomain,
	"internal/agents":           layerDomain,
	"internal/project":          layerDomain,
	"internal/task":             layerDomain,
	"internal/work":             layerDomain,
	"internal/inbox":            layerDomain,
	"internal/fields":           layerDomain,
	"internal/automation":       layerDomain,
	"internal/snapshot/apps":    layerDomain,
	"internal/snapshot/capture": layerDomain,
	"internal/snapshot/restore": layerDomain,
	"internal/action":           layerContract,
	"internal/snapshot/engine":  layerService,
	"internal/remote":           layerService,
	"internal/setup":            layerService,
	"internal/doctor":           layerService,
	"internal/daemon":           layerService,
	"internal/tui":              layerService,
	"internal/app":              layerComposition,
	"internal/cli":              layerSurface,
	"cmd/sesh":                  layerSurface,
}

var sameLayerImports = map[packageEdge]string{
	{"cmd/sesh", "internal/cli"}:                     "the binary runs the command tree",
	{"internal/snapshot/capture", "internal/agents"}: "capture records the agent session in each pane",
	{"internal/project", "internal/session"}:         "a project lists the windows of its session",
	{"internal/task", "internal/project"}:            "a task belongs to a project",
	{"internal/task", "internal/snapshot/apps"}:      "a task window launches its harness through a placeholder",
	{"internal/work", "internal/task"}:               "a conversation shows its task's pull request state",
	{"internal/work", "internal/agents"}:             "conversations are recorded from observed agent sessions",
	{"internal/setup", "internal/remote"}:            "tmux bindings read the remote attachment pane option",
	{"internal/doctor", "internal/setup"}:            "doctor checks the services setup writes",
	{"internal/doctor", "internal/snapshot/engine"}:  "doctor reports restore state",
}

var moduleLayers = map[string]layer{
	"github.com/zigai/gotmux": layerAdapter,
}

var testSupport = []string{
	"testing",
	"github.com/zigai/gotmux/tmuxtest",
	"github.com/google/go-cmp",
	"pgregory.net/rapid",
	nativeModule + "internal/testsupport",
	nativeModule + "test",
}

type layer int

type packageEdge struct {
	source, target string
}

type architectureScan struct {
	packages   map[string]int
	violations []string
}

func (l layer) String() string {
	return layerNames[l]
}

func TestProductionImportBoundaries(t *testing.T) {
	scan, err := scanArchitecture(os.DirFS(".."))
	if err != nil {
		t.Fatal(err)
	}

	for root := range packageLayers {
		if !scan.covers(root) {
			t.Errorf("packageLayers entry %s matches no production package", root)
		}
	}

	for edge := range sameLayerImports {
		if !scan.covers(edge.source) || !scan.covers(edge.target) {
			t.Errorf("sameLayerImports entry %s -> %s matches no production package", edge.source, edge.target)
		}
	}

	for _, violation := range scan.violations {
		t.Error(violation)
	}
}

func scanArchitecture(source fs.FS) (architectureScan, error) {
	scan := architectureScan{packages: make(map[string]int)}

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

		violations, err := checkArchitectureFile(source, filename)
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

	for _, pkg := range slices.Sorted(maps.Keys(scan.packages)) {
		if _, ok := layerOf(pkg); !ok {
			scan.violations = append(scan.violations, pkg+": package has no architecture layer in packageLayers")
		}
	}

	return scan, nil
}

func (scan architectureScan) covers(root string) bool {
	for pkg := range scan.packages {
		if withinPackage(pkg, root) {
			return true
		}
	}

	return false
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

func checkArchitectureFile(source fs.FS, filename string) ([]string, error) {
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

		for _, reason := range importViolations(path.Dir(filename), imported) {
			violations = append(violations, fmt.Sprintf("%s: import %q: %s", positions.Position(spec.Pos()), imported, reason))
		}
	}

	return violations, nil
}

func importViolations(source, imported string) []string {
	var reasons []string

	if slices.ContainsFunc(testSupport, func(root string) bool { return withinPackage(imported, root) }) {
		reasons = append(reasons, testSupportReason)
	}

	sourceLayer, ok := layerOf(source)
	if !ok {
		return reasons
	}

	for module, lowest := range moduleLayers {
		if withinPackage(imported, module) && sourceLayer < lowest {
			reasons = append(reasons, fmt.Sprintf("%s package %s must not import %s, which starts at the %s layer", sourceLayer, source, module, lowest))
		}
	}

	target, native := strings.CutPrefix(imported, nativeModule)
	if !native {
		return reasons
	}

	targetLayer, ok := layerOf(target)
	if !ok || layerImportAllowed(source, sourceLayer, target, targetLayer) {
		return reasons
	}

	reason := fmt.Sprintf("%s package %s must not import %s package %s", sourceLayer, source, targetLayer, target)
	if targetLayer == sourceLayer {
		reason += "; list the edge in sameLayerImports if it is intended"
	}

	return append(reasons, reason)
}

func layerImportAllowed(source string, sourceLayer layer, target string, targetLayer layer) bool {
	if targetLayer != sourceLayer {
		return targetLayer < sourceLayer
	}

	if directoryTree(source) == directoryTree(target) {
		return true
	}

	_, listed := sameLayerImports[packageEdge{source, target}]

	return listed
}

func layerOf(pkg string) (layer, bool) {
	var (
		found    layer
		matched  string
		assigned bool
	)

	for root, rootLayer := range packageLayers {
		if withinPackage(pkg, root) && len(root) > len(matched) {
			found, matched, assigned = rootLayer, root, true
		}
	}

	return found, assigned
}

func directoryTree(pkg string) string {
	first, rest, _ := strings.Cut(pkg, "/")
	second, _, _ := strings.Cut(rest, "/")

	return first + "/" + second
}

func withinPackage(candidate, root string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func TestImportBoundariesRejectForbiddenDependencies(t *testing.T) {
	tests := []struct {
		name, filename, imported, reason string
	}{
		{"entrypoint testing", "cmd/sesh/main.go", "testing", testSupportReason},
		{"testing subpackage", "internal/cli/command.go", "testing/fstest", testSupportReason},
		{"published fixtures", "internal/session/session.go", "github.com/zigai/gotmux/tmuxtest", testSupportReason},
		{"property testing", "internal/task/record.go", "pgregory.net/rapid", testSupportReason},
		{"native fixtures", "internal/cli/command.go", nativeModule + "internal/testsupport/process", testSupportReason},
		{"integration fixtures", "internal/cli/command.go", nativeModule + "test/integration/fixture", testSupportReason},
		{"model to store", "internal/snapshot/model/codec.go", nativeModule + "internal/snapshot/store", "must not import storage package internal/snapshot/store"},
		{"model to gotmux", "internal/snapshot/model/codec.go", "github.com/zigai/gotmux/tmux", "must not import github.com/zigai/gotmux"},
		{"store to gotmux", "internal/snapshot/store/read.go", "github.com/zigai/gotmux/tmux", "must not import github.com/zigai/gotmux"},
		{"base to gotmux", "internal/privatefs/write.go", "github.com/zigai/gotmux/tmux", "must not import github.com/zigai/gotmux"},
		{"store to capture", "internal/snapshot/store/read.go", nativeModule + "internal/snapshot/capture", "must not import domain package internal/snapshot/capture"},
		{"store to process", "internal/snapshot/store/read.go", nativeModule + "internal/integration/process", "must not import adapter package internal/integration/process"},
		{"adapter to config", "internal/integration/tmuxserver/server.go", nativeModule + "internal/config", "must not import config package internal/config"},
		{"linux to cli", "internal/integration/process/read_linux.go", nativeModule + "internal/cli", "must not import surface package internal/cli"},
		{"darwin to tui", "internal/integration/process/read_darwin.go", nativeModule + "internal/tui/whichkey", "must not import service package internal/tui/whichkey"},
		{"windows to session", "internal/integration/process/read_windows.go", nativeModule + "internal/session", "must not import domain package internal/session"},
		{"adapter to engine", "internal/integration/tmuxserver/target.go", nativeModule + "internal/snapshot/engine", "must not import service package internal/snapshot/engine"},
		{"generated production", "internal/integration/tmuxserver/generated.go", nativeModule + "internal/snapshot/restore", "must not import domain package internal/snapshot/restore"},
		{"action to app", "internal/action/session.go", nativeModule + "internal/app", "must not import composition package internal/app"},
		{"action to engine", "internal/action/snapshot.go", nativeModule + "internal/snapshot/engine", "must not import service package internal/snapshot/engine"},
		{"service to app", "internal/tui/model.go", nativeModule + "internal/app", "must not import composition package internal/app"},
		{"domain to surface", "internal/task/record.go", nativeModule + "internal/cli", "must not import surface package internal/cli"},
		{"unlisted domain peer", "internal/work/store.go", nativeModule + "internal/session", "list the edge in sameLayerImports"},
		{"reversed listed peer", "internal/task/record.go", nativeModule + "internal/work", "list the edge in sameLayerImports"},
		{"unlisted service peer", "internal/remote/peer.go", nativeModule + "internal/tui", "list the edge in sameLayerImports"},
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

func TestImportBoundariesRejectPackageWithoutLayer(t *testing.T) {
	scan, err := scanArchitecture(fstest.MapFS{"internal/newarea/newarea.go": {Data: []byte("package newarea\nimport \"github.com/zigai/sesh/internal/strictjson\"")}})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"internal/newarea: package has no architecture layer in packageLayers"}
	if !slices.Equal(scan.violations, want) {
		t.Fatalf("violations = %q, want %q", scan.violations, want)
	}
}

func TestImportBoundariesAllowDomainDependenciesAndTestFixtures(t *testing.T) {
	source := fstest.MapFS{
		"cmd/sesh/main.go":                                 {Data: []byte("package main\nimport `github.com/zigai/sesh/internal/cli`")},
		"internal/snapshot/model/codec.go":                 {Data: []byte("package model\nimport \"github.com/zigai/sesh/internal/strictjson\"")},
		"internal/snapshot/store/repository.go":            {Data: []byte("package store\nimport (\"github.com/zigai/sesh/internal/snapshot/model\"; \"github.com/zigai/sesh/internal/privatefs\")")},
		"internal/integration/tmuxserver/target_darwin.go": {Data: []byte("package tmuxserver\nimport (\"github.com/zigai/sesh/internal/snapshot/model\"; \"github.com/zigai/sesh/internal/integration/process\"; \"github.com/zigai/gotmux/tmux\")")},
		"internal/snapshot/capture/capture.go":             {Data: []byte("package capture\nimport (\"github.com/zigai/sesh/internal/agents\"; \"github.com/zigai/sesh/internal/snapshot/apps\")")},
		"internal/tui/taskpicker/model.go":                 {Data: []byte("package taskpicker\nimport (\"github.com/zigai/sesh/internal/tui\"; \"github.com/zigai/sesh/internal/task\")")},
		"internal/cli/command.go":                          {Data: []byte("package cli\nimport (\"example.com/testing/client\"; \"github.com/zigai/sesh/internal/testsupporting\"; \"github.com/zigai/sesh/internal/app\")")},
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
