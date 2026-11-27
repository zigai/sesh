package task

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/git"
)

const (
	manyRemoteBranches = 10_000
	longSubject        = "Describe a change in enough words that every listed ref carries a long subject line"
)

func TestListBranchesKeepsEveryBranchWhenARepositoryHasTenThousandRemoteBranches(t *testing.T) {
	root := repositoryWithManyRemoteBranches(t)

	for _, describe := range []bool{false, true} {
		t.Run(fmt.Sprintf("describe=%t", describe), func(t *testing.T) {
			known, err := listBranches(t.Context(), root, "main", describe)
			if err != nil {
				t.Fatalf("listBranches() error = %v", err)
			}

			local := slices.Sorted(slices.Values(known.local))
			if want := []string{"feature-00001", "local-only", "main"}; !slices.Equal(local, want) {
				t.Fatalf("local = %v, want %v", local, want)
			}

			if len(known.remote) != manyRemoteBranches-1 {
				t.Fatalf("got %d remote branches, want %d", len(known.remote), manyRemoteBranches-1)
			}

			remote := make(map[string]bool, len(known.remote))
			for _, branch := range known.remote {
				if branch.remote != "origin" {
					t.Fatalf(
						"remote branch %q has remote %q, want origin",
						branch.name,
						branch.remote,
					)
				}

				remote[branch.name] = true
			}

			for _, name := range []string{"feature-00000", "feature-09999"} {
				if !remote[name] {
					t.Fatalf("remote branches lack %q", name)
				}
			}

			for _, name := range []string{"feature-00001", "main", "HEAD"} {
				if remote[name] {
					t.Fatalf("remote branches list %q", name)
				}
			}

			if !describe {
				return
			}

			if want := 3 + manyRemoteBranches + 1; len(known.refs) != want {
				t.Fatalf("got %d described refs, want %d", len(known.refs), want)
			}

			last := known.refs["refs/remotes/origin/feature-09999"]
			if last.Subject != longSubject || last.TimeUnix == 0 {
				t.Fatalf(
					"refs/remotes/origin/feature-09999 = %+v, want subject %q and a commit time",
					last,
					longSubject,
				)
			}
		})
	}
}

func repositoryWithManyRemoteBranches(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{
			"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
			"commit", "-q", "--no-verify", "--allow-empty", "-m", longSubject,
		},
		{"branch", "local-only"},
		{"branch", "feature-00001"},
	} {
		if _, err := git.Output(t.Context(), root, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	head, err := git.Output(t.Context(), root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}

	var updates strings.Builder

	fmt.Fprintf(&updates, "create refs/remotes/origin/main %s\n", head)

	for number := range manyRemoteBranches {
		fmt.Fprintf(&updates, "create refs/remotes/origin/feature-%05d %s\n", number, head)
	}

	command := exec.CommandContext(t.Context(), "git", "update-ref", "--stdin")
	command.Dir = root
	command.Stdin = strings.NewReader(updates.String())

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref --stdin: %v: %s", err, output)
	}

	if _, err := git.Output(
		t.Context(),
		root,
		"symbolic-ref",
		"refs/remotes/origin/HEAD",
		"refs/remotes/origin/main",
	); err != nil {
		t.Fatalf("git symbolic-ref: %v", err)
	}

	return root
}
