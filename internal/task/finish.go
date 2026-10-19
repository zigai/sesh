package task

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/git"
)

const ghOutputLimit = 64 << 10

var (
	ErrWouldLoseWork   = errors.New("finishing the task would lose work")
	ErrBaseMissing     = errors.New("the task's base branch no longer exists")
	ErrTaskStarting    = errors.New("task is still being created")
	ErrGitHubFailed    = errors.New("gh could not list pull requests")
	ErrGitHubResponse  = errors.New("gh returned an unexpected pull request listing")
	errUnknownPRState  = errors.New("unknown pull request state")
	errPopupNotRemoved = errors.New("task popup session was not closed")
)

type FinishOptions struct {
	Discard bool
	SkipRun bool
}

type PRFinder interface {
	FindPR(ctx context.Context, dir, branch string) (PullRequest, string, bool, error)
}

type GitHub struct{}

type LossError struct {
	UnmergedCommits int
	ChangedFiles    int
	ClosedUnmerged  bool
}

func (e *LossError) Error() string {
	var parts []string

	if e.ClosedUnmerged {
		parts = append(parts, "its pull request was closed without merging")
	}

	if e.UnmergedCommits > 0 {
		parts = append(parts, plural(e.UnmergedCommits, "unmerged commit"))
	}

	if e.ChangedFiles > 0 {
		parts = append(parts, plural(e.ChangedFiles, "uncommitted change"))
	}

	if len(parts) == 0 {
		parts = append(parts, "its branch is not merged")
	}

	return "finishing would discard " + strings.Join(parts, " and ") + "; finish with discard to remove it anyway"
}

func (e *LossError) Is(target error) bool {
	return target == ErrWouldLoseWork
}

func (s *Service) RefreshPR(ctx context.Context, id string) (Record, bool, error) {
	record, err := s.record(ctx, id)
	if err != nil {
		return record, false, taskError("refresh_pr", err)
	}

	refreshed, _, changed, err := s.refreshPR(ctx, record)

	return refreshed, changed, err
}

func (s *Service) refreshPR(ctx context.Context, record Record) (Record, string, bool, error) {
	if record.Kind != KindProject || record.Branch == "" || record.Branch == record.Base ||
		record.State == StateCreating || record.State == StateFinishing {
		return record, "", false, nil
	}

	pr, head, found, err := s.prs.FindPR(ctx, record.Directory, record.Branch)
	if err != nil || !found {
		return record, "", false, taskError("refresh_pr", err)
	}

	if pr == record.PR {
		return record, head, false, nil
	}

	stored := record

	_, err = s.store.Update(ctx, func(ledger *Ledger) error {
		current, ok := ledger.Find(record.ID)
		if !ok {
			return ErrNotFound
		}

		current.PR = pr
		ledger.Put(current)
		stored = current

		return nil
	})
	if err != nil {
		return record, "", false, taskError("refresh_pr", err)
	}

	return stored, head, true, nil
}

// Finish removes only the worktree and branch sesh created. Without discard it refuses to lose
// unmerged commits, uncommitted changes, or a pull request closed without merging. A failing
// finish command stops it; a stopped finish leaves the task finishing, and finishing again
// completes it.
func (s *Service) Finish(ctx context.Context, id string, opts FinishOptions) (Record, error) {
	record, err := s.record(ctx, id)
	if err != nil {
		return record, taskError("finish_task", err)
	}

	if record.State == StateCreating && !Stalled(ctx, record) {
		return record, taskError("finish_task", ErrTaskStarting)
	}

	if record.State != StateFinishing {
		record, err = s.prepareFinish(ctx, record, opts)
		if err != nil {
			return record, taskError("finish_task", err)
		}
	}

	record.State = StateFinishing
	record.Owner = currentProcess(ctx)

	if err := s.put(ctx, record); err != nil {
		return record, taskError("finish_task", err)
	}

	if err := s.removeTask(ctx, record); err != nil {
		return record, taskError("finish_task", err)
	}

	_, err = s.store.Update(context.WithoutCancel(ctx), func(ledger *Ledger) error {
		ledger.Remove(record.ID)

		return nil
	})

	return record, taskError("finish_task", err)
}

