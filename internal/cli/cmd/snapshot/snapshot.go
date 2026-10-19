package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	snapshotCmd       = "snapshot"
	colWidthID        = 8
	colWidthState     = 10
	colWidthDigest    = 14
	colWidthSession   = 16
	colWidthDirectory = 20
)

type ListResultJSON struct {
	Generation string        `json:"generation"`
	Sessions   []sessionJSON `json:"sessions"`
}

type sessionJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Windows   int    `json:"windows"`
}

type HistoryResultJSON struct {
	Current     string           `json:"current"`
	Attempts    []attemptJSON    `json:"attempts"`
	Generations []generationJSON `json:"generations"`
}

type attemptJSON struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type generationJSON struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	Complete       bool   `json:"complete"`
	ManifestSHA256 string `json:"manifestSha256"`
	Current        bool   `json:"current"`
}

type VerifyResultJSON struct {
	Generation             string         `json:"generation"`
	ByteIntegrity          bool           `json:"byteIntegrity"`
	SourceEvidenceComplete bool           `json:"sourceEvidenceComplete"`
	RestoreReady           bool           `json:"restoreReady"`
	RestoreReason          string         `json:"restoreReason"`
	Consistency            string         `json:"consistency"`
	Sessions               int            `json:"sessions"`
	Windows                int            `json:"windows"`
	Panes                  int            `json:"panes"`
	Artifacts              int            `json:"artifacts"`
	Evidence               []evidenceJSON `json:"evidence"`
}

type evidenceJSON struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func NewCommand() *cobra.Command {
	return newSnapshotCommand()
}

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage native tmux session snapshots",
		Long:  "Manage native tmux session snapshots.\n\nSnapshots support practical session reconstruction and best-effort application recovery while keeping exact fidelity limits visible.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		newSnapshotSaveCommand(),
		newSnapshotRestoreCommand(),
		newSnapshotListCommand(),
		newSnapshotHistoryCommand(),
		newSnapshotVerifyCommand(),
		newSnapshotPruneCommand(),
		newSnapshotGCCommand(),
		newSnapshotRecoverCommand(),
	)

	return cmd
}

func newSnapshotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions saved in the native repository",
		Long:  "List sessions saved in the native repository.\n\nPractical recovery generations remain observational and retain their exact fidelity limits.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runSnapshotList,
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("generation", "", "Specific generation ID to inspect")
	cmd.Flags().Bool("full", false, "Show complete values using an adaptive layout")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func loadListManifest(ctx context.Context, repo *store.Repository, genID string) (model.Manifest, string, error) {
	resolved, err := practicalGeneration(ctx, repo, genID)
	if err != nil {
		return model.Manifest{}, "", err
	}

	m, err := repo.Read(ctx, resolved)
	if err != nil {
		return model.Manifest{}, "", oops.In("cli").With("operation", "read_generation").Wrap(err)
	}

	return m, resolved, nil
}

func practicalGeneration(ctx context.Context, repo *store.Repository, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}

	generation, _, err := repo.ReadPracticalCurrent(ctx)
	if err != nil {
		return "", oops.In("cli").With("operation", "read_current").Wrap(err)
	}

	return generation.ID, nil
}

func renderListJSON(cmd *cobra.Command, manifest model.Manifest, genID string, windowCount map[string]int) error {
	items := make([]sessionJSON, 0, len(manifest.Sessions))
	for _, s := range manifest.Sessions {
		items = append(items, sessionJSON{
			ID:        s.ID,
			Name:      string(s.Name),
			Directory: string(s.Directory),
			Windows:   windowCount[s.ID],
		})
	}

	return clibase.WriteJSONResult(cmd, ListResultJSON{
		Generation: genID,
		Sessions:   items,
	})
}

func renderListTable(cmd *cobra.Command, manifest model.Manifest, windowCount map[string]int, full bool) error {
	if len(manifest.Sessions) == 0 {
		return clibase.WriteLine(cmd, "No saved sessions found.")
	}

	rows := make([]prettytable.Row, 0, len(manifest.Sessions))
	for _, s := range manifest.Sessions {
		rows = append(rows, prettytable.Row{
			string(s.Name),
			s.ID,
			windowCount[s.ID],
			string(s.Directory),
		})
	}

	columns := []clibase.TableColumn{
		{Heading: "Name", Fixed: false, MinWidth: colWidthSession, Wrap: clibase.WrapSession},
		{Heading: "ID", Fixed: false, MinWidth: colWidthID, Wrap: nil},
		{Heading: "Windows", Fixed: false, MinWidth: colWidthID, Wrap: nil},
		{Heading: "Directory", Fixed: false, MinWidth: colWidthDirectory, Wrap: clibase.WrapIdentifier},
	}

	return clibase.WriteResponsiveTable(cmd, columns, rows, full)
}

