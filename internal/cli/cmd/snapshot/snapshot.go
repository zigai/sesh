package cmdsnapshot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
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

var (
	ErrPartialSnapshot = clibase.ErrPartialSnapshot
	errPartialSnapshot = clibase.ErrPartialSnapshot
	ErrPartialRestore  = clibase.ErrPartialRestore
	errPartialRestore  = clibase.ErrPartialRestore
)

type SaveBoundaryError = clibase.SaveBoundaryError

type (
	RestoreBoundaryError = clibase.RestoreBoundaryError
	commandResultError   = CommandResultError
	CommandResultError   struct{ error }
)

func (e *CommandResultError) Unwrap() error { return e.error }

type snapshotSaveResultJSON struct {
	Attempt    string `json:"attempt"`
	Generation string `json:"generation"`
	Status     string `json:"status"`
}

type SnapshotListResultJSON struct {
	Generation string                `json:"generation"`
	Sessions   []snapshotSessionJSON `json:"sessions"`
}

type snapshotListResultJSON = SnapshotListResultJSON

type snapshotSessionJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Windows   int    `json:"windows"`
}

type SnapshotHistoryResultJSON struct {
	Current     string                   `json:"current"`
	Attempts    []snapshotAttemptJSON    `json:"attempts"`
	Generations []snapshotGenerationJSON `json:"generations"`
}

type snapshotHistoryResultJSON = SnapshotHistoryResultJSON

type snapshotAttemptJSON struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type snapshotGenerationJSON struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	Complete       bool   `json:"complete"`
	ManifestSHA256 string `json:"manifestSha256"`
	Current        bool   `json:"current"`
}

type snapshotRestoreResultJSON struct {
	OperationID         string                 `json:"operationId"`
	Generation          string                 `json:"generation"`
	Status              string                 `json:"status"`
	Reason              string                 `json:"reason"`
	SessionsRestored    []string               `json:"sessionsRestored"`
	Panes               []restore.PaneResult   `json:"panes,omitempty"`
	Verified            bool                   `json:"verified"`
	Uncertain           bool                   `json:"uncertain"`
	MissingRequirements []snapshotEvidenceJSON `json:"missingRequirements"`
}

type SnapshotVerifyResultJSON struct {
	Generation             string                 `json:"generation"`
	ByteIntegrity          bool                   `json:"byteIntegrity"`
	SourceEvidenceComplete bool                   `json:"sourceEvidenceComplete"`
	RestoreReady           bool                   `json:"restoreReady"`
	RestoreReason          string                 `json:"restoreReason"`
	Consistency            string                 `json:"consistency"`
	Sessions               int                    `json:"sessions"`
	Windows                int                    `json:"windows"`
	Panes                  int                    `json:"panes"`
	Artifacts              int                    `json:"artifacts"`
	Evidence               []snapshotEvidenceJSON `json:"evidence"`
}

type snapshotVerifyResultJSON = SnapshotVerifyResultJSON

type snapshotEvidenceJSON struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func NewCommand() *cobra.Command {
	return newSnapshotCommand()
}

func NewActionSaveCommand() *cobra.Command {
	cmd := newSnapshotSaveCommand()
	cmd.Use = "snapshot-save"

	return cmd
}

