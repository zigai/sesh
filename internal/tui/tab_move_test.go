package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func newTabMoveTestModel(t *testing.T, mode displayMode) *Model {
	t.Helper()
	model := newTestModelWithConfig(t, config.DefaultConfig())
	model.mode = modePopup
	model.width, model.height = 160, 30
	model.displayMode = mode
	model.initialAgentSelectionPending = false
	model.config.Inbox.Collapsed = nil
	model.sessions = []session.Session{
		{
			ID: "$1", Name: "source", Kind: session.KindLive, Origin: session.OriginLocal,
			TmuxServerSocket: "/tmp/move-test/socket", AgentTotal: 1,
			Agents: []session.AgentSession{
				{
					RegistryID:  "agent-1",
					PaneID:      "%1",
					Task:        "Move tabs",
					Harness:     "pi",
					CWD:         "/work/api",
					ProjectRoot: "/work/api",
					Status:      session.AgentStatusRunning,
					StatusSince: time.Unix(1, 0),
					GitChanges:  "+13 -11",
				},
			},
		},
		{
			ID: "$2", Name: "destination", Kind: session.KindLive, Origin: session.OriginLocal,
			TmuxServerSocket: "/tmp/move-test/socket", AgentTotal: 1,
			Agents: []session.AgentSession{
				{
					RegistryID:     "agent-2",
					PaneID:         "%2",
					Task:           "Other work",
					HarnessSession: "Other work",
					Harness:        "claude",
					CWD:            "/work/other",
					Status:         session.AgentStatusRunning,
					StatusSince:    time.Unix(1, 0),
				},
			},
		},
		{
			ID: "$3", Name: "empty", Kind: session.KindLive, Origin: session.OriginLocal,
			TmuxServerSocket: "/tmp/move-test/socket",
		},
		{
			ID: "$4", Name: "other-server", Kind: session.KindLive, Origin: session.OriginLocal,
			TmuxServerSocket: "/different/socket",
		},
		{ID: "$5", Name: "remote", Kind: session.KindLive, Origin: session.OriginSSH},
	}
	model.refreshDisplaySessions()
	model.filterSessions()

	for si, row := range model.filtered {
		for ai, agent := range row.Agents {
			if agent.PaneID == "%1" {
				model.selectListRow(popupListRow{sessionIndex: si, agentIndex: ai})
				return model
			}
		}
	}

	t.Fatal("source agent not present")

	return nil
}

func beginTestTabMove(t *testing.T, model *Model) {
	t.Helper()

	handler := model.openRowMenu()
	if handler == nil {
		t.Fatal("row menu unavailable")
	}

	handler()

	index := slices.IndexFunc(model.menu.entries, func(entry menuEntry) bool {
		return entry.label == "move"
	})
	if index < 0 {
		t.Fatal("move entry missing from row menu")
	}

	model.runMenuEntry(index)()

	if model.tabMove == nil || model.menu != nil {
		t.Fatal("move entry did not close menu and enter destination mode")
	}
}

func selectTabMoveDestination(t *testing.T, model *Model, id string) {
	t.Helper()

	for _, row := range model.popupSelectableRows() {
		if model.filtered[row.sessionIndex].ID == id {
			model.selectListRow(row)
			return
		}
	}

	t.Fatalf("destination %s not selectable", id)
}

