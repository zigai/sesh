package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func TestHandleNavigationPageAndJumpKeys(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 12

	model.filtered = make([]session.Session, 20)
	for i := range model.filtered {
		model.filtered[i] = session.Session{Name: "session"}
	}

	if cmd := model.handleNavigationKeys(keyCode(tea.KeyPgDown)); cmd == nil {
		t.Fatal("handleNavigationKeys(pgdown) returned nil")
	} else {
		cmd()
	}

	if got, want := model.cursor, 8; got != want {
		t.Fatalf("cursor after pgdown = %d, want %d", got, want)
	}

	if cmd := model.handleNavigationKeys(keyCode(tea.KeyEnd)); cmd == nil {
		t.Fatal("handleNavigationKeys(end) returned nil")
	} else {
		cmd()
	}

	if got, want := model.cursor, len(model.filtered)-1; got != want {
		t.Fatalf("cursor after end = %d, want %d", got, want)
	}

	if cmd := model.handleNavigationKeys(keyCode(tea.KeyPgUp)); cmd == nil {
		t.Fatal("handleNavigationKeys(pgup) returned nil")
	} else {
		cmd()
	}

	if got, want := model.cursor, 11; got != want {
		t.Fatalf("cursor after pgup = %d, want %d", got, want)
	}

	if cmd := model.handleNavigationKeys(keyCode(tea.KeyHome)); cmd == nil {
		t.Fatal("handleNavigationKeys(home) returned nil")
	} else {
		cmd()
	}

	if got := model.cursor; got != 0 {
		t.Fatalf("cursor after home = %d, want 0", got)
	}
}

func TestHandleNavigationStepKeysWrapSessionList(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}

	model.cursor = len(model.filtered) - 1
	if cmd := model.handleNavigationKeys(keyCode(tea.KeyDown)); cmd == nil {
		t.Fatal("handleNavigationKeys(down) returned nil")
	} else {
		cmd()
	}

	if got := model.cursor; got != 0 {
		t.Fatalf("cursor after down at bottom = %d, want wrapped top", got)
	}

	if cmd := model.handleNavigationKeys(keyCode(tea.KeyUp)); cmd == nil {
		t.Fatal("handleNavigationKeys(up) returned nil")
	} else {
		cmd()
	}

	if got, want := model.cursor, len(model.filtered)-1; got != want {
		t.Fatalf("cursor after up at top = %d, want wrapped bottom %d", got, want)
	}
}

func TestHandleNavigationUsesConfiguredKeys(t *testing.T) {
	model := newTestModel()
	model.config.Keys.PageDown = "ctrl+f"
	model.config.Keys.Top = "g"
	model.width = 80
	model.height = 12
	model.filtered = make([]session.Session, 10)
	model.cursor = 5

	if cmd := model.handleNavigationKeys(keyText("g")); cmd == nil {
		t.Fatal("handleNavigationKeys(configured top) returned nil")
	} else {
		cmd()
	}

	if got := model.cursor; got != 0 {
		t.Fatalf("cursor after configured top = %d, want 0", got)
	}

	if cmd := model.handleNavigationKeys(keyCtrl('f')); cmd == nil {
		t.Fatal("handleNavigationKeys(configured page down) returned nil")
	} else {
		cmd()
	}

	if got := model.cursor; got == 0 {
		t.Fatal("cursor did not move after configured page down")
	}
}
