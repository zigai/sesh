package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/project"
)

const (
	// WhereNew is a new worktree on a new branch that starts from Branch.
	WhereNew Where = "new"
	// WhereBranch is a new worktree that checks out Branch: a local branch, or one only Remote
	// has, which then gets a local branch tracking it.
	WhereBranch Where = "branch"
	// WhereWorktree is an existing worktree of the project, made by sesh or by hand.
	WhereWorktree Where = "worktree"
	// WhereMain is the project's main checkout on whatever branch it has checked out.
	WhereMain Where = "main"

	maxSlugRunes = 48
)

var ErrLocationUnavailable = errors.New("that checkout or branch is no longer free in this project")

// Where is the kind of place a project task works in.
type Where string

// Location is a place a project task can work in. Branch is the branch checked out there, the
// branch a new worktree checks out, or the base a new branch starts from; Remote names the remote
// holding a branch no local branch has; Directory names an existing worktree or the main
// checkout. Task and TaskTitle name the task that already works in a checkout, which then cannot
// take another.
type Location struct {
	Where     Where
	Branch    string
	Remote    string
	Directory string
	Task      string
	TaskTitle string
}

// Request is what a new task starts with: its harness, an optional title that names it and is
// the agent's first prompt, and, for a project task, its location.
type Request struct {
	Harness  string
	Title    string
	Location Location
}

// Offer is what a new task can start with at a place: the offered harnesses, the root of the
// place's project with its locations, and the index of the checkout the place is in. NextNumber and Branches are what a new worktree's branch is named
// from. Outside a project Project is empty and there are no locations.
type Offer struct {
	Harnesses  []Harness
	Project    string
	Locations  []Location
	Current    int
	NextNumber int
	Branches   []string
}

type worktree struct {
	directory string
	branch    string
	prunable  bool
}

// branches are a project's local branch names and the branches only a remote has, as
// remote and branch pairs.
type branches struct {
	local  []string
	remote [][2]string
}

// Offer lists the harnesses and, in a project session or a repository, the locations a new task
// can start with. A directory holds at most one task, so a checkout a task works in is offered
// only to go to that task.
func (s *Service) Offer(ctx context.Context, place Place) (Offer, error) {
	ledger, err := s.store.Load(ctx)
	if err != nil {
		return emptyOffer(), taskError("offer_task", err)
	}

	root, err := s.projectOf(ctx, place)
	if err != nil {
		return emptyOffer(), taskError("offer_task", err)
	}

	state := ledger.Project(root)
	offer := emptyOffer()
	offer.Harnesses = Harnesses(s.cfg, s.lookPath)
	offer.Project = root

	if root == "" {
		return offer, nil
	}

	p, err := s.catalog.Resolve(ctx, root)
	if err != nil {
		return offer, taskError("offer_task", err)
	}

	offer.Locations, offer.Branches, err = s.locations(ctx, p, ledger, place.Dir)
	if err != nil {
		return offer, taskError("offer_task", err)
	}

	offer.Current = currentCheckout(offer.Locations, place.Dir)

	if len(offer.Locations) > 0 && offer.Locations[0].Where == WhereNew && s.worktreeDir != "" {
		offer.NextNumber, _, err = s.freeNumber(ctx, p, offer.Locations[0].Branch, state.LastNumber)
		if err != nil {
			return offer, taskError("offer_task", err)
		}
	}

	return offer, nil
}

// locations lists, in order: new worktrees from the default branch and from the branch checked
// out at dir, the main checkout and the other worktrees, local branches no worktree has checked
// out, branches only a remote has, and new worktrees from every other local branch. It also
// returns the local branch names.
func (s *Service) locations(ctx context.Context, p project.Project, ledger Ledger, dir string) ([]Location, []string, error) {
	if p.Kind != project.KindGit {
		id, title := taskIn(ledger, p.Root)

		return []Location{{Where: WhereMain, Branch: "", Remote: "", Directory: p.Root, Task: id, TaskTitle: title}}, nil, nil
	}

	base, err := defaultBranch(ctx, p.Root)
	if err != nil {
		return nil, nil, err
	}

	worktrees, err := listWorktrees(ctx, p.Root)
	if err != nil {
		return nil, nil, err
	}

	known, err := listBranches(ctx, p.Root)
	if err != nil {
		return nil, nil, err
	}

	existing, current := checkouts(p.Root, worktrees, ledger, dir)
	bases := []string{base}

	checkedOut := make([]string, 0, len(worktrees))
	for _, tree := range worktrees {
		checkedOut = append(checkedOut, tree.branch)
	}

	if current != "" && current != base {
		bases = append(bases, current)
	}

	locations := make([]Location, 0, len(bases)+len(existing)+len(known.local)*2+len(known.remote))

	for _, branch := range bases {
		locations = append(locations, newWorktreeFrom(branch))
	}

	locations = append(locations, existing...)

	return append(locations, branchLocations(known, checkedOut, bases)...), known.local, nil
}

