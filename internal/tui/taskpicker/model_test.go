package taskpicker_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/git"
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
	run(model, key(tea.KeyRight), key(tea.KeyLeft), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "abc" || started.choices[0].Harness != "claude" {
		t.Fatalf("started %+v, want title abc with claude", started.choices)
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

func TestPasteInsertsIntoTheTaskTitleAtTheCursor(t *testing.T) {
	model, started := compactModel(t, 1, "")

	run(model, typed("fix issue")...)
	run(model, ctrl('a'))
	run(model, tea.PasteMsg{Content: "Please "}, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "Please fix issue" {
		t.Fatalf("started %+v, want the pasted title", started.choices)
	}
}

func TestPasteFiltersTheTaskLocationsWithoutChangingTheTitle(t *testing.T) {
	started := &starts{}
	options := listOptions("", started.start)
	options.Title = "Fix issue"
	model := sized(options)

	run(model, tea.PasteMsg{Content: "feature"}, key(tea.KeyEnter))

	want := taskpicker.Choice{Harness: "pi", Title: "Fix issue", Location: featureBr}
	if len(started.choices) != 1 || started.choices[0] != want {
		t.Fatalf("started %+v, want %+v", started.choices, want)
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

	want := taskpicker.Choice{Reopen: true, Expanded: false, Title: "Fix login", Harness: "claude"}
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

func TestWithoutAnAvailableAgentEnterSaysSoInsteadOfStarting(t *testing.T) {
	started := &starts{}
	options := compactOptions(1, "pi", started.start)
	options.Harnesses = nil
	model := sized(options)

	run(model, key(tea.KeyRight), key(tea.KeyEnter))

	if view := plainView(model); !strings.Contains(view, "no agent is available") {
		t.Fatalf("the missing agent is not shown:\n%s", view)
	}

	if len(started.choices) != 0 {
		t.Fatalf("started %+v without an agent", started.choices)
	}
}

func TestOutsideAProjectATaskOrTabStartsInThePanesDirectory(t *testing.T) {
	model, started := looseModel(t)

	if row := agentRow(t, model); !strings.Contains(row, "pi   claude   tab") {
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
	run(model, tea.MouseClickMsg(tea.Mouse{X: strings.Index(lines[row], "claude"), Y: row, Button: tea.MouseLeft}), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Harness != "claude" || started.choices[0].Location != hotfixTree {
		t.Fatalf("started %+v, want claude in the hotfix worktree", started.choices)
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
		{options: compact, want: []string{"new task", "shop", "new worktree", "wt/main/4", "this checkout", "shop-hotfix", "existing branch", "pi", "claude", "enter start"}},
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

	if view := plainView(model); !strings.Contains(view, "←→ agent") || !strings.Contains(view, "↑↓ checkout") || !strings.Contains(view, "enter start") {
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

func TestListRowsShowEachBranchsLatestCommitDistanceAndAge(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	options := listOptions("", nil)
	options.Now = now
	options.Refs = map[string]task.Ref{
		"refs/heads/main":    {Time: now.Add(-5 * time.Minute).Unix(), Subject: "ship it", Ahead: 0, Behind: 0},
		"refs/heads/hotfix":  {Time: now.Add(-3 * time.Hour).Unix(), Subject: "fix the login redirect for expired sessions", Ahead: 2, Behind: 0},
		"refs/heads/feature": {Time: now.Add(-15 * 24 * time.Hour).Unix(), Subject: "draft", Ahead: 1, Behind: 40},
	}
	model := sized(options)

	hotfix := sectionRows(t, model, "checkouts")[1]
	for _, part := range []string{"shop-hotfix", "fix the login", "...", "hotfix", "↑2 ↓0", "3h"} {
		if !strings.Contains(hotfix, part) {
			t.Errorf("hotfix row %q lacks %q", hotfix, part)
		}
	}

	feature := sectionRows(t, model, "branches")[0]
	if !strings.Contains(feature, "↑1 ↓40") || !strings.Contains(feature, "2w") || !strings.Contains(feature, "draft") {
		t.Errorf("feature row %q, want its distance from main, age and subject", feature)
	}

	main := sectionRows(t, model, "checkouts")[0]
	if !strings.Contains(main, "↑0 ↓0") || !strings.Contains(main, "5m") {
		t.Errorf("main row %q, want an age and no distance from itself", main)
	}

	if column(hotfix, "3h") != column(main, "5m") {
		t.Errorf("ages are not aligned:\n%s\n%s", main, hotfix)
	}
}

func TestCheckoutsShowTheLinesTheirChangesAddAndDeleteOnceCounted(t *testing.T) {
	options := listOptions("", nil)
	options.Changes = func() map[string]git.Diff {
		return map[string]git.Diff{"/code/shop-hotfix": {Added: 12, Deleted: 3}, "/code/shop": {Added: 0, Deleted: 0}}
	}
	model := sized(options)

	if strings.Contains(plainView(model), "+0") {
		t.Fatal("changes show before they are counted")
	}

	for _, msg := range collect(model.Init()) {
		model.Update(msg)
	}

	rows := sectionRows(t, model, "checkouts")
	if !strings.Contains(rows[1], "+12 -3") || !strings.Contains(rows[0], "+0  -0") {
		t.Fatalf("checkout rows = %q, want +12 -3 in the hotfix worktree and +0 -0 in the clean main checkout", rows)
	}
}

func TestANarrowListDropsTheSubjectButKeepsTheNames(t *testing.T) {
	options := listOptions("", nil)
	options.Refs = map[string]task.Ref{"refs/heads/hotfix": {Time: 1, Subject: "a subject that cannot fit", Ahead: 12, Behind: 340}}
	model := taskpicker.NewModel(config.DefaultConfig(), options)
	model.Update(tea.WindowSizeMsg{Width: 44, Height: 20})

	row := sectionRows(t, model, "checkouts")[1]
	if strings.Contains(row, "subject") || !strings.Contains(row, "shop-hotfix") {
		t.Fatalf("narrow hotfix row = %q, want the name without the subject", row)
	}
}

// column is the display column where part starts in line.
func column(line, part string) int {
	before, _, _ := strings.Cut(line, part)

	return utf8.RuneCountInString(before)
}

// collect runs a command and the commands of a batch it returns, and returns their messages.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}

	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, inner := range batch {
			msgs = append(msgs, collect(inner)...)
		}

		return msgs
	}

	return []tea.Msg{msg}
}

func TestCommitSubjectsCannotSendTerminalSequences(t *testing.T) {
	options := listOptions("", nil)
	options.Refs = map[string]task.Ref{"refs/heads/feature": {Time: 0, Subject: "\x1b]2;owned\x07draft \u202eevil", Ahead: 0, Behind: 0}}
	model := sized(options)

	view := model.View().Content
	if strings.Contains(view, "\x1b]2;") || strings.Contains(view, "\u202e") {
		t.Fatalf("view passes the subject's terminal sequences through: %q", view)
	}

	if feature := sectionRows(t, model, "branches")[0]; !strings.Contains(feature, "draft evil") {
		t.Errorf("feature row %q, want the subject's text", feature)
	}
}

func TestANewWorktreeRowShowsTheCommitTheWorktreeStartsFrom(t *testing.T) {
	options := listOptions("", nil)
	options.Refs = map[string]task.Ref{
		"refs/heads/main":          {Time: 0, Subject: "local base", Ahead: 0, Behind: 0},
		"refs/heads/origin/main":   {Time: 0, Subject: "a local branch named like the remote one", Ahead: 0, Behind: 0},
		"refs/remotes/origin/main": {Time: 0, Subject: "remote base", Ahead: 0, Behind: 0},
	}
	options.Starts = map[string]string{"main": "refs/remotes/origin/main"}
	model := sized(options)

	if row := sectionRows(t, model, "new worktree")[0]; !strings.Contains(row, "remote base") {
		t.Errorf("new worktree row %q, want origin's main commit", row)
	}
}

func machineOptions(start func(taskpicker.Choice) error) taskpicker.Options {
	options := compactOptions(1, "", start)
	options.Machines = []taskpicker.Machine{{Name: "local", Usable: true}, {Name: "old", Usable: false}, {Name: "devbox", Usable: true}}

	return options
}

func shift(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModShift})
}

func TestTheHostRowIsHiddenWithOnlyThisMachine(t *testing.T) {
	options := compactOptions(1, "", nil)
	options.Machines = []taskpicker.Machine{{Name: "local", Usable: true}}

	if view := plainView(sized(options)); strings.Contains(view, "host") {
		t.Fatalf("a popup with one machine shows a host row:\n%s", view)
	}
}

func TestShiftArrowsPickTheNextUsableHostKeepingTitleAndAgent(t *testing.T) {
	model := sized(machineOptions(nil))

	if row := line(t, model, "host"); !strings.Contains(row, "local") || !strings.Contains(row, "old (update sesh)") || !strings.Contains(row, "devbox") {
		t.Fatalf("host row = %q, want local, old (update sesh) and devbox", row)
	}

	run(model, typed("fix")...)
	run(model, key(tea.KeyRight), shift(tea.KeyRight))

	choice, ok := model.Chosen()
	if want := (taskpicker.Choice{Reopen: true, Machine: 2, Title: "fix", Harness: "claude"}); !ok || choice != want {
		t.Fatalf("chosen %+v, %v; want %+v", choice, ok, want)
	}
}

func TestClickingAHostThatCannotStartTasksDoesNothing(t *testing.T) {
	model := sized(machineOptions(nil))
	lines := strings.Split(plainView(model), "\n")
	row := slicesIndex(lines, "host")

	run(model, tea.MouseClickMsg(tea.Mouse{X: strings.Index(lines[row], "old"), Y: row, Button: tea.MouseLeft}))

	if _, ok := model.Chosen(); ok {
		t.Fatal("clicking a host that needs a newer sesh chose it")
	}

	run(model, tea.MouseClickMsg(tea.Mouse{X: strings.Index(lines[row], "devbox"), Y: row, Button: tea.MouseLeft}))

	if choice, ok := model.Chosen(); !ok || !choice.Reopen || choice.Machine != 2 {
		t.Fatalf("chosen %+v, %v; want a reopen on devbox", choice, ok)
	}
}

func TestAProjectMissingOnTheHostSaysSoAndStartsNothing(t *testing.T) {
	started := &starts{}
	options := taskpicker.Options{
		Project: "shop", Current: -1, Start: started.start, Machine: 2, Problem: "not on devbox",
		Machines: []taskpicker.Machine{{Name: "local", Usable: true}, {Name: "old", Usable: false}, {Name: "devbox", Usable: true}},
	}
	width, height := taskpicker.Size(config.DefaultConfig(), options)
	model := sized(options)
	view := plainView(model)

	if !strings.Contains(view, "not on devbox") {
		t.Fatalf("view does not say the project is missing:\n%s", view)
	}

	if lines := strings.Split(view, "\n"); len(lines) != height || ansi.StringWidth(lines[0]) != width {
		t.Fatalf("view is not %dx%d:\n%s", width, height, view)
	}

	run(model, key(tea.KeyEnter))

	if len(started.choices) != 0 {
		t.Fatalf("started %+v on a host without the project", started.choices)
	}
}
