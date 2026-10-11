//go:build linux || darwin

package snapshot

import (
	"context"
	"errors"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/tui/snapshotpicker"
)

var (
	errSnapshotPickerJSONConflict       = errors.New("--pick cannot be combined with --json")
	errSnapshotPickerGenerationConflict = errors.New("--pick cannot be combined with --generation")
	errSnapshotPickerOperationConflict  = errors.New("--pick cannot be combined with --operation")
	errSnapshotPickerConnectionConflict = errors.New("--pick selects generations on this machine and cannot be combined with --connection")
	errSnapshotPickerTerminalRequired   = errors.New("--pick requires an interactive terminal")
)

type fileDescriptor interface {
	Fd() uintptr
}

func restorePickerEnabled(cmd *cobra.Command) bool {
	return cmd != nil && getBoolFlag(cmd, "pick")
}

func validateSnapshotRestorePicker(cmd *cobra.Command) error {
	if !restorePickerEnabled(cmd) {
		return nil
	}

	for _, conflict := range []struct {
		flag string
		err  error
	}{
		{flag: clibase.JSONFlagName, err: errSnapshotPickerJSONConflict},
		{flag: "generation", err: errSnapshotPickerGenerationConflict},
		{flag: "operation", err: errSnapshotPickerOperationConflict},
		{flag: actioncli.ConnectionFlag, err: errSnapshotPickerConnectionConflict},
	} {
		if cmd.Flags().Changed(conflict.flag) {
			return clibase.NewExitError(clibase.ExitCodeUsage, conflict.err)
		}
	}

	if !cmdpicker.HasTerminal(cmd) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errSnapshotPickerTerminalRequired)
	}

	return nil
}

func selectSnapshotRestoreGeneration(
	ctx context.Context,
	cmd *cobra.Command,
	repositoryPath string,
) (string, bool, error) {
	cfg, err := clibase.LoadCommandConfig(ctx, cmd)
	if err != nil {
		return "", false, err
	}

	repository, err := store.OpenExisting(ctx, repositoryPath)
	if err != nil {
		return "", false, oops.In("cli").With("operation", "open_snapshot_picker_repository").Wrap(err)
	}

	entries, loadErr := loadSnapshotPickerEntries(ctx, repository)

	closeErr := repository.Close()
	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_snapshot_picker_repository").Wrap(closeErr)
	}

	if err := errors.Join(loadErr, closeErr); err != nil {
		return "", false, err
	}

	model, err := snapshotpicker.NewModel(cfg, entries)
	if err != nil {
		return "", false, oops.In("cli").With("operation", "construct_snapshot_picker").Wrap(err)
	}

	final, err := runSnapshotPickerProgram(ctx, cmd, model)
	if err != nil {
		return "", false, err
	}

	generation := final.SelectedID()

	return generation, generation != "", nil
}

func loadSnapshotPickerEntries(ctx context.Context, repository *store.Repository) ([]snapshotpicker.Entry, error) {
	catalog, err := repository.Inspect(ctx)
	if err != nil {
		return nil, oops.In("cli").With("operation", "inspect_snapshot_picker_repository").Wrap(err)
	}

	usage, err := repository.Usage(ctx)
	if err != nil {
		return nil, oops.In("cli").With("operation", "verify_snapshot_picker_usage").Wrap(err)
	}

	generations := slices.Clone(catalog.Generations)
	slices.SortFunc(generations, func(left, right store.Generation) int {
		switch {
		case left.Sequence > right.Sequence:
			return -1
		case left.Sequence < right.Sequence:
			return 1
		default:
			return 0
		}
	})

	entries := make([]snapshotpicker.Entry, 0, len(generations))
	for _, generation := range generations {
		manifest, readErr := repository.Read(ctx, generation.ID)
		if readErr != nil {
			return nil, oops.In("cli").With("operation", "read_snapshot_picker_generation").Wrap(readErr)
		}

		entries = append(entries, pickerEntry(
			generation,
			manifest.Finished,
			len(manifest.Sessions),
			len(manifest.Windows),
			len(manifest.Panes),
			usage.GenerationBytes[generation.ID],
			catalog.Current,
		))
	}

	return entries, nil
}

func pickerEntry(
	generation store.Generation,
	finished int64,
	sessions, windows, panes int,
	size int64,
	current string,
) snapshotpicker.Entry {
	var (
		validity = snapshotpicker.SnapshotValid
		warnings []string
	)

	if !generation.Complete {
		validity = snapshotpicker.SnapshotPartial
		warnings = []string{"parity evidence is incomplete; restore will report a partial result"}
	}

	return snapshotpicker.Entry{
		ID:           generation.ID,
		Path:         generation.ID,
		SavedAt:      time.Unix(0, finished),
		SizeBytes:    size,
		SessionCount: sessions,
		WindowCount:  windows,
		PaneCount:    panes,
		Validity:     validity,
		Latest:       generation.ID == current,
		Warnings:     warnings,
	}
}

func runSnapshotPickerProgram(
	ctx context.Context,
	cmd *cobra.Command,
	model *snapshotpicker.Model,
) (*snapshotpicker.Model, error) {
	options := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(cmdpicker.ProgramOutput(cmd.OutOrStdout())),
		tea.WithoutSignalHandler(),
	}
	if output, ok := cmd.OutOrStdout().(fileDescriptor); ok {
		if width, height, sizeErr := term.GetSize(output.Fd()); sizeErr == nil {
			options = append(options, tea.WithWindowSize(width, height))
		}
	}

	final, err := tea.NewProgram(model, options...).Run()
	if err != nil {
		return model, oops.In("cli").With("operation", "execute_snapshot_picker").Wrap(err)
	}

	typed, ok := final.(*snapshotpicker.Model)
	if !ok {
		return model, nil
	}

	return typed, nil
}
