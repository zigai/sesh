package tui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

type popupViewRequest chan string

type popupStartupTestModel struct {
	*Model
}

func (m popupStartupTestModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if reply, ok := msg.(popupViewRequest); ok {
		reply <- viewString(m.View())

		return m, nil
	}

	_, command := m.Model.Update(msg)

	return m, command
}

func TestPopupStartupShowsLoadingWhileInitialLoadIsBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})

		var released sync.Once

		unblock := func() { released.Do(func() { close(release) }) }
		cfg := config.DefaultConfig()
		cfg.AgentSessions.Enabled = true
		cfg.Settings.DisplayModes = []string{"agents"}
		cfg.AgentSessions.PopupCounts = false
		model := loaderTestModel(t, cfg, Dependencies{
			LoadCatalog: func(context.Context) (session.CatalogResult, error) {
				<-release

				return session.CatalogResult{
					Sessions: []session.Session{{ID: "$1", Name: "loaded-agent-session", Kind: session.KindLive}},
					Status:   session.StatusComplete,
				}, nil
			},
			LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
				<-release

				return []session.AgentSummary{{TmuxSessionID: "$1", Total: 1}}, nil
			},
		})
		model.mode = modePopup
		program := tea.NewProgram(popupStartupTestModel{Model: model}, tea.WithInput(nil), tea.WithOutput(io.Discard),
			tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithWindowSize(80, 20))
		done := make(chan error, 1)

		go func() {
			_, err := program.Run()
			done <- err
		}()

		defer func() {
			unblock()
			program.Quit()

			if err := <-done; err != nil {
				t.Errorf("popup program failed: %v", err)
			}

			model.Close()
			time.Sleep(time.Minute)
			synctest.Wait()
		}()

		readView := func() string {
			reply := make(popupViewRequest, 1)
			program.Send(reply)

			return stripANSI(<-reply)
		}

		synctest.Wait()

		if view := readView(); view != "" {
			t.Fatalf("initial popup view = %q, want blank during fast startup", view)
		}

		time.Sleep(200 * time.Millisecond)
		synctest.Wait()

		view := readView()
		if !strings.Contains(view, "Loading sessions...") {
			t.Fatalf("blocked popup view = %q, want visible loading status", view)
		}

		assertRightBorder(t, view, 80)

		if strings.Contains(view, "No live agent sessions found") || strings.Contains(view, "loaded-agent-session") {
			t.Fatalf("blocked popup view = %q, want no empty state or incomplete rows", view)
		}

		unblock()
		synctest.Wait()

		view = readView()
		if !strings.Contains(view, "loaded-agent-session") || strings.Contains(view, "Loading sessions...") {
			t.Fatalf("completed popup view = %q, want populated agent session", view)
		}
	})
}

func TestPopupStartupKeepsHiddenRowsInactiveUntilLoadingCompletes(t *testing.T) {
	for _, waiting := range []bool{true, false} {
		t.Run(map[bool]string{true: "waiting", false: "loading"}[waiting], func(t *testing.T) {
			model := newTestModelWithDeps(Dependencies{
				RequestOriginSwitch: func(context.Context, session.Session) error { return nil },
			})
			defer model.Close()

			model.mode = modePopup
			model.displayMode = displayModeAgents
			model.initialAgentSelectionPending = true
			model.SetSize(80, 20)
			model.Init()
			updateTestModel(t, model, originSessionsMsg{rows: []session.Session{{
				ID: "$2", Name: "hidden-origin-session", Kind: session.KindLive, Origin: session.OriginBridge,
				AgentTotal: 1, Agents: []session.AgentSession{{PaneID: "%2", Harness: "codex"}},
			}}})

			if !waiting {
				updateTestModel(t, model, popupStartupDeadlineMsg{})
			}

			layout := model.popupMouseLayout()

			inputs := []tea.Msg{
				keyCode(tea.KeyEnter), keyCtrl('1'), keyCode(tea.KeyDown), keyCode(tea.KeyPgDown),
				mouseClick(layout.bodyX, layout.listY, tea.MouseLeft),
				mouseMotion(layout.bodyX, layout.listY+1),
				mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelDown),
			}
			for _, input := range inputs {
				_, cmd := model.Update(input)
				if cmd != nil || !model.initialAgentSelectionPending || model.agentCursor != parentAgentIndex {
					t.Fatalf("hidden row input %T produced command or changed initial selection", input)
				}
			}

			model.handleSessionLoadResult(sessionLoadResultMsg{status: session.StatusComplete})

			_, cmd := model.Update(keyCode(tea.KeyEnter))
			if cmd == nil {
				t.Fatalf("completed popup did not allow opening origin row: filtered=%#v status=%q query=%q bridge=%T", model.filtered, model.statusMessage, model.textInput.Value(), model.deps.OriginBridge)
			}

			trace := runActionTrace(t, model, cmd)
			if len(trace.results) != 1 || trace.results[0].request.subject.Sessions[0].Name != "hidden-origin-session" || !trace.quit {
				t.Fatalf("completed row action = %+v, want origin session activation", trace)
			}
		})
	}
}

func TestPopupStartupAllowsSearchAndClose(t *testing.T) {
	model := newTestModel()
	defer model.Close()

	model.mode = modePopup
	model.Init()
	updateTestModel(t, model, keyText("dev"))

	if got := model.textInput.Value(); got != "dev" {
		t.Fatalf("startup search = %q, want dev", got)
	}

	_, command := model.Update(keyCode(tea.KeyEscape))
	if command == nil {
		t.Fatal("startup close key returned no command")
	}

	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("startup close key did not quit the picker")
	}
}
