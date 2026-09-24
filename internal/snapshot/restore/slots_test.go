package restore

import "testing"

type slotSimulation map[string]map[int]string

func assertSimulatedSlots(t *testing.T, plan Plan, base int) {
	t.Helper()

	sessions := make(slotSimulation)
	for _, step := range plan.Steps {
		sessions.apply(t, step, base)
	}

	for session, slots := range sessions {
		want := make(map[int]string)

		for _, link := range plan.SourceGraph.Links {
			if link.Session == session {
				want[link.Index] = link.Window
			}
		}

		if len(want) != len(slots) {
			t.Fatalf("unexpected final slots for %s: %v", session, slots)
		}

		for index, window := range want {
			if slots[index] != window {
				t.Fatalf("session %s slot %d: got %s want %s", session, index, slots[index], window)
			}
		}
	}
}

func (s slotSimulation) apply(t *testing.T, step Step, base int) {
	t.Helper()

	switch step := step.(type) {
	case *CreateSessionStep:
		cs := step
		if cs.GroupLeaderSessionID != "" {
			s[cs.SessionID] = s[cs.GroupLeaderSessionID]
		} else {
			window := cs.InitialWindowID
			if cs.BootstrapWindow {
				window = "bootstrap:" + cs.SessionID
			}

			s[cs.SessionID] = map[int]string{base: window}
		}
	case *PlaceInitialWindowStep:
		s.place(t, *step)
	case *CreateWindowStep:
		cw := step
		s.link(t, cw.OwnerSessionID, cw.WindowID, cw.Index)
	case *LinkWindowStep:
		lw := step
		s.link(t, lw.SessionID, lw.WindowID, lw.Index)
	case *CloseBootstrapWindowStep:
		s.closeBootstrap(t, step.SessionID)
	case *SelectWindowStep:
		s.selectWindow(t, *step)
	case *SplitPaneStep, *ApplyLayoutStep, *SetPaneTitleStep, *SelectPaneStep, *ApplyZoomStep:
	}
}

func (s slotSimulation) place(t *testing.T, step PlaceInitialWindowStep) {
	t.Helper()

	slots := s[step.SessionID]
	if len(slots) != 1 {
		t.Fatal("placement must precede additional window links")
	}

	for index, window := range slots {
		delete(slots, index)
		slots[step.Index] = window

		break
	}
}

func (s slotSimulation) link(t *testing.T, session, window string, index int) {
	t.Helper()

	slots := s[session]
	if slots == nil || slots[index] != "" {
		t.Fatalf("link into missing session or occupied slot: %s:%d", session, index)
	}

	slots[index] = window
}

func (s slotSimulation) closeBootstrap(t *testing.T, session string) {
	t.Helper()

	slots := s[session]
	if len(slots) < 2 {
		t.Fatal("closing sole window kills the session")
	}

	for index, window := range slots {
		if window == "bootstrap:"+session {
			delete(slots, index)
			return
		}
	}

	t.Fatal("bootstrap missing before closure")
}

func (s slotSimulation) selectWindow(t *testing.T, step SelectWindowStep) {
	t.Helper()

	slots := s[step.SessionID]
	if slots[step.ActiveIndex] != step.ActiveWindowID {
		t.Fatal("active slot missing before navigation")
	}

	if step.LastWindowID != "" && slots[step.LastIndex] != step.LastWindowID {
		t.Fatal("last slot missing before navigation")
	}
}