func TestTabMoveKeepsVisibleViewAndOnlySelectsLiveSameServerSessions(t *testing.T) {
	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			model := newTabMoveTestModel(t, mode)
			before := slices.Clone(model.filtered)
			query := model.textInput.Value()
			beginTestTabMove(t, model)

			if model.displayMode != mode || model.textInput.Value() != query {
				t.Fatal("move mode replaced view or search")
			}

			if len(model.filtered) < len(before) ||
				!reflect.DeepEqual(before, model.filtered[:len(before)]) {
				t.Fatal("move mode changed the existing rows or agent content")
			}

			destinations := make([]string, 0, len(model.popupSelectableRows()))
			for _, row := range model.popupSelectableRows() {
				if row.isAgent() || !model.rowSelectable(row) {
					t.Fatalf("invalid selectable destination: %+v", row)
				}

				destinations = append(destinations, model.filtered[row.sessionIndex].ID)
			}

			if !reflect.DeepEqual(destinations, []string{"$2", "$3"}) {
				t.Fatalf("destinations = %v, want [$2 $3]", destinations)
			}

			for _, row := range model.popupListRows() {
				if row.isAgent() && (model.rowSelectable(row) || !model.fadesListRow(row)) {
					t.Fatalf("agent row is not disabled and dimmed: %+v", row)
				}
			}
		})
	}
}

func TestTabMoveNavigationAndCancelRestoreContext(t *testing.T) {
	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			model := newTabMoveTestModel(t, mode)
			model.textInput.SetValue("api")
			model.textInput.SetCursor(2)
			// Preserve a real selected identity even if the query subsequently changes.
			original := model.selectedSessionIdentity()
			model.listTop = 1
			model.listViewportPolicy = listViewportPreserveTop
			beginTestTabMove(t, model)
			model.textInput.SetValue("")
			model.refreshDisplaySessions()
			model.filterSessions()

			for _, key := range []tea.KeyPressMsg{
				keyCode(tea.KeyDown), keyCode(tea.KeyUp),
				keyCtrlCode(tea.KeyDown), keyCtrlCode(tea.KeyUp), keyCode(tea.KeyPgDown),
				keyCode(tea.KeyPgUp), keyCode(tea.KeyHome), keyCode(tea.KeyEnd),
			} {
				model.Update(key)

				if !model.tabMoveDestination(model.selectedListRow()) {
					t.Fatalf(
						"key %s selected a non-session row: %+v",
						key.String(),
						model.selectedListRow(),
					)
				}
			}

			model.Update(keyCode(tea.KeyEsc))

			if model.tabMove != nil || model.textInput.Value() != "api" ||
				model.textInput.Position() != 2 ||
				model.listTop != 1 ||
				model.listViewportPolicy != listViewportPreserveTop ||
				model.selectedSessionIdentity().paneID != original.paneID {
				t.Fatalf(
					"cancel did not restore context: query=%q selection=%+v top=%d",
					model.textInput.Value(),
					model.selectedSessionIdentity(),
					model.listTop,
				)
			}
		})
	}
}

func TestTabMoveSearchNeverSelectsAgentAndNoMatchCannotDispatch(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	beginTestTabMove(t, model)
	model.Update(keyText("destination"))

	if !model.tabMoveDestination(model.selectedListRow()) ||
		model.filtered[model.cursor].ID != "$2" {
		t.Fatal("search did not select destination session heading")
	}

	model.Update(keyText("not-a-session"))

	_, cmd := model.Update(keyCode(tea.KeyEnter))
	if cmd != nil || model.tabMove.pending {
		t.Fatal("empty destination list dispatched a move")
	}

	if !strings.Contains(model.tabMoveInstruction(), "No destination") {
		t.Fatal("missing empty destination instruction")
	}

	model.Update(keyCode(tea.KeyEsc))
}

func TestTabMoveMouseIgnoresAgentsAndContextMenus(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	beginTestTabMove(t, model)

	layout := model.popupMouseLayout()
	for index, row := range model.visibleListRows(layout.layout.listHeight) {
		if !row.isAgent() {
			continue
		}

		before := model.selectedListRow()

		_, cmd := model.Update(mouseClick(layout.bodyX+3, layout.listY+index, tea.MouseLeft))
		if cmd != nil || !model.selectedListRow().equal(before) || model.tabMove.pending {
			t.Fatal("clicking dimmed agent executed an action or changed destination")
		}

		model.Update(mouseClick(layout.bodyX+3, layout.listY+index, tea.MouseRight))

		if model.menu != nil {
			t.Fatal("right click opened menu during destination selection")
		}

		return
	}

	t.Fatal("no visible agent row to click")
}

