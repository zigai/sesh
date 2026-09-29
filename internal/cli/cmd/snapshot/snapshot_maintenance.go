package cmdsnapshot

import (
	"errors"
	"math"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	retentionDaysFlag  = "retention-days"
	maxCountFlag       = "max-count"
	maxBytesFlag       = "max-bytes"
	maxGenerationsFlag = "max-generations"

	minimumRetainedGenerations = 5
	maximumRetentionDays       = math.MaxInt64 / int64(24*time.Hour)
)

var (
	errInvalidRetentionPolicy  = errors.New("invalid snapshot retention policy")
	errInvalidCollectionBudget = errors.New("invalid snapshot collection budget")
)

type snapshotPruneResultJSON struct {
	Removed        []string `json:"removed"`
	Retained       []string `json:"retained"`
	Protected      []string `json:"protected"`
	RetainedBytes  int64    `json:"retainedBytes"`
	ProtectedBytes int64    `json:"protectedBytes"`
	BudgetUnmet    bool     `json:"budgetUnmet"`
}

type snapshotCollectionSkipJSON struct {
	Generation string `json:"generation"`
	Reason     string `json:"reason"`
}

type snapshotGCResultJSON struct {
	Intents     []string                     `json:"intents"`
	Resumed     bool                         `json:"resumed"`
	Removed     []string                     `json:"removed"`
	Deferred    []string                     `json:"deferred"`
	Preserved   []snapshotCollectionSkipJSON `json:"preserved"`
	Quarantined []snapshotCollectionSkipJSON `json:"quarantined"`
	Bytes       int64                        `json:"bytes"`
}

type renderedCommandError struct{ error }

func (e *renderedCommandError) Unwrap() error { return e.error }

func (e *renderedCommandError) RenderedCommandError() bool { return true }

func newSnapshotPruneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Apply configured snapshot retention to catalog membership",
		Long:  "Apply configured snapshot retention to catalog membership.\n\nPruning removes catalog references and retires generations without deleting physical bytes immediately.",
		Args:  validateSnapshotMaintenanceArgs,
		RunE:  runSnapshotPrune,
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().Int(retentionDaysFlag, 0, "Retain complete snapshot generations from the last N days (0 or greater)")
	cmd.Flags().Int(maxCountFlag, 0, "Retain at most N complete snapshot generations (0 or at least 5)")
	cmd.Flags().Int64(maxBytesFlag, 0, "Retain complete snapshot generations up to N total bytes (0 or greater)")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func newSnapshotGCCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Physically collect retired snapshot generation bytes",
		Long:  "Physically collect retired snapshot generation bytes.\n\nPhysical collection requires explicit bounded budgets, acquires the physical collection lock, and preserves quarantined or protected evidence.",
		Args:  validateSnapshotMaintenanceArgs,
		RunE:  runSnapshotGC,
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().Int(maxGenerationsFlag, 0, "Collect at most N retired generations in one pass (required; 1 to 10000)")
	cmd.Flags().Int64(maxBytesFlag, 0, "Collect at most N retired generation bytes in one pass (required; at least 1)")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func validateSnapshotMaintenanceArgs(cmd *cobra.Command, args []string) error {
	if err := clibase.UsageArgs(cobra.NoArgs)(cmd, args); err != nil {
		return err
	}

	return validateSnapshotMaintenanceCommand(cmd)
}

func validateSnapshotMaintenanceCommand(cmd *cobra.Command) error {
	switch cmd.Name() {
	case "prune":
		return validateExplicitPruneFlags(cmd)
	case "gc":
		return validateExplicitGCFlags(cmd)
	default:
		return nil
	}
}

func validateExplicitPruneFlags(cmd *cobra.Command) error {
	days, _ := cmd.Flags().GetInt(retentionDaysFlag)
	count, _ := cmd.Flags().GetInt(maxCountFlag)
	bytes, _ := cmd.Flags().GetInt64(maxBytesFlag)

	if (cmd.Flags().Changed(retentionDaysFlag) && (days < 0 || int64(days) > maximumRetentionDays)) ||
		(cmd.Flags().Changed(maxCountFlag) && (count < 0 || count > 0 && count < minimumRetainedGenerations)) ||
		(cmd.Flags().Changed(maxBytesFlag) && bytes < 0) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errInvalidRetentionPolicy)
	}

	return nil
}

func validateExplicitGCFlags(cmd *cobra.Command) error {
	generations, _ := cmd.Flags().GetInt(maxGenerationsFlag)

	bytes, _ := cmd.Flags().GetInt64(maxBytesFlag)
	if !cmd.Flags().Changed(maxGenerationsFlag) || !cmd.Flags().Changed(maxBytesFlag) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errInvalidCollectionBudget)
	}

	if err := (store.CollectionBudget{Generations: generations, Bytes: bytes}).Validate(); err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, errInvalidCollectionBudget)
	}

	return nil
}

func runSnapshotPrune(cmd *cobra.Command, _ []string) error {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	policy, err := resolvedRetentionPolicy(cmd, cfg.Snapshot)
	if err != nil {
		return err
	}

	repository, err := openMaintenanceRepository(cmd)
	if err != nil {
		return err
	}

	result, pruneErr := engine.Prune(cmd.Context(), repository, time.Now().UTC(), policy)
	closeErr := wrapMaintenanceCloseError(repository.Close())
	resultJSON := pruneResultJSON(result)

	return renderPruneResult(cmd, resultJSON, errors.Join(pruneErr, closeErr))
}

