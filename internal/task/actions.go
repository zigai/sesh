package task

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Variables are the values a task action prompt can refer to.
func Variables(record Record) map[string]string {
	project := filepath.Base(record.Project)
	if record.Kind == KindLoose {
		project = ""
	}

	return map[string]string{
		"task": record.ID, "title": record.Title, "branch": record.Branch, "base": record.Base,
		"project": project, "worktree": record.Directory,
	}
}

// Render fills a prompt template with a task's variables.
func Render(prompt string, record Record) string {
	variables := Variables(record)

	pairs := make([]string, 0, len(variables)+len(variables))
	for name, value := range variables {
		pairs = append(pairs, "{"+name+"}", value)
	}

	return strings.NewReplacer(pairs...).Replace(prompt)
}

// RecordRun stores the latest run of a named action on a task, replacing the previous run of the
// same action.
func (s *Service) RecordRun(ctx context.Context, id string, run Run) error {
	_, err := s.store.Update(ctx, func(ledger *Ledger) error {
		record, found := ledger.Find(id)
		if !found {
			return ErrNotFound
		}

		runs := make([]Run, 0, len(record.Runs)+1)
		for _, previous := range record.Runs {
			if previous.Action != run.Action {
				runs = append(runs, previous)
			}
		}

		runs = append(runs, run)
		record.Runs = runs
		ledger.Put(record)

		return nil
	})

	return taskError("record_run", err)
}

// Retitle names a task's window after the title its agent reports, unless the user renamed the
// window: a window still named what sesh last named it follows the agent's title.
func (s *Service) Retitle(ctx context.Context, id, title string) (bool, error) {
	title = windowTitle(title)
	if title == "" {
		return false, nil
	}

	record, live, err := s.task(ctx, id)
	if err != nil {
		return false, taskError("retitle_task", err)
	}

	if record.Title == title || live.WindowName != record.Title {
		return false, nil
	}

	window, err := s.server.Window(ctx, live.WindowID)
	if err != nil {
		return false, taskError("retitle_task", fmt.Errorf("find task window: %w", err))
	}

	if err := window.Rename(ctx, title); err != nil {
		return false, taskError("retitle_task", fmt.Errorf("rename task window: %w", err))
	}

	record.Title = title

	return true, taskError("retitle_task", s.put(ctx, record))
}

func windowTitle(title string) string {
	return strings.Join(strings.FieldsFunc(title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}