func TestTabMoveCapturesSourceAndDestinationAndBlocksDuplicateSubmissions(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)

	var captured action.MoveWindow

	calls := 0
	dependenciesForTest(model).RunAction = func(_ context.Context, invocation action.Invocation) (any, error) {
		calls++

		var ok bool

		captured, ok = invocation.Input.(action.MoveWindow)
		if !ok {
			t.Fatalf("input = %T, want MoveWindow", invocation.Input)
		}

		return action.MovedWindow{}, nil
	}
	beginTestTabMove(t, model)
	selectTabMoveDestination(t, model, "$3")

	_, cmd := model.Update(keyCode(tea.KeyEnter))
	if cmd == nil || !model.tabMove.pending {
		t.Fatal("confirm did not dispatch")
	}

	if _, duplicate := model.Update(keyCode(tea.KeyEnter)); duplicate != nil {
		t.Fatal("second Enter submitted another move")
	}

	query := model.textInput.Value()
	model.Update(tea.PasteMsg{Content: "ignored while moving"})

	if model.textInput.Value() != query {
		t.Fatal("paste changed destination search during an in-flight move")
	}

	model.Update(keyCode(tea.KeyEsc))

	if model.tabMove == nil {
		t.Fatal("cancel abandoned an in-flight mutation")
	}

	_ = cmd()

	if calls != 1 || captured.Source.ID != "$1" || captured.Pane != "%1" ||
		captured.Destination.ID != "$3" ||
		captured.Destination.Name != "empty" ||
		captured.Destination.Socket != "/tmp/move-test/socket" {
		t.Fatalf("move request lost captured identities: calls=%d request=%+v", calls, captured)
	}
}

func TestTabMoveRefreshPreservesDestinationByIDAndRejectsVanishedTargets(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	beginTestTabMove(t, model)
	selectTabMoveDestination(t, model, "$3")

	selected := model.selectedSessionIdentity()
	for i := range model.sessions {
		if model.sessions[i].ID == "$3" {
			model.sessions[i].Name = "renamed"
		}
	}

	slices.Reverse(model.sessions)
	model.refreshDisplaySessions()
	model.filterSessions()
	model.restoreSelectedSession(selected)

	if model.filtered[model.cursor].ID != "$3" || model.tabMove.subject.Sessions[0].ID != "$1" {
		t.Fatal("refresh redirected destination or source")
	}

	model.sessions = slices.DeleteFunc(
		model.sessions,
		func(row session.Session) bool { return row.ID != "$1" },
	)
	model.refreshDisplaySessions()
	model.filterSessions()

	if _, cmd := model.confirmTabMove(); cmd != nil {
		t.Fatal("vanished destination was submitted")
	}
}

func TestTabMoveFailurePreservesRowsAndAllowsRetryOrCancel(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	dependenciesForTest(model).RunAction = func(context.Context, action.Invocation) (any, error) {
		return nil, errTmuxUnavailable
	}
	before := slices.Clone(model.sessions)
	beginTestTabMove(t, model)
	_, cmd := model.confirmTabMove()

	msg, ok := cmd().(actionResultMsg)
	if !ok {
		t.Fatal("move command did not return actionResultMsg")
	}

	_, refresh := model.handleActionResult(msg)
	if refresh == nil || model.tabMove == nil || model.tabMove.pending ||
		!strings.Contains(model.statusMessage, "Move failed") {
		t.Fatal("move failure did not refresh and allow recovery")
	}

	if !reflect.DeepEqual(before, model.sessions) {
		t.Fatal("failed move optimistically changed the session rows")
	}

	model.Update(keyCode(tea.KeyEsc))

	if model.tabMove != nil {
		t.Fatal("failed move cannot be canceled")
	}
}

