package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

const (
	// TaskPaneAgent names the agent pane in task layouts.
	TaskPaneAgent = "agent"

	OrientationLandscape = "landscape"
	OrientationPortrait  = "portrait"

	DirectionRight = "right"
	DirectionBelow = "below"
	DirectionLeft  = "left"
	DirectionAbove = "above"

	ArrangeMainVertical   = "main-vertical"
	ArrangeMainHorizontal = "main-horizontal"

	maxPercent = 100
	// cellAspect is how many times taller than wide a terminal cell is, about.
	cellAspect = 2
)

var (
	errTaskPaneName        = errors.New("pane names may only contain a-z, 0-9, _ and -")
	errTaskPaneReserved    = errors.New(`pane name "agent" is the agent's`)
	errTaskPaneDuplicate   = errors.New("duplicate pane name")
	errTaskLayoutName      = errors.New("layout names may only contain a-z, 0-9, _ and -")
	errTaskLayoutDuplicate = errors.New("duplicate layout name")
	errTaskLayoutShape     = errors.New("layout needs either arrange or splits")
	errTaskLayoutArrange   = errors.New("arrange must be a tmux layout: main-vertical, main-horizontal, main-vertical-mirrored, main-horizontal-mirrored, even-horizontal, even-vertical or tiled")
	errTaskLayoutMainSize  = errors.New("main_size needs a main-* arrange")
	errTaskLayoutPanes     = errors.New("panes lists the panes of an arrange; splits place their own")
	errTaskLayoutPane      = errors.New("layout places a pane that is not configured")
	errTaskLayoutPlaced    = errors.New("layout places a pane twice")
	errTaskLayoutOf        = errors.New(`split of must be "agent" or a pane placed by an earlier split`)
	errTaskLayoutDirection = errors.New("split direction must be right, below, left or above")
	errTaskLayoutFocus     = errors.New(`focus must be "agent" or a pane the layout places`)
	errTaskLayoutWhen      = errors.New(`when needs orientation "landscape" or "portrait", sizes of at least 0, and max sizes not below min sizes`)
	errTaskLayoutHarness   = errors.New("when.harness names a harness that is not offered")
	errTaskPaneCommand     = errors.New("pane command needs a program")
	errTaskPaneRestart     = errors.New("restart needs a command to run again")
	errPaneSize            = errors.New(`size must be cells such as "80" or a percentage from 1% to 99%`)

	arrangements = []string{
		ArrangeMainVertical, ArrangeMainHorizontal, "main-vertical-mirrored", "main-horizontal-mirrored",
		"even-horizontal", "even-vertical", "tiled",
	}
)

// TaskPaneConfig is a pane a task window opens beside the agent. Command runs in the user's
// interactive shell with task variables filled in; without one the pane is a shell. Dir is
// relative to the task's directory. A restart pane runs its command again after a snapshot
// restore.
type TaskPaneConfig struct {
	Name    string   `toml:"name"`
	Command []string `toml:"command"`
	Dir     string   `toml:"dir"`
	Restart bool     `toml:"restart"`
}

// TaskLayoutConfig arranges a task window's panes. A window takes the first layout whose When
// matches it. Arrange applies a tmux layout to the agent and Panes, the agent as its main pane;
// Splits place each pane by splitting the agent or an earlier pane instead. Focus is the pane
// selected when the window opens.
type TaskLayoutConfig struct {
	Name     string            `toml:"name"`
	When     TaskLayoutWhen    `toml:"when"`
	Arrange  string            `toml:"arrange"`
	MainSize string            `toml:"main_size"`
	Panes    []string          `toml:"panes"`
	Splits   []TaskSplitConfig `toml:"splits"`
	Focus    string            `toml:"focus"`
}

// TaskLayoutWhen is what a task window must be for a layout to apply. Zero values match any
// window. A landscape window is at least twice as many columns wide as it is rows tall, which is
// about as wide as tall on screen.
type TaskLayoutWhen struct {
	Orientation string   `toml:"orientation"`
	MinWidth    int      `toml:"min_width"`
	MaxWidth    int      `toml:"max_width"`
	MinHeight   int      `toml:"min_height"`
	MaxHeight   int      `toml:"max_height"`
	Harness     []string `toml:"harness"`
}