// offered returns the location offered at root that a request names. A new worktree needs only
// its kind and base, the default branch when none is named; a branch needs its name and remote;
// an existing checkout its directory, and the main checkout may be named by kind alone. A
// checkout another task works in is not offered.
func (s *Service) offered(ctx context.Context, p project.Project, requested Location) (Location, []string, error) {
	ledger, err := s.store.Load(ctx)
	if err != nil {
		return emptyLocation(), nil, err
	}

	locations, local, err := s.locations(ctx, p, ledger, "")
	if err != nil {
		return emptyLocation(), nil, err
	}

	index := slices.IndexFunc(locations, func(l Location) bool { return l.Task == "" && l.names(requested) })
	if index < 0 {
		return emptyLocation(), nil, ErrLocationUnavailable
	}

	return locations[index], local, nil
}

// names reports whether a requested location names l.
func (l Location) names(requested Location) bool {
	if l.Where != requested.Where {
		return false
	}

	switch l.Where {
	case WhereNew:
		return requested.Branch == "" || l.Branch == requested.Branch
	case WhereBranch:
		return l.Branch == requested.Branch && l.Remote == requested.Remote
	case WhereWorktree:
		return sameDirectory(l.Directory, requested.Directory)
	case WhereMain:
		return requested.Directory == "" || sameDirectory(l.Directory, requested.Directory)
	default:
		return false
	}
}

// NewBranch is the branch a new worktree gets: wt/ and the title as a slug, or without a title
// wt/, the base and the task number. A name taken by a local branch gets the lowest free numeric
// suffix.
func NewBranch(title, base string, number int, taken []string) string {
	name := taskBranch(base, number)
	if slug := slug(title); slug != "" {
		name = branchPrefix + slug
	}

	candidate := name
	for suffix := 2; slices.Contains(taken, candidate); suffix++ {
		candidate = name + "-" + strconv.Itoa(suffix)
	}

	return candidate
}

// currentCheckout returns the index of the innermost checkout that holds dir, else the main
// checkout's.
func currentCheckout(locations []Location, dir string) int {
	current, depth := -1, -1

	for index, location := range locations {
		if location.Where != WhereMain && location.Where != WhereWorktree {
			continue
		}

		if within(dir, location.Directory) && len(location.Directory) > depth {
			current, depth = index, len(location.Directory)
		}
	}

	if current < 0 {
		current = slices.IndexFunc(locations, func(l Location) bool { return l.Where == WhereMain })
	}

	return current
}

// slug turns a title into a branch name part: lower case letters and digits joined by single
// dashes, cut to a bounded length.
func slug(title string) string {
	var out strings.Builder

	dash := false

	for _, r := range strings.ToLower(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && out.Len() > 0 {
				out.WriteByte('-')
			}

			out.WriteRune(r)

			dash = false
		default:
			dash = true
		}

		if out.Len() >= maxSlugRunes {
			break
		}
	}

	return out.String()
}

// branchLocations lists the branches a new worktree can check out, local ones before ones only a
// remote has, and then new worktrees from the local branches that are not bases already.
func branchLocations(known branches, checkedOut, bases []string) []Location {
	locations := make([]Location, 0, len(known.local)*2+len(known.remote))

	for _, branch := range known.local {
		if !slices.Contains(checkedOut, branch) {
			locations = append(locations, Location{Where: WhereBranch, Branch: branch, Remote: "", Directory: "", Task: "", TaskTitle: ""})
		}
	}

	for _, pair := range known.remote {
		locations = append(locations, Location{Where: WhereBranch, Branch: pair[1], Remote: pair[0], Directory: "", Task: "", TaskTitle: ""})
	}

	for _, branch := range known.local {
		if !slices.Contains(bases, branch) {
			locations = append(locations, newWorktreeFrom(branch))
		}
	}

	return locations
}

