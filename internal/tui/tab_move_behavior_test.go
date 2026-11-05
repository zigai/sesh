package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestTabMoveDestinationClickMovesRatherThanAttaches(t *testing.T) {
	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			model := newTabMoveTestModel(t, mode)
			calls := 0
			dependenciesForTest(model).RunAction = func(_ context.Context, invocation action.Invocation) (any, error) {
				if invocation.Action != action.SessionMoveWindow.ID {
					t.Fatalf("click invoked %s instead of move", invocation.Action)
				}

				calls++

				return action.MovedWindow{}, nil
			}
			beginTestTabMove(t, model)

			layout := model.popupMouseLayout()
			for index, row := range model.visibleListRows(layout.layout.listHeight) {
				if row.isAgent() || model.filtered[row.sessionIndex].ID != "$2" {
					continue
				}

				_, cmd := model.Update(
					mouseClick(layout.bodyX+3, layout.listY+index, tea.MouseLeft),
				)
				if cmd == nil {
					t.Fatal("destination click did not submit move")
				}

				_ = cmd()

				if calls != 1 || !model.tabMove.pending || model.switchAfterQuit != "" ||
					model.menu != nil {
					t.Fatal("destination click attached, opened menu, or did not move exactly once")
				}

				return
			}

			t.Fatal("destination row not visible")
		})
	}
}

func TestTabMoveDisablesNormalActionsAndUsesNoninteractiveModeBadge(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	beginTestTabMove(t, model)

	for _, key := range []tea.KeyPressMsg{keyAlt("x"), keyAlt("r"), keyAlt("m"), keyCode(tea.KeyTab)} {
		model.Update(key)

		if model.tabMove == nil || model.menu != nil || model.dialog != dialogNone ||
			model.displayMode != displayModeAgents {
			t.Fatalf("normal action %s escaped destination selection", key.String())
		}
	}

	badges := model.footerBadges()
	if len(badges) != 1 || !strings.Contains(badges[0].text, "MOVE") || badges[0].click != nil {
		t.Fatalf("move mode left sort/group controls active: %+v", badges)
	}

	if handler := model.resolveFooterAction(newFooterPart("esc", "cancel")); handler == nil {
		t.Fatal("footer cancellation missing")
	} else {
		_, cmd := handler()
		if cmd != nil || model.tabMove != nil {
			t.Fatal("footer cancel quit instead of restoring view")
		}
	}
}

func TestTabMoveMenuUnavailableForRemoteAndHistoricalInboxRows(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.sessions[0].Origin = session.OriginSSH
	model.refreshDisplaySessions()
	model.filterSessions()

	for si, row := range model.filtered {
		if row.ID == "$1" {
			model.selectListRow(popupListRow{sessionIndex: si, agentIndex: 0})
		}
	}

	if _, ok := model.tabMoveSubject(); ok {
		t.Fatal("remote agent offered local move")
	}

	model = newTabMoveTestModel(t, displayModeInbox)
	selected := model.selectedListRow()

	item, ok := model.selectedInboxItem()
	if !ok {
		t.Fatal("missing Inbox source")
	}

	item.owner = session.Session{}
	item.kind = inboxClosedRow
	model.inboxItems[inboxKey(item.agent.Host, item.agent.PaneID)] = item
	model.selectListRow(selected)

	if _, ok := model.tabMoveSubject(); ok {
		t.Fatal("historical Inbox row offered move")
	}
}

func TestTabMoveSuccessMigratesReadTimestampWithoutMarkingUnseenPanes(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	source, destination := model.sessions[0], model.sessions[1]
	viewed := time.Unix(50, 123)
	model.state.RecordAgentViewAt(source.SemanticIdentity(), "%1", viewed)
	beginTestTabMove(t, model)
	selectTabMoveDestination(t, model, destination.ID)

	_, cmd := model.confirmTabMove()
	if cmd == nil {
		t.Fatal("move command missing")
	}

	model.handleActionResult(actionResultMsg{
		request: pendingAction{entry: action.SessionMoveWindow},
		output: action.MovedWindow{
			Source: action.RefOf(source), Destination: action.RefOf(destination),
			Moved: true, PaneIDs: []string{"%1", "%99"}, HistoryUpdated: true,
		},
	})

	if got := model.state.GetLastAgentView(
		destination.SemanticIdentity(),
		"%1",
	); got != viewed.UnixNano() {
		t.Fatalf("view timestamp = %d, want %d", got, viewed.UnixNano())
	}

	if model.state.GetLastAgentView(source.SemanticIdentity(), "%1") != 0 ||
		model.state.GetLastAgentView(destination.SemanticIdentity(), "%99") != 0 {
		t.Fatal("move retained stale ownership or marked an unseen pane viewed")
	}
}

