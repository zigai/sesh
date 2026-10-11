package taskpicker_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/project"
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
	webProject   = project.Project{Root: "/code/web", Name: "web", Kind: project.KindGit}
)

// starts records what the picker asked to start and answers with fixed results.
type starts struct {
	choices []taskpicker.Choice
	err     error
}

func (s *starts) start(choice taskpicker.Choice) (string, error) {
	s.choices = append(s.choices, choice)

	return "t9", s.err
}

func TestEnterStartsWithTheLastChoices(t *testing.T) {
	model, started := projectModel(t, 1, "claude")

	run(model, key(tea.KeyEnter))

	want := taskpicker.Choice{Harness: "claude", Location: mainCheckout}
	if len(started.choices) != 1 || started.choices[0] != want {
		t.Fatalf("started %+v, want %+v", started.choices, want)
	}

	if _, ok := model.Chosen(); !ok {
		t.Fatal("Chosen() reports no choice after a successful start")
	}
}

func TestTypedTitleIsPassedTrimmed(t *testing.T) {
	model, started := projectModel(t, 0, "pi")

	run(model, typed("  Fix login redirect ")...)
	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "Fix login redirect" {
		t.Fatalf("started %+v, want the trimmed title", started.choices)
	}
}

func TestNewWorktreesFromOtherBranchesAppearOnlyWhenFiltering(t *testing.T) {
	options := projectOptions(0, "pi", nil)
	fromFeature := task.Location{Where: task.WhereNew, Branch: "feature"}
	options.Locations = append(options.Locations, fromFeature)
	model := sized(options)

	if rows := sectionRows(t, model, "new worktree"); len(rows) != 1 || !strings.Contains(rows[0], "main") {
		t.Fatalf("new worktree rows = %q before filtering, want only main", rows)
	}

	started := &starts{}
	options.Start = started.start
	model = sized(options)

	run(model, key(tea.KeyTab))
	run(model, typed("new feat")...)
	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != fromFeature {
		t.Fatalf("started %+v, want a new worktree from feature", started.choices)
	}
}

