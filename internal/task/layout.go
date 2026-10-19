package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
)

const (
	OptionRestart = "@sesh_restart"

	percentOf          = 100
	borderCells        = 1
	halves             = 2
	minSplitCells      = halves + borderCells
	checksumTop        = 15
	cellHeightPerWidth = 2
)

const (
	rearrangeWindow arrangeMode = iota
	arrangeNewWindow
)

var (
	ErrUnknownLayout   = errors.New("no task layout has that name")
	ErrWindowTooSmall  = errors.New("the task window is too small for its layout")
	ErrNoAgentPane     = errors.New("the task window has no agent pane")
	ErrNotInTaskWindow = errors.New("the pane is not in a task window")
	ErrPaneDirectory   = errors.New("a task pane's directory does not exist")
)

type arrangeMode int

type restartCommand struct {
	Directory string   `json:"directory"`
	Argv      []string `json:"argv"`
}

type cell struct {
	pane       string
	sideBySide bool
	children   []*cell
	parent     *cell
	x, y       int
	w, h       int
}

type windowPane struct {
	id     gotmux.PaneID
	role   string
	width  int
	height int
}

// Relayout arranges the task window of the pane at place again, for the window's current size:
// with the named layout, else the first whose conditions the window meets. Panes the layout
// places that the window lacks are opened again, and panes it does not place are kept.
func (s *Service) Relayout(ctx context.Context, place Place, name string) error {
	if place.Task == "" {
		return taskError("relayout_task", ErrNotInTaskWindow)
	}

	record, err := s.record(ctx, place.Task)
	if err != nil {
		return taskError("relayout_task", err)
	}

	pane, err := s.server.Pane(ctx, place.Pane)
	if err != nil {
		return taskError("relayout_task", fmt.Errorf("find pane: %w", err))
	}

	window, err := pane.Window(ctx)
	if err != nil {
		return taskError("relayout_task", fmt.Errorf("find task window: %w", err))
	}

	return taskError("relayout_task", s.arrange(ctx, window, record, name, rearrangeWindow, nil))
}

// arrange opens the panes the task window's layout places that it lacks, then lays the window
// out. The layout is picked for shownAt, else the window's own size. The whole layout is
// planned before any pane opens, so a window too small for it is left untouched.
func (s *Service) arrange(
	ctx context.Context,
	window gotmux.Window,
	record Record,
	name string,
	mode arrangeMode,
	shownAt *gotmux.Size,
) error {
	info, err := window.Info(ctx)
	if err != nil {
		return fmt.Errorf("inspect task window: %w", err)
	}

	width, height := info.Width, info.Height
	if shownAt != nil {
		width, height = shownAt.Width, shownAt.Height
	}

	setup := s.windows(record.Project)

	layout, err := chooseLayout(setup, record.Harness, width, height, name)
	if err != nil {
		return err
	}

	panes, err := s.windowPanes(ctx, window)
	if err != nil {
		return err
	}

	layout, err = fitting(setup, layout, panes, info.Width, info.Height, mode)
	if err != nil {
		return err
	}

	placed := setup.Placed(layout)

	if err := s.openPanes(ctx, window, record, setup, placed, panes); err != nil {
		return err
	}

	panes, err = s.windowPanes(ctx, window)
	if err != nil {
		return err
	}

	if err := applyLayout(ctx, s.server, window, layout, placed, panes, info.Width, info.Height); err != nil {
		return err
	}

	if mode != arrangeNewWindow {
		return nil
	}

	return s.focus(ctx, panes, layout.Focus)
}

// fitting checks that a splits layout fits the window with every pane it will hold. One that
// does not falls back to the default arrangement in a new window and fails otherwise.
func fitting(setup config.TaskWindow, layout config.TaskLayoutConfig, panes []windowPane, width, height int, mode arrangeMode) (config.TaskLayoutConfig, error) {
	if len(layout.Splits) == 0 {
		return layout, nil
	}

	placed := setup.Placed(layout)
	if _, err := splitCells(width, height, layout.Splits, extraKeys(panes, paneKeys(panes, placed), placed)); err != nil {
		if mode != arrangeNewWindow || !errors.Is(err, ErrWindowTooSmall) {
			return layout, err
		}

		return defaultArrangement(), nil
	}

	return layout, nil
}

func (s *Service) focus(ctx context.Context, panes []windowPane, role string) error {
	index := slices.IndexFunc(panes, func(pane windowPane) bool { return pane.role == role })
	if role == "" || role == config.TaskPaneAgent || index < 0 {
		return nil
	}

	focus, err := s.server.Pane(ctx, panes[index].id)
	if err != nil {
		return fmt.Errorf("find focus pane: %w", err)
	}

	if err := focus.Select(ctx); err != nil {
		return fmt.Errorf("focus pane: %w", err)
	}

	return nil
}