func TestTabMovePartialOrUncertainMutationCannotBeResubmitted(t *testing.T) {
	for _, knownMoved := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "uncertain", true: "history-failed"}[knownMoved],
			func(t *testing.T) {
				model := newTabMoveTestModel(t, displayModeAgents)
				beginTestTabMove(t, model)

				_, cmd := model.confirmTabMove()
				if cmd == nil {
					t.Fatal("move command missing")
				}

				code := action.CodeUncertain
				if knownMoved {
					code = action.CodePartial
				}

				_, refresh := model.handleActionResult(actionResultMsg{
					request: pendingAction{entry: action.SessionMoveWindow},
					output:  action.MovedWindow{Moved: knownMoved},
					err:     action.Wrap(code, errTmuxUnavailable),
				})
				if refresh == nil || model.tabMove != nil ||
					!strings.Contains(model.statusMessage, "Move incomplete") {
					t.Fatal("uncertain or completed mutation left duplicate submission armed")
				}
			},
		)
	}
}

func TestTabMoveClearsSourceOnlyQueryToRevealMovedAgent(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.textInput.SetValue("source")
	model.filterSessions()
	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})
	beginTestTabMove(t, model)
	model.textInput.SetValue("")
	model.filterSessions()
	selectTabMoveDestination(t, model, "$2")
	model.confirmTabMove()
	model.handleActionResult(actionResultMsg{
		request: pendingAction{entry: action.SessionMoveWindow},
		output:  action.MovedWindow{Moved: true},
	})
	rows := slices.Clone(model.sessions)
	rows[1].Agents = append(slices.Clone(rows[1].Agents), rows[0].Agents...)
	rows[0].Agents = nil
	model.applySessionRows(
		sessionLoadResultMsg{
			sessions:         rows,
			moveObservation:  model.tabMoveObservation,
			agentObservation: model.agentSummaryObservation,
		},
	)

	if model.textInput.Value() != "" || model.selectedSessionIdentity().paneID != "%1" ||
		model.filtered[model.cursor].ID != "$2" {
		t.Fatal("source-only query hid the moved agent")
	}
}

func TestTabMoveSuccessClearsSourceSessionKeyboardFocus(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.focusedSession = model.sessions[0].RuntimeKey()
	beginTestTabMove(t, model)
	model.confirmTabMove()
	model.handleActionResult(actionResultMsg{
		request: pendingAction{entry: action.SessionMoveWindow},
		output:  action.MovedWindow{Moved: true},
	})

	if model.focusedSession != "" {
		t.Fatal("successful move left keyboard shortcuts focused on source session")
	}
}

func TestTabMoveLateRefreshDoesNotStealSelectionAfterMouseHover(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	beginTestTabMove(t, model)
	model.confirmTabMove()
	model.handleActionResult(actionResultMsg{
		request: pendingAction{entry: action.SessionMoveWindow},
		output:  action.MovedWindow{Moved: true},
	})

	if model.movedAgentSelection == nil {
		t.Fatal("missing pending follow after move")
	}

	layout := model.popupMouseLayout()
	for index, row := range model.visibleListRows(layout.layout.listHeight) {
		if !row.isAgent() ||
			model.filtered[row.sessionIndex].Agents[row.agentIndex].PaneID != "%2" {
			continue
		}

		model.Update(mouseMotion(layout.bodyX+3, layout.listY+index))

		if model.movedAgentSelection != nil || model.selectedSessionIdentity().paneID != "%2" {
			t.Fatal("hover did not cancel pending follow before background refresh")
		}

		return
	}

	t.Fatal("hover target not found")
}

func TestTabMoveConfigurableActionUsesSameViewDestinationSelection(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)

	handler := model.invokeAction(action.SessionMoveWindow.ID, nil)
	if handler == nil {
		t.Fatal("move action unavailable")
	}

	_, cmd := handler()
	if cmd != nil || model.tabMove == nil || model.dialog != dialogNone ||
		model.displayMode != displayModeAgents {
		t.Fatal("move action opened a text prompt or replaced the existing view")
	}
}