func TestTabMoveSuccessFollowsAgentAfterCatalogRefresh(t *testing.T) {
	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			model := newTabMoveTestModel(t, mode)
			beginTestTabMove(t, model)
			selectTabMoveDestination(t, model, "$2")

			_, cmd := model.confirmTabMove()
			if cmd == nil {
				t.Fatal("move command missing")
			}

			request := pendingAction{entry: action.SessionMoveWindow}

			_, refresh := model.handleActionResult(
				actionResultMsg{request: request, output: action.MovedWindow{Moved: true}},
			)
			if refresh == nil || model.tabMove != nil || model.movedAgentSelection == nil {
				t.Fatal("success did not restore normal mode and schedule refresh")
			}

			if !strings.Contains(
				stripANSI(viewString(model.View())),
				"Moved Move tabs to destination",
			) {
				t.Fatal("successful move has no visible confirmation")
			}

			rows := slices.Clone(model.sessions)
			moved := rows[0].Agents[0]
			rows[0].Agents = nil
			rows[0].AgentTotal = 0
			rows[1].Agents = append(slices.Clone(rows[1].Agents), moved)
			rows[1].AgentTotal = len(rows[1].Agents)
			model.applySessionRows(
				sessionLoadResultMsg{
					sessions:         rows,
					moveObservation:  model.tabMoveObservation,
					agentObservation: model.agentSummaryObservation,
				},
			)

			if model.selectedSessionIdentity().paneID != "%1" || model.movedAgentSelection != nil {
				t.Fatalf("moved agent not followed: %+v", model.selectedSessionIdentity())
			}

			owner := model.filtered[model.cursor]
			if item, ok := model.selectedInboxItem(); ok {
				owner = item.owner
			}

			if owner.ID != "$2" {
				t.Fatalf("moved agent owner = %s, want $2", owner.ID)
			}
		})
	}
}

func TestTabMoveVisualGolden(t *testing.T) {
	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		for _, width := range []int{48, 96, 160} {
			t.Run(fmt.Sprintf("%s/%d", mode, width), func(t *testing.T) {
				model := newTabMoveTestModel(t, mode)
				model.width, model.height = width, 24
				beginTestTabMove(t, model)
				rendered := viewString(model.View())
				assertVisualViewport(t, rendered, width, 24)
				assertVisualText(t, stripANSI(rendered), "enter move", "esc cancel", "MOVE")

				layout := model.calcSessionLayout(width - popupBodyWidthMargin)

				styles := model.sessionRowStyles(layout)
				for _, row := range model.popupListRows() {
					if !row.isAgent() {
						continue
					}

					var (
						out    strings.Builder
						values sessionFormatValues
					)
					model.writePopupListRow(&out, layout, styles, row, &values)

					if !strings.Contains(out.String(), styles.fadedSeq) ||
						strings.Contains(ansi.Strip(out.String()), "\x1b") {
						t.Fatal("agent row is not rendered with uniform dim styling")
					}
				}
				// Keep an ANSI golden as well as assertions on text and bounds: changes
				// to status/Git colors on disabled rows must be visible to review.
				path := filepath.Join(
					"testdata",
					"tab_move",
					fmt.Sprintf("%s_%d.golden", mode, width),
				)
				// Normalize status ages so goldens do not depend on wall-clock time.
				model.config.Display.AgentsAgentFormat = "{tree}{agent_session} {agent_git_changes}"
				model.config.Display.InboxAgentFormat = model.config.Display.AgentsAgentFormat

				formats, err := compileSessionRowFormats(
					model.config.Display,
					model.config.Settings,
				)
				if err != nil {
					t.Fatal(err)
				}

				model.sessionFormats = formats
				got := viewString(model.View())

				if os.Getenv("SESH_UPDATE_AGENT_GOLDENS") == "1" {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}

					if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}

				if got != string(want) {
					t.Fatalf("move view changed\ngot: %q\nwant: %q", got, want)
				}
			})
		}
	}
}
