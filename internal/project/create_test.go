package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/project"
)

func TestCreateMakesAnEmptyRepositoryInTheFirstRoot(t *testing.T) {
	root := mkdir(t, filepath.Join(realTempDir(t), "code"))
	catalog := newCatalog(t, []string{root}, nil)

	created, err := catalog.Create(t.Context(), "notes")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	want := project.Project{Root: filepath.Join(root, "notes"), Name: "notes", Kind: project.KindGit}
	if created != want {
		t.Fatalf("Create() = %+v, want %+v", created, want)
	}

	if info, err := os.Stat(filepath.Join(root, "notes", ".git")); err != nil || !info.IsDir() {
		t.Fatalf("created project has no repository: %v", err)
	}
}

func TestCreateRefusesNamesOutsideTheRootAndExistingDirectories(t *testing.T) {
	root := mkdir(t, filepath.Join(realTempDir(t), "code"))
	mkdir(t, filepath.Join(root, "taken"))
	catalog := newCatalog(t, []string{root}, nil)

	for _, name := range []string{"", ".hidden", "..", "a/b", "line\nbreak"} {
		if _, err := catalog.Create(t.Context(), name); !errors.Is(err, project.ErrNameInvalid) {
			t.Errorf("Create(%q) error = %v, want ErrNameInvalid", name, err)
		}
	}

	if _, err := catalog.Create(t.Context(), "taken"); !errors.Is(err, project.ErrExists) {
		t.Errorf("Create(taken) error = %v, want ErrExists", err)
	}

	if _, err := newCatalog(t, nil, nil).Create(t.Context(), "notes"); !errors.Is(err, project.ErrNoRoot) {
		t.Errorf("Create() without roots error = %v, want ErrNoRoot", err)
	}
}

func TestCloneNamesTheProjectAfterTheRepository(t *testing.T) {
	base := realTempDir(t)
	source := initRepository(t, filepath.Join(base, "upstream", "widget"))
	bare := filepath.Join(base, "remote", "widget.git")
	runGit(t, base, "clone", "-q", "--bare", source, bare)

	root := mkdir(t, filepath.Join(base, "code"))
	catalog := newCatalog(t, []string{root}, nil)

	cloned, err := catalog.Clone(t.Context(), "file://"+bare+"/")
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	if cloned.Name != "widget" || cloned.Root != filepath.Join(root, "widget") {
		t.Fatalf("Clone() = %+v, want widget under the root", cloned)
	}

	if _, err := catalog.Clone(t.Context(), "file://"+bare); !errors.Is(err, project.ErrExists) {
		t.Fatalf("second Clone() error = %v, want ErrExists", err)
	}
}
