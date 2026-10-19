package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/git"
)

const projectDirMode = 0o755

var (
	ErrNoRoot      = errors.New("no project root is configured")
	ErrNameInvalid = errors.New("project name must be a plain directory name")
	ErrExists      = errors.New("project directory already exists")
)

func (c Catalog) Clone(ctx context.Context, url string) (Project, error) {
	dir, err := c.newDirectory(repositoryName(url))
	if err != nil {
		return Project{}, err
	}

	if _, err := git.Output(ctx, filepath.Dir(dir), "clone", "--quiet", "--", url, dir); err != nil {
		return Project{}, oops.In("project").Code("project_clone_failed").With("operation", "clone_project").Wrap(err)
	}

	return gitProject(dir), nil
}

func (c Catalog) Create(ctx context.Context, name string) (Project, error) {
	dir, err := c.newDirectory(name)
	if err != nil {
		return Project{}, err
	}

	if err := os.Mkdir(dir, projectDirMode); err != nil {
		return Project{}, oops.In("project").Code("project_create_failed").With("operation", "create_project").Wrap(err)
	}

	if _, err := git.Output(ctx, dir, "init", "--quiet"); err != nil {
		return Project{}, oops.In("project").Code("project_create_failed").With("operation", "init_project").Wrap(err)
	}

	return gitProject(dir), nil
}

func (c Catalog) newDirectory(name string) (string, error) {
	if len(c.roots) == 0 {
		return "", ErrNoRoot
	}

	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\x00") || containsControl(name) {
		return "", fmt.Errorf("%w: %q", ErrNameInvalid, name)
	}

	root, err := filepath.EvalSymlinks(c.roots[0])
	if err != nil {
		return "", oops.In("project").Code("project_create_failed").With("operation", "resolve_root").Wrap(err)
	}

	dir := filepath.Join(root, name)
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrExists, name)
	}

	return dir, nil
}

func repositoryName(url string) string {
	// Matches the directory name git clone itself would choose.
	trimmed := strings.TrimRight(url, "/")
	if index := strings.LastIndexAny(trimmed, "/:"); index >= 0 {
		trimmed = trimmed[index+1:]
	}

	return strings.TrimSuffix(trimmed, ".git")
}

func containsControl(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r == 0x7f })
}