func NewActionRestoreCommand() *cobra.Command {
	cmd := newSnapshotRestoreCommand()
	cmd.Use = "snapshot-restore"

	return cmd
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

func newSnapshotSaveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "save",
		Short:       "Save explicitly selected live tmux sessions to the native repository",
		Long:        "Save explicitly selected live tmux sessions to the native repository.\n\nSaves are practical recovery observations, not atomic checkpoints or application memory images.",
		RunE:        runSnapshotSave,
		Annotations: map[string]string{clibase.AnnotationOperation: "save"},
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.Flags().StringSlice("session", nil, "Explicit selected session IDs")
	cmd.Flags().Bool("process-enrichment", true, "Capture foreground process and native application recovery context")
	cmd.Flags().StringSlice("process-env", nil, "Selected environment keys to capture for observed foreground processes")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func parseSaveInput(cmd *cobra.Command, args []string) (string, string, []gotmux.SessionID, engine.SaveConfig, error) {
	absRepoPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return "", "", nil, engine.SaveConfig{LeaseRoot: "", ProcessEnrichment: false, ProcessEnvironmentKeys: nil, AgentSource: nil, Profile: engine.RecoveryExact}, err
	}

	socketPath := clibase.ResolveSocket(cmd)

	leaseRoot, err := clibase.ResolveLeaseRoot(cmd)
	if err != nil {
		return "", "", nil, engine.SaveConfig{LeaseRoot: "", ProcessEnrichment: false, ProcessEnvironmentKeys: nil, AgentSource: nil, Profile: engine.RecoveryExact}, err
	}

	sessionFlags, _ := cmd.Flags().GetStringSlice("session")
	allSessions := slices.Concat(sessionFlags, args)

	var selected []gotmux.SessionID

	seen := make(map[string]bool)

	for _, s := range allSessions {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			selected = append(selected, gotmux.SessionID(s))
		}
	}

	if len(selected) == 0 {
		return "", "", nil, engine.SaveConfig{LeaseRoot: "", ProcessEnrichment: false, ProcessEnvironmentKeys: nil, AgentSource: nil, Profile: engine.RecoveryExact}, capture.ErrSelection
	}

	processEnrichment, _ := cmd.Flags().GetBool("process-enrichment")

	cmdCfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return "", "", nil, engine.SaveConfig{LeaseRoot: "", ProcessEnrichment: false, ProcessEnvironmentKeys: nil, AgentSource: nil, Profile: engine.RecoveryExact}, err
	}

	filteredEnv, agentStatePath, envErr := resolveSaveEnvironment(cmd, cmdCfg, processEnrichment)
	if envErr != nil {
		return "", "", nil, engine.SaveConfig{LeaseRoot: "", ProcessEnrichment: false, ProcessEnvironmentKeys: nil, AgentSource: nil, Profile: engine.RecoveryExact}, envErr
	}

	cfg := engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      processEnrichment,
		ProcessEnvironmentKeys: filteredEnv,
		AgentSource:            agents.NewAHTStore(agentStatePath),
		Profile:                engine.RecoveryPractical,
	}

	return absRepoPath, socketPath, selected, cfg, nil
}

func resolveSaveEnvironment(cmd *cobra.Command, cmdCfg *config.Config, enrichment bool) ([]string, string, error) {
	var processEnv []string

	switch {
	case cmd.Flags().Changed("process-env"):
		processEnv, _ = cmd.Flags().GetStringSlice("process-env")
	case cmdCfg != nil:
		processEnv = slices.Clone(cmdCfg.Snapshot.ProcessEnvironment)
	default:
		processEnv = slices.Clone(config.DefaultProcessEnvironment)
	}

	if err := process.ValidateEnvironmentKeys(processEnv); err != nil {
		return nil, "", clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	filteredEnv := config.FilterProcessEnvironmentKeys(processEnv)
	if !enrichment && !cmd.Flags().Changed("process-env") {
		filteredEnv = nil
	}

	agentPath := resolveAgentStatePath(cmd)
	if agentPath == "" && cmdCfg != nil && cmdCfg.AgentStatus.StorePath != "" {
		agentPath = cmdCfg.AgentStatus.StorePath
	}

	return filteredEnv, agentPath, nil
}

func executeSave(ctx context.Context, repoPath, socketPath string, selected []gotmux.SessionID, cfg engine.SaveConfig) (engine.SaveResult, error) {
	server, err := integration.Open(socketPath)
	if err != nil {
		return engine.SaveResult{Attempt: "", Generation: "", Status: "failed"}, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	repo, err := store.Open(ctx, repoPath)
	if err != nil {
		return engine.SaveResult{Attempt: "", Generation: "", Status: "failed"}, oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	res, saveErr := engine.SaveObservation(ctx, repo, server, selected, cfg)

	closeErr := repo.Close()
	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_repository").Wrap(closeErr)
	}

	if saveErr != nil {
		saveErr = oops.In("cli").With("operation", "save_observation").Wrap(saveErr)
		if closeErr == nil {
			saveErr = clibase.NewSaveBoundaryError(saveErr)
		}
	}

	return res, errors.Join(saveErr, closeErr)
}

func renderSaveResult(cmd *cobra.Command, res engine.SaveResult, saveErr error) error {
	if saveErr != nil {
		if clibase.JSONOutputEnabled(cmd) && res.Attempt != "" {
			return clibase.WriteJSONResultError(cmd, snapshotSaveResultJSON{
				Attempt:    res.Attempt,
				Generation: res.Generation,
				Status:     res.Status,
			}, saveErr)
		}

		return saveErr
	}

	if res.Status == "partial" {
		saveOutcomeErr := clibase.NewExitError(clibase.ExitCodeGeneral, errPartialSnapshot)

		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, snapshotSaveResultJSON{
				Attempt:    res.Attempt,
				Generation: res.Generation,
				Status:     res.Status,
			}, saveOutcomeErr)
		}

		if err := clibase.WriteOutput(
			cmd,
			"Attempt:    %s\nGeneration: %s\nStatus:     %s\n",
			res.Attempt,
			res.Generation,
			res.Status,
		); err != nil {
			return errors.Join(saveOutcomeErr, err)
		}

		return saveOutcomeErr
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, snapshotSaveResultJSON{
			Attempt:    res.Attempt,
			Generation: res.Generation,
			Status:     res.Status,
		})
	}

	return clibase.WriteOutput(
		cmd,
		"Attempt:    %s\nGeneration: %s\nStatus:     %s\n",
		res.Attempt,
		res.Generation,
		res.Status,
	)
}