func (s *Service) windowPanes(ctx context.Context, window gotmux.Window) ([]windowPane, error) {
	infos, err := window.PanesWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{OptionRole}})
	if err != nil {
		return nil, fmt.Errorf("list task panes: %w", err)
	}

	slices.SortFunc(infos, func(a, b gotmux.PaneInfo) int { return a.Index - b.Index })

	panes := make([]windowPane, 0, len(infos))
	for _, info := range infos {
		role, _ := info.Raw(OptionRole)
		panes = append(panes, windowPane{id: info.ID, role: string(role), width: info.Width, height: info.Height})
	}

	if !slices.ContainsFunc(panes, func(pane windowPane) bool { return pane.role == config.TaskPaneAgent }) {
		return nil, ErrNoAgentPane
	}

	return panes, nil
}

func (s *Service) openPanes(
	ctx context.Context,
	window gotmux.Window,
	record Record,
	setup config.TaskWindow,
	placed []string,
	panes []windowPane,
) error {
	for _, name := range placed {
		if slices.ContainsFunc(panes, func(pane windowPane) bool { return pane.role == name }) {
			continue
		}

		target := slices.MaxFunc(panes, func(a, b windowPane) int { return a.width*a.height - b.width*b.height })

		direction := gotmux.DirectionVertical
		if splitSideBySide(target.width, target.height) {
			direction = gotmux.DirectionHorizontal
		}

		configured, _ := setup.Pane(name)
		if err := s.openPane(ctx, target.id, record, configured, direction); err != nil {
			return err
		}

		var err error

		panes, err = s.windowPanes(ctx, window)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) openPane(
	ctx context.Context,
	target gotmux.PaneID,
	record Record,
	configured config.TaskPaneConfig,
	direction gotmux.Direction,
) error {
	shell, err := defaultShell(ctx, s.server)
	if err != nil {
		return err
	}

	pane, err := s.server.Pane(ctx, target)
	if err != nil {
		return fmt.Errorf("find pane to split: %w", err)
	}

	command := make([]string, 0, len(configured.Command))
	for _, argument := range configured.Command {
		command = append(command, Render(argument, record))
	}

	directory := record.Directory
	if dir := Render(configured.Dir, record); dir != "" {
		directory = filepath.Join(record.Directory, dir)
	}

	if stat, err := os.Stat(directory); err != nil || !stat.IsDir() {
		return fmt.Errorf("%w: %s", ErrPaneDirectory, configured.Name)
	}

	created, err := pane.Split(ctx, gotmux.SplitOptions{
		Direction: direction, Size: gotmux.SplitSize{Cells: 0, Percent: 0}, Dir: directory,
		Program: shellProgram(shell, command), Env: nil, TmuxEnv: Env(record), Select: false, Before: false,
		FullSize: false, KillTarget: false, Zoom: false, Title: "", BorderLines: "", Style: "",
		ActiveBorderStyle: "", InactiveBorderStyle: "", Message: "",
	})
	if err != nil {
		return fmt.Errorf("open %s pane: %w", configured.Name, err)
	}

	if err := created.Options().SetUser(ctx, OptionRole, configured.Name); err != nil {
		return fmt.Errorf("tag task pane: %w", err)
	}

	if !configured.Restart {
		return nil
	}

	encoded, err := json.Marshal(restartCommand{Directory: directory, Argv: shellArgv(shell, command)})
	if err != nil {
		return fmt.Errorf("encode restart command: %w", err)
	}

	if err := created.Options().SetUser(ctx, OptionRestart, string(encoded)); err != nil {
		return fmt.Errorf("tag task pane: %w", err)
	}

	return nil
}

func layoutNamed(setup config.TaskWindow, name string) bool {
	return slices.ContainsFunc(setup.Layouts, func(layout config.TaskLayoutConfig) bool { return layout.Name == name })
}

func chooseLayout(setup config.TaskWindow, harness string, width, height int, name string) (config.TaskLayoutConfig, error) {
	if name != "" {
		index := slices.IndexFunc(setup.Layouts, func(layout config.TaskLayoutConfig) bool { return layout.Name == name })
		if index < 0 {
			return defaultArrangement(), fmt.Errorf("%w: %s", ErrUnknownLayout, name)
		}

		return setup.Layouts[index], nil
	}

	for _, layout := range setup.Layouts {
		if layout.When.Matches(width, height, harness) {
			return layout, nil
		}
	}

	return defaultArrangement(), nil
}

func defaultArrangement() config.TaskLayoutConfig {
	return config.TaskLayoutConfig{
		Name: "", When: config.TaskLayoutWhen{Orientation: "", MinWidth: 0, MaxWidth: 0, MinHeight: 0, MaxHeight: 0, Harness: nil},
		Arrange: config.ArrangeMainHorizontal, MainSize: "", Panes: nil, Splits: nil, Focus: "",
	}
}

// paneKeys names each pane in a layout: the agent and each placed pane by its role, every other
// pane, and a second pane with a role already taken, by its pane ID.
func paneKeys(panes []windowPane, placed []string) map[gotmux.PaneID]string {
	keys := make(map[gotmux.PaneID]string, len(panes))
	taken := make(map[string]bool, len(panes))

	for _, pane := range panes {
		key := string(pane.id)
		if (pane.role == config.TaskPaneAgent || slices.Contains(placed, pane.role)) && !taken[pane.role] {
			key = pane.role
		}

		keys[pane.id], taken[key] = key, true
	}

	return keys
}

// extraKeys are the keys of the panes a layout does not place, in window order.
func extraKeys(panes []windowPane, keys map[gotmux.PaneID]string, placed []string) []string {
	var extras []string

	for _, pane := range panes {
		if key := keys[pane.id]; key != config.TaskPaneAgent && !slices.Contains(placed, key) {
			extras = append(extras, key)
		}
	}

	return extras
}

// applyLayout lays a window out. An arrangement orders the agent first, then the panes it
// places, then the others in their order, and applies the tmux layout. Splits, with the other
// panes in the largest cells, are laid out as one custom tmux layout, which tmux fills with the
// window's panes in order.
func applyLayout(
	ctx context.Context,
	server *gotmux.Server,
	window gotmux.Window,
	layout config.TaskLayoutConfig,
	placed []string,
	panes []windowPane,
	width, height int,
) error {
	keys := paneKeys(panes, placed)
	ids := make(map[string]gotmux.PaneID, len(keys))

	for id, key := range keys {
		ids[key] = id
	}

	if len(layout.Splits) == 0 {
		order := append([]string{config.TaskPaneAgent}, placed...)
		order = append(order, extraKeys(panes, keys, placed)...)

		if err := orderPanes(ctx, server, panes, order, ids); err != nil {
			return err
		}

		if err := setMainSize(ctx, window, layout); err != nil {
			return err
		}

		if err := window.SelectLayout(ctx, gotmux.Layout(layout.Arrange)); err != nil {
			return fmt.Errorf("arrange task window: %w", err)
		}

		return nil
	}

	root, err := splitCells(width, height, layout.Splits, extraKeys(panes, keys, placed))
	if err != nil {
		return err
	}

	if err := orderPanes(ctx, server, panes, root.leaves(), ids); err != nil {
		return err
	}

	numbers := make(map[string]string, len(ids))
	for key, id := range ids {
		numbers[key] = strings.TrimPrefix(string(id), "%")
	}

	if err := window.SelectLayout(ctx, gotmux.Layout(customLayout(root, numbers))); err != nil {
		return fmt.Errorf("lay out task window: %w", err)
	}

	return nil
}

func setMainSize(ctx context.Context, window gotmux.Window, layout config.TaskLayoutConfig) error {
	option := "main-pane-height"
	if strings.HasPrefix(layout.Arrange, config.ArrangeMainVertical) {
		option = "main-pane-width"
	}

	if !strings.HasPrefix(layout.Arrange, "main-") {
		return nil
	}

	var err error

	if layout.MainSize == "" {
		err = window.Options().Unset(ctx, option)
	} else {
		err = window.Options().Set(ctx, option, layout.MainSize)
	}

	if err != nil {
		return fmt.Errorf("size main pane: %w", err)
	}

	return nil
}

func orderPanes(ctx context.Context, server *gotmux.Server, panes []windowPane, order []string, ids map[string]gotmux.PaneID) error {
	current := make([]gotmux.PaneID, 0, len(panes))
	for _, pane := range panes {
		current = append(current, pane.id)
	}

	for position, key := range order {
		from := slices.Index(current, ids[key])
		if from < 0 || from == position {
			continue
		}

		first, err := server.Pane(ctx, current[position])
		if err != nil {
			return fmt.Errorf("find task pane: %w", err)
		}

		second, err := server.Pane(ctx, current[from])
		if err != nil {
			return fmt.Errorf("find task pane: %w", err)
		}

		if err := first.Swap(ctx, second, gotmux.SwapPaneOptions{Select: false}); err != nil {
			return fmt.Errorf("order task panes: %w", err)
		}

		current[position], current[from] = current[from], current[position]
	}

	return nil
}

// splitCells lays splits out in a window of width by height cells, as tmux would split the
// panes: a split takes its size from the pane it splits, one cell goes to the border between
// them, and a split along the line its pane already shares with siblings joins that line. Each
// extra pane then takes half of the largest cell, along its longer side.
func splitCells(width, height int, splits []config.TaskSplitConfig, extras []string) (*cell, error) {
	root := &cell{pane: config.TaskPaneAgent, sideBySide: false, children: nil, parent: nil, x: 0, y: 0, w: width, h: height}

	for _, split := range splits {
		size, err := config.ParsePaneSize(split.Size)
		if err != nil {
			return nil, fmt.Errorf("task split size: %w", err)
		}

		sideBySide := split.Direction == config.DirectionRight || split.Direction == config.DirectionLeft
		before := split.Direction == config.DirectionLeft || split.Direction == config.DirectionAbove

		if err := root.find(split.Of).splitOff(split.Pane, sideBySide, before, size); err != nil {
			return nil, err
		}
	}

	for _, extra := range extras {
		largest := root.largest()
		sideBySide := splitSideBySide(largest.w, largest.h)

		if err := largest.splitOff(extra, sideBySide, false, config.PaneSize{Cells: 0, Percent: 0}); err != nil {
			return nil, err
		}
	}

	return root, nil
}

func splitSideBySide(width, height int) bool {
	return width >= cellHeightPerWidth*height
}

func (c *cell) splitOff(pane string, sideBySide, before bool, size config.PaneSize) error {
	total := c.h
	if sideBySide {
		total = c.w
	}

	if total < minSplitCells {
		return ErrWindowTooSmall
	}

	added := (total - borderCells) / halves

	switch {
	case size.Percent > 0:
		added = total * size.Percent / percentOf
	case size.Cells > 0:
		added = size.Cells
	}

	c.split(pane, sideBySide, before, min(max(added, 1), total-borderCells-1))

	return nil
}

func (c *cell) largest() *cell {
	if c.isLeaf() {
		return c
	}

	var best *cell

	for _, child := range c.children {
		if candidate := child.largest(); best == nil || candidate.w*candidate.h > best.w*best.h {
			best = candidate
		}
	}

	return best
}

func (c *cell) find(pane string) *cell {
	if c.pane == pane {
		return c
	}

	for _, child := range c.children {
		if found := child.find(pane); found != nil {
			return found
		}
	}

	return nil
}

func (c *cell) isLeaf() bool {
	return c.pane != ""
}

func (c *cell) split(pane string, sideBySide, before bool, size int) {
	added := &cell{pane: pane, sideBySide: false, children: nil, parent: nil, x: c.x, y: c.y, w: c.w, h: c.h}
	kept := c

	if c.parent == nil || c.parent.sideBySide != sideBySide {
		kept = &cell{pane: c.pane, sideBySide: false, children: nil, parent: c, x: c.x, y: c.y, w: c.w, h: c.h}
		c.pane, c.sideBySide, c.children = "", sideBySide, []*cell{kept}
		added.parent = c
	}

	parent := kept.parent
	at := slices.Index(parent.children, kept)

	if before {
		parent.children = slices.Insert(parent.children, at, added)
	} else {
		parent.children = slices.Insert(parent.children, at+1, added)
	}

	added.parent = parent
	first, second := kept, added

	if before {
		first, second = added, kept
	}

	if sideBySide {
		total := kept.w
		first.w, second.w = sizeOf(first == added, size, total), sizeOf(second == added, size, total)
		second.x = first.x + first.w + borderCells
	} else {
		total := kept.h
		first.h, second.h = sizeOf(first == added, size, total), sizeOf(second == added, size, total)
		second.y = first.y + first.h + borderCells
	}
}

func sizeOf(isAdded bool, size, total int) int {
	if isAdded {
		return size
	}

	return total - size - borderCells
}

func (c *cell) leaves() []string {
	if c.isLeaf() {
		return []string{c.pane}
	}

	var leaves []string
	for _, child := range c.children {
		leaves = append(leaves, child.leaves()...)
	}

	return leaves
}

func (c *cell) layout(ids map[string]string) string {
	head := strconv.Itoa(c.w) + "x" + strconv.Itoa(c.h) + "," + strconv.Itoa(c.x) + "," + strconv.Itoa(c.y)
	if c.isLeaf() {
		return head + "," + ids[c.pane]
	}

	parts := make([]string, 0, len(c.children))
	for _, child := range c.children {
		parts = append(parts, child.layout(ids))
	}

	if c.sideBySide {
		return head + "{" + strings.Join(parts, ",") + "}"
	}

	return head + "[" + strings.Join(parts, ",") + "]"
}

// customLayout is a tmux layout string: the checksum tmux verifies, then the cells.
func customLayout(root *cell, ids map[string]string) string {
	body := root.layout(ids)

	var sum uint16
	for _, character := range []byte(body) {
		sum = (sum >> 1) + ((sum & 1) << checksumTop)
		sum += uint16(character)
	}

	return fmt.Sprintf("%04x,%s", sum, body)
}
