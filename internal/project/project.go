// Package project decides which project a directory belongs to and lists the known projects.
// A project is a git repository, with its linked worktrees, or a folder registered in config.
package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/session"
)

const (
	KindGit    Kind = "git"
	KindFolder Kind = "folder"

	gitDirName = ".git"
)

var (
	ErrNotProject  = errors.New("directory does not belong to a project")
	ErrPathInvalid = errors.New("project path must be absolute or start with ~/")
)

type Kind string

type repository struct {
	toplevel string
	root     string
}

// Project is identified by Root: the main checkout of a repository, or a registered folder.
type Project struct {
	Root string `json:"root"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
}

// Catalog resolves directories to projects and lists projects under the configured roots.
type Catalog struct {
	roots []string
	extra []string
}

func NewCatalog(cfg config.ProjectsConfig) (Catalog, error) {
	roots, err := expandAll(cfg.Roots)
	if err != nil {
		return Catalog{}, err
	}

	extra, err := expandAll(cfg.Extra)
	if err != nil {
		return Catalog{}, err
	}

	return Catalog{roots: roots, extra: extra}, nil
}

// Resolve returns the project dir belongs to. The most specific match wins: a registered
// folder inside a repository owns the paths below it, and a repository inside a registered
// folder owns its own paths. Linked worktrees always resolve to their main checkout.
func (c Catalog) Resolve(ctx context.Context, dir string) (Project, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Project{}, oops.In("project").Code("project_resolve_failed").With("operation", "resolve_project").Wrap(err)
	}

	folder, hasFolder := c.registeredFolder(resolved)

	repository, hasRepository, err := repositoryOf(ctx, resolved)
	if err != nil {
		return Project{}, err
	}

	switch {
	case hasFolder && (!hasRepository || within(folder, repository.toplevel)):
		return folderProject(folder), nil
	case hasRepository:
		return gitProject(repository.root), nil
	default:
		return Project{}, ErrNotProject
	}
}

// List returns the repositories directly under each root and the registered folders, sorted
// by name. Linked worktrees and hidden directories are not projects of their own.
func (c Catalog) List() ([]Project, error) {
	seen := make(map[string]bool)
	projects := make([]Project, 0)

	for _, root := range c.roots {
		found, err := repositoriesUnder(root)
		if err != nil {
			return nil, err
		}

		for _, project := range found {
			if !seen[project.Root] {
				seen[project.Root] = true
				projects = append(projects, project)
			}
		}
	}

	for _, folder := range c.extra {
		resolved, err := filepath.EvalSymlinks(folder)
		if err != nil {
			return nil, oops.In("project").Code("project_list_failed").With("operation", "list_registered_folder").Wrap(err)
		}

		if !seen[resolved] {
			seen[resolved] = true
			projects = append(projects, folderProject(resolved))
		}
	}

	slices.SortFunc(projects, func(left, right Project) int {
		return strings.Compare(left.Name+"\x00"+left.Root, right.Name+"\x00"+right.Root)
	})

	return projects, nil
}

func (c Catalog) registeredFolder(dir string) (string, bool) {
	best := ""

	for _, folder := range c.extra {
		resolved, err := filepath.EvalSymlinks(folder)
		if err != nil {
			continue
		}

		if within(dir, resolved) && len(resolved) > len(best) {
			best = resolved
		}
	}

	return best, best != ""
}

func repositoryOf(ctx context.Context, dir string) (repository, bool, error) {
	output, err := git.Output(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if _, failed := errors.AsType[*git.CommandError](err); failed {
		return repository{toplevel: "", root: ""}, false, nil
	}

	if err != nil {
		return repository{toplevel: "", root: ""}, false, oops.In("project").Code("project_resolve_failed").With("operation", "query_repository").Wrap(err)
	}

	toplevel, commonDir, found := strings.Cut(output, "\n")
	if !found || toplevel == "" {
		return repository{toplevel: "", root: ""}, false, nil
	}

	root := toplevel
	if filepath.Base(commonDir) == gitDirName {
		root = filepath.Dir(commonDir)
	}

	return repository{toplevel: toplevel, root: root}, true, nil
}

func repositoriesUnder(root string) ([]Project, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, oops.In("project").Code("project_list_failed").With("operation", "list_root").Wrap(err)
	}

	projects := make([]Project, 0, len(entries))

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		dir := filepath.Join(root, entry.Name())

		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		gitInfo, err := os.Lstat(filepath.Join(dir, gitDirName))
		if errors.Is(err, fs.ErrNotExist) || (err == nil && !gitInfo.IsDir()) {
			continue
		}

		if err != nil {
			return nil, oops.In("project").Code("project_list_failed").With("operation", "inspect_repository").Wrap(err)
		}

		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, oops.In("project").Code("project_list_failed").With("operation", "inspect_repository").Wrap(err)
		}

		projects = append(projects, gitProject(resolved))
	}

	return projects, nil
}

func gitProject(root string) Project {
	return Project{Root: root, Name: filepath.Base(root), Kind: KindGit}
}

func folderProject(root string) Project {
	return Project{Root: root, Name: filepath.Base(root), Kind: KindFolder}
}

func expandAll(paths []string) ([]string, error) {
	expanded := make([]string, 0, len(paths))

	for _, path := range paths {
		value, _ := session.ExpandHomePath(path)
		if !filepath.IsAbs(value) {
			return nil, fmt.Errorf("%w: %s", ErrPathInvalid, path)
		}

		expanded = append(expanded, filepath.Clean(value))
	}

	return expanded, nil
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}