// TaskSplitConfig places Pane by splitting Of in Direction; Size is the new pane's.
type TaskSplitConfig struct {
	Pane      string `toml:"pane"`
	Of        string `toml:"of"`
	Direction string `toml:"direction"`
	Size      string `toml:"size"`
}

// TaskWindow is the task window configuration of one project.
type TaskWindow struct {
	Panes   []TaskPaneConfig
	Layouts []TaskLayoutConfig
}

// PaneSize is a size in cells or a percentage of the space split; zero for both splits it evenly.
type PaneSize struct {
	Cells   int
	Percent int
}

// DefaultTaskLayouts are used without configured layouts: the agent beside the other panes in a
// landscape window, above them otherwise.
func DefaultTaskLayouts() []TaskLayoutConfig {
	return []TaskLayoutConfig{
		{
			Name: "side", When: TaskLayoutWhen{Orientation: OrientationLandscape, MinWidth: 0, MaxWidth: 0, MinHeight: 0, MaxHeight: 0, Harness: nil},
			Arrange: ArrangeMainVertical, MainSize: "50%", Panes: nil, Splits: nil, Focus: "",
		},
		{
			Name: "stacked", When: TaskLayoutWhen{Orientation: "", MinWidth: 0, MaxWidth: 0, MinHeight: 0, MaxHeight: 0, Harness: nil},
			Arrange: ArrangeMainHorizontal, MainSize: "50%", Panes: nil, Splits: nil, Focus: "",
		},
	}
}

// TaskWindowFor returns the task window of the project at root, or of a loose task for an empty
// root: the global panes with the project's panes replacing those of the same name and adding
// the others, and the project's layouts if it sets them, else the global ones. An empty list of
// layouts means DefaultTaskLayouts.
func (c *Config) TaskWindowFor(root string) TaskWindow {
	return c.taskWindow(c.ProjectOverride(root))
}

func (c *Config) taskWindow(override ProjectConfig) TaskWindow {
	panes := cloneTaskPanes(c.Tasks.Panes)
	for _, pane := range override.TaskPanes {
		index := slices.IndexFunc(panes, func(existing TaskPaneConfig) bool { return existing.Name == pane.Name })
		if index < 0 {
			panes = append(panes, pane.Clone())
		} else {
			panes[index] = pane.Clone()
		}
	}

	layouts := c.Tasks.Layouts
	if override.TaskLayouts != nil {
		layouts = override.TaskLayouts
	}

	if len(layouts) == 0 {
		return TaskWindow{Panes: panes, Layouts: DefaultTaskLayouts()}
	}

	return TaskWindow{Panes: panes, Layouts: cloneTaskLayouts(layouts)}
}

// Placed returns the panes a layout places besides the agent, in the order it places them.
func (w TaskWindow) Placed(layout TaskLayoutConfig) []string {
	if len(layout.Splits) > 0 {
		placed := make([]string, 0, len(layout.Splits))
		for _, split := range layout.Splits {
			placed = append(placed, split.Pane)
		}

		return placed
	}

	if layout.Panes != nil {
		return slices.Clone(layout.Panes)
	}

	placed := make([]string, 0, len(w.Panes))
	for _, pane := range w.Panes {
		placed = append(placed, pane.Name)
	}

	return placed
}

// Pane returns the configured pane called name.
func (w TaskWindow) Pane(name string) (TaskPaneConfig, bool) {
	index := slices.IndexFunc(w.Panes, func(pane TaskPaneConfig) bool { return pane.Name == name })
	if index < 0 {
		return TaskPaneConfig{Name: "", Command: nil, Dir: "", Restart: false}, false
	}

	return w.Panes[index], true
}

// Matches reports whether a layout applies to a window of width by height cells running harness.
func (w TaskLayoutWhen) Matches(width, height int, harness string) bool {
	landscape := width >= cellAspect*height

	switch {
	case w.Orientation == OrientationLandscape && !landscape, w.Orientation == OrientationPortrait && landscape:
		return false
	case width < w.MinWidth, w.MaxWidth > 0 && width > w.MaxWidth:
		return false
	case height < w.MinHeight, w.MaxHeight > 0 && height > w.MaxHeight:
		return false
	case len(w.Harness) > 0 && !slices.Contains(w.Harness, harness):
		return false
	default:
		return true
	}
}

