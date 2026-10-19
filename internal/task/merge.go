package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/zigai/sesh/internal/git"
)

const (
	MergeExitFailed   = 1
	MergeExitConflict = 3
	MergeExitCheck    = 4
	MergeExitDirty    = 5

	MergeAction = "merge"

	mergeRemote = "origin"
)

var (
	ErrMergeDirty     = errors.New("the checkout has uncommitted changes")
	ErrMergeConflict  = errors.New("rebasing onto the base branch conflicts; the rebase was aborted")
	ErrMergeCheck     = errors.New("the merge check failed")
	ErrFinishRun      = errors.New("the finish command failed")
	ErrMergeDetached  = errors.New("the checkout has no branch")
	ErrMergeNoRemote  = errors.New("the repository has no origin remote")
	ErrMergeNoBase    = errors.New("origin has no default branch to merge into")
	ErrMergeRejected  = errors.New("origin refused the fast-forward push")
	ErrNotProjectTask = errors.New("only a project task has a branch to merge")
)

type Merged struct {
	Branch string
	Base   string
}

type CheckError struct {
	ExitCode int
}

type FinishRunError struct {
	ExitCode int
}

func (e *CheckError) Error() string {
	return fmt.Sprintf("the merge check exited with code %d", e.ExitCode)
}

func (e *CheckError) Is(target error) bool {
	return target == ErrMergeCheck
}

func (e *FinishRunError) Error() string {
	return fmt.Sprintf("the finish command exited with code %d", e.ExitCode)
}

func (e *FinishRunError) Is(target error) bool {
	return target == ErrFinishRun
}

func MergeExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrMergeConflict):
		return MergeExitConflict
	case errors.Is(err, ErrMergeCheck):
		return MergeExitCheck
	case errors.Is(err, ErrMergeDirty):
		return MergeExitDirty
	default:
		return MergeExitFailed
	}
}

// MergeBranch rebases onto origin's base and pushes as a fast-forward, without a merge commit. It
// refuses uncommitted changes, aborts the rebase on a conflict, and fast-forwards the main
// checkout only when it is clean on the base. An empty base means origin's default branch.
func MergeBranch(ctx context.Context, dir, base string, check []string, out io.Writer) (Merged, error) {
	if err := requireClean(ctx, dir); err != nil {
		return Merged{Branch: "", Base: base}, err
	}

	merged, err := resolveMergeBranches(ctx, dir, base)
	if err != nil {
		return merged, err
	}

	target := mergeRemote + "/" + merged.Base

	if err := mergeStep(ctx, out, dir, "fetch "+target, "fetch", mergeRemote, merged.Base); err != nil {
		return merged, err
	}

	if err := mergeStep(ctx, out, dir, "rebase "+merged.Branch+" onto "+target, "rebase", target); err != nil {
		_, abortErr := git.Output(context.WithoutCancel(ctx), dir, "rebase", "--abort")

		return merged, errors.Join(ErrMergeConflict, err, abortErr)
	}

	if len(check) > 0 {
		if err := runMergeCheck(ctx, dir, check, out); err != nil {
			return merged, err
		}
	}

	if err := mergeStep(ctx, out, dir, "push to "+target, "push", mergeRemote, "HEAD:refs/heads/"+merged.Base); err != nil {
		return merged, errors.Join(ErrMergeRejected, err)
	}

	return merged, fastForwardMainCheckout(ctx, dir, merged.Base, out)
}

func requireClean(ctx context.Context, dir string) error {
	status, err := git.Output(ctx, dir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect checkout: %w", err)
	}

	if status != "" {
		return ErrMergeDirty
	}

	return nil
}

func resolveMergeBranches(ctx context.Context, dir, base string) (Merged, error) {
	merged := Merged{Branch: "", Base: base}

	var err error

	merged.Branch, err = git.Output(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || merged.Branch == "" {
		return merged, errors.Join(ErrMergeDetached, err)
	}

	if remotes, err := git.Output(ctx, dir, "remote"); err != nil || !strings.Contains("\n"+remotes+"\n", "\n"+mergeRemote+"\n") {
		return merged, errors.Join(ErrMergeNoRemote, err)
	}

	if merged.Base == "" {
		remoteHead, err := git.Output(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+mergeRemote+"/HEAD")
		if err != nil {
			return merged, errors.Join(ErrMergeNoBase, err)
		}

		merged.Base = strings.TrimPrefix(remoteHead, mergeRemote+"/")
	}

	return merged, nil
}

func mergeStep(ctx context.Context, out io.Writer, dir, step string, args ...string) error {
	_, _ = fmt.Fprintln(out, "· "+step)

	if _, err := git.Output(ctx, dir, args...); err != nil {
		return fmt.Errorf("%s: %w", step, err)
	}

	return nil
}

func runMergeCheck(ctx context.Context, dir string, check []string, out io.Writer) error {
	_, _ = fmt.Fprintln(out, "· check: "+strings.Join(check, " "))

	command := exec.CommandContext(ctx, check[0], check[1:]...)
	command.Dir = dir
	command.Stdout, command.Stderr = out, out
	command.Env = os.Environ()

	err := command.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return &CheckError{ExitCode: exitErr.ExitCode()}
	}

	if err != nil {
		return errors.Join(ErrMergeCheck, err)
	}

	return nil
}

func fastForwardMainCheckout(ctx context.Context, dir, base string, out io.Writer) error {
	worktrees, err := git.Output(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("list worktrees: %w", err)
	}

	main, _, _ := strings.Cut(worktrees, "\n")

	main, found := strings.CutPrefix(main, "worktree ")
	if !found {
		return nil
	}

	if !cleanOnBranch(ctx, main, base) {
		return nil
	}

	return mergeStep(ctx, out, main, "fast-forward "+base+" in "+main, "merge", "--ff-only", mergeRemote+"/"+base)
}

func cleanOnBranch(ctx context.Context, dir, branch string) bool {
	head, err := git.Output(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || head != branch {
		return false
	}

	status, err := git.Output(ctx, dir, "status", "--porcelain")

	return err == nil && status == ""
}