func (s *Service) prepareFinish(ctx context.Context, record Record, opts FinishOptions) (Record, error) {
	if !opts.Discard {
		mergedHead := ""

		if refreshed, head, _, refreshErr := s.refreshPR(ctx, record); refreshErr == nil {
			record = refreshed
			mergedHead = head
		}

		if err := s.checkLoss(ctx, record, mergedHead); err != nil {
			return record, err
		}
	}

	if opts.SkipRun {
		return record, nil
	}

	return record, s.runFinish(ctx, record)
}

func (s *Service) runFinish(ctx context.Context, record Record) error {
	if s.finishCommand == nil || !record.OwnsWorktree {
		return nil
	}

	run := s.finishCommand(record.Project)
	if len(run) == 0 {
		return nil
	}

	if _, err := os.Stat(record.Directory); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	command := exec.CommandContext(ctx, run[0], run[1:]...)
	command.Dir = record.Directory
	command.Env = os.Environ()

	err := command.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return &FinishRunError{ExitCode: exitErr.ExitCode()}
	}

	if err != nil {
		return errors.Join(ErrFinishRun, err)
	}

	return nil
}

func (s *Service) checkLoss(ctx context.Context, record Record, mergedHead string) error {
	loss := LossError{UnmergedCommits: 0, ChangedFiles: 0, ClosedUnmerged: record.OwnsBranch && record.PR.State == PRClosed}

	if _, err := os.Stat(record.Directory); err == nil && record.OwnsWorktree {
		status, err := git.Output(ctx, record.Directory, "status", "--porcelain")
		if err != nil {
			return fmt.Errorf("inspect worktree: %w", err)
		}

		loss.ChangedFiles = countLines(status)
	}

	if !record.OwnsBranch {
		return lossOf(loss)
	}

	exists, err := branchExists(ctx, record.Project, record.Branch)
	if err != nil {
		return fmt.Errorf("inspect branch: %w", err)
	}

	if !exists {
		return lossOf(loss)
	}

	loss.UnmergedCommits, err = unmergedCommitCount(ctx, record, mergedHead)
	if err != nil {
		return err
	}

	return lossOf(loss)
}

func unmergedCommitCount(ctx context.Context, record Record, mergedHead string) (int, error) {
	bases, err := mergeTargets(ctx, record)
	if err != nil {
		return 0, err
	}

	count, err := git.Output(ctx, record.Project, append([]string{"rev-list", "--count", record.Branch, "--not"}, bases...)...)
	if err != nil {
		return 0, fmt.Errorf("count unmerged commits: %w", err)
	}

	if count != "0" && record.PR.State == PRMerged && validCommitOID(mergedHead) {
		args := append([]string{"rev-list", "--count", record.Branch, "--not"}, bases...)

		count, err = git.Output(ctx, record.Project, append(args, mergedHead)...)
		if err != nil {
			return 0, fmt.Errorf("count commits after pull request merge: %w", err)
		}
	}

	commits, err := strconv.Atoi(count)
	if err != nil {
		return 0, fmt.Errorf("parse unmerged commit count: %w", err)
	}

	return commits, nil
}

func mergeTargets(ctx context.Context, record Record) ([]string, error) {
	if record.Upstream != "" {
		return []string{record.Upstream}, nil
	}

	var bases []string

	// A new task branch may start from the local base or from origin's.
	for _, ref := range []string{"refs/heads/" + record.Base, "refs/remotes/origin/" + record.Base} {
		exists, err := refExists(ctx, record.Project, ref)
		if err != nil {
			return nil, err
		}

		if exists {
			bases = append(bases, ref)
		}
	}

	if len(bases) == 0 {
		return nil, fmt.Errorf("count unmerged commits: %w", ErrBaseMissing)
	}

	return bases, nil
}

