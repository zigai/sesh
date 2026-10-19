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
	"sync"
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
	// Layout names the task window's layout; empty picks the first that fits the window.
	Layout string
}

// Offer is what a new task can start with at a place: the offered harnesses, the root of the
// place's project with its locations, and the index of the checkout the place is in. NextNumber
// and Branches are what a new worktree's branch is named from. Outside a project Project is
// empty and there are no locations.
type Offer struct {
	Harnesses  []Harness
	Project    string
	Locations  []Location
	Current    int
	NextNumber int
	Branches   []string
	// Refs describes the project's branches by full ref name, and Starts names the ref a new
	// worktree from each base starts from.
	Refs   map[string]Ref
	Starts map[string]string
}

// Ref is a branch's latest commit, as a Unix time and subject, and how many commits the branch
// has that the default branch lacks and the other way round.
type Ref struct {
	Time    int64
	Subject string
	Ahead   int
	Behind  int
}

type remoteBranch struct {
	remote string
	name   string
}

type refLine struct {
	name        string
	symref      string
	committed   string
	aheadBehind string
	subject     string
}

type worktree struct {
	directory string
	branch    string
	prunable  bool
}

// branches are a project's local branch names and the branches only a remote has, most
// recently committed first, and every branch's latest commit.
type branches struct {
	local  []string
	remote []remoteBranch
	refs   map[string]Ref
	starts map[string]string
}

// Offer lists the harnesses and, in a project session or a repository, the locations a new task
// can start with, with each branch's latest commit and distance from the default branch when
// describe is set. A directory holds at most one task, so a checkout a task works in is offered
// only to go to that task.
func (s *Service) Offer(ctx context.Context, place Place, describe bool) (Offer, error) {
	ledger, err := s.store.Load(ctx)
	if err != nil {
		return emptyOffer(), taskError("offer_task", err)
	}

	root, err := s.gitProjectRoot(ctx, place)
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

	var known branches

	offer.Locations, known, err = s.locations(ctx, p, ledger, place.Dir, describe)
	if err != nil {
		return offer, taskError("offer_task", err)
	}

	offer.Branches, offer.Refs, offer.Starts = known.local, known.refs, known.starts

	offer.Current = currentCheckout(offer.Locations, place.Dir)

	if len(offer.Locations) > 0 && offer.Locations[0].Where == WhereNew {
		offer.NextNumber, _, err = s.freeNumber(ctx, p, offer.Locations[0].Branch, state.LastNumber)
		if err != nil {
			return offer, taskError("offer_task", err)
		}
	}

	return offer, nil
}

func (s *Service) locations(ctx context.Context, p project.Project, ledger Ledger, dir string, describe bool) ([]Location, branches, error) {
	if p.Kind != project.KindGit {
		id, title := taskIn(ledger, p.Root)

		return []Location{{Where: WhereMain, Branch: "", Remote: "", Directory: p.Root, Task: id, TaskTitle: title}}, noBranches(), nil
	}

	base, err := defaultBranch(ctx, p.Root)
	if err != nil {
		return nil, noBranches(), err
	}

	worktrees, err := listWorktrees(ctx, p.Root)
	if err != nil {
		return nil, noBranches(), err
	}

	known, err := listBranches(ctx, p.Root, base, describe)
	if err != nil {
		return nil, noBranches(), err
	}

	existing := checkouts(p.Root, worktrees, ledger)
	bases := []string{base}

	checkedOut := make([]string, 0, len(worktrees))
	for _, tree := range worktrees {
		checkedOut = append(checkedOut, tree.branch)
	}

	if index := innermostCheckout(existing, dir); index >= 0 && existing[index].Branch != "" && existing[index].Branch != base {
		bases = append(bases, existing[index].Branch)
	}

	locations := make([]Location, 0, len(bases)+len(existing)+len(known.local)*2+len(known.remote))

	for _, branch := range bases {
		locations = append(locations, newWorktreeFrom(branch))
	}

	locations = append(locations, existing...)
	locations = append(locations, branchLocations(known, checkedOut, bases)...)

	if describe {
		known.starts, err = startPoints(ctx, p.Root, locations)
		if err != nil {
			return nil, noBranches(), err
		}
	}

	return locations, known, nil
}

func (s *Service) offered(ctx context.Context, p project.Project, requested Location) (Location, []string, error) {
	ledger, err := s.store.Load(ctx)
	if err != nil {
		return emptyLocation(), nil, err
	}

	locations, known, err := s.locations(ctx, p, ledger, "", false)
	if err != nil {
		return emptyLocation(), nil, err
	}

	index := slices.IndexFunc(locations, func(l Location) bool { return l.Task == "" && l.names(requested) })
	if index < 0 {
		return emptyLocation(), nil, ErrLocationUnavailable
	}

	return locations[index], known.local, nil
}

func (l Location) createsWorktree() bool {
	return l.Where == WhereNew || l.Where == WhereBranch
}

func (l Location) createsBranch() bool {
	// A branch only a remote has gets a local branch tracking it.
	return l.Where == WhereNew || l.Remote != ""
}

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

// Changes counts the lines each checkout's uncommitted changes add and delete, as agent rows do,
// leaving out a checkout git cannot read.
func Changes(ctx context.Context, directories []string) map[string]git.Diff {
	var (
		mutex   sync.Mutex
		group   sync.WaitGroup
		changes = make(map[string]git.Diff, len(directories))
	)

	for _, directory := range directories {
		group.Go(func() {
			output, err := git.Output(ctx, directory, "diff", "--no-ext-diff", "--no-textconv", "--shortstat", "HEAD", "--")
			if err != nil {
				return
			}

			diff, ok := git.ParseShortStat(output)
			if !ok {
				return
			}

			mutex.Lock()
			defer mutex.Unlock()

			changes[directory] = diff
		})
	}

	group.Wait()

	return changes
}

