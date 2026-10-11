package task

import (
	"cmp"
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
	ErrTaskStarting    = errors.New("task is still being created")
	ErrGitHubFailed    = errors.New("gh could not list pull requests")
	ErrGitHubResponse  = errors.New("gh returned an unexpected pull request listing")
	errUnknownPRState  = errors.New("unknown pull request state")
	errPopupNotRemoved = errors.New("task popup session was not closed")
)

// PRFinder looks up the pull request opened from a branch, in any state.
type PRFinder interface {
	FindPR(ctx context.Context, dir, branch string) (PullRequest, string, bool, error)
}

// GitHub finds pull requests with the gh command.
type GitHub struct{}

// LossError lists what finishing a task would destroy: commits only its branch holds and changes
// only its worktree holds, when sesh owns them, and a closed pull request of a branch it owns.
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

// RefreshPR records the pull request of a project task's branch and reports whether it changed.
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

// Finish removes a task: its popup session, its window, and the worktree and branch sesh created
// for it. A worktree or branch sesh did not create stays as it is. Without discard it refuses
// when the removal would lose work: commits not merged into the base on a branch it deletes,
// uncommitted changes in a worktree it removes, or a pull request of that branch closed without
// merging. A finish that stops part way leaves the task finishing, and finishing it again
// completes the removal.
func (s *Service) Finish(ctx context.Context, id string, discard bool) (Record, error) {
	record, err := s.record(ctx, id)
	if err != nil {
		return record, taskError("finish_task", err)
	}

	if record.State == StateCreating {
		return record, taskError("finish_task", ErrTaskStarting)
	}

	if record.State != StateFinishing && !discard {
		mergedHead := ""

		if refreshed, head, _, refreshErr := s.refreshPR(ctx, record); refreshErr == nil {
			record = refreshed
			mergedHead = head
		}

		if err := s.checkLoss(ctx, record, mergedHead); err != nil {
			return record, taskError("finish_task", err)
		}
	}

	record.State = StateFinishing
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
	base := cmp.Or(record.Upstream, record.Base)

	count, err := git.Output(ctx, record.Project, "rev-list", "--count", base+".."+record.Branch)
	if err != nil {
		return 0, fmt.Errorf("count unmerged commits: %w", err)
	}

	if count != "0" && record.PR.State == PRMerged && validCommitOID(mergedHead) {
		count, err = git.Output(ctx, record.Project, "rev-list", "--count", base+".."+record.Branch, "--not", mergedHead)
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

func lossOf(loss LossError) error {
	if loss.ClosedUnmerged || loss.UnmergedCommits > 0 || loss.ChangedFiles > 0 {
		return &loss
	}

	return nil
}

// removeTask closes everything a task owns. Each step accepts that an earlier finish already
// removed its part.
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
		"--json", "number,state,url,headRefOid", "--limit", "1")
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
	}

	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		return noPR(), "", false, errors.Join(ErrGitHubResponse, err)
	}

	if len(listed) == 0 {
		return noPR(), "", false, nil
	}

	state, err := prState(listed[0].State)
	if err != nil || listed[0].Number <= 0 || (listed[0].Head != "" && !validCommitOID(listed[0].Head)) {
		return noPR(), "", false, errors.Join(ErrGitHubResponse, err)
	}

	return PullRequest{Number: listed[0].Number, State: state, URL: listed[0].URL}, listed[0].Head, true, nil
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
