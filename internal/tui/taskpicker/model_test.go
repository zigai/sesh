package taskpicker_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui/taskpicker"
)

var (
	errBranchTaken = errors.New("branch is already checked out")

	newWorktree  = task.Location{Where: task.WhereNew, Branch: "main"}
	mainCheckout = task.Location{Where: task.WhereMain, Branch: "main", Directory: "/code/shop"}
	hotfixTree   = task.Location{Where: task.WhereWorktree, Branch: "hotfix", Directory: "/code/shop-hotfix"}
	featureBr    = task.Location{Where: task.WhereBranch, Branch: "feature"}
	reviewTree   = task.Location{Where: task.WhereWorktree, Branch: "review", Directory: "/code/shop-review", Task: "t7", TaskTitle: "review docs"}
)

const (
	hereCheckout = 2
	busyCheckout = 4
)

// starts records what the picker asked to start and answers with a fixed result.
type starts struct {
	choices []taskpicker.Choice
	err     error
}

func (s *starts) start(choice taskpicker.Choice) error {
	s.choices = append(s.choices, choice)

	return s.err
}

func TestEnterStartsTheFirstAgentInANewWorktreeWithTheTrimmedTitle(t *testing.T) {
	model, started := compactModel(t, 1, "")

	run(model, typed("  Fix login ")...)
	run(model, key(tea.KeyEnter))

	want := taskpicker.Choice{Harness: "pi", Title: "Fix login", Location: newWorktree}
	if len(started.choices) != 1 || started.choices[0] != want {
		t.Fatalf("started %+v, want %+v", started.choices, want)
	}

	if _, ok := model.Chosen(); !ok {
		t.Fatal("Chosen() reports no choice after a successful start")
	}
}

