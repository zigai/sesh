package setup_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/setup"
)

func TestBindingsDefaults(t *testing.T) {
	t.Parallel()

	bindings, err := setup.Bindings(defaultKeys())
	if err != nil {
		t.Fatalf("Bindings failed: %v", err)
	}

	if len(bindings) != 4 {
		t.Fatalf("bindings = %d, want 4", len(bindings))
	}

	wantKeys := []string{setup.DefaultNextKey, setup.DefaultLocalNextKey, setup.DefaultBackKey, setup.DefaultPickerKey}

	for index, binding := range bindings {
		if binding.Key != wantKeys[index] {
			t.Fatalf("binding %d key = %q, want %q", index, binding.Key, wantKeys[index])
		}

		if binding.Purpose == "" {
			t.Fatalf("binding %d has no purpose", index)
		}

		if _, err := binding.CommandSequence(); err != nil {
			t.Fatalf("binding %d command sequence failed: %v", index, err)
		}
	}

	if got := bindings[0].Program[2]; got != "sesh action agent-next" {
		t.Fatalf("next program = %q, want the native flat action", got)
	}

	if got := bindings[1].Program[2]; got != "sesh action agent-next --local" {
		t.Fatalf("local-next program = %q", got)
	}

	if got := bindings[2].Program[2]; got != "sesh action agent-back" {
		t.Fatalf("back program = %q", got)
	}

	if got := bindings[3].Program[2]; got != "sesh action picker-open -c '#{client_name}' -t '#{pane_id}' --current-session-id '#{session_id}' -w '#{client_width}' --client-height '#{client_height}'" {
		t.Fatalf("picker program = %q", got)
	}
}

func TestBindingsRejectsInvalidKey(t *testing.T) {
	t.Parallel()

	keys := defaultKeys()
	keys.Next = "not a key"

	if _, err := setup.Bindings(keys); !errors.Is(err, setup.ErrInvalidKey) {
		t.Fatalf("error = %v, want ErrInvalidKey", err)
	}
}

func TestBindingsRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	keys := defaultKeys()
	keys.Back = keys.Picker

	if _, err := setup.Bindings(keys); !errors.Is(err, setup.ErrDuplicateKey) {
		t.Fatalf("error = %v, want ErrDuplicateKey", err)
	}
}

func TestBindingsRejectsInvalidTable(t *testing.T) {
	t.Parallel()

	for _, table := range []string{"", "has space", "has\ttab"} {
		if err := setup.ValidateTable(table); !errors.Is(err, setup.ErrInvalidTable) {
			t.Fatalf("ValidateTable(%q) = %v, want ErrInvalidTable", table, err)
		}
	}

	if err := setup.ValidateTable("custom-table"); err != nil {
		t.Fatalf("custom table rejected: %v", err)
	}

	if err := setup.ValidateTable(setup.DefaultTable); err != nil {
		t.Fatalf("default table rejected: %v", err)
	}
}

func TestRenderBlockQuotesArgumentsWithSpaces(t *testing.T) {
	t.Parallel()

	bindings, err := setup.Bindings(defaultKeys())
	if err != nil {
		t.Fatal(err)
	}

	block := setup.RenderBlock(bindings, setup.DefaultTable)

	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) != len(bindings)+1 {
		t.Fatalf("rendered %d lines, want %d", len(lines), len(bindings)+1)
	}

	if !strings.HasPrefix(lines[0], "#") {
		t.Fatalf("first line must be a comment, got %q", lines[0])
	}

	for index, binding := range bindings {
		line := lines[index+1]

		if !strings.Contains(line, "bind-key -T root "+binding.Key+" run-shell -b ") {
			t.Fatalf("line %d = %q, want a root-table run-shell binding", index, line)
		}

		if !strings.Contains(line, `"`+binding.Program[2]+`"`) {
			t.Fatalf("line %d = %q, want the quoted program", index, line)
		}
	}
}

func TestBindingTextQuotesShellMetacharacters(t *testing.T) {
	t.Parallel()

	binding := setup.Binding{
		Key:     "F1",
		Purpose: "test",
		Program: []string{"run-shell", "-b", `echo "$HOME"`},
	}

	text := binding.Text(setup.DefaultTable)

	if !strings.Contains(text, `"echo \"$HOME\""`) {
		t.Fatalf("text = %q, want an escaped quoted argument", text)
	}
}

func defaultKeys() setup.Keys {
	return setup.Keys{
		Next:      setup.DefaultNextKey,
		LocalNext: setup.DefaultLocalNextKey,
		Back:      setup.DefaultBackKey,
		Picker:    setup.DefaultPickerKey,
	}
}