func currentCheckout(locations []Location, dir string) int {
	if current := innermostCheckout(locations, dir); current >= 0 {
		return current
	}

	return slices.IndexFunc(locations, func(l Location) bool { return l.Where == WhereMain })
}

func innermostCheckout(locations []Location, dir string) int {
	current, depth := -1, -1

	for index, location := range locations {
		if location.Where != WhereMain && location.Where != WhereWorktree {
			continue
		}

		if within(dir, location.Directory) && len(resolved(location.Directory)) > depth {
			current, depth = index, len(resolved(location.Directory))
		}
	}

	return current
}

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

func branchLocations(known branches, checkedOut, bases []string) []Location {
	locations := make([]Location, 0, len(known.local)*2+len(known.remote))

	for _, branch := range known.local {
		if !slices.Contains(checkedOut, branch) {
			locations = append(locations, Location{Where: WhereBranch, Branch: branch, Remote: "", Directory: "", Task: "", TaskTitle: ""})
		}
	}

	for _, branch := range known.remote {
		locations = append(locations, Location{Where: WhereBranch, Branch: branch.name, Remote: branch.remote, Directory: "", Task: "", TaskTitle: ""})
	}

	for _, branch := range known.local {
		if !slices.Contains(bases, branch) {
			locations = append(locations, newWorktreeFrom(branch))
		}
	}

	return locations
}

func checkouts(root string, worktrees []worktree, ledger Ledger) []Location {
	existing := make([]Location, 0, len(worktrees))

	for _, tree := range worktrees {
		if tree.prunable {
			continue
		}

		where := WhereWorktree
		if sameDirectory(tree.directory, root) {
			where = WhereMain
		}

		id, title := taskIn(ledger, tree.directory)
		existing = append(existing, Location{Where: where, Branch: tree.branch, Remote: "", Directory: tree.directory, Task: id, TaskTitle: title})
	}

	return existing
}

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

func noBranches() branches {
	return branches{local: nil, remote: nil, refs: nil, starts: nil}
}

func emptyOffer() Offer {
	return Offer{
		Harnesses: nil, Project: "", Locations: nil, Current: -1, NextNumber: 0, Branches: nil, Refs: nil, Starts: nil,
	}
}

// listBranches lists the local branches and the remote branches no local branch shares a name
// with, most recently committed first, and when describe is set their latest commits and how far
// each is from base.
func listBranches(ctx context.Context, root, base string, describe bool) (branches, error) {
	aheadBehind := ""

	if describe {
		var err error

		aheadBehind, err = aheadBehindFormat(ctx, root, base)
		if err != nil {
			return noBranches(), err
		}
	}

	format := []string{"%(refname)", "%(symref)", "%(committerdate:unix)", aheadBehind, "%(contents:subject)"}

	output, err := git.Output(ctx, root, "for-each-ref", "--sort=-committerdate",
		"--format="+strings.Join(format, "%09")+"%00", "refs/heads/", "refs/remotes/")
	if err != nil {
		return branches{}, fmt.Errorf("list branches: %w", err)
	}

	found := branches{local: nil, remote: nil, refs: make(map[string]Ref), starts: nil}

	var remotes []remoteBranch

	for line := range strings.Lines(output) {
		record, _ := strings.CutSuffix(strings.TrimRight(line, "\n"), "\x00")

		fields := strings.SplitN(record, "\t", len(format))
		if len(fields) != len(format) {
			continue
		}

		ref := refLine{name: fields[0], symref: fields[1], committed: fields[2], aheadBehind: fields[3], subject: fields[4]}
		if ref.isSymref() {
			continue
		}

		if branch, local := strings.CutPrefix(ref.name, "refs/heads/"); local {
			found.local = append(found.local, branch)
		} else if remote, branch, ok := strings.Cut(strings.TrimPrefix(ref.name, "refs/remotes/"), "/"); ok {
			remotes = append(remotes, remoteBranch{remote: remote, name: branch})
		}

		if describe {
			found.refs[ref.name] = ref.ref()
		}
	}

	for _, branch := range remotes {
		if !slices.Contains(found.local, branch.name) {
			found.remote = append(found.remote, branch)
		}
	}

	return found, nil
}

func (r refLine) isSymref() bool {
	return r.symref != ""
}

func (r refLine) ref() Ref {
	committed, _ := strconv.ParseInt(r.committed, 10, 64)
	aheadText, behindText, _ := strings.Cut(r.aheadBehind, " ")
	ahead, _ := strconv.Atoi(aheadText)
	behind, _ := strconv.Atoi(behindText)

	return Ref{Time: committed, Subject: r.subject, Ahead: ahead, Behind: behind}
}

func aheadBehindFormat(ctx context.Context, root, base string) (string, error) {
	for _, ref := range []string{"refs/heads/" + base, "refs/remotes/origin/" + base} {
		exists, err := refExists(ctx, root, ref)
		if err != nil {
			return "", err
		}

		if exists {
			return "%(ahead-behind:" + ref + ")", nil
		}
	}

	return "", nil
}

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

func startPoints(ctx context.Context, root string, locations []Location) (map[string]string, error) {
	starts := make(map[string]string)

	for _, location := range locations {
		if location.Where != WhereNew {
			continue
		}

		if _, seen := starts[location.Branch]; seen {
			continue
		}

		start, err := startPoint(ctx, root, location.Branch)
		if err != nil {
			return nil, err
		}

		starts[location.Branch] = start
	}

	return starts, nil
}