// ParsePaneSize reads a pane size: cells such as "80", a percentage such as "40%", or empty.
func ParsePaneSize(size string) (PaneSize, error) {
	if size == "" {
		return PaneSize{Cells: 0, Percent: 0}, nil
	}

	if digits, percent := strings.CutSuffix(size, "%"); percent {
		value, err := strconv.Atoi(digits)
		if err != nil || value < 1 || value >= maxPercent || strconv.Itoa(value) != digits {
			return PaneSize{Cells: 0, Percent: 0}, errPaneSize
		}

		return PaneSize{Cells: 0, Percent: value}, nil
	}

	value, err := strconv.Atoi(size)
	if err != nil || value < 1 || strconv.Itoa(value) != size {
		return PaneSize{Cells: 0, Percent: 0}, errPaneSize
	}

	return PaneSize{Cells: value, Percent: 0}, nil
}

func (p TaskPaneConfig) Clone() TaskPaneConfig {
	clone := p
	clone.Command = slices.Clone(p.Command)

	return clone
}

func (l TaskLayoutConfig) Clone() TaskLayoutConfig {
	clone := l
	clone.When.Harness = slices.Clone(l.When.Harness)
	clone.Panes = slices.Clone(l.Panes)
	clone.Splits = slices.Clone(l.Splits)

	return clone
}

// ValidateTaskWindows checks the global task window and the window of every project with
// overrides, after the overrides are applied.
func ValidateTaskWindows(cfg *Config) error {
	harnesses := cfg.harnessNames()

	if err := validateTaskPanes("tasks", cfg.Tasks.Panes); err != nil {
		return err
	}

	if err := validateTaskWindow("tasks", cfg.TaskWindowFor(""), harnesses); err != nil {
		return err
	}

	for _, project := range slices.Sorted(maps.Keys(cfg.Projects.Overrides)) {
		override := cfg.Projects.Overrides[project]
		prefix := "projects.overrides." + project

		if err := validateTaskPanes(prefix, override.TaskPanes); err != nil {
			return err
		}

		if err := validateTaskWindow(prefix, cfg.taskWindow(override), harnesses); err != nil {
			return err
		}
	}

	return nil
}

// harnessNames are the names a layout's when.harness can use: the configured harnesses, else every
// harness AHT supports.
func (c *Config) harnessNames() []string {
	if len(c.Tasks.Harness) == 0 {
		return nil
	}

	names := make([]string, 0, len(c.Tasks.Harness))
	for _, configured := range c.Tasks.Harness {
		names = append(names, configured.Name)
	}

	return names
}

// validateTaskPanes checks one list of panes as written, before a project's panes are merged into
// the global ones, so a name written twice is reported.
func validateTaskPanes(prefix string, panes []TaskPaneConfig) error {
	names := make(map[string]bool, len(panes))

	for _, pane := range panes {
		if err := validateTaskPane(pane, names); err != nil {
			return fmt.Errorf("invalid %s pane %q: %w", prefix, pane.Name, err)
		}

		names[pane.Name] = true
	}

	return nil
}

func validateTaskWindow(prefix string, window TaskWindow, harnesses []string) error {
	layouts := make(map[string]bool, len(window.Layouts))

	for index, layout := range window.Layouts {
		if err := validateTaskLayout(window, layout, layouts, harnesses); err != nil {
			return fmt.Errorf("invalid %s layout %d: %w", prefix, index+1, err)
		}

		if layout.Name != "" {
			layouts[layout.Name] = true
		}
	}

	return nil
}

func validateTaskPane(pane TaskPaneConfig, seen map[string]bool) error {
	switch {
	case !ValidHarnessName(pane.Name):
		return errTaskPaneName
	case pane.Name == TaskPaneAgent:
		return errTaskPaneReserved
	case seen[pane.Name]:
		return errTaskPaneDuplicate
	case containsControlCharacter(pane.Dir):
		return errTaskActionPromptPath
	case len(pane.Command) > 0 && strings.TrimSpace(pane.Command[0]) == "":
		return errTaskPaneCommand
	case pane.Restart && len(pane.Command) == 0:
		return errTaskPaneRestart
	}

	for _, argument := range pane.Command {
		if err := ValidateTaskActionTemplate(argument); err != nil {
			return err
		}
	}

	return ValidateTaskActionTemplate(pane.Dir)
}