func lossOf(loss LossError) error {
	if loss.ClosedUnmerged || loss.UnmergedCommits > 0 || loss.ChangedFiles > 0 {
		return &loss
	}

	return nil
}

func (s *Service) removeTask(ctx context.Context, record Record) error {
	if s.closePopup != nil {
		if err := s.closePopup(ctx, record.ID); err != nil {
			return errors.Join(errPopupNotRemoved, err)
		}
	}

	if err := s.closeWindow(ctx, record.ID); err != nil {
		return err
	}

	if record.OwnsWorktree {
		if err := removeWorktree(ctx, record); err != nil {
			return err
		}
	}

	if !record.OwnsBranch {
		return nil
	}

	exists, err := branchExists(ctx, record.Project, record.Branch)
	if err != nil || !exists {
		return err
	}

	if _, err := git.Output(ctx, record.Project, "branch", "-D", record.Branch); err != nil {
		return fmt.Errorf("delete branch: %w", err)
	}

	return nil
}

func removeWorktree(ctx context.Context, record Record) error {
	if _, err := os.Stat(record.Directory); err == nil {
		if _, err := git.Output(ctx, record.Project, "worktree", "remove", "--force", record.Directory); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}

		return nil
	}

	if _, err := git.Output(ctx, record.Project, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune worktrees: %w", err)
	}

	return nil
}

func (s *Service) closeWindow(ctx context.Context, id string) error {
	located, err := locateTasks(ctx, s.server)
	if err != nil {
		return err
	}

	live, found := located[id]
	if !found {
		return nil
	}

	window, err := s.server.Window(ctx, live.WindowID)
	if errors.Is(err, gotmux.ErrNotFound) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("find task window: %w", err)
	}

	if err := window.Kill(ctx); err != nil && !errors.Is(err, gotmux.ErrNotFound) {
		return fmt.Errorf("close task window: %w", err)
	}

	return nil
}

func (GitHub) FindPR(ctx context.Context, dir, branch string) (PullRequest, string, bool, error) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return noPR(), "", false, errors.Join(ErrGitHubFailed, err)
	}

	command := exec.CommandContext(ctx, ghPath, "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,url,headRefOid,isCrossRepository")
	command.Dir = dir

	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")

	output := git.NewLimitedBuffer(ghOutputLimit)

	command.Stdout = output
	if err := command.Run(); err != nil {
		return noPR(), "", false, errors.Join(ErrGitHubFailed, err)
	}

	var listed []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Head   string `json:"headRefOid"`
		Fork   bool   `json:"isCrossRepository"`
	}

	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		return noPR(), "", false, errors.Join(ErrGitHubResponse, err)
	}

	for _, pr := range listed {
		if pr.Fork {
			continue
		}

		state, err := prState(pr.State)
		if err != nil || pr.Number <= 0 || (pr.Head != "" && !validCommitOID(pr.Head)) {
			return noPR(), "", false, errors.Join(ErrGitHubResponse, err)
		}

		return PullRequest{Number: pr.Number, State: state, URL: pr.URL}, pr.Head, true, nil
	}

	return noPR(), "", false, nil
}

func validCommitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}

	_, err := hex.DecodeString(oid)

	return err == nil
}

func noPR() PullRequest {
	return PullRequest{Number: 0, State: "", URL: ""}
}

func prState(value string) (PRState, error) {
	switch strings.ToUpper(value) {
	case "OPEN":
		return PROpen, nil
	case "MERGED":
		return PRMerged, nil
	case "CLOSED":
		return PRClosed, nil
	default:
		return "", errUnknownPRState
	}
}

func countLines(text string) int {
	if text == "" {
		return 0
	}

	return strings.Count(text, "\n") + 1
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(count) + " " + noun + "s"
}
