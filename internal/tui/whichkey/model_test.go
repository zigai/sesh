package whichkey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
)

var errWhichKeyTestLoadFailed = errors.New("key load failed")

func TestNilSourceDoesNotUseProductionDefault(t *testing.T) {
	model := NewModel(context.Background(), config.DefaultConfig(), nil, "prefix")

	model.Update(model.loadKeysCommand()())

	if !errors.Is(model.LoadError(), errNilSource) {
		t.Fatalf("LoadError() = %v, want nil-source error", model.LoadError())
	}
}

func TestViewFitsNarrowPhonePopupViewport(t *testing.T) {
	model := NewModel(context.Background(), config.DefaultConfig(), &whichKeyTestSource{}, "prefix")
	model.width = 35
	model.height = 14
	model.bindings = []Binding{{
		Key:         "C-really-long-key",
		Description: "A key binding with a long description",
	}}
	model.filter()

	output := model.render()

	lines := strings.Split(output, "\n")
	if len(lines) > model.height {
		t.Fatalf("phone keys popup has %d lines, want <= %d: %q", len(lines), model.height, output)
	}

	for i, line := range lines {
		if got := lipgloss.Width(line); got > model.width {
			t.Fatalf("phone keys popup line %d width = %d, want <= %d: %q", i, got, model.width, line)
		}
	}
}

func TestPageNavigationClampsAtListBounds(t *testing.T) {
	model := NewModel(context.Background(), config.DefaultConfig(), &whichKeyTestSource{}, "prefix")

	model.height = 12
	for index := range 12 {
		model.bindings = append(model.bindings, Binding{Key: string(rune('a' + index))})
	}

	model.filter()

	model.cursor = 1
	model.handleMoveKey("pgup")

	if model.cursor != 0 {
		t.Fatalf("page up cursor = %d, want top", model.cursor)
	}

	model.cursor = len(model.filtered) - 2
	model.handleMoveKey("pgdown")

	if model.cursor != len(model.filtered)-1 {
		t.Fatalf("page down cursor = %d, want bottom", model.cursor)
	}
}

type whichKeyTestSource struct {
	bindings    []Binding
	ctxDone     <-chan struct{}
	deadline    time.Time
	hasDeadline bool
}

func (source *whichKeyTestSource) ListKeyBindings(ctx context.Context, _ string) ([]Binding, error) {
	source.ctxDone = ctx.Done()
	source.deadline, source.hasDeadline = ctx.Deadline()

	return append([]Binding(nil), source.bindings...), nil
}

func TestPressingBindingKeyDefersCommandAndQuits(t *testing.T) {
	model := NewModel(context.Background(), config.DefaultConfig(), &whichKeyTestSource{
		bindings: []Binding{{Key: "c", Description: "Create a new window", Command: "new-window"}},
	}, "prefix")
	model.bindings = []Binding{{Key: "c", Description: "Create a new window", Command: "new-window"}}
	model.filter()

	_, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Text: "c"}))
	if cmd == nil {
		t.Fatal("Update() returned nil command, want quit")
	}

	if got := model.DeferredCommand(); got != "new-window" {
		t.Fatalf("DeferredCommand() = %q, want new-window", got)
	}

	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("command message = %T, want tea.QuitMsg", msg)
	}
}

func TestLoadErrorExposesKeyBindingFailure(t *testing.T) {
	model := NewModel(context.Background(), config.DefaultConfig(), &whichKeyTestSource{}, "missing")
	model.err = errWhichKeyTestLoadFailed

	if !errors.Is(model.LoadError(), errWhichKeyTestLoadFailed) {
		t.Fatalf("LoadError() = %v, want key load failure", model.LoadError())
	}

	var nilModel *Model
	if nilModel.LoadError() != nil {
		t.Fatalf("nil LoadError() = %v, want nil", nilModel.LoadError())
	}
}

func TestLoadKeysUsesLifecycleCancellationWithoutRetainingParentContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	source := &whichKeyTestSource{}
	model := NewModel(parent, config.DefaultConfig(), source, "prefix")

	cancel()

	if msg := model.loadKeysCommand()(); msg != nil {
		select {
		case <-source.ctxDone:
		case <-time.After(time.Second):
			t.Fatal("binding source did not receive parent cancellation")
		}
	}
}

func TestModelOperationContextPreservesDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Second)

	parent, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	source := &whichKeyTestSource{}
	model := NewModel(parent, config.DefaultConfig(), source, "prefix")

	_ = model.loadKeysCommand()()

	if !source.hasDeadline || !source.deadline.Equal(deadline) {
		t.Fatalf("operation deadline = %v, %v; want %v, true", source.deadline, source.hasDeadline, deadline)
	}
}

func TestModelCloseCancelsOperationContext(t *testing.T) {
	source := &whichKeyTestSource{}
	model := NewModel(context.Background(), config.DefaultConfig(), source, "prefix")

	model.Close()
	model.Close()

	if msg := model.loadKeysCommand()(); msg != nil {
		t.Fatalf("loadKeysCommand() after Close() = %T, want nil", msg)
	}

	if source.ctxDone != nil {
		t.Fatal("source was invoked after Close()")
	}
}

type blockingWhichKeySource struct {
	started chan struct{}
	joined  chan struct{}
}

func (source *blockingWhichKeySource) ListKeyBindings(ctx context.Context, _ string) ([]Binding, error) {
	close(source.started)
	<-ctx.Done()
	close(source.joined)

	return nil, fmt.Errorf("binding read canceled: %w", ctx.Err())
}

func TestModelCloseWaitsForBindingRead(t *testing.T) {
	source := &blockingWhichKeySource{
		started: make(chan struct{}),
		joined:  make(chan struct{}),
	}
	model := NewModel(context.Background(), config.DefaultConfig(), source, "prefix")
	commandDone := make(chan struct{})

	go func() {
		_ = model.loadKeysCommand()()

		close(commandDone)
	}()

	<-source.started
	model.Close()

	select {
	case <-source.joined:
	default:
		t.Fatal("Close() returned before binding read joined")
	}

	<-commandDone
}

func TestTruncateStyledWidthPreservesANSIAndGraphemeClusters(t *testing.T) {
	input := "\x1b]8;;https://example.com\x1b\\A👩‍💻B\x1b]8;;\x1b\\"

	got := truncateStyledWidth(input, 3)

	if text := ansi.Strip(got); text != "A👩‍💻" {
		t.Fatalf("truncateStyledWidth() text = %q, want %q", text, "A👩‍💻")
	}

	if width := ansi.StringWidth(got); width != 3 {
		t.Fatalf("truncateStyledWidth() width = %d, want 3", width)
	}
}
