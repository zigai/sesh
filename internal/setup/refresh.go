package setup

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"
)

const maxDefinitionBytes = 64 * 1024

const (
	definitionFileMode      = 0o644
	definitionDirectoryMode = 0o755
)

const (
	ArtifactMissing ArtifactState = "missing"
	ArtifactCurrent ArtifactState = "current"
	ArtifactStale   ArtifactState = "stale"
	ArtifactForeign ArtifactState = "foreign"
	ArtifactRetired ArtifactState = "retired"
)

var ErrPlanMismatch = errors.New("definition and plan counts differ")

var retirableServiceNames = []string{TmuxServiceName, RestoreServiceName}

type ArtifactState string

type ArtifactPlan struct {
	Name     string
	Path     string
	State    ArtifactState
	Changed  bool
	Requires []string
}

type ApplyResult struct {
	Written      int
	SkippedNames []string
	BlockedNames []string
}

type definitionFile struct {
	Exists  bool
	Owned   bool
	Content string
}

func PlanDefinitions(definitions []ServiceDefinition) ([]ArtifactPlan, error) {
	plans := make([]ArtifactPlan, 0, len(definitions))

	for _, definition := range definitions {
		existing, err := readDefinition(definition.Path)
		if err != nil {
			return nil, oops.In("setup").Code("setup_definition_failed").With("operation", "plan_definitions").Wrap(err)
		}

		plan := ArtifactPlan{
			Name:     definition.Name,
			Path:     definition.Path,
			State:    ArtifactCurrent,
			Changed:  false,
			Requires: definition.Requires,
		}

		switch {
		case !existing.Exists:
			plan.State, plan.Changed = ArtifactMissing, true
		case !existing.Owned:
			plan.State, plan.Changed = ArtifactForeign, false
		case existing.Content != definition.Content:
			plan.State, plan.Changed = ArtifactStale, true
		}

		if len(plan.Requires) > 0 {
			plan.Changed = false
		}

		plans = append(plans, plan)
	}

	return plans, nil
}

// PlanRetiredDefinitions finds the sesh-owned managed tmux units in the definitions' directory that
// the rendered definitions leave out.
func PlanRetiredDefinitions(definitions []ServiceDefinition) ([]ArtifactPlan, error) {
	if len(definitions) == 0 {
		return nil, nil
	}

	directory := filepath.Dir(definitions[0].Path)

	var plans []ArtifactPlan

	for _, name := range retirableServiceNames {
		if slices.ContainsFunc(definitions, func(definition ServiceDefinition) bool { return definition.Name == name }) {
			continue
		}

		path := filepath.Join(directory, name)

		existing, err := readDefinition(path)
		if err != nil {
			return nil, oops.In("setup").Code("setup_definition_failed").With("operation", "plan_retired_definitions").Wrap(err)
		}

		if existing.Exists && existing.Owned {
			plans = append(plans, ArtifactPlan{Name: name, Path: path, State: ArtifactRetired, Changed: true, Requires: nil})
		}
	}

	return plans, nil
}

func ApplyDefinitions(
	definitions []ServiceDefinition,
	plans []ArtifactPlan,
	force bool,
) (ApplyResult, error) {
	if len(definitions) != len(plans) {
		return emptyApplyResult(), oops.In("setup").Code("setup_definition_failed").With("operation", "apply_definitions").Wrap(ErrPlanMismatch)
	}

	result := emptyApplyResult()

	for index, definition := range definitions {
		plan := plans[index]

		if len(plan.Requires) > 0 {
			result.BlockedNames = append(result.BlockedNames, plan.Name)

			continue
		}

		if plan.State == ArtifactForeign && !force {
			result.SkippedNames = append(result.SkippedNames, plan.Name)

			continue
		}

		if !plan.Changed && plan.State != ArtifactForeign {
			continue
		}

		if err := writeDefinition(definition.Path, definition.Content); err != nil {
			return result, oops.In("setup").Code("setup_definition_failed").With("operation", "apply_definitions").Wrap(err)
		}

		result.Written++
	}

	return result, nil
}

func emptyApplyResult() ApplyResult {
	return ApplyResult{
		Written:      0,
		SkippedNames: nil,
		BlockedNames: nil,
	}
}

func readDefinition(path string) (definitionFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyDefinitionFile(), nil
		}

		return emptyDefinitionFile(), oops.In("setup").Code("setup_definition_failed").With("operation", "inspect_definition").Wrap(err)
	}

	if !info.Mode().IsRegular() || info.Size() > maxDefinitionBytes {
		return definitionFile{Exists: true, Owned: false, Content: ""}, nil
	}

	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyDefinitionFile(), nil
		}

		return emptyDefinitionFile(), oops.In("setup").Code("setup_definition_failed").With("operation", "open_definition").Wrap(err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxDefinitionBytes))
	closeErr := file.Close()

	if readErr != nil {
		readFailure := oops.In("setup").Code("setup_definition_failed").With("operation", "read_definition").Wrap(readErr)
		if closeErr != nil {
			closeFailure := oops.In("setup").Code("setup_definition_failed").With("operation", "close_definition").Wrap(closeErr)
			return emptyDefinitionFile(), errors.Join(readFailure, closeFailure)
		}

		return emptyDefinitionFile(), readFailure
	}

	if closeErr != nil {
		return emptyDefinitionFile(), oops.In("setup").Code("setup_definition_failed").With("operation", "close_definition").Wrap(closeErr)
	}

	content := string(data)

	return definitionFile{
		Exists:  true,
		Owned:   strings.Contains(content, ManagedMarker),
		Content: content,
	}, nil
}

func emptyDefinitionFile() definitionFile {
	return definitionFile{Exists: false, Owned: false, Content: ""}
}

func writeDefinition(path, content string) error {
	directory := filepath.Dir(path)

	if err := os.MkdirAll(directory, definitionDirectoryMode); err != nil {
		return oops.In("setup").Code("setup_definition_failed").With("operation", "create_definition_directory").Wrap(err)
	}

	name := filepath.Base(path)
	temp := filepath.Join(directory, "."+name+".tmp."+strconv.Itoa(os.Getpid()))

	if err := writeDefinitionTemp(temp, []byte(content)); err != nil {
		return err
	}

	if err := os.Rename(temp, path); err != nil {
		renameErr := oops.In("setup").Code("setup_definition_failed").With("operation", "publish_definition").Wrap(err)
		return errors.Join(renameErr, removeDefinitionTemp(temp))
	}

	if err := syncDirectory(directory); err != nil {
		return oops.In("setup").Code("setup_definition_failed").With("operation", "sync_definition_directory").Wrap(err)
	}

	return nil
}

func writeDefinitionTemp(temp string, data []byte) error {
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, definitionFileMode)
	if err != nil {
		return oops.In("setup").Code("setup_definition_failed").With("operation", "create_definition").Wrap(err)
	}

	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()

	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		writeFailure := oops.In("setup").Code("setup_definition_failed").With("operation", "write_definition").Wrap(err)
		return errors.Join(writeFailure, removeDefinitionTemp(temp))
	}

	return nil
}

func removeDefinitionTemp(temp string) error {
	if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return oops.In("setup").Code("setup_definition_failed").With("operation", "remove_temp_definition").Wrap(err)
	}

	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return oops.In("setup").Code("setup_definition_failed").With("operation", "open_definition_directory").Wrap(err)
	}

	syncErr := dir.Sync()
	closeErr := dir.Close()

	if err := errors.Join(syncErr, closeErr); err != nil {
		return oops.In("setup").Code("setup_definition_failed").With("operation", "sync_definition_directory").Wrap(err)
	}

	return nil
}
