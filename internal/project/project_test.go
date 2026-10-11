package project_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/project"
)

func TestResolveFindsTheProjectOfADirectory(t *testing.T) {
	base := realTempDir(t)
	repo := initRepository(t, filepath.Join(base, "code", "shop"))
	nested := mkdir(t, filepath.Join(repo, "src", "cart"))
	worktree := filepath.Join(base, "worktrees", "shop", "fix-cart")
	runGit(t, repo, "worktree", "add", "-q", "-b", "fix-cart", worktree)
	mkdir(t, filepath.Join(base, "notes", "daily"))
	docs := mkdir(t, filepath.Join(repo, "docs"))
	vendored := initRepository(t, filepath.Join(base, "notes", "vendored"))
	symlink := filepath.Join(base, "shop-link")

	if err := os.Symlink(repo, symlink); err != nil {
		t.Fatal(err)
	}

	catalog := newCatalog(t, nil, []string{filepath.Join(base, "notes"), docs})

	tests := []struct {
		name string
		dir  string
		want project.Project
	}{
		{"repository root", repo, project.Project{Root: repo, Name: "shop", Kind: project.KindGit}},
		{"repository subdirectory", nested, project.Project{Root: repo, Name: "shop", Kind: project.KindGit}},
		{"linked worktree", worktree, project.Project{Root: repo, Name: "shop", Kind: project.KindGit}},
		{"symlink to repository", symlink, project.Project{Root: repo, Name: "shop", Kind: project.KindGit}},
		{"registered folder", filepath.Join(base, "notes", "daily"), project.Project{Root: filepath.Join(base, "notes"), Name: "notes", Kind: project.KindFolder}},
		{"registered folder inside repository", docs, project.Project{Root: docs, Name: "docs", Kind: project.KindFolder}},
		{"repository inside registered folder", vendored, project.Project{Root: vendored, Name: "vendored", Kind: project.KindGit}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := catalog.Resolve(t.Context(), tt.dir)
			if err != nil {
				t.Fatalf("Resolve(%s) error = %v", tt.dir, err)
			}

			if got != tt.want {
				t.Errorf("Resolve(%s) = %+v, want %+v", tt.dir, got, tt.want)
			}
		})
	}
}

func TestResolveReportsDirectoriesOutsideProjects(t *testing.T) {
	outside := mkdir(t, filepath.Join(realTempDir(t), "downloads"))
	catalog := newCatalog(t, nil, nil)

	if _, err := catalog.Resolve(t.Context(), outside); !errors.Is(err, project.ErrNotProject) {
		t.Fatalf("Resolve() error = %v, want ErrNotProject", err)
	}
}

func TestListFindsRepositoriesUnderRootsAndRegisteredFolders(t *testing.T) {
	base := realTempDir(t)
	root := mkdir(t, filepath.Join(base, "code"))
	shop := initRepository(t, filepath.Join(root, "shop"))
	api := initRepository(t, filepath.Join(root, "api"))
	runGit(t, shop, "worktree", "add", "-q", "-b", "wt", filepath.Join(root, "shop-wt"))
	mkdir(t, filepath.Join(root, "plain-folder"))
	initRepository(t, filepath.Join(root, ".hidden"))
	notes := mkdir(t, filepath.Join(base, "notes"))

	got, err := newCatalog(t, []string{root}, []string{notes, shop}).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := []project.Project{
		{Root: api, Name: "api", Kind: project.KindGit},
		{Root: notes, Name: "notes", Kind: project.KindFolder},
		{Root: shop, Name: "shop", Kind: project.KindGit},
	}

	if len(got) != len(want) {
		t.Fatalf("List() = %+v, want %+v", got, want)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Errorf("List()[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestListFailsOnAMissingRoot(t *testing.T) {
	missing := filepath.Join(realTempDir(t), "missing")

	if _, err := newCatalog(t, []string{missing}, nil).List(); err == nil {
		t.Fatal("List() error = nil, want missing root failure")
	}
}

func TestNewCatalogRejectsRelativePaths(t *testing.T) {
	if _, err := project.NewCatalog(config.ProjectsConfig{Roots: []string{"code"}, Extra: nil, Overrides: nil}); !errors.Is(err, project.ErrPathInvalid) {
		t.Fatalf("NewCatalog() error = %v, want ErrPathInvalid", err)
	}
}

func newCatalog(t *testing.T, roots, extra []string) project.Catalog {
	t.Helper()

	catalog, err := project.NewCatalog(config.ProjectsConfig{Roots: roots, Extra: extra, Overrides: nil})
	if err != nil {
		t.Fatal(err)
	}

	return catalog
}

func realTempDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	return dir
}

func initRepository(t *testing.T, dir string) string {
	t.Helper()

	mkdir(t, dir)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")

	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = dir

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