func runSnapshotList(cmd *cobra.Command, _ []string) error {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo, err := workspace.OpenExistingRepository(ctx)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	genID, _ := cmd.Flags().GetString("generation")
	manifest, generationID, readErr := loadListManifest(ctx, repo, genID)

	closeErr := repo.Close()
	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_repository").Wrap(closeErr)
	}

	if combinedErr := errors.Join(readErr, closeErr); combinedErr != nil {
		if errors.Is(combinedErr, store.ErrUnavailable) && genID == "" {
			if clibase.JSONOutputEnabled(cmd) {
				return clibase.WriteJSONResult(cmd, ListResultJSON{
					Generation: "",
					Sessions:   []sessionJSON{},
				})
			}

			return clibase.WriteLine(cmd, "No saved sessions found.")
		}

		return combinedErr
	}

	windowCount := make(map[string]int)
	for _, link := range manifest.Links {
		windowCount[link.Session]++
	}

	if clibase.JSONOutputEnabled(cmd) {
		return renderListJSON(cmd, manifest, generationID, windowCount)
	}

	full, _ := cmd.Flags().GetBool("full")

	return renderListTable(cmd, manifest, windowCount, full)
}

func newSnapshotHistoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent saves: when each ran, whether it finished and which one restore uses",
		Long:  "Show recent saves: when each ran, whether it finished and which one restore uses.\n\nEvery save records tmux, not an atomic checkpoint; --json reports the raw attempt states.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runSnapshotHistory,
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().Bool("full", false, "Show complete values using an adaptive layout")
	cmd.Flags().Int("limit", defaultHistoryLimit, "Number of the newest save attempts to show")
	cmd.Flags().Bool("all", false, "Show every save attempt")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func renderHistoryJSON(cmd *cobra.Command, catalog store.Catalog) error {
	attempts := make([]attemptJSON, 0, len(catalog.Attempts))
	for _, a := range catalog.Attempts {
		attempts = append(attempts, attemptJSON{
			ID:             a.ID,
			Sequence:       a.Sequence,
			State:          a.State,
			Reason:         a.Reason,
			ManifestSHA256: a.ManifestSHA256,
		})
	}

	generations := make([]generationJSON, 0, len(catalog.Generations))
	for _, g := range catalog.Generations {
		generations = append(generations, generationJSON{
			ID:             g.ID,
			Sequence:       g.Sequence,
			Complete:       g.Complete,
			ManifestSHA256: g.ManifestSHA256,
			Current:        g.ID == catalog.Current,
		})
	}

	return clibase.WriteJSONResult(cmd, HistoryResultJSON{
		Current:     catalog.Current,
		Attempts:    attempts,
		Generations: generations,
	})
}

func runSnapshotHistory(cmd *cobra.Command, _ []string) error {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo, err := workspace.OpenExistingRepository(ctx)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	catalog, inspectErr := repo.Inspect(ctx)
	if inspectErr != nil {
		inspectErr = oops.In("cli").With("operation", "inspect_repository").Wrap(inspectErr)
	}

	var entries []historyEntry
	if inspectErr == nil && !clibase.JSONOutputEnabled(cmd) {
		entries = loadHistoryEntries(ctx, repo, catalog, historyLimit(cmd))
	}

	closeErr := repo.Close()
	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_repository").Wrap(closeErr)
	}

	if combinedErr := errors.Join(inspectErr, closeErr); combinedErr != nil {
		return combinedErr
	}

	if clibase.JSONOutputEnabled(cmd) {
		return renderHistoryJSON(cmd, catalog)
	}

	full, _ := cmd.Flags().GetBool("full")

	return renderHistoryTable(cmd, entries, len(catalog.Attempts), full)
}

func historyLimit(cmd *cobra.Command) int {
	if all, _ := cmd.Flags().GetBool("all"); all {
		return 0
	}

	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		return defaultHistoryLimit
	}

	return limit
}

func newSnapshotVerifyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify snapshot byte integrity and inspect restore readiness capabilities",
		Long:  "Verify snapshot byte integrity and inspect restore readiness capabilities.\n\nPractical readiness means a valid reconstructable graph; runtime preflight and fidelity limits still apply.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runSnapshotVerify,
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("generation", "", "Specific generation ID to verify")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func loadVerifyManifest(ctx context.Context, repo *store.Repository, genID string) (string, model.Manifest, error) {
	resolved, err := practicalGeneration(ctx, repo, genID)
	if err != nil {
		return "", model.Manifest{}, err
	}

	m, err := repo.Read(ctx, resolved)
	if err != nil {
		return resolved, model.Manifest{}, oops.In("cli").With("operation", "read_generation").Wrap(err)
	}

	return resolved, m, nil
}