// checkouts lists the main checkout and the worktrees that still exist, with the task working in
// each, and returns the branch checked out where dir is.
func checkouts(root string, worktrees []worktree, ledger Ledger, dir string) ([]Location, string) {
	current := ""
	existing := make([]Location, 0, len(worktrees))

	for _, tree := range worktrees {
		if tree.prunable {
			continue
		}

		if within(dir, tree.directory) {
			current = tree.branch
		}

		where := WhereWorktree
		if sameDirectory(tree.directory, root) {
			where = WhereMain
		}

		id, title := taskIn(ledger, tree.directory)
		existing = append(existing, Location{Where: where, Branch: tree.branch, Remote: "", Directory: tree.directory, Task: id, TaskTitle: title})
	}

	return existing, current
}

// taskIn returns the ID and title of the task that works in directory.
func taskIn(ledger Ledger, directory string) (string, string) {
	index := slices.IndexFunc(ledger.Tasks, func(record Record) bool { return sameDirectory(record.Directory, directory) })
	if index < 0 {
		return "", ""
	}

	return ledger.Tasks[index].ID, ledger.Tasks[index].Title
}

func newWorktreeFrom(branch string) Location {
	return Location{Where: WhereNew, Branch: branch, Remote: "", Directory: "", Task: "", TaskTitle: ""}
}

func emptyLocation() Location {
	return Location{Where: "", Branch: "", Remote: "", Directory: "", Task: "", TaskTitle: ""}
}

func emptyOffer() Offer {
	return Offer{
		Harnesses: nil, Project: "", Locations: nil, Current: -1, NextNumber: 0, Branches: nil,
	}
}

// listBranches lists the local branches and the remote branches no local branch shares a name
// with, leaving out each remote's HEAD.
func listBranches(ctx context.Context, root string) (branches, error) {
	output, err := git.Output(ctx, root, "for-each-ref", "--format=%(refname)%09%(symref)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return branches{}, fmt.Errorf("list branches: %w", err)
	}

	var found branches

	var remotes [][2]string

	for line := range strings.Lines(output) {
		ref, symref, _ := strings.Cut(strings.TrimRight(line, "\n"), "\t")
		if symref != "" {
			continue
		}

		if branch, local := strings.CutPrefix(ref, "refs/heads/"); local {
			found.local = append(found.local, branch)
		} else if remote, branch, ok := strings.Cut(strings.TrimPrefix(ref, "refs/remotes/"), "/"); ok {
			remotes = append(remotes, [2]string{remote, branch})
		}
	}

	for _, pair := range remotes {
		if !slices.Contains(found.local, pair[1]) {
			found.remote = append(found.remote, pair)
		}
	}

	return found, nil
}

// listWorktrees parses `git worktree list --porcelain`: blocks of attribute lines, one block per
// worktree, the main checkout first.
func listWorktrees(ctx context.Context, root string) ([]worktree, error) {
	output, err := git.Output(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	var worktrees []worktree

	for line := range strings.Lines(output) {
		line = strings.TrimRight(line, "\n")

		if directory, found := strings.CutPrefix(line, "worktree "); found {
			worktrees = append(worktrees, worktree{directory: directory, branch: "", prunable: false})

			continue
		}

		if len(worktrees) == 0 {
			continue
		}

		current := &worktrees[len(worktrees)-1]

		if branch, found := strings.CutPrefix(line, "branch refs/heads/"); found {
			current.branch = branch
		}

		if line == "prunable" || strings.HasPrefix(line, "prunable ") {
			current.prunable = true
		}
	}

	for index := range worktrees {
		if _, err := os.Stat(worktrees[index].directory); err != nil {
			worktrees[index].prunable = true
		}
	}

	return worktrees, nil
}

// within reports whether dir is directory or inside it. A worktree placed inside the main
// checkout matches both; git lists the main checkout first, so the last match is the closest.
func within(dir, directory string) bool {
	if dir == "" || directory == "" {
		return false
	}

	relative, err := filepath.Rel(resolved(directory), resolved(dir))

	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// sameDirectory compares two directories after resolving symlinks, since git reports resolved
// paths while project roots may be configured through a link.
func sameDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	return resolved(a) == resolved(b)
}

func resolved(path string) string {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target
	}

	return filepath.Clean(path)
}