func TestLeftAndRightPickTheAgentWhileTyping(t *testing.T) {
	model, started := compactModel(t, 1, "")

	run(model, typed("ab")...)
	run(model, key(tea.KeyRight))
	run(model, typed("c")...)
	run(model, key(tea.KeyRight), key(tea.KeyRight), key(tea.KeyLeft), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "abc" || started.choices[0].Harness != config.HarnessNone {
		t.Fatalf("started %+v, want title abc with none", started.choices)
	}
}

func TestTheTitleKeepsItsReadlineKeys(t *testing.T) {
	model, started := compactModel(t, 1, "")

	run(model, typed("fix the bug")...)
	run(model, ctrl('w'), ctrl('b'), ctrl('b'))
	run(model, typed("X")...)
	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "fix thXe" {
		t.Fatalf("started %+v, want title %q", started.choices, "fix thXe")
	}
}

func TestTheNewWorktreeLineNamesItsBranch(t *testing.T) {
	model, _ := compactModel(t, 1, "")

	if line := placeLine(t, model, "new worktree"); !strings.Contains(line, "wt/main/4") {
		t.Fatalf("new worktree line = %q, want the numbered branch", line)
	}

	run(model, typed("Fix login")...)

	if line := placeLine(t, model, "new worktree"); !strings.Contains(line, "wt/fix-login-2") {
		t.Fatalf("new worktree line = %q, want the titled branch past the taken name", line)
	}
}

func TestDownChoosesThePanesCheckout(t *testing.T) {
	model, started := compactModel(t, hereCheckout, "")

	if line := placeLine(t, model, "this checkout"); !strings.Contains(line, "shop-hotfix · hotfix") {
		t.Fatalf("this checkout line = %q, want the hotfix checkout and its branch", line)
	}

	run(model, key(tea.KeyDown), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != hotfixTree {
		t.Fatalf("started %+v, want the hotfix worktree", started.choices)
	}
}

func TestEveryUpOrDownPicksAnotherPlaceAndTypingStillGoesToTheTitle(t *testing.T) {
	model, started := compactModel(t, hereCheckout, "")

	run(model, typed("x")...)
	run(model, key(tea.KeyUp))

	if line := placeLine(t, model, "existing branch"); !strings.Contains(line, "▌") {
		t.Fatalf("up from the first place selects %q, want the last place", line)
	}

	run(model, key(tea.KeyDown), key(tea.KeyDown))
	run(model, typed("y")...)
	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "xy" || started.choices[0].Location != hotfixTree {
		t.Fatalf("started %+v, want title xy in the hotfix worktree", started.choices)
	}
}

func TestABusyCheckoutGoesToItsTaskInsteadOfStartingOne(t *testing.T) {
	model, started := compactModel(t, busyCheckout, "")

	if line := placeLine(t, model, "this checkout"); !strings.Contains(line, "review docs") {
		t.Fatalf("this checkout line = %q, want the task working there", line)
	}

	run(model, key(tea.KeyDown), key(tea.KeyEnter))

	if choice, ok := model.Chosen(); !ok || choice.Show != "t7" || len(started.choices) != 0 {
		t.Fatalf("Chosen() = %+v, %t, started %+v; want to go to t7 without starting", choice, ok, started.choices)
	}
}

func TestEnterOnAnExistingBranchAsksForTheListKeepingTitleAndAgent(t *testing.T) {
	model, started := compactModel(t, 1, "")

	run(model, typed(" Fix login ")...)
	run(model, key(tea.KeyRight), key(tea.KeyDown), key(tea.KeyDown), key(tea.KeyEnter))

	want := taskpicker.Choice{Reopen: true, Expanded: true, Title: "Fix login", Harness: "claude"}
	if choice, ok := model.Chosen(); !ok || choice != want || len(started.choices) != 0 {
		t.Fatalf("Chosen() = %+v, %t, started %+v; want %+v", choice, ok, started.choices, want)
	}
}

func TestEscapeClosesWithoutAChoice(t *testing.T) {
	model, started := compactModel(t, 1, "")

	run(model, key(tea.KeyEscape))

	if _, ok := model.Chosen(); ok || len(started.choices) != 0 {
		t.Fatalf("closing reported a choice or started %+v", started.choices)
	}
}

func TestTheListStartsWithTheTitleAndAgentItWasGiven(t *testing.T) {
	started := &starts{}
	options := listOptions("claude", started.start)
	options.Title = "Fix login"
	model := sized(options)

	run(model, typed("feature")...)
	run(model, key(tea.KeyEnter))

	want := taskpicker.Choice{Harness: "claude", Title: "Fix login", Location: featureBr}
	if len(started.choices) != 1 || started.choices[0] != want {
		t.Fatalf("started %+v, want %+v", started.choices, want)
	}
}

func TestEscapeInTheListClearsTheFilterAndThenAsksToCloseTheList(t *testing.T) {
	started := &starts{}
	options := listOptions("", started.start)
	options.Title = "Fix login"
	model := sized(options)

	run(model, typed("feature")...)
	run(model, key(tea.KeyEscape))

	if rows := rowLines(t, model); len(rows) != 5 {
		t.Fatalf("after esc the rows are %q, want every location back", rows)
	}

	run(model, key(tea.KeyLeft), key(tea.KeyEscape))

	want := taskpicker.Choice{Reopen: true, Expanded: false, Title: "Fix login", Harness: config.HarnessNone}
	if choice, ok := model.Chosen(); !ok || choice != want || len(started.choices) != 0 {
		t.Fatalf("Chosen() = %+v, %t; want %+v", choice, ok, want)
	}
}

func TestNewWorktreesFromOtherBranchesAppearOnlyWhenFiltering(t *testing.T) {
	options := listOptions("", nil)
	fromFeature := task.Location{Where: task.WhereNew, Branch: "feature"}
	options.Locations = append(options.Locations, fromFeature)

	if rows := sectionRows(t, sized(options), "new worktree"); len(rows) != 1 || !strings.Contains(rows[0], "main") {
		t.Fatalf("new worktree rows = %q before filtering, want only main", rows)
	}

	started := &starts{}
	options.Start = started.start
	model := sized(options)

	run(model, typed("new feat")...)
	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != fromFeature {
		t.Fatalf("started %+v, want a new worktree from feature", started.choices)
	}
}

func TestFilterPutsNamesContainingTheQueryBeforeScatteredMatches(t *testing.T) {
	model, _ := listModel(t, "")

	run(model, typed("hot")...)

	if row := rowLines(t, model)[0]; !strings.Contains(row, "shop-hotfix") {
		t.Fatalf("first row = %q, want the hotfix worktree before the scattered match in main checkout", row)
	}
}

func TestArrowKeysWrapAroundTheList(t *testing.T) {
	model, started := listModel(t, "")

	run(model, key(tea.KeyUp), key(tea.KeyEnter))

	if choice, ok := model.Chosen(); !ok || choice.Show != "t7" || len(started.choices) != 0 {
		t.Fatalf("up from the first row chose %+v, %t; want the last row, the busy checkout", choice, ok)
	}

	model, started = listModel(t, "")
	run(model, key(tea.KeyUp), key(tea.KeyDown), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != newWorktree {
		t.Fatalf("down from the last row started %+v, want the first row", started.choices)
	}
}

func TestLongListsScrollToKeepTheSelectionVisible(t *testing.T) {
	options := listOptions("", nil)
	for index := range 20 {
		options.Locations = append(options.Locations, task.Location{Where: task.WhereBranch, Branch: "branch-" + strconv.Itoa(index)})
	}

	model := sized(options)
	for range len(options.Locations) - 1 {
		run(model, key(tea.KeyDown))
	}

	if lines := listLines(t, model); !strings.Contains(lines[len(lines)-1], "▌") || !strings.Contains(lines[len(lines)-2], "in use") {
		t.Fatalf("list = %q, want the selected last row visible under its heading", lines)
	}
}

func TestAFailedStartShowsWhyAndGoesBackToTheForm(t *testing.T) {
	model, started := compactModel(t, hereCheckout, "")
	started.err = errBranchTaken

	run(model, key(tea.KeyEnter))

	if view := plainView(model); !strings.Contains(view, "branch is already checked out") {
		t.Fatalf("the failure is not shown:\n%s", view)
	}

	if _, ok := model.Chosen(); ok {
		t.Fatal("Chosen() reports a choice after a failed start")
	}

	started.err = nil

	run(model, key(tea.KeyEnter), key(tea.KeyDown), key(tea.KeyEnter))

	if len(started.choices) != 2 || started.choices[1].Location != hotfixTree {
		t.Fatalf("started %+v, want a retry in the hotfix worktree", started.choices)
	}
}

func TestOutsideAProjectATaskOrTabStartsInThePanesDirectory(t *testing.T) {
	model, started := looseModel(t)

	if row := agentRow(t, model); !strings.Contains(row, "pi   claude   tab") || strings.Contains(row, "none") {
		t.Fatalf("agent row = %q, want pi, claude, tab", row)
	}

	if view := plainView(model); strings.Contains(view, "worktree") || strings.Contains(view, "checkout") {
		t.Fatalf("the popup outside a project offers places to work:\n%s", view)
	}

	run(model, key(tea.KeyLeft), key(tea.KeyEnter))

	if len(started.choices) != 1 || !started.choices[0].Tab {
		t.Fatalf("started %+v, want a plain tab", started.choices)
	}

	model, started = looseModel(t)
	run(model, typed("Fix login")...)
	run(model, key(tea.KeyDown), key(tea.KeyEnter))

	want := taskpicker.Choice{Harness: "pi", Title: "Fix login"}
	if len(started.choices) != 1 || started.choices[0] != want {
		t.Fatalf("started %+v, want %+v", started.choices, want)
	}
}

func TestAFolderProjectWorksInItsDirectory(t *testing.T) {
	started := &starts{}
	folder := task.Location{Where: task.WhereMain, Directory: "/code/notes"}
	model := sized(taskpicker.Options{
		Project: "notes", Locations: []task.Location{folder}, Current: 0, Harnesses: []string{"pi"}, Start: started.start,
	})

	if view := plainView(model); strings.Contains(view, "worktree") {
		t.Fatalf("a folder offers places to work:\n%s", view)
	}

	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != folder {
		t.Fatalf("started %+v, want the folder", started.choices)
	}
}

func TestClickingAPlaceAndAnAgentSelectsThem(t *testing.T) {
	model, started := compactModel(t, hereCheckout, "")
	lines := strings.Split(plainView(model), "\n")

	row := slicesContaining(lines, "this checkout")
	run(model, tea.MouseClickMsg(tea.Mouse{X: strings.Index(lines[row], "this checkout"), Y: row, Button: tea.MouseLeft}))

	row = slicesIndex(lines, "agent")
	run(model, tea.MouseClickMsg(tea.Mouse{X: strings.Index(lines[row], "none"), Y: row, Button: tea.MouseLeft}), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Harness != config.HarnessNone || started.choices[0].Location != hotfixTree {
		t.Fatalf("started %+v, want none in the hotfix worktree", started.choices)
	}
}

func TestPopupsFitTheirContentWithoutTruncating(t *testing.T) {
	compact := compactOptions(hereCheckout, "", nil)
	list := listOptions("", nil)
	list.Locations = append([]task.Location{{Where: task.WhereNew, Branch: "wt/fix-login-redirect"}}, list.Locations...)
	list.Current = 2

	for _, tc := range []struct {
		options taskpicker.Options
		want    []string
	}{
		{options: compact, want: []string{"new task", "shop", "new worktree", "wt/main/4", "this checkout", "shop-hotfix", "existing branch", "pi", "none", "enter start"}},
		{options: list, want: []string{"new worktree", "checkouts", "branches", "in use", "shop-hotfix", "feature", "review docs", "wt/fix-login-redirect", "esc back"}},
		{options: taskpicker.Options{Harnesses: []string{"pi", "claude"}}, want: []string{"new task", "pi", "claude", "tab", "enter start"}},
	} {
		width, height := taskpicker.Size(config.DefaultConfig(), tc.options)
		view := plainView(sized(tc.options))

		if lines := strings.Split(view, "\n"); len(lines) != height {
			t.Fatalf("view has %d lines, want %d:\n%s", len(lines), height, view)
		}

		for row := range strings.SplitSeq(view, "\n") {
			if rowWidth := ansi.StringWidth(row); rowWidth != width {
				t.Fatalf("line %q is %d wide, want %d", row, rowWidth, width)
			}
		}

		for _, want := range tc.want {
			if !strings.Contains(view, want) {
				t.Fatalf("view does not show %q:\n%s", want, view)
			}
		}
	}
}

func TestTheListIsAsWideAsTheCompactPopup(t *testing.T) {
	compactWidth, _ := taskpicker.Size(config.DefaultConfig(), compactOptions(1, "", nil))
	listWidth, _ := taskpicker.Size(config.DefaultConfig(), listOptions("", nil))

	if compactWidth != listWidth {
		t.Fatalf("compact width %d, list width %d; want the same", compactWidth, listWidth)
	}
}

func TestKeyHintsNameTheKeysAndCanBeTurnedOff(t *testing.T) {
	model, _ := compactModel(t, 1, "")

	if view := plainView(model); !strings.Contains(view, "←→ agent") || !strings.Contains(view, "↑↓ where") || !strings.Contains(view, "enter start") {
		t.Fatalf("the hints are wrong:\n%s", view)
	}

	run(model, key(tea.KeyUp))

	if view := plainView(model); !strings.Contains(view, "enter branches") {
		t.Fatalf("an existing branch does not say enter opens the branches:\n%s", view)
	}

	cfg := config.DefaultConfig()
	cfg.Tasks.ShowFooter = false
	options := compactOptions(1, "", nil)
	hidden := taskpicker.NewModel(cfg, options)
	width, height := taskpicker.Size(cfg, options)
	hidden.Update(tea.WindowSizeMsg{Width: width, Height: height})

	if view := plainView(hidden); strings.Contains(view, "enter") {
		t.Fatalf("the popup with show_footer off shows key hints:\n%s", view)
	}
}

func compactModel(t *testing.T, current int, agent string) (*taskpicker.Model, *starts) {
	t.Helper()

	started := &starts{}

	return sized(compactOptions(current, agent, started.start)), started
}

func listModel(t *testing.T, agent string) (*taskpicker.Model, *starts) {
	t.Helper()

	started := &starts{}

	return sized(listOptions(agent, started.start)), started
}

func looseModel(t *testing.T) (*taskpicker.Model, *starts) {
	t.Helper()

	started := &starts{}

	return sized(taskpicker.Options{Harnesses: []string{"pi", "claude"}, Start: started.start}), started
}

// compactOptions are the project popup's with the pane in the checkout at index current.
func compactOptions(current int, agent string, start func(taskpicker.Choice) error) taskpicker.Options {
	return taskpicker.Options{
		Project:    "shop",
		Locations:  []task.Location{newWorktree, mainCheckout, hotfixTree, featureBr, reviewTree},
		Current:    current,
		NextNumber: 4,
		Branches:   []string{"main", "hotfix", "feature", "review", "wt/fix-login"},
		Harnesses:  []string{"pi", "claude"},
		Agent:      agent,
		Start:      start,
	}
}

// listOptions are the expanded project popup's.
func listOptions(agent string, start func(taskpicker.Choice) error) taskpicker.Options {
	options := compactOptions(1, agent, start)
	options.Expanded = true

	return options
}

func sized(opts taskpicker.Options) *taskpicker.Model {
	model := taskpicker.NewModel(config.DefaultConfig(), opts)
	width, height := taskpicker.Size(config.DefaultConfig(), opts)
	model.Update(tea.WindowSizeMsg{Width: width, Height: height})

	return model
}

// run sends messages. The command enter returns starts the choice, so it runs before the next
// message and its result is sent back, the way the program would; other commands only blink the
// cursor.
func run(model *taskpicker.Model, messages ...tea.Msg) {
	for _, msg := range messages {
		_, cmd := model.Update(msg)
		if press, ok := msg.(tea.KeyPressMsg); !ok || press.Code != tea.KeyEnter || cmd == nil {
			continue
		}

		if result := cmd(); result != nil {
			if _, quit := result.(tea.QuitMsg); !quit {
				model.Update(result)
			}
		}
	}
}

func key(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func ctrl(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
}

func typed(text string) []tea.Msg {
	messages := make([]tea.Msg, 0, len(text))
	for _, r := range text {
		messages = append(messages, tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}

	return messages
}

func plainView(model *taskpicker.Model) string {
	return ansi.Strip(model.View().Content)
}

// listLines are the lines of the expanded list: from below the filter line to the bottom border.
func listLines(t *testing.T, model *taskpicker.Model) []string {
	t.Helper()

	lines := strings.Split(plainView(model), "\n")
	start := slicesContaining(lines, "/") + 1

	if start < 1 {
		t.Fatalf("no list:\n%s", strings.Join(lines, "\n"))
	}

	return lines[start : len(lines)-1]
}

// rowLines are the list rows, without the section headings.
func rowLines(t *testing.T, model *taskpicker.Model) []string {
	t.Helper()

	var rows []string

	for _, row := range listLines(t, model) {
		if strings.Contains(row, "─ ") {
			rows = append(rows, row)
		}
	}

	return rows
}

// sectionRows are the list rows under the section heading label.
func sectionRows(t *testing.T, model *taskpicker.Model, label string) []string {
	t.Helper()

	var rows []string

	inside := false

	for _, row := range listLines(t, model) {
		switch {
		case !strings.Contains(row, "─ "):
			inside = strings.TrimSpace(strings.Trim(row, "│")) == label
		case inside:
			rows = append(rows, row)
		}
	}

	return rows
}

// line is the body line whose label is label.
func line(t *testing.T, model *taskpicker.Model, label string) string {
	t.Helper()

	lines := strings.Split(plainView(model), "\n")

	index := slicesIndex(lines, label)
	if index < 0 {
		t.Fatalf("no %s line:\n%s", label, strings.Join(lines, "\n"))
	}

	return lines[index]
}

// placeLine is the line of the place to work called name.
func placeLine(t *testing.T, model *taskpicker.Model, name string) string {
	t.Helper()

	lines := strings.Split(plainView(model), "\n")

	index := slicesContaining(lines, name)
	if index < 0 {
		t.Fatalf("no %s line:\n%s", name, strings.Join(lines, "\n"))
	}

	return lines[index]
}

func agentRow(t *testing.T, model *taskpicker.Model) string {
	t.Helper()

	return line(t, model, "agent")
}

// slicesIndex finds the body line that starts with label.
func slicesIndex(lines []string, label string) int {
	for index, row := range lines {
		if strings.HasPrefix(strings.TrimPrefix(row, "│ "), label+" ") {
			return index
		}
	}

	return -1
}

// slicesContaining finds the first line that contains text.
func slicesContaining(lines []string, text string) int {
	for index, row := range lines {
		if strings.Contains(row, text) {
			return index
		}
	}

	return -1
}