func runSnapshotSave(cmd *cobra.Command, args []string) error {
	repoPath, socketPath, selected, cfg, err := parseSaveInput(cmd, args)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	res, saveErr := executeSave(ctx, repoPath, socketPath, selected, cfg)

	return renderSaveResult(cmd, res, saveErr)
}

func newSnapshotRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "restore",
		Short:       "Restore sessions from the native snapshot repository onto tmux",
		Long:        "Restore sessions from the native snapshot repository onto tmux.\n\nGuards against live session collisions, acquires endpoint leases, and verifies structural fidelity.",
		RunE:        runSnapshotRestore,
		Annotations: map[string]string{clibase.AnnotationOperation: "restore"},
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.Flags().String("generation", "", "Generation ID to restore [default: the current generation]")
	cmd.Flags().StringSlice("session", nil, "Session IDs to restore [default: all saved sessions]")
	cmd.Flags().String("operation", "", "Restore under this operation ID; an existing operation with this ID resumes without replaying uncertain effects [default: a new operation ID]")
	cmd.Flags().Bool("pick", false, "Interactively select an immutable generation from the snapshot timeline")
	cmd.Flags().Bool("no-processes", false, "Disable launching processes recorded in launch context")
	cmd.Flags().Bool("allow-missing-dir", false, "Allow falling back if saved working directory does not exist")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func parseRestoreInput(cmd *cobra.Command, args []string) (string, string, engine.RestoreConfig, error) {
	if err := validateSnapshotRestorePicker(cmd); err != nil {
		return "", "", engine.RestoreConfig{}, err
	}

	absRepoPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return "", "", engine.RestoreConfig{}, err
	}

	socketPath := clibase.ResolveSocket(cmd)

	leaseRoot, err := clibase.ResolveLeaseRoot(cmd)
	if err != nil {
		return "", "", engine.RestoreConfig{}, err
	}

	generation, _ := cmd.Flags().GetString("generation")
	operationID, _ := cmd.Flags().GetString("operation")
	sessionFlags, _ := cmd.Flags().GetStringSlice("session")
	allSessions := slices.Concat(sessionFlags, args)

	noProcesses, _ := cmd.Flags().GetBool("no-processes")
	allowMissingDir, _ := cmd.Flags().GetBool("allow-missing-dir")

	cfg := engine.RestoreConfig{
		LeaseRoot:              leaseRoot,
		Generation:             generation,
		OperationID:            operationID,
		Sessions:               allSessions,
		LaunchProcesses:        !noProcesses,
		AllowMissingWorkingDir: allowMissingDir,
		Profile:                engine.RecoveryPractical,
	}

	return absRepoPath, socketPath, cfg, nil
}

