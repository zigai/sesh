package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestModelConstructorsRejectNilConfigAndState(t *testing.T) {
	tests := []struct {
		name string
		new  func(*config.Config, *session.State) (*Model, error)
	}{
		{name: "newModel", new: newModel},
		{name: "newModelWithDeps", new: func(cfg *config.Config, st *session.State) (*Model, error) {
			return newModelWithDeps(cfg, st, Dependencies{})
		}},
		{name: "NewPopupModel", new: func(cfg *config.Config, st *session.State) (*Model, error) {
			return NewPopupModel(context.Background(), cfg, st, testServices(Dependencies{}))
		}},
		{name: "newModelWithTestContext", new: func(cfg *config.Config, st *session.State) (*Model, error) {
			return newModelWithTestContext(context.Background(), cfg, st, Dependencies{})
		}},
	}

	for _, test := range tests {
		t.Run(test.name+" rejects nil config", func(t *testing.T) {
			model, err := test.new(nil, &session.State{})
			if !errors.Is(err, errNilConfig) {
				t.Fatalf("constructor error = %v, want %v", err, errNilConfig)
			}

			if model != nil {
				t.Fatalf("constructor returned model %#v for nil config", model)
			}
		})

		t.Run(test.name+" rejects nil state", func(t *testing.T) {
			model, err := test.new(config.DefaultConfig(), nil)
			if !errors.Is(err, errNilState) {
				t.Fatalf("constructor error = %v, want %v", err, errNilState)
			}

			if model != nil {
				t.Fatalf("constructor returned model %#v for nil state", model)
			}
		})
	}
}

func TestInjectedServicesDoNotUseProductionDefaults(t *testing.T) {
	_, err := NewPopupModel(
		context.Background(),
		config.DefaultConfig(),
		&session.State{},
		Services{},
	)
	if !errors.Is(err, errIncompleteServices) {
		t.Fatalf("NewPopupModel() error = %v, want %v", err, errIncompleteServices)
	}
}

func TestPopupConstructorForcesPopupMode(t *testing.T) {
	model, err := NewPopupModel(
		context.Background(),
		config.DefaultConfig(),
		&session.State{},
		testServices(Dependencies{TmuxIsInsideTmux: func() bool { return false }}),
	)
	if err != nil {
		t.Fatalf("NewPopupModel() error = %v", err)
	}

	if model.mode != modePopup {
		t.Fatalf("popup model mode = %v, want popup", model.mode)
	}
}

func TestModelDetectsPresentationMode(t *testing.T) {
	tests := []struct {
		name   string
		inside bool
		popup  bool
		want   uiMode
	}{
		{name: "standalone outside tmux", inside: false, want: modeFullscreen},
		{name: "inline inside tmux", inside: true, want: modeInline},
		{name: "popup inside tmux", inside: true, popup: true, want: modePopup},
		{name: "popup flag outside tmux stays standalone", inside: false, popup: true, want: modeFullscreen},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.popup {
				t.Setenv("TMUX_POPUP", "1")
			}

			deps := Dependencies{TmuxIsInsideTmux: func() bool { return test.inside }}

			model, err := newModelWithDeps(config.DefaultConfig(), &session.State{}, deps)
			if err != nil {
				t.Fatalf("newModelWithDeps() error = %v", err)
			}

			if model.mode != test.want {
				t.Fatalf("model mode = %v, want %v", model.mode, test.want)
			}
		})
	}
}

func TestModelRestoresPersistedDisplayMode(t *testing.T) {
	tests := []struct {
		name      string
		persisted string
		want      displayMode
	}{
		{name: "agents", persisted: string(displayModeAgents), want: displayModeAgents},
		{name: "sesh", persisted: string(displayModeSesh), want: displayModeSesh},
		{name: "empty falls back to first mode", persisted: "", want: displayModeSesh},
		{name: "unknown falls back to first mode", persisted: "diagonal", want: displayModeSesh},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, err := newModelWithDeps(
				config.DefaultConfig(),
				&session.State{PopupDisplayMode: test.persisted},
				Dependencies{},
			)
			if err != nil {
				t.Fatalf("newModelWithDeps() error = %v", err)
			}

			if model.displayMode != test.want {
				t.Fatalf("model display mode = %v, want %v", model.displayMode, test.want)
			}
		})
	}
}

func TestModelCloseCancelsOperationContext(t *testing.T) {
	model := newTestModel()

	operationContext := model.operationContext()

	select {
	case <-operationContext.Done():
		t.Fatal("operation context was canceled before Close()")
	default:
	}

	model.Close()

	select {
	case <-operationContext.Done():
	default:
		t.Fatal("Close() did not cancel the operation context")
	}

	if !errors.Is(operationContext.Err(), context.Canceled) {
		t.Fatalf("operation context error = %v, want context.Canceled", operationContext.Err())
	}

	model.Close()
}

func TestUpdatePassesNonKeyMessagesToTextInput(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue(testSupportSessionDevName)
	before := model.textInput.Styles().Cursor.Blink

	updated, _ := model.Update(textinput.Blink())

	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}

	if next.textInput.Value() != testSupportSessionDevName {
		t.Fatalf("text input value = %q, want preserved value", next.textInput.Value())
	}

	if next.textInput.Styles().Cursor.Blink != before {
		t.Fatalf(
			"initial blink changed cursor directly = %v, want %v before timer tick",
			next.textInput.Styles().Cursor.Blink,
			before,
		)
	}
}

func TestNewModelConfiguresVisibleSearchCursor(t *testing.T) {
	model := newTestModel()
	styles := model.textInput.Styles()

	gotR, gotG, gotB, gotA := styles.Cursor.Color.RGBA()

	wantR, wantG, wantB, wantA := lipgloss.Color("15").RGBA()
	if gotR != wantR || gotG != wantG || gotB != wantB || gotA != wantA {
		t.Fatalf("cursor foreground = %#v, want %#v", styles.Cursor.Color, lipgloss.Color("15"))
	}

	if got := fmt.Sprint(styles.Focused.Text.GetForeground()); got != "252" {
		t.Fatalf("text foreground = %s, want 252", got)
	}
}

func BenchmarkNewModelWithCompleteDependencies(b *testing.B) {
	cfg := config.DefaultConfig()
	st := testState()
	deps := completeNoopDependencies()

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		model, err := newModelWithDeps(cfg, st, deps)
		if err != nil {
			b.Fatalf("newModelWithDeps() returned error: %v", err)
		}

		if model == nil {
			b.Fatal("newModelWithDeps() returned nil")
		}

		model.Close()
	}
}

func completeNoopDependencies() Dependencies {
	return Dependencies{}
}