func validateTaskLayout(window TaskWindow, layout TaskLayoutConfig, seen map[string]bool, harnesses []string) error {
	if err := validateTaskLayoutShape(layout, seen); err != nil {
		return err
	}

	if err := validateTaskLayoutWhen(layout.When, harnesses); err != nil {
		return err
	}

	if _, err := ParsePaneSize(layout.MainSize); err != nil {
		return err
	}

	placed, err := validatePlacement(window, layout)
	if err != nil {
		return err
	}

	if layout.Focus != "" && layout.Focus != TaskPaneAgent && !slices.Contains(placed, layout.Focus) {
		return errTaskLayoutFocus
	}

	return nil
}

func validateTaskLayoutShape(layout TaskLayoutConfig, seen map[string]bool) error {
	switch {
	case layout.Name != "" && !ValidHarnessName(layout.Name):
		return errTaskLayoutName
	case seen[layout.Name]:
		return errTaskLayoutDuplicate
	case (layout.Arrange == "") == (len(layout.Splits) == 0):
		return errTaskLayoutShape
	case layout.Arrange != "" && !slices.Contains(arrangements, layout.Arrange):
		return errTaskLayoutArrange
	case layout.MainSize != "" && !strings.HasPrefix(layout.Arrange, "main-"):
		return errTaskLayoutMainSize
	case layout.Panes != nil && len(layout.Splits) > 0:
		return errTaskLayoutPanes
	default:
		return nil
	}
}

func validateTaskLayoutWhen(when TaskLayoutWhen, harnesses []string) error {
	switch {
	case when.Orientation != "" && when.Orientation != OrientationLandscape && when.Orientation != OrientationPortrait:
		return errTaskLayoutWhen
	case when.MinWidth < 0, when.MaxWidth < 0, when.MinHeight < 0, when.MaxHeight < 0:
		return errTaskLayoutWhen
	case when.MaxWidth > 0 && when.MaxWidth < when.MinWidth, when.MaxHeight > 0 && when.MaxHeight < when.MinHeight:
		return errTaskLayoutWhen
	case slices.ContainsFunc(when.Harness, func(name string) bool { return !knownHarness(name, harnesses) }):
		return errTaskLayoutHarness
	default:
		return nil
	}
}

func validatePlacement(window TaskWindow, layout TaskLayoutConfig) ([]string, error) {
	placed := make([]string, 0, len(window.Panes))

	for _, split := range layout.Splits {
		switch {
		case split.Of != TaskPaneAgent && !slices.Contains(placed, split.Of):
			return nil, errTaskLayoutOf
		case !slices.Contains([]string{DirectionRight, DirectionBelow, DirectionLeft, DirectionAbove}, split.Direction):
			return nil, errTaskLayoutDirection
		}

		if _, err := ParsePaneSize(split.Size); err != nil {
			return nil, err
		}

		placed = append(placed, split.Pane)
	}

	if len(layout.Splits) == 0 {
		placed = window.Placed(layout)
	}

	for index, name := range placed {
		if _, found := window.Pane(name); !found {
			return nil, fmt.Errorf("%w: %s", errTaskLayoutPane, name)
		}

		if slices.Contains(placed[:index], name) {
			return nil, errTaskLayoutPlaced
		}
	}

	return placed, nil
}

// knownHarness reports whether name is one of the configured harnesses, or without any, one AHT
// supports.
func knownHarness(name string, configured []string) bool {
	if configured == nil {
		return SupportedHarness(name)
	}

	return slices.Contains(configured, name)
}

func cloneTaskPanes(panes []TaskPaneConfig) []TaskPaneConfig {
	if panes == nil {
		return nil
	}

	clone := make([]TaskPaneConfig, 0, len(panes))
	for _, pane := range panes {
		clone = append(clone, pane.Clone())
	}

	return clone
}

func cloneTaskLayouts(layouts []TaskLayoutConfig) []TaskLayoutConfig {
	if layouts == nil {
		return nil
	}

	clone := make([]TaskLayoutConfig, 0, len(layouts))
	for _, layout := range layouts {
		clone = append(clone, layout.Clone())
	}

	return clone
}