func runSnapshotRestore(cmd *cobra.Command, args []string) error {
	repoPath, socketPath, cfg, err := parseRestoreInput(cmd, args)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if snapshotRestorePickerEnabled(cmd) {
		generation, selected, pickErr := selectSnapshotRestoreGeneration(ctx, cmd, repoPath)
		if pickErr != nil {
			return pickErr
		}

		if !selected {
			return nil
		}

		cfg.Generation = generation
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	repo, err := store.OpenExisting(ctx, repoPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	if cfg.Generation == "" {
		cfg.Generation, err = practicalGeneration(ctx, repo, "")
		if err != nil {
			return errors.Join(err, repo.Close())
		}
	}

	res, restoreErr := engine.Restore(ctx, repo, server, cfg)
	if restoreErr != nil {
		restoreErr = clibase.NewRestoreBoundaryError(restoreErr)
	}

	closeErr := repo.Close()
	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_repository").Wrap(closeErr)
	}

	return renderRestoreOutcome(cmd, res, errors.Join(restoreErr, closeErr))
}

func renderRestoreOutcome(cmd *cobra.Command, res engine.RestoreResult, restoreErr error) error {
	if restoreErr == nil && res.Status != "complete" && res.Status != "best_effort" {
		restoreErr = errPartialRestore
	}

	if restoreErr != nil {
		if clibase.JSONOutputEnabled(cmd) && res.OperationID != "" {
			return clibase.WriteJSONResultError(cmd, restoreJSONResult(res), restoreErr)
		}

		if res.OperationID != "" {
			if writeErr := renderRestoreTable(cmd, res); writeErr != nil {
				return errors.Join(restoreErr, writeErr)
			}

			return &commandResultError{error: restoreErr}
		}

		return restoreErr
	}

	if clibase.JSONOutputEnabled(cmd) {
		return renderRestoreJSON(cmd, res)
	}

	return renderRestoreTable(cmd, res)
}

func renderRestoreJSON(cmd *cobra.Command, res engine.RestoreResult) error {
	return clibase.WriteJSONResult(cmd, restoreJSONResult(res))
}

func restoreJSONResult(res engine.RestoreResult) snapshotRestoreResultJSON {
	missing := make([]snapshotEvidenceJSON, 0, len(res.MissingRequirements))
	for _, m := range res.MissingRequirements {
		missing = append(missing, snapshotEvidenceJSON{
			Domain: m.Domain,
			Status: m.Status,
			Reason: string(m.Reason),
		})
	}

	return snapshotRestoreResultJSON{
		OperationID:         res.OperationID,
		Generation:          res.Generation,
		Status:              res.Status,
		Reason:              res.Reason,
		SessionsRestored:    res.SessionsRestored,
		Panes:               slices.Clone(res.Panes),
		Verified:            res.Verified,
		Uncertain:           res.Uncertain,
		MissingRequirements: missing,
	}
}

func renderRestoreTable(cmd *cobra.Command, res engine.RestoreResult) error {
	var out strings.Builder

	fmt.Fprintf(&out, "Operation:          %s\n", res.OperationID)
	fmt.Fprintf(&out, "Generation:         %s\n", res.Generation)
	fmt.Fprintf(&out, "Status:             %s\n", res.Status)
	fmt.Fprintf(&out, "Reason:             %s\n", res.Reason)
	fmt.Fprintf(&out, "Sessions Restored:  %s\n", strings.Join(res.SessionsRestored, ", "))
	fmt.Fprintf(&out, "Verified:           %t\n", res.Verified)
	fmt.Fprintf(&out, "Uncertain:          %t\n", res.Uncertain)

	if len(res.Panes) > 0 {
		out.WriteString("Panes:\n")

		for _, pane := range res.Panes {
			fmt.Fprintf(
				&out,
				"  %s runtime=%q outcome=%s reason=%q app-verified=%t cwd-fallback=%t\n",
				pane.PaneID,
				pane.RuntimeID,
				pane.Outcome,
				pane.Reason,
				pane.AppVerified,
				pane.CWDFallback,
			)
		}
	}

	if len(res.MissingRequirements) > 0 {
		fmt.Fprintf(&out, "Missing Parity:     %d unverified or observational requirements\n", len(res.MissingRequirements))
	}

	return clibase.WriteOutput(cmd, "%s", out.String())
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
	items := make([]snapshotSessionJSON, 0, len(manifest.Sessions))
	for _, s := range manifest.Sessions {
		items = append(items, snapshotSessionJSON{
			ID:        s.ID,
			Name:      string(s.Name),
			Directory: string(s.Directory),
			Windows:   windowCount[s.ID],
		})
	}

	return clibase.WriteJSONResult(cmd, snapshotListResultJSON{
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
	absRepoPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo, err := store.OpenExisting(ctx, absRepoPath)
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
				return clibase.WriteJSONResult(cmd, snapshotListResultJSON{
					Generation: "",
					Sessions:   []snapshotSessionJSON{},
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
		Short: "Show save attempts and committed generations in the repository timeline",
		Long:  "Show save attempts and committed generations in the repository timeline.\n\nThe timeline distinguishes exact completeness from practical recovery observations.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runSnapshotHistory,
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().Bool("full", false, "Show complete values using an adaptive layout")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func renderHistoryJSON(cmd *cobra.Command, catalog store.Catalog) error {
	attempts := make([]snapshotAttemptJSON, 0, len(catalog.Attempts))
	for _, a := range catalog.Attempts {
		attempts = append(attempts, snapshotAttemptJSON{
			ID:             a.ID,
			Sequence:       a.Sequence,
			State:          a.State,
			Reason:         a.Reason,
			ManifestSHA256: a.ManifestSHA256,
		})
	}

	generations := make([]snapshotGenerationJSON, 0, len(catalog.Generations))
	for _, g := range catalog.Generations {
		generations = append(generations, snapshotGenerationJSON{
			ID:             g.ID,
			Sequence:       g.Sequence,
			Complete:       g.Complete,
			ManifestSHA256: g.ManifestSHA256,
			Current:        g.ID == catalog.Current,
		})
	}

	return clibase.WriteJSONResult(cmd, snapshotHistoryResultJSON{
		Current:     catalog.Current,
		Attempts:    attempts,
		Generations: generations,
	})
}

func renderHistoryTable(cmd *cobra.Command, catalog store.Catalog, full bool) error {
	if len(catalog.Attempts) == 0 && len(catalog.Generations) == 0 {
		return clibase.WriteLine(cmd, "No snapshot history found.")
	}

	var currentSeq uint64

	for _, g := range catalog.Generations {
		if g.ID == catalog.Current {
			currentSeq = g.Sequence

			break
		}
	}

	rows := make([]prettytable.Row, 0, len(catalog.Attempts))
	for _, a := range slices.Backward(catalog.Attempts) {
		current := ""
		if a.ID == catalog.Current || (currentSeq > 0 && a.Sequence == currentSeq) {
			current = "yes"
		}

		digest := a.ManifestSHA256
		if !full && len(digest) > 12 {
			digest = digest[:12]
		}

		rows = append(rows, prettytable.Row{
			a.Sequence,
			a.ID,
			a.State,
			a.Reason,
			current,
			digest,
		})
	}

	columns := []clibase.TableColumn{
		{Heading: "Sequence", Fixed: true, MinWidth: colWidthID, Wrap: nil},
		{Heading: "Attempt", Fixed: false, MinWidth: colWidthSession, Wrap: clibase.WrapIdentifier},
		{Heading: "State", Fixed: false, MinWidth: colWidthState, Wrap: nil},
		{Heading: "Reason", Fixed: false, MinWidth: colWidthSession, Wrap: nil},
		{Heading: "Current", Fixed: true, MinWidth: colWidthID, Wrap: nil},
		{Heading: "Digest", Fixed: false, MinWidth: colWidthDigest, Wrap: clibase.WrapIdentifier},
	}

	return clibase.WriteResponsiveTable(cmd, columns, rows, full)
}

func runSnapshotHistory(cmd *cobra.Command, _ []string) error {
	absRepoPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo, err := store.OpenExisting(ctx, absRepoPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	catalog, inspectErr := repo.Inspect(ctx)
	if inspectErr != nil {
		inspectErr = oops.In("cli").With("operation", "inspect_repository").Wrap(inspectErr)
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

	return renderHistoryTable(cmd, catalog, full)
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

	evidenceList := make([]snapshotEvidenceJSON, 0, len(manifest.Evidence))

	for _, e := range manifest.Evidence {
		evidenceList = append(evidenceList, snapshotEvidenceJSON{
			Domain: e.Domain,
			Status: e.Status,
			Reason: e.Reason,
		})
	}

	return clibase.WriteJSONResult(cmd, snapshotVerifyResultJSON{
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
	absRepoPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo, err := store.OpenExisting(ctx, absRepoPath)
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
			return clibase.WriteJSONResultError(cmd, snapshotVerifyResultJSON{
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
				Evidence:               []snapshotEvidenceJSON{},
			}, combinedErr)
		}

		return combinedErr
	}

	if clibase.JSONOutputEnabled(cmd) {
		return renderVerifyJSON(cmd, targetGen, manifest)
	}

	return renderVerifyTable(cmd, targetGen, manifest)
}

func resolveAgentStatePath(cmd *cobra.Command) string {
	statePath, _ := cmd.Flags().GetString("state")
	if statePath == "" {
		return ""
	}

	return statePath
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