func renderVerifyJSON(cmd *cobra.Command, targetGen string, manifest model.Manifest) error {
	sourceEvidenceComplete := manifest.Complete()
	practicalReady := manifest.PracticalRecoveryReady()

	evidenceList := make([]evidenceJSON, 0, len(manifest.Evidence))

	for _, e := range manifest.Evidence {
		evidenceList = append(evidenceList, evidenceJSON{
			Domain: e.Domain,
			Status: e.Status,
			Reason: e.Reason,
		})
	}

	return clibase.WriteJSONResult(cmd, VerifyResultJSON{
		Generation:             targetGen,
		ByteIntegrity:          true,
		SourceEvidenceComplete: sourceEvidenceComplete,
		RestoreReady:           practicalReady,
		RestoreReason:          practicalRestoreReason(practicalReady),
		Consistency:            manifest.Source.Consistency,
		Sessions:               len(manifest.Sessions),
		Windows:                len(manifest.Windows),
		Panes:                  len(manifest.Panes),
		Artifacts:              len(manifest.Artifacts),
		Evidence:               evidenceList,
	})
}

func renderVerifyTable(cmd *cobra.Command, targetGen string, manifest model.Manifest) error {
	var out strings.Builder

	fmt.Fprintf(&out, "Generation:               %s\n", targetGen)
	fmt.Fprintf(&out, "Byte Integrity:           valid (manifest and %d artifacts verified)\n", len(manifest.Artifacts))

	if manifest.Complete() {
		fmt.Fprintf(&out, "Source Evidence Complete: yes\n")
	} else {
		fmt.Fprintf(&out, "Source Evidence Complete: no (observational save; consistency: %s)\n", manifest.Source.Consistency)
	}

	if manifest.PracticalRecoveryReady() {
		fmt.Fprintf(&out, "Restore Ready:            yes (best-effort reconstruction; exact terminal and application memory recovery remain limited)\n")
	} else {
		fmt.Fprintf(&out, "Restore Ready:            no (snapshot graph is not reconstructable)\n")
	}

	fmt.Fprintf(&out, "Sessions:                 %d\n", len(manifest.Sessions))
	fmt.Fprintf(&out, "Windows:                  %d\n", len(manifest.Windows))
	fmt.Fprintf(&out, "Panes:                    %d\n", len(manifest.Panes))
	fmt.Fprintf(&out, "Artifacts:                %d\n", len(manifest.Artifacts))

	return clibase.WriteOutput(cmd, "%s", out.String())
}

func practicalRestoreReason(ready bool) string {
	if ready {
		return "practical_reconstruction_available"
	}

	return "snapshot_invalid"
}

func runSnapshotVerify(cmd *cobra.Command, _ []string) error {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo, err := workspace.OpenExistingRepository(ctx)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	genID, _ := cmd.Flags().GetString("generation")
	targetGen, manifest, verifyReadErr := loadVerifyManifest(ctx, repo, genID)

	closeErr := repo.Close()
	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_repository").Wrap(closeErr)
	}

	combinedErr := errors.Join(verifyReadErr, closeErr)
	if combinedErr != nil {
		if errors.Is(combinedErr, store.ErrRecoveryRequired) {
			return combinedErr
		}

		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, VerifyResultJSON{
				Generation:             targetGen,
				ByteIntegrity:          false,
				SourceEvidenceComplete: false,
				RestoreReady:           false,
				RestoreReason:          "runtime_restore_unsupported",
				Consistency:            "",
				Sessions:               0,
				Windows:                0,
				Panes:                  0,
				Artifacts:              0,
				Evidence:               []evidenceJSON{},
			}, combinedErr)
		}

		return combinedErr
	}

	if clibase.JSONOutputEnabled(cmd) {
		return renderVerifyJSON(cmd, targetGen, manifest)
	}

	return renderVerifyTable(cmd, targetGen, manifest)
}

func getBoolFlag(cmd *cobra.Command, name string) bool {
	if cmd == nil || cmd.Flags().Lookup(name) == nil {
		return false
	}

	value, _ := cmd.Flags().GetBool(name)

	return value
}

func commandOperation(cmd *cobra.Command) string {
	if cmd != nil && cmd.Annotations != nil {
		if op := cmd.Annotations[clibase.AnnotationOperation]; op != "" {
			return op
		}
	}

	if cmd != nil {
		return cmd.Name()
	}

	return ""
}