func TestLeftAndRightPickTheAgentFromEveryField(t *testing.T) {
	model, started := projectModel(t, 0, "pi")

	run(model, typed("ab")...)
	run(model, key(tea.KeyRight), key(tea.KeyTab), key(tea.KeyRight), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Title != "ab" || started.choices[0].Harness != config.HarnessNone {
		t.Fatalf("started %+v, want title ab with none", started.choices)
	}
}

func TestFilteringNarrowsTheLocations(t *testing.T) {
	model, started := projectModel(t, 0, "pi")

	run(model, key(tea.KeyTab))
	run(model, typed("feature")...)
	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != featureBr {
		t.Fatalf("started %+v, want the feature branch", started.choices)
	}
}

func TestFilterPutsNamesContainingTheQueryBeforeScatteredMatches(t *testing.T) {
	model, _ := projectModel(t, 0, "pi")

	run(model, key(tea.KeyTab))
	run(model, typed("hot")...)

	if row := rowLines(t, model)[0]; !strings.Contains(row, "shop-hotfix") {
		t.Fatalf("first row = %q, want the hotfix worktree before the scattered match in main checkout", row)
	}
}

func TestEscapeClearsTheFilterAndThenClosesWithoutAChoice(t *testing.T) {
	model, started := projectModel(t, 0, "pi")

	run(model, key(tea.KeyTab))
	run(model, typed("feature")...)
	run(model, key(tea.KeyEscape))

	if rows := rowLines(t, model); len(rows) != 5 || !strings.Contains(rows[0], "main") || !strings.Contains(rows[3], "feature") {
		t.Fatalf("after esc the rows are %q, want every location back", rows)
	}

	run(model, key(tea.KeyEscape))

	if _, ok := model.Chosen(); ok || len(started.choices) != 0 {
		t.Fatalf("closing reported a choice or started %+v", started.choices)
	}
}

func TestACheckoutWithATaskGoesToThatTaskInsteadOfStartingOne(t *testing.T) {
	model, started := projectModel(t, 4, "pi")

	if rows := sectionRows(t, model, "in use"); len(rows) != 1 || !strings.Contains(rows[0], "shop-review") || !strings.Contains(rows[0], "review docs") {
		t.Fatalf("in use rows = %q, want the busy checkout with its task", rows)
	}

	run(model, key(tea.KeyEnter))

	if choice, ok := model.Chosen(); !ok || choice.Show != "t7" || len(started.choices) != 0 {
		t.Fatalf("Chosen() = %+v, %t, started %+v; want to go to t7 without starting", choice, ok, started.choices)
	}
}

func TestAFailedStartShowsWhyAndGoesBackToTheForm(t *testing.T) {
	model, started := projectModel(t, 3, "pi")
	started.err = errBranchTaken

	run(model, key(tea.KeyEnter))

	if view := plainView(model); !strings.Contains(view, "branch is already checked out") {
		t.Fatalf("the failure is not shown:\n%s", view)
	}

	if _, ok := model.Chosen(); ok {
		t.Fatal("Chosen() reports a choice after a failed start")
	}

	started.err = nil

	run(model, key(tea.KeyEnter), key(tea.KeyUp), key(tea.KeyEnter))

	if len(started.choices) != 2 || started.choices[1].Location != hotfixTree {
		t.Fatalf("started %+v, want a retry in the hotfix worktree", started.choices)
	}
}

func TestOutsideAProjectTheDirectoryOffersATabAndProjectsOfferAgents(t *testing.T) {
	model, started := looseModel(t)

	if row := agentRow(t, model); !strings.Contains(row, "tab   pi   claude") || strings.Contains(row, "none") {
		t.Fatalf("agent row here = %q, want tab, pi, claude", row)
	}

	run(model, key(tea.KeyEnter))

	if len(started.choices) != 1 || !started.choices[0].Tab {
		t.Fatalf("started %+v, want a plain tab", started.choices)
	}

	model, started = looseModel(t)
	run(model, key(tea.KeyDown))

	if row := agentRow(t, model); strings.Contains(row, "tab") || !strings.Contains(row, "none") {
		t.Fatalf("agent row for a project = %q, want the agents and none", row)
	}

	run(model, key(tea.KeyEnter))

	choice, ok := model.Chosen()

	if len(started.choices) != 1 || started.choices[0].Project != "/code/web" || started.choices[0].Harness != "pi" {
		t.Fatalf("started %+v, want pi in the web project", started.choices)
	}

	if !ok || choice.Show != "t9" {
		t.Fatalf("Chosen() = %+v, %t; want to go to the started task", choice, ok)
	}
}

func TestSelectingARegisteredFolderStartsInItsMainDirectory(t *testing.T) {
	started := &starts{}
	folder := project.Project{Root: "/home/dev/notes", Name: "notes", Kind: project.KindFolder}
	model := sized(taskpicker.Options{
		Dir: "/home/dev/scratch", Home: "/home/dev", Projects: []project.Project{folder},
		Harnesses: []string{"pi"}, LastHarness: "", Start: started.start,
	})

	run(model, key(tea.KeyDown), key(tea.KeyEnter))

	want := taskpicker.Choice{Project: folder.Root, Harness: "pi", Location: task.Location{Where: task.WhereMain, Directory: folder.Root}}
	if len(started.choices) != 1 || started.choices[0] != want {
		t.Fatalf("started %+v, want %+v", started.choices, want)
	}

	if choice, ok := model.Chosen(); !ok || choice.Show != "t9" {
		t.Fatalf("Chosen() = %+v, %t; want to show the started task", choice, ok)
	}
}

func TestClickingAnAgentSelectsIt(t *testing.T) {
	model, started := projectModel(t, 0, "pi")
	lines := strings.Split(plainView(model), "\n")
	row := slicesIndex(lines, "agent")
	column := strings.Index(lines[row], "none")

	run(model, tea.MouseClickMsg(tea.Mouse{X: column, Y: row, Button: tea.MouseLeft}), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Harness != config.HarnessNone {
		t.Fatalf("started %+v, want none", started.choices)
	}
}

func TestPopupFitsEveryOfferWithoutTruncating(t *testing.T) {
	options := projectOptions(0, "pi", nil)
	options.Locations = append([]task.Location{{Where: task.WhereNew, Branch: "wt/fix-login-redirect"}}, options.Locations...)
	model := sized(options)
	width, height := taskpicker.Size(config.DefaultConfig(), options)
	view := plainView(model)

	if lines := strings.Split(view, "\n"); len(lines) != height {
		t.Fatalf("view has %d lines, want %d", len(lines), height)
	}

	for line := range strings.SplitSeq(view, "\n") {
		if lineWidth := ansi.StringWidth(line); lineWidth != width {
			t.Fatalf("line %q is %d wide, want %d", line, lineWidth, width)
		}
	}

	for _, want := range []string{"new task", "shop", "new worktree", "checkouts", "branches", "in use", "shop-hotfix", "hotfix", "feature", "review docs", "wt/fix-login-redirect"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view does not show %q:\n%s", want, view)
		}
	}
}

func TestKeyHintsShowOnlyWhenTheFooterIsEnabled(t *testing.T) {
	options := projectOptions(0, "pi", nil)

	if view := plainView(sized(options)); strings.Contains(view, "enter") {
		t.Fatalf("the default popup shows key hints:\n%s", view)
	}

	cfg := config.DefaultConfig()
	cfg.Tasks.ShowFooter = true
	model := taskpicker.NewModel(cfg, options)
	width, height := taskpicker.Size(cfg, options)
	model.Update(tea.WindowSizeMsg{Width: width, Height: height})

	if view := plainView(model); !strings.Contains(view, "enter start") {
		t.Fatalf("the popup with show_footer hides key hints:\n%s", view)
	}
}

func TestArrowKeysWrapAroundTheList(t *testing.T) {
	model, started := projectModel(t, 0, "pi")

	run(model, key(tea.KeyUp), key(tea.KeyEnter))

	if choice, ok := model.Chosen(); !ok || choice.Show != "t7" || len(started.choices) != 0 {
		t.Fatalf("up from the first row chose %+v, %t; want the last row, the busy checkout", choice, ok)
	}

	model, started = projectModel(t, 4, "pi")
	run(model, key(tea.KeyDown), key(tea.KeyEnter))

	if len(started.choices) != 1 || started.choices[0].Location != newWorktree {
		t.Fatalf("down from the last row started %+v, want the first row", started.choices)
	}
}

func TestLongLocationListsScrollToKeepTheSelectionVisible(t *testing.T) {
	options := projectOptions(0, "pi", nil)
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

func projectModel(t *testing.T, location int, lastHarness string) (*taskpicker.Model, *starts) {
	t.Helper()

	started := &starts{}

	return sized(projectOptions(location, lastHarness, started.start)), started
}

func looseModel(t *testing.T) (*taskpicker.Model, *starts) {
	t.Helper()

	started := &starts{}

	return sized(taskpicker.Options{
		Dir: "/home/dev/notes", Home: "/home/dev", Projects: []project.Project{webProject},
		Harnesses: []string{"pi", "claude"}, LastHarness: "", Start: started.start,
	}), started
}

func projectOptions(location int, lastHarness string, start func(taskpicker.Choice) (string, error)) taskpicker.Options {
	return taskpicker.Options{
		Project:     "shop",
		Home:        "/home/dev",
		Locations:   []task.Location{newWorktree, mainCheckout, hotfixTree, featureBr, reviewTree},
		Location:    location,
		Harnesses:   []string{"pi", "claude"},
		LastHarness: lastHarness,
		Start:       start,
	}
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

// listLines are the lines of the where list: from below the title and filter lines to the blank
// line above the agent row.
func listLines(t *testing.T, model *taskpicker.Model) []string {
	t.Helper()

	lines := strings.Split(plainView(model), "\n")

	end := 3
	for end < len(lines) && strings.TrimSpace(strings.Trim(lines[end], "│")) != "" {
		end++
	}

	return lines[3:end]
}

// rowLines are the where rows, without the section headings.
func rowLines(t *testing.T, model *taskpicker.Model) []string {
	t.Helper()

	var rows []string

	for _, line := range listLines(t, model) {
		if strings.Contains(line, "─ ") {
			rows = append(rows, line)
		}
	}

	return rows
}

// sectionRows are the rows under the section heading label.
func sectionRows(t *testing.T, model *taskpicker.Model, label string) []string {
	t.Helper()

	var rows []string

	inside := false

	for _, line := range listLines(t, model) {
		switch {
		case !strings.Contains(line, "─ "):
			inside = strings.TrimSpace(strings.Trim(line, "│")) == label
		case inside:
			rows = append(rows, line)
		}
	}

	return rows
}

func agentRow(t *testing.T, model *taskpicker.Model) string {
	t.Helper()

	lines := strings.Split(plainView(model), "\n")

	return lines[slicesIndex(lines, "agent")]
}

// slicesIndex finds the body line that starts with label.
func slicesIndex(lines []string, label string) int {
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimPrefix(line, "│ "), label) {
			return index
		}
	}

	return -1
}