func resolvedRetentionPolicy(cmd *cobra.Command, snapshot config.SnapshotConfig) (store.RetentionPolicy, error) {
	days, count, bytes := snapshot.RetentionDays, snapshot.MaxCount, snapshot.MaxBytes
	if cmd.Flags().Changed(retentionDaysFlag) {
		days, _ = cmd.Flags().GetInt(retentionDaysFlag)
	}

	if cmd.Flags().Changed(maxCountFlag) {
		count, _ = cmd.Flags().GetInt(maxCountFlag)
	}

	if cmd.Flags().Changed(maxBytesFlag) {
		bytes, _ = cmd.Flags().GetInt64(maxBytesFlag)
	}

	if days < 0 || int64(days) > maximumRetentionDays || count < 0 || (count > 0 && count < minimumRetainedGenerations) || bytes < 0 {
		return store.RetentionPolicy{}, clibase.NewExitError(clibase.ExitCodeUsage, errInvalidRetentionPolicy)
	}

	return store.RetentionPolicy{
		MaxAge:   time.Duration(days) * 24 * time.Hour,
		MaxCount: count,
		MaxBytes: bytes,
	}, nil
}

func runSnapshotGC(cmd *cobra.Command, _ []string) error {
	budget, err := collectionBudget(cmd)
	if err != nil {
		return err
	}

	repository, err := openMaintenanceRepository(cmd)
	if err != nil {
		return err
	}

	result, collectErr := engine.GarbageCollect(cmd.Context(), repository, budget)
	closeErr := wrapMaintenanceCloseError(repository.Close())
	resultJSON := gcResultJSON(result)

	return renderGCResult(cmd, resultJSON, errors.Join(collectErr, closeErr))
}

func collectionBudget(cmd *cobra.Command) (store.CollectionBudget, error) {
	generations, _ := cmd.Flags().GetInt(maxGenerationsFlag)

	bytes, _ := cmd.Flags().GetInt64(maxBytesFlag)

	budget := store.CollectionBudget{Generations: generations, Bytes: bytes}
	if err := budget.Validate(); err != nil {
		return store.CollectionBudget{}, clibase.NewExitError(clibase.ExitCodeUsage, errInvalidCollectionBudget)
	}

	return budget, nil
}

func openMaintenanceRepository(cmd *cobra.Command) (*store.Repository, error) {
	absPath, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return nil, err
	}

	repository, err := store.OpenExisting(cmd.Context(), absPath)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	return repository, nil
}

func wrapMaintenanceCloseError(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("cli").With("operation", "close_repository").Wrap(err)
}

func pruneResultJSON(result store.RetentionResult) snapshotPruneResultJSON {
	return snapshotPruneResultJSON{
		Removed: result.Removed, Retained: result.Retained, Protected: result.Protected,
		RetainedBytes: result.RetainedBytes, ProtectedBytes: result.ProtectedBytes,
		BudgetUnmet: result.BudgetUnmet,
	}
}

func gcResultJSON(result store.CollectionResult) snapshotGCResultJSON {
	return snapshotGCResultJSON{
		Intents: result.Intents, Resumed: result.Resumed, Removed: result.Removed,
		Deferred: result.Deferred, Preserved: collectionSkipsJSON(result.Preserved),
		Quarantined: collectionSkipsJSON(result.Quarantined), Bytes: result.Bytes,
	}
}

func collectionSkipsJSON(skips []store.CollectionSkip) []snapshotCollectionSkipJSON {
	result := make([]snapshotCollectionSkipJSON, len(skips))
	for index, skip := range skips {
		result[index] = snapshotCollectionSkipJSON{Generation: skip.Generation, Reason: skip.Reason}
	}

	return result
}

func renderPruneResult(cmd *cobra.Command, result snapshotPruneResultJSON, err error) error {
	if clibase.JSONOutputEnabled(cmd) {
		if err != nil {
			return clibase.WriteJSONResultError(cmd, result, err)
		}

		return clibase.WriteJSONResult(cmd, result)
	}

	if writeErr := clibase.WriteOutput(
		cmd,
		"Membership removed: %d\nMembership retained: %d\nProtected: %d\nRetained bytes: %d\nProtected bytes: %d\nProtected overage: %t\n",
		len(result.Removed), len(result.Retained), len(result.Protected), result.RetainedBytes, result.ProtectedBytes, result.BudgetUnmet,
	); writeErr != nil {
		return errors.Join(err, writeErr)
	}

	if err != nil {
		if writeErr := clibase.WriteCommandError(cmd, err); writeErr != nil {
			return errors.Join(err, writeErr)
		}

		return &renderedCommandError{error: err}
	}

	return nil
}

func renderGCResult(cmd *cobra.Command, result snapshotGCResultJSON, err error) error {
	if clibase.JSONOutputEnabled(cmd) {
		if err != nil {
			return clibase.WriteJSONResultError(cmd, result, err)
		}

		return clibase.WriteJSONResult(cmd, result)
	}

	if writeErr := clibase.WriteOutput(
		cmd,
		"Physical generations removed: %d\nPhysical bytes removed: %d\nDeferred: %d\nPreserved: %d\nQuarantined: %d\nResumed intent: %t\n",
		len(result.Removed), result.Bytes, len(result.Deferred), len(result.Preserved), len(result.Quarantined), result.Resumed,
	); writeErr != nil {
		return errors.Join(err, writeErr)
	}

	if err != nil {
		if writeErr := clibase.WriteCommandError(cmd, err); writeErr != nil {
			return errors.Join(err, writeErr)
		}

		return &renderedCommandError{error: err}
	}

	return nil
}
